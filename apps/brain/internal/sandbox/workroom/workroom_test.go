package workroom_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/workroom"
)

func TestAWorkspaceIsAPublicWorkspaceRoomRunningTheRole(t *testing.T) {
	t.Parallel()
	w := workroom.Want()
	if strings.Join(w.Argv, " ") != "sandbox workspace --listen :8081 --root /workspace" {
		t.Errorf("argv = %v", w.Argv)
	}
	if w.Network != sandbox.NetworkPublic || !w.Workspace || len(w.Egress) != 0 {
		t.Errorf("network %q, workspace %v, egress %v", w.Network, w.Workspace, w.Egress)
	}
	if w.Port != workroom.Port || w.HealthPath != "/healthz" || w.TokenEnv != "WORKSPACE_TOKEN" {
		t.Errorf("%+v", w)
	}
}

type ensurer struct {
	owner keeper.Owner
	team  uuid.UUID
}

func (e *ensurer) Ensure(_ context.Context, o keeper.Owner, team uuid.UUID, _ keeper.Want) (sandbox.Endpoint, error) {
	e.owner, e.team = o, team
	return sandbox.Endpoint{URL: "http://room"}, nil
}

func (e *ensurer) Touch(_ context.Context, o keeper.Owner) error {
	e.owner = o
	return nil
}

func TestTheRoomIsTheSessions(t *testing.T) {
	t.Parallel()
	e := &ensurer{}
	team, session := uuid.New(), uuid.New()
	if _, err := workroom.New(e).Ensure(t.Context(), team, session); err != nil {
		t.Fatal(err)
	}
	if e.owner != (keeper.Owner{Kind: "session", ID: session}) || e.team != team {
		t.Errorf("owner %+v, team %v", e.owner, e.team)
	}
	e.owner = keeper.Owner{}
	if err := workroom.New(e).Touch(t.Context(), session); err != nil || e.owner != (keeper.Owner{Kind: "session", ID: session}) {
		t.Errorf("touched %+v, %v", e.owner, err)
	}
}

// room answers each route with what answer says, and records what it was
// sent.
type room struct {
	srv  *httptest.Server
	got  map[string]any
	auth string
	path string
}

func newRoom(t *testing.T, status int, body string) *room {
	t.Helper()
	r := &room{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.path, r.auth = req.URL.Path, req.Header.Get("Authorization")
		b, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(b, &r.got)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *room) client() *workroom.Client {
	return workroom.NewClient(sandbox.Endpoint{URL: r.srv.URL + "/", Header: http.Header{"Authorization": {"Bearer tok"}}}, r.srv.Client())
}

func TestACallCarriesTheEndpointsHeadersAndItsArguments(t *testing.T) {
	t.Parallel()
	r := newRoom(t, 200, `{"stdout":"hi\n","exit_code":2}`)
	out, err := r.client().Exec(t.Context(), "echo hi", 30)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stdout != "hi\n" || out.ExitCode != 2 {
		t.Errorf("%+v", out)
	}
	if r.path != "/exec" || r.auth != "Bearer tok" || r.got["command"] != "echo hi" || r.got["timeout_seconds"] != 30.0 {
		t.Errorf("sent %s %q %v", r.path, r.auth, r.got)
	}
}

func TestAProblemIsTheModelsToFix(t *testing.T) {
	t.Parallel()
	r := newRoom(t, 422, `{"error":"old_string is in f.txt 2 times"}`)
	_, err := r.client().Edit(t.Context(), "f.txt", "a", "b", false)
	var p *workroom.Problem
	if !errors.As(err, &p) || p.Msg != "old_string is in f.txt 2 times" {
		t.Fatalf("err = %v, want the workspace's problem", err)
	}
}

func TestARoomTheProviderNoLongerHasIsGone(t *testing.T) {
	t.Parallel()
	for _, status := range []int{404, 502, 503} {
		r := newRoom(t, status, `no such room`)
		if _, err := r.client().Read(t.Context(), "a", 0, 0); !errors.Is(err, workroom.ErrGone) {
			t.Errorf("%d: %v, want ErrGone", status, err)
		}
	}
	r := newRoom(t, 500, `{"error":"the workspace failed: disk"}`)
	if _, err := r.client().Read(t.Context(), "a", 0, 0); err == nil || errors.Is(err, workroom.ErrGone) {
		t.Errorf("500: %v, want an error that is not ErrGone", err)
	}
}

// A provider's address can carry its token, so a failure never repeats it.
func TestAnUnreachableRoomIsGoneAndItsAddressIsNotRepeated(t *testing.T) {
	t.Parallel()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	c := workroom.NewClient(sandbox.Endpoint{URL: "http://" + addr + "/?token=provider-secret"}, http.DefaultClient)
	_, err = c.Glob(t.Context(), "**", "")
	if !errors.Is(err, workroom.ErrGone) {
		t.Fatalf("err = %v, want ErrGone", err)
	}
	if strings.Contains(err.Error(), "provider-secret") {
		t.Fatalf("the error repeats the address: %v", err)
	}
}
