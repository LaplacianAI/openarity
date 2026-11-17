package docker_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/docker"
)

type call struct{ Method, Path, Query, Body string }

// daemon is an Engine API on a unix socket that records what it is sent. The
// socket lives under a short path: unix socket paths are limited to about a
// hundred bytes, and t.TempDir is longer than that on some systems.
func daemon(t *testing.T, h func(call) (int, string)) (*docker.Client, func() []call) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []call
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := call{r.Method, r.URL.Path, r.URL.RawQuery, string(b)}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		status, body := h(c)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return docker.New(sock), func() []call {
		mu.Lock()
		defer mu.Unlock()
		return append([]call(nil), calls...)
	}
}

func TestCreateNetworkMakesAnInternalBridgeWithLabels(t *testing.T) {
	t.Parallel()
	c, calls := daemon(t, func(call) (int, string) { return 201, `{"Id":"n"}` })
	if err := c.CreateNetwork(t.Context(), "oa-sb-x", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	got := calls()[0]
	var body map[string]any
	_ = json.Unmarshal([]byte(got.Body), &body)
	if got.Method != "POST" || got.Path != "/v1.44/networks/create" || body["Internal"] != true || body["Driver"] != "bridge" ||
		body["Name"] != "oa-sb-x" || fmt.Sprint(body["Labels"]) != "map[k:v]" {
		t.Fatalf("%+v", got)
	}
}

// Nothing but what Container holds can reach the create body, and the body
// carries every hardening field the daemon needs to enforce.
func TestCreateContainerSendsTheWholeHardening(t *testing.T) {
	t.Parallel()
	c, calls := daemon(t, func(call) (int, string) { return 201, `{"Id":"abc"}` })
	id, err := c.CreateContainer(t.Context(), "oa-sb-x", docker.Container{
		Image: "room:1", Entrypoint: []string{"a", "b"}, Env: []string{"A=1"}, User: "65534",
		HostConfig: docker.HostConfig{
			ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
			PidsLimit: 256, Memory: 1, MemorySwap: 1, NanoCPUs: 5, NetworkMode: "oa-sb-x",
			Tmpfs: map[string]string{"/tmp": "rw"}, ExtraHosts: []string{"h:1.2.3.4"},
			RestartPolicy: docker.RestartPolicy{Name: "no"},
		},
	})
	if err != nil || id != "abc" {
		t.Fatalf("%q %v", id, err)
	}
	got := calls()[0]
	if got.Path != "/v1.44/containers/create" || got.Query != "name=oa-sb-x" {
		t.Fatalf("%+v", got)
	}
	var body struct {
		Image, User string
		Entrypoint  []string
		Cmd         []string
		HostConfig  map[string]any
	}
	_ = json.Unmarshal([]byte(got.Body), &body)
	h := body.HostConfig
	if body.User != "65534" || len(body.Cmd) != 0 || strings.Join(body.Entrypoint, " ") != "a b" ||
		h["ReadonlyRootfs"] != true || h["Privileged"] != false || h["PidsLimit"] != float64(256) ||
		h["NetworkMode"] != "oa-sb-x" || h["NanoCpus"] != float64(5) || h["MemorySwap"] != float64(1) {
		t.Fatalf("body = %s", got.Body)
	}
	if _, ok := h["Binds"]; ok {
		t.Fatal("the create body has a Binds field")
	}
}

func TestANotFoundIsRecognisedAndCarriesTheDaemonsMessage(t *testing.T) {
	t.Parallel()
	c, _ := daemon(t, func(call) (int, string) { return 404, `{"message":"no such network"}` })
	_, err := c.InspectNetwork(t.Context(), "x")
	if !docker.IsNotFound(err) || !strings.Contains(err.Error(), "no such network") {
		t.Fatalf("err = %v", err)
	}
}

func TestInspectNetworkStripsTheAddressPrefixLength(t *testing.T) {
	t.Parallel()
	c, _ := daemon(t, func(call) (int, string) {
		return 200, `{"Id":"n","Name":"x","Labels":{"a":"b"},"Internal":true,"Containers":{"c1":{"Name":"self","IPv4Address":"172.18.0.2/16"}}}`
	})
	n, err := c.InspectNetwork(t.Context(), "x")
	if err != nil || n.Containers["c1"].IPv4 != "172.18.0.2" || n.Containers["c1"].Name != "self" || n.Labels["a"] != "b" {
		t.Fatalf("%+v %v", n, err)
	}
}

func TestListNetworksFiltersByLabel(t *testing.T) {
	t.Parallel()
	c, calls := daemon(t, func(call) (int, string) { return 200, `[{"Id":"n","Name":"x"}]` })
	ns, err := c.ListNetworks(t.Context(), "oa.sandboxd.room")
	if err != nil || len(ns) != 1 {
		t.Fatalf("%v %v", ns, err)
	}
	if q := calls()[0].Query; !strings.Contains(q, "filters=") || !strings.Contains(q, "oa.sandboxd.room") {
		t.Fatalf("query = %s", q)
	}
}

func TestConnectingToANetworkAlreadyJoinedIsNotAnError(t *testing.T) {
	t.Parallel()
	c, _ := daemon(t, func(call) (int, string) {
		return 403, `{"message":"endpoint with name self already exists in network x"}`
	})
	if err := c.ConnectNetwork(t.Context(), "x", "self"); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveContainerForcesAndDropsItsVolumes(t *testing.T) {
	t.Parallel()
	c, calls := daemon(t, func(call) (int, string) { return 204, "" })
	if err := c.RemoveContainer(t.Context(), "oa-sb-x"); err != nil {
		t.Fatal(err)
	}
	got := calls()[0]
	if got.Method != "DELETE" || !strings.Contains(got.Query, "force=1") {
		t.Fatalf("%+v", got)
	}
}

func TestPingFailsWhenNothingListens(t *testing.T) {
	t.Parallel()
	if err := docker.New("/nonexistent/docker.sock").Ping(t.Context()); err == nil {
		t.Fatal("ping succeeded")
	}
}
