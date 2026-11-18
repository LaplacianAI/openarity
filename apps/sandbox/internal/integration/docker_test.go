// Package integration runs sandboxd against a real Docker daemon. The tests
// skip unless SANDBOXD_TEST_DOCKER names the daemon's socket, because they
// build images, start containers and need the internet (the npm registry and
// one public host).
//
//	SANDBOXD_TEST_DOCKER=$HOME/.docker/run/docker.sock go test ./internal/integration/
//
// sandboxd itself runs in a container, as in production: it has to be a
// member of each room's network, which a process on the host cannot be.
// SANDBOXD_TEST_DOCKER_MOUNT is the socket's path as that container sees it
// (default /var/run/docker.sock, which Docker Desktop provides).
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	apiToken = "integration-api-token"
	shimTok  = "integration-shim-token"
	roomImg  = "sandboxd-it-room:test"
	sbdImg   = "sandboxd-it:test"
	// pullRepo is a small public image the pull test removes and sandboxd
	// fetches again.
	pullRepo = "docker.io/library/alpine"
)

var built sync.Once

type env struct {
	t      *testing.T
	socket string
	base   string // sandboxd's URL from the host
}

func (e *env) docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(e.t.Context()), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+e.socket)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func (e *env) mustDocker(args ...string) string {
	e.t.Helper()
	out, err := e.docker(args...)
	if err != nil {
		e.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

// setup builds the images and starts sandboxd in a container.
func setup(t *testing.T, memoryBudget string) *env {
	t.Helper()
	socket := os.Getenv("SANDBOXD_TEST_DOCKER")
	if socket == "" {
		t.Skip("SANDBOXD_TEST_DOCKER does not name a Docker socket")
	}
	mount := os.Getenv("SANDBOXD_TEST_DOCKER_MOUNT")
	if mount == "" {
		mount = "/var/run/docker.sock"
	}
	e := &env{t: t, socket: socket}
	appDir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// The room image is built from the repository root, as it is published.
	repoDir := filepath.Join(appDir, "..", "..")
	// Building compiles the module, a minute on a cold cache: once per run.
	built.Do(func() {
		e.mustDocker("build", "-q", "-f", filepath.Join(repoDir, "deployment", "sandbox", "Dockerfile"), "-t", roomImg, repoDir)
		e.mustDocker("build", "-q", "--target", "sandboxd", "-t", sbdImg, appDir)
	})

	var b [4]byte
	_, _ = rand.Read(b[:])
	name := "sandboxd-it-" + hex.EncodeToString(b[:])
	port := freePort(t)
	e.base = "http://127.0.0.1:" + strconv.Itoa(port)
	e.mustDocker("run", "-d", "--name", name, "--user", "0",
		"-p", "127.0.0.1:"+strconv.Itoa(port)+":9090",
		"-v", mount+":/var/run/docker.sock",
		"-e", "SANDBOXD_TOKEN="+apiToken,
		"-e", "SANDBOXD_PUBLIC_URL="+e.base,
		"-e", "SANDBOXD_DOCKER_SOCKET=/var/run/docker.sock",
		"-e", "SANDBOXD_MEMORY_BUDGET="+memoryBudget,
		"-e", "SANDBOXD_IMAGES=sandboxd-it-room,"+pullRepo,
		sbdImg)
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := e.docker("logs", "--tail", "40", name)
			t.Logf("sandboxd logs:\n%s", logs)
		}
		e.cleanRooms()
		_, _ = e.docker("rm", "-f", name)
	})
	e.waitHealthy()
	return e
}

// cleanRooms removes whatever rooms a failed test left.
func (e *env) cleanRooms() {
	ctx := context.Background()
	for _, id := range e.roomIDs() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, e.base+"/v1/rooms/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+apiToken)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
}

func (e *env) roomIDs() []string {
	out, _ := e.docker("network", "ls", "--filter", "label=oa.sandboxd.room", "--format", `{{.Label "oa.sandboxd.room"}}`)
	return strings.Fields(out)
}

func (e *env) waitHealthy() {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(e.t.Context(), http.MethodGet, e.base+"/healthz", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatal("sandboxd never became healthy")
}

func (e *env) api(method, path string, body any) (int, []byte) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.WithoutCancel(e.t.Context()), method, e.base+path, r)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

type egress struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// room makes and starts a room that sleeps, so tests can exec in it.
func (e *env) room(egr []egress, argv ...string) (id, container string) {
	e.t.Helper()
	if egr == nil {
		egr = []egress{}
	}
	status, b := e.api("POST", "/v1/rooms", map[string]any{
		"image": roomImg, "env": map[string]string{
			"SHIM_TOKEN": shimTok, "SECRET": "s3",
			// the mail server's own settings: fake, and never dialled by
			// initialize or tools/list
			"IMAP_HOST": "imap.example.invalid", "IMAP_PORT": "993", "IMAP_SECURE": "true",
			"SMTP_HOST": "smtp.example.invalid", "SMTP_PORT": "465", "SMTP_SECURE": "true",
			"EMAIL_USER": "fake@example.invalid", "EMAIL_PASS": "fake",
			// npm's audit and update check are more work than half a CPU
			// does in the time its lock allows ("Lock compromised"); a
			// room that runs npx should turn them off.
			"npm_config_audit": "false", "npm_config_fund": "false", "npm_config_update_notifier": "false",
		},
		"egress": egr, "labels": map[string]string{"test": "it"},
	})
	if status != 201 {
		e.t.Fatalf("create = %d %s", status, b)
	}
	var out struct{ ID string }
	_ = json.Unmarshal(b, &out)
	e.t.Cleanup(func() { e.api("DELETE", "/v1/rooms/"+out.ID, nil) })
	if len(argv) == 0 {
		argv = []string{"sleep", "3600"}
	}
	if status, b := e.api("POST", "/v1/rooms/"+out.ID+"/start", map[string]any{"argv": argv}); status != 204 {
		e.t.Fatalf("start = %d %s", status, b)
	}
	return out.ID, "oa-sb-" + out.ID
}

func (e *env) exec(container string, argv ...string) (string, error) {
	return e.docker(append([]string{"exec", container}, argv...)...)
}

// reach reports what a TCP connection from inside the room to addr does.
func (e *env) reach(container, host string, port int) string {
	js := fmt.Sprintf(`const s=require('net').connect({host:%q,port:%d,timeout:4000});`+
		`s.on('connect',()=>{console.log('CONNECTED');process.exit(0)});`+
		`s.on('timeout',()=>{console.log('TIMEOUT');process.exit(0)});`+
		`s.on('error',e=>{console.log('ERR '+e.code);process.exit(0)})`, host, port)
	out, _ := e.exec(container, "node", "-e", js)
	return out
}

func (e *env) fetch(container, url string) string {
	js := fmt.Sprintf(`const r=require('https').get(%q,{timeout:8000},r=>{console.log('STATUS '+r.statusCode);process.exit(0)});`+
		`r.on('timeout',()=>{console.log('TIMEOUT');process.exit(0)});`+
		`r.on('error',e=>{console.log('ERR '+e.code);process.exit(0)})`, url)
	out, _ := e.exec(container, "node", "-e", js)
	return out
}

func TestARoomIsReadOnlyNonRootAndHardened(t *testing.T) {
	e := setup(t, "")
	_, c := e.room(nil)

	if got := e.mustDocker("exec", c, "id", "-u"); got != "65534" {
		t.Errorf("uid = %q, want 65534", got)
	}
	if out, err := e.exec(c, "touch", "/etc/x"); err == nil {
		t.Errorf("the root filesystem is writable: %s", out)
	}
	if out, err := e.exec(c, "sh", "-c", "touch /usr/x"); err == nil {
		t.Errorf("/usr is writable: %s", out)
	}
	if out, err := e.exec(c, "sh", "-c", "echo hi > /tmp/x && echo hi > /home/sandbox/x && echo $HOME"); err != nil || out != "/home/sandbox" {
		t.Errorf("tmpfs or HOME: %q %v", out, err)
	}
	if out, _ := e.exec(c, "sh", "-c", "grep ' / ' /proc/mounts"); !strings.Contains(out, " ro,") && !strings.Contains(out, " ro ") {
		t.Errorf("the root mount is not read-only: %q", out)
	}
	// /tmp is noexec; only the home directory, where npx unpacks, may run code.
	if out, err := e.exec(c, "sh", "-c", "printf '#!/bin/sh\\necho ran\\n' > /tmp/f && chmod +x /tmp/f && /tmp/f"); err == nil {
		t.Errorf("a binary ran from /tmp: %s", out)
	}
	if out, err := e.exec(c, "sh", "-c", "printf '#!/bin/sh\\necho ran\\n' > /home/sandbox/f && chmod +x /home/sandbox/f && /home/sandbox/f"); err != nil {
		t.Errorf("a binary could not run from the home directory: %s %v", out, err)
	}
	// A tmpfs of 64 MiB, not more.
	if out, err := e.exec(c, "sh", "-c", "dd if=/dev/zero of=/tmp/big bs=1M count=80 2>&1; echo done"); err != nil || !strings.Contains(out, "No space left") {
		t.Errorf("/tmp is not bounded: %q %v", out, err)
	}
	if out, err := e.exec(c, "sh", "-c", "grep CapEff /proc/self/status"); err != nil || !strings.Contains(out, "0000000000000000") {
		t.Errorf("capabilities: %q %v", out, err)
	}
	if out, _ := e.exec(c, "sh", "-c", "grep NoNewPrivs /proc/self/status"); !strings.Contains(out, "1") {
		t.Errorf("no-new-privileges: %q", out)
	}

	var hc struct {
		ReadonlyRootfs bool
		Privileged     bool
		CapDrop        []string
		SecurityOpt    []string
		PidsLimit      int64
		Memory         int64
		NanoCpus       int64
		NetworkMode    string
		Binds          []string
	}
	_ = json.Unmarshal([]byte(e.mustDocker("inspect", "--format", "{{json .HostConfig}}", c)), &hc)
	if !hc.ReadonlyRootfs || hc.Privileged || len(hc.CapDrop) != 1 || hc.CapDrop[0] != "ALL" ||
		hc.PidsLimit != 256 || hc.Memory != 256<<20 || hc.NanoCpus != 500_000_000 || len(hc.Binds) != 0 ||
		len(hc.SecurityOpt) != 1 || hc.SecurityOpt[0] != "no-new-privileges" || !strings.HasPrefix(hc.NetworkMode, "oa-sb-") {
		t.Errorf("host config = %+v", hc)
	}
	if got := e.mustDocker("inspect", "--format", `{{index .Config.Labels "user.test"}}`, c); got != "it" {
		t.Errorf("label = %q", got)
	}
}

func TestARoomReachesOnlyItsListedHosts(t *testing.T) {
	e := setup(t, "")
	_, c := e.room([]egress{{"example.com", 443}, {"registry.npmjs.org", 443}})

	if got := e.fetch(c, "https://example.com"); !strings.HasPrefix(got, "STATUS 2") && !strings.HasPrefix(got, "STATUS 3") {
		t.Errorf("a listed host was not reached: %q", got)
	}
	if got := e.fetch(c, "https://registry.npmjs.org/mcp-mail-server"); !strings.HasPrefix(got, "STATUS 200") {
		t.Errorf("the second host on the same port was not reached (SNI routing): %q", got)
	}
	// Dial a listed host, name another in the handshake: the guard reads the
	// name and refuses one that is not listed.
	js := `const r=require('https').get('https://example.com',{servername:'example.org',timeout:8000},r=>{console.log('STATUS '+r.statusCode);process.exit(0)});` +
		`r.on('timeout',()=>{console.log('TIMEOUT');process.exit(0)});r.on('error',e=>{console.log('ERR '+e.code);process.exit(0)})`
	if got, _ := e.exec(c, "node", "-e", js); !strings.HasPrefix(got, "ERR") {
		t.Errorf("a listed address with an unlisted name was relayed: %q", got)
	}
	if got := e.fetch(c, "https://example.org"); !strings.HasPrefix(got, "ERR") {
		t.Errorf("an unlisted host was reached: %q", got)
	}
	// A bare address skips DNS: the internal network has no route.
	if got := e.reach(c, "1.1.1.1", 443); got == "CONNECTED" {
		t.Errorf("1.1.1.1:443 was reached directly")
	}
	if got := e.reach(c, "93.184.216.34", 80); got == "CONNECTED" {
		t.Errorf("a public address on an unlisted port was reached")
	}
}

func TestTheGuardRefusesAListedHostThatIsPrivate(t *testing.T) {
	e := setup(t, "")
	// localhost resolves, in sandboxd, to loopback: listing it must not
	// open a path to sandboxd's own ports.
	_, c := e.room([]egress{{"localhost", 9443}})
	got := e.reach(c, "localhost", 9443)
	if got == "CONNECTED" {
		// The TCP handshake with the guard succeeds; what matters is that
		// nothing is relayed, so ask sandboxd's API through it.
		js := `const s=require('net').connect({host:'localhost',port:9443},()=>{s.write('GET /healthz HTTP/1.0\r\n\r\n')});` +
			`let d='';s.on('data',x=>d+=x);s.on('close',()=>{console.log(d.includes('200')?'RELAYED':'CLOSED');process.exit(0)});` +
			`s.on('error',()=>{console.log('CLOSED');process.exit(0)});setTimeout(()=>{console.log('CLOSED');process.exit(0)},5000)`
		out, _ := e.exec(c, "node", "-e", js)
		if out != "CLOSED" {
			t.Fatalf("the guard relayed to a loopback address: %q", out)
		}
	}
}

func TestTwoRoomsCannotReachEachOther(t *testing.T) {
	e := setup(t, "")
	idA, a := e.room(nil)
	_, b := e.room(nil)
	_ = idA

	ipB := e.mustDocker("inspect", "--format", `{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}`, b)
	ipA := e.mustDocker("inspect", "--format", `{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}`, a)
	if ipA == "" || ipB == "" || ipA == ipB && false {
		t.Fatalf("addresses: %q %q", ipA, ipB)
	}
	// B listens; A tries it. The same address from B's own side connects, so
	// a failure from A is the network's doing.
	e.mustDocker("exec", "-d", b, "node", "-e", "require('net').createServer(s=>s.end('hi')).listen(8081)")
	time.Sleep(time.Second)
	if got := e.reach(b, ipB, 8081); got != "CONNECTED" {
		t.Fatalf("control: B cannot reach itself: %q", got)
	}
	if got := e.reach(a, ipB, 8081); got == "CONNECTED" {
		t.Errorf("room A reached room B at %s", ipB)
	}
}

func TestTheBudgetRefusesARoomWith503AndDeleteGivesItBack(t *testing.T) {
	e := setup(t, strconv.Itoa(2*256<<20))
	id1, _ := e.room(nil)
	e.room(nil)
	status, b := e.api("POST", "/v1/rooms", map[string]any{"image": roomImg, "env": map[string]string{}, "egress": []egress{}, "labels": map[string]string{}})
	if status != 503 || !strings.Contains(string(b), "the sandbox is busy") {
		t.Fatalf("third room = %d %s", status, b)
	}
	if status, _ := e.api("DELETE", "/v1/rooms/"+id1, nil); status != 204 {
		t.Fatalf("delete = %d", status)
	}
	if status, b := e.api("POST", "/v1/rooms", map[string]any{"image": roomImg, "env": map[string]string{}, "egress": []egress{}, "labels": map[string]string{}}); status != 201 {
		t.Fatalf("after delete = %d %s", status, b)
	}
	if status, _ := e.api("DELETE", "/v1/rooms/"+id1, nil); status != 404 {
		t.Fatalf("second delete = %d, want 404", status)
	}
}

func TestTwoPlaintextHostsOnOnePortAreRefusedAtCreateWith422(t *testing.T) {
	e := setup(t, "")
	status, b := e.api("POST", "/v1/rooms", map[string]any{
		"image": roomImg, "env": map[string]string{}, "labels": map[string]string{},
		"egress": []egress{{"a.example.com", 25}, {"b.example.com", 25}},
	})
	if status != 422 || !strings.Contains(string(b), `"error"`) {
		t.Fatalf("= %d %s", status, b)
	}
	if ids := e.roomIDs(); len(ids) != 0 {
		t.Fatalf("a refused room left networks: %v", ids)
	}
}

// headerClient puts the proxy's headers on every request, as the brain does
// for the endpoint it was given, and the shim's own bearer in Authorization.
type headerClient struct{ h http.Header }

func (c headerClient) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range c.h {
		r.Header[k] = v
	}
	r.Header.Set("Authorization", "Bearer "+shimTok)
	return http.DefaultTransport.RoundTrip(r)
}

func TestTheShimAnswersThroughTheProxy(t *testing.T) {
	e := setup(t, "")
	id, _ := e.room([]egress{{"registry.npmjs.org", 443}},
		"sandbox", "shim", "--", "npx", "-y", "mcp-mail-server@2.1.3")
	// the mail server's own settings; fake, and never dialled by initialize
	// or tools/list
	_ = id

	status, b := e.api("GET", "/v1/rooms/"+id+"/ports/8080", nil)
	if status != 200 {
		t.Fatalf("ports = %d %s", status, b)
	}
	var ep struct {
		URL     string
		Headers map[string]string
	}
	_ = json.Unmarshal(b, &ep)
	if ep.URL != e.base+"/v1/rooms/"+id+"/ports/8080/proxy" || ep.Headers["X-Sandboxd-Token"] == "" {
		t.Fatalf("endpoint = %+v", ep)
	}
	hdr := http.Header{}
	for k, v := range ep.Headers {
		hdr.Set(k, v)
	}

	// npx downloads the package through the guard: wait for the shim.
	deadline := time.Now().Add(150 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ep.URL+"/healthz", nil)
		req.Header = hdr.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			logs, _ := e.docker("logs", "--tail", "30", "oa-sb-"+id)
			t.Fatalf("the shim never became healthy; last status %v; logs:\n%s", err, logs)
		}
		time.Sleep(time.Second)
	}

	// the proxy's token is required
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ep.URL+"/healthz", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("no proxy token = %d, want 401", resp.StatusCode)
		}
	}
	// the shim's own bearer is checked by the shim: a wrong one fails there
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, ep.URL+"/mcp", strings.NewReader(`{}`))
	req.Header = hdr.Clone()
	req.Header.Set("Authorization", "Bearer wrong")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 401 {
		t.Errorf("a wrong shim bearer: %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "brain", Version: "test"}, nil)
	cs, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:   ep.URL + "/mcp",
		HTTPClient: &http.Client{Transport: headerClient{hdr}},
	}, nil)
	if err != nil {
		t.Fatalf("initialize through the proxy: %v", err)
	}
	defer func() { _ = cs.Close() }()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools/list: %v %v", tools, err)
	}
	t.Logf("%d tools through the proxy", len(tools.Tools))

	// the room's rooms are gone with it
	if status, _ := e.api("DELETE", "/v1/rooms/"+id, nil); status != 204 {
		t.Fatalf("delete = %d", status)
	}
	if out, _ := e.docker("ps", "-aq", "--filter", "name=oa-sb-"+id); out != "" {
		t.Fatalf("the container survived delete: %s", out)
	}
	if status, _ := e.api("GET", "/v1/rooms/"+id+"/ports/8080", nil); status != 404 {
		t.Fatalf("a deleted room = %d, want 404", status)
	}
}
