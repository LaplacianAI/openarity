// Package docker is the small part of the Docker Engine API sandboxd uses,
// spoken over the daemon's unix socket with net/http. The moby client would
// bring a few dozen modules for six calls.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// apiVersion is the oldest Engine API a current daemon still serves.
const apiVersion = "v1.44"

// Error is the daemon refusing a call.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("docker answered %d: %s", e.Status, e.Message) }

// IsNotFound reports whether err is the daemon saying a thing does not exist.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Client talks to one daemon.
type Client struct {
	http *http.Client
	// pull has no overall timeout: a pull is as long as the image is large,
	// and ends with its context.
	pull *http.Client
}

// New returns a client for the daemon listening on the unix socket.
func New(socket string) *Client {
	d := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &Client{
		http: &http.Client{Timeout: 2 * time.Minute, Transport: tr},
		pull: &http.Client{Transport: tr},
	}
}

// Container is the part of a container's configuration the daemon is given.
// sandboxd builds every one itself, so there are no fields a request could
// reach: this is the whole of what can be set.
type Container struct {
	Image      string
	Entrypoint []string
	Env        []string
	Labels     map[string]string
	User       string
	HostConfig HostConfig
}

// HostConfig is the hardening.
type HostConfig struct {
	ReadonlyRootfs bool
	Privileged     bool
	Tmpfs          map[string]string
	CapDrop        []string
	SecurityOpt    []string
	PidsLimit      int64
	Memory         int64
	MemorySwap     int64
	NanoCPUs       int64
	NetworkMode    string
	ExtraHosts     []string
	RestartPolicy  RestartPolicy
	// Mounts are named volumes, by the path they are mounted at: a
	// workspace's files, which outlive the container being stopped.
	Mounts map[string]string
}

type RestartPolicy struct{ Name string }

// Network is a network as the daemon reports it.
type Network struct {
	ID         string
	Name       string
	Labels     map[string]string
	Internal   bool
	Containers map[string]Member // by container id
}

// Member is a container on a network.
type Member struct {
	Name string
	IPv4 string // an address, without the prefix length
}

// CreateNetwork makes an internal bridge network: containers on it reach
// each other and nothing outside.
func (c *Client) CreateNetwork(ctx context.Context, name string, labels map[string]string) error {
	body := map[string]any{"Name": name, "Driver": "bridge", "Internal": true, "Labels": labels, "CheckDuplicate": true}
	return c.do(ctx, http.MethodPost, "/networks/create", nil, body, nil)
}

func (c *Client) InspectNetwork(ctx context.Context, name string) (Network, error) {
	var raw networkJSON
	if err := c.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil, &raw); err != nil {
		return Network{}, err
	}
	return raw.network(), nil
}

// ListNetworks returns the networks carrying every one of the labels.
func (c *Client) ListNetworks(ctx context.Context, label string) ([]Network, error) {
	f, _ := json.Marshal(map[string][]string{"label": {label}})
	var raw []networkJSON
	if err := c.do(ctx, http.MethodGet, "/networks", url.Values{"filters": {string(f)}}, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.network())
	}
	return out, nil
}

func (c *Client) ConnectNetwork(ctx context.Context, network, container string) error {
	body := map[string]any{"Container": container}
	err := c.do(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", nil, body, nil)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusForbidden && strings.Contains(e.Message, "already exists") {
		return nil
	}
	return err
}

func (c *Client) DisconnectNetwork(ctx context.Context, network, container string) error {
	body := map[string]any{"Container": container, "Force": true}
	return c.do(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/disconnect", nil, body, nil)
}

func (c *Client) RemoveNetwork(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/networks/"+url.PathEscape(name), nil, nil, nil)
}

// CreateContainer returns the new container's id.
func (c *Client) CreateContainer(ctx context.Context, name string, ct Container) (string, error) {
	var out struct{ Id string } //nolint:revive,staticcheck // the daemon's spelling
	err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, ct.json(), &out)
	return out.Id, err
}

func (c *Client) StartContainer(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/start", nil, nil, nil)
}

// ContainerID resolves a name, or a short id, to the full id.
func (c *Client) ContainerID(ctx context.Context, nameOrID string) (string, error) {
	var out struct{ Id string } //nolint:revive,staticcheck // the daemon's spelling
	err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(nameOrID)+"/json", nil, nil, &out)
	return out.Id, err
}

// RemoveContainer stops and removes it; a container already gone is an
// IsNotFound error.
func (c *Client) RemoveContainer(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(name), url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
}

// SplitRef splits an image reference into the repository and the tag or
// digest the Engine API takes apart: "host:5000/a/b:t" is "host:5000/a/b" and
// "t"; "a/b@sha256:..." is "a/b" and "sha256:..."; no tag means "latest",
// because an empty tag would pull every tag of the repository.
func SplitRef(ref string) (repo, tag string) {
	if r, d, ok := strings.Cut(ref, "@"); ok {
		// the digest decides; a tag beside it is dropped
		repo, _ = SplitRef(r)
		return repo, d
	}
	// A colon is a tag's only after the last slash: before it, it is the
	// registry's port.
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func imagePath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/images/" + strings.Join(parts, "/") + "/json"
}

// HasImage reports whether the daemon holds the image.
func (c *Client) HasImage(ctx context.Context, ref string) (bool, error) {
	err := c.do(ctx, http.MethodGet, imagePath(ref), nil, nil, nil)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// PullImage pulls the image and reads the progress to its end. The daemon
// answers 200 as soon as the pull starts and reports a failure after that as
// a message in the stream, so a 200 alone says nothing: the stream is the
// answer. A failure before the stream starts (the repository does not
// exist) is a plain error status.
func (c *Client) PullImage(ctx context.Context, ref string) error {
	repo, tag := SplitRef(ref)
	u := "http://docker/" + apiVersion + "/images/create?" + url.Values{"fromImage": {repo}, "tag": {tag}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.pull.Do(req)
	if err != nil {
		return fmt.Errorf("docker POST /images/create: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var e struct{ Message string }
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(raw))
		}
		return &Error{Status: resp.StatusCode, Message: e.Message}
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Error       string
			ErrorDetail struct{ Message string }
		}
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("reading the pull's progress: %w", err)
		}
		if m.Error != "" || m.ErrorDetail.Message != "" {
			msg := m.ErrorDetail.Message
			if msg == "" {
				msg = m.Error
			}
			return &Error{Status: http.StatusOK, Message: msg}
		}
	}
}

// Ping fails unless the daemon answers.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/_ping", nil, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	u := "http://docker/" + apiVersion + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct{ Message string }
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(raw))
		}
		return &Error{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type networkJSON struct {
	ID         string
	Name       string
	Labels     map[string]string
	Internal   bool
	Containers map[string]struct {
		Name        string
		IPv4Address string
	}
}

func (n networkJSON) network() Network {
	out := Network{ID: n.ID, Name: n.Name, Labels: n.Labels, Internal: n.Internal, Containers: map[string]Member{}}
	for id, m := range n.Containers {
		ip, _, _ := strings.Cut(m.IPv4Address, "/")
		out.Containers[id] = Member{Name: m.Name, IPv4: ip}
	}
	return out
}

// json is the create body. Field names are the daemon's.
func (ct Container) json() map[string]any {
	h := ct.HostConfig
	return map[string]any{
		"Image":      ct.Image,
		"Entrypoint": ct.Entrypoint,
		"Cmd":        []string{},
		"Env":        ct.Env,
		"Labels":     ct.Labels,
		"User":       ct.User,
		"HostConfig": map[string]any{
			"ReadonlyRootfs": h.ReadonlyRootfs,
			"Privileged":     h.Privileged,
			"Tmpfs":          h.Tmpfs,
			"CapDrop":        h.CapDrop,
			"SecurityOpt":    h.SecurityOpt,
			"PidsLimit":      h.PidsLimit,
			"Memory":         h.Memory,
			"MemorySwap":     h.MemorySwap,
			"NanoCpus":       h.NanoCPUs,
			"NetworkMode":    h.NetworkMode,
			"ExtraHosts":     h.ExtraHosts,
			"RestartPolicy":  map[string]string{"Name": h.RestartPolicy.Name},
			"Mounts":         mounts(h.Mounts),
		},
		"NetworkingConfig": map[string]any{},
	}
}

// mounts is the daemon's form of named volumes, sorted so a body is the same
// every time.
func mounts(m map[string]string) []map[string]any {
	paths := make([]string, 0, len(m))
	for p := range m {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]map[string]any, 0, len(m))
	for _, p := range paths {
		out = append(out, map[string]any{"Type": "volume", "Source": m[p], "Target": p})
	}
	return out
}

// CreateVolume makes a named volume on the local driver.
func (c *Client) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	return c.do(ctx, http.MethodPost, "/volumes/create", nil, map[string]any{"Name": name, "Driver": "local", "Labels": labels}, nil)
}

// RemoveVolume removes it; one already gone is an IsNotFound error.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), url.Values{"force": {"1"}}, nil, nil)
}

// StopContainer stops it, killing it after grace; one already stopped is not
// an error.
func (c *Client) StopContainer(ctx context.Context, name string, grace time.Duration) error {
	err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/stop",
		url.Values{"t": {strconv.Itoa(int(grace / time.Second))}}, nil, nil)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotModified {
		return nil
	}
	return err
}

// ContainerRunning reports whether the container is running; one that does
// not exist is an IsNotFound error.
func (c *Client) ContainerRunning(ctx context.Context, name string) (bool, error) {
	var out struct{ State struct{ Running bool } }
	err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, nil, &out)
	return out.State.Running, err
}

// ContainerState reports whether the container is running and when it last
// stopped. A container that never ran has the daemon's zero time, which is
// returned as the zero time.Time. One that does not exist is an IsNotFound
// error.
func (c *Client) ContainerState(ctx context.Context, name string) (running bool, finishedAt time.Time, err error) {
	var out struct {
		State struct {
			Running    bool
			FinishedAt string
		}
	}
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, nil, &out); err != nil {
		return false, time.Time{}, err
	}
	if out.State.FinishedAt == "" {
		return out.State.Running, time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, out.State.FinishedAt)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("the container's finish time %q: %w", out.State.FinishedAt, err)
	}
	return out.State.Running, t, nil
}

// VolumeSizes is the bytes each volume carrying the label holds, by name, as
// the daemon measures them. The daemon walks every volume to answer, so it
// is asked rarely.
func (c *Client) VolumeSizes(ctx context.Context, label string) (map[string]int64, error) {
	var out struct {
		Volumes []struct {
			Name      string
			Labels    map[string]string
			UsageData struct{ Size int64 }
		}
	}
	if err := c.do(ctx, http.MethodGet, "/system/df", url.Values{"type": {"volume"}}, nil, &out); err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	for _, v := range out.Volumes {
		if _, ok := v.Labels[label]; ok {
			sizes[v.Name] = v.UsageData.Size
		}
	}
	return sizes, nil
}
