package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// keptCount is the store's count of the files a run kept before.
type keptCount struct {
	n   int64
	err error
}

func (c keptCount) CountRunKeptFiles(context.Context, uuid.UUID) (int64, error) { return c.n, c.err }

// serverRooms are command servers' rooms: each server's files as its room
// serves them, a room not running, or one that cannot be reached.
type serverRooms struct {
	t      *testing.T
	mu     sync.Mutex
	files  map[string]map[string]string // server name, path, bytes
	down   map[string]error
	asked  []string
	served *httptest.Server
}

func newServerRooms(t *testing.T) *serverRooms {
	r := &serverRooms{t: t, files: map[string]map[string]string{}, down: map[string]error{}}
	r.served = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server := strings.TrimPrefix(req.URL.Path, "/")
		server = strings.TrimSuffix(server, "/file")
		r.mu.Lock()
		data, ok := r.files[server][req.URL.Query().Get("path")]
		r.mu.Unlock()
		if !ok {
			w.Header().Set("X-Room-Problem", "1")
			http.Error(w, req.URL.Query().Get("path")+" does not exist", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(data))
	}))
	t.Cleanup(r.served.Close)
	return r
}

func (r *serverRooms) Files(_ context.Context, _ uuid.UUID, s db.McpServer) (sandbox.Endpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, s.Name)
	if err := r.down[s.Name]; err != nil {
		return sandbox.Endpoint{}, err
	}
	return sandbox.Endpoint{URL: r.served.URL + "/" + s.Name + "/file"}, nil
}

// keepFixture is a run that may keep room files: granted the workspace when
// work is set, holding a tool of each command server named.
type keepFixture struct {
	*fixture
	ws    *fakeWorkspaces
	rows  *fileRows
	rooms *serverRooms
	count keptCount
}

func newKeepFixture(t *testing.T, work bool, commands ...string) *keepFixture {
	t.Helper()
	f, ws := newWorkspaceFixture(t, work)
	k := &keepFixture{fixture: f, ws: ws, rows: &fileRows{}, rooms: newServerRooms(t)}
	for _, name := range commands {
		g := commandServer(name)
		f.store.grants = append(f.store.grants, g)
		f.store.catalogue = append(f.store.catalogue, db.ListGrantedMCPToolsRow{
			McpServerID: g.McpServer.ID, Name: "save_attachment", Description: "saves it",
			InputSchema: []byte(`{"type":"object"}`),
		})
	}
	f.builder.WithToolFiles(ToolFiles{Store: k.rows, Objects: f.objects, Logger: quietLogger()})
	return k
}

func (k *keepFixture) withRoomFiles() *keepFixture {
	k.builder.WithRoomFiles(RoomFiles{Store: &k.count, Servers: k.rooms, Client: http.DefaultClient})
	return k
}

func (k *keepFixture) keep(t *testing.T, args string) (string, error) {
	t.Helper()
	r, err := callAs(t, tool(t, k.build(t), KeepFileToolName), "c1", args)
	return r.Output, err
}

// A run is given keep_file where a tool can have written a file in a room,
// and where it can be kept: a workspace, or a command server whose room the
// brain can read, with F2's keeper. Nowhere else.
func TestKeepFileIsGivenWhereARoomCanHoldAFile(t *testing.T) {
	for name, tc := range map[string]struct {
		make func(t *testing.T) *keepFixture
		want bool
	}{
		"a workspace":      {func(t *testing.T) *keepFixture { return newKeepFixture(t, true).withRoomFiles() }, true},
		"a command server": {func(t *testing.T) *keepFixture { return newKeepFixture(t, false, "mail").withRoomFiles() }, true},
		"neither":          {func(t *testing.T) *keepFixture { return newKeepFixture(t, false).withRoomFiles() }, false},
		"no room files":    {func(t *testing.T) *keepFixture { return newKeepFixture(t, true) }, false},
		"a URL server only": {func(t *testing.T) *keepFixture {
			k := newKeepFixture(t, false).withRoomFiles()
			g := urlServer("web", "https://mcp.example/mcp", false)
			k.store.grants = append(k.store.grants, g)
			k.store.catalogue = append(k.store.catalogue, db.ListGrantedMCPToolsRow{
				McpServerID: g.McpServer.ID, Name: "search", Description: "searches", InputSchema: []byte(`{"type":"object"}`),
			})
			return k
		}, false},
		"no keeper": {func(t *testing.T) *keepFixture {
			k := newKeepFixture(t, true).withRoomFiles()
			k.builder.files = nil
			return k
		}, false},
		"a command server, with no room to read": {func(t *testing.T) *keepFixture {
			k := newKeepFixture(t, false, "mail")
			k.builder.WithRoomFiles(RoomFiles{Store: &k.count})
			return k
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := held(tc.make(t).build(t), KeepFileToolName); got != tc.want {
				t.Errorf("keep_file held %v, want %v", got, tc.want)
			}
		})
	}
}

// A file the run wrote in its workspace is read from the room whole and
// kept as F2 keeps a tool's file: under keep_file, by the call, named by its
// path's last part.
func TestAWorkspaceFileIsKept(t *testing.T) {
	k := newKeepFixture(t, true).withRoomFiles()
	k.ws.room.answer("/file", http.StatusOK, "\x89PNG\r\n\x1a\nchart")

	out, err := k.keep(t, `{"path":"out/chart.png"}`)
	if err != nil {
		t.Fatalf("keep_file: %v", err)
	}
	if !strings.HasPrefix(out, "Kept chart.png (image/png, ") || !strings.Contains(out, "as file ") {
		t.Errorf("the model was told %q", out)
	}
	if len(k.rows.rows) != 1 {
		t.Fatalf("%d files kept", len(k.rows.rows))
	}
	row := k.rows.rows[0]
	if *row.Tool != KeepFileToolName || *row.CallID != "c1" || row.Filename != "chart.png" || row.MediaType != "image/png" {
		t.Errorf("kept %+v", row)
	}
}

// A file a command server saved is read from that server's room, which is
// asked only for one of the run's own servers.
func TestAServersSavedFileIsKept(t *testing.T) {
	k := newKeepFixture(t, false, "mail").withRoomFiles()
	k.rooms.files["mail"] = map[string]string{"invoices/a.pdf": "%PDF-1.7\n"}

	out, err := k.keep(t, `{"path":"invoices/a.pdf","server":"mail"}`)
	if err != nil {
		t.Fatalf("keep_file: %v", err)
	}
	if !strings.HasPrefix(out, "Kept a.pdf (application/pdf, ") {
		t.Errorf("the model was told %q", out)
	}
	if len(k.rows.rows) != 1 || k.rows.rows[0].Filename != "a.pdf" {
		t.Errorf("kept %+v", k.rows.rows)
	}
}

// What cannot be kept says why, in words the model can act on, and keeps
// nothing.
func TestAFileThatCannotBeKeptSaysWhy(t *testing.T) {
	for name, tc := range map[string]struct {
		work    bool
		setup   func(k *keepFixture)
		args    string
		says    string
		noAsked bool
	}{
		"no path":              {false, nil, `{"path":"  ","server":"mail"}`, "path is empty", true},
		"another server":       {false, nil, `{"path":"a.pdf","server":"github"}`, `no command MCP server named "github"`, true},
		"no workspace":         {false, nil, `{"path":"a.pdf"}`, "no workspace", true},
		"a missing file":       {false, nil, `{"path":"nope.pdf","server":"mail"}`, "nope.pdf does not exist", false},
		"a room not running":   {false, func(k *keepFixture) { k.rooms.down["mail"] = sandbox.ErrNoRoom }, `{"path":"a.pdf","server":"mail"}`, "is gone", false},
		"a room unreachable":   {false, func(k *keepFixture) { k.rooms.down["mail"] = errors.New("provider down") }, `{"path":"a.pdf","server":"mail"}`, "could not be reached", false},
		"a workspace replaced": {true, func(k *keepFixture) { k.ws.ensureErr = sandbox.ErrReplaced }, `{"path":"a.png"}`, "is gone", true},
		"not the arguments":    {false, nil, `{"path":5}`, "arguments are not", true},
	} {
		t.Run(name, func(t *testing.T) {
			k := newKeepFixture(t, tc.work, "mail").withRoomFiles()
			if tc.setup != nil {
				tc.setup(k)
			}
			_, err := k.keep(t, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("keep_file = %v, want it to say %q", err, tc.says)
			}
			if len(k.rows.rows) != 0 {
				t.Errorf("%d files kept", len(k.rows.rows))
			}
			if tc.noAsked && len(k.rooms.asked) != 0 {
				t.Errorf("a room was asked: %v", k.rooms.asked)
			}
		})
	}
}

// A run keeps twenty files at most, those before a resume among them; a
// server named wrong costs no slot.
func TestARunKeepsTwentyFilesAtMost(t *testing.T) {
	k := newKeepFixture(t, false, "mail").withRoomFiles()
	k.count.n = keptPerRun - 1
	k.rooms.files["mail"] = map[string]string{"a.pdf": "%PDF-1.7\n"}
	send := tool(t, k.build(t), KeepFileToolName)

	if _, err := callAs(t, send, "c0", `{"path":"a.pdf","server":"github"}`); err == nil {
		t.Fatal("a server not the run's was read")
	}
	if _, err := callAs(t, send, "c1", `{"path":"a.pdf","server":"mail"}`); err != nil {
		t.Fatalf("the twentieth: %v", err)
	}
	_, err := callAs(t, send, "c2", `{"path":"a.pdf","server":"mail"}`)
	if err == nil || !strings.Contains(err.Error(), "20 files") {
		t.Fatalf("the twenty-first = %v, want it refused", err)
	}
	if len(k.rows.rows) != 1 {
		t.Errorf("%d kept, want the twentieth alone", len(k.rows.rows))
	}
}

// How many a run kept before is read when it is built; a store that cannot
// say fails the build rather than lift the limit.
func TestACountThatCannotBeReadFailsTheBuild(t *testing.T) {
	k := newKeepFixture(t, true)
	down := errors.New("connection refused")
	k.builder.WithRoomFiles(RoomFiles{Store: keptCount{err: down}})
	if err := k.buildErr(t); !errors.Is(err, down) {
		t.Fatalf("Build = %v, want %v", err, down)
	}
}

// keep_file is a read of the room's own files, behind the gate as the
// workspace's reads are: a team's rule on it holds.
func TestARuleOnKeepFileHolds(t *testing.T) {
	k := newKeepFixture(t, false, "mail").withRoomFiles()
	k.rooms.files["mail"] = map[string]string{"a.pdf": "%PDF-1.7\n"}
	k.store.settings.ApprovalMode = string(approval.Writes)
	person := uuid.New()
	k.run.UserID = &person
	d := &ruled{desk: &desk{byCall: map[string]db.Approval{}}}
	d.rules = []db.ApprovalRule{{TargetKind: "tool", Target: KeepFileToolName, Mode: "deny"}}
	k.builder.WithApprovals(d)

	_, err := k.keep(t, `{"path":"a.pdf","server":"mail"}`)
	if err == nil || !strings.Contains(err.Error(), "does not allow keep_file") {
		t.Fatalf("keep_file = %v, want the rule's refusal", err)
	}
	if len(k.rows.rows) != 0 {
		t.Errorf("%d kept under a deny rule", len(k.rows.rows))
	}
}

// A replay holds a stand-in, as its rooms have moved on; a server's tool may
// not take the name.
func TestKeepFileInAReplayAndAsAName(t *testing.T) {
	k := newKeepFixture(t, true).withRoomFiles()
	k.builder.replay = true
	b := k.build(t)
	if !held(b, KeepFileToolName) || b.reads[KeepFileToolName] {
		t.Errorf("a replay holds keep_file %v, as a read %v", held(b, KeepFileToolName), b.reads[KeepFileToolName])
	}
	if !reserved(KeepFileToolName, reservedFor{}) {
		t.Error("an MCP tool may be named keep_file")
	}

	// A replay of a run that could keep nothing holds nothing either.
	none := newKeepFixture(t, false).withRoomFiles()
	none.builder.replay = true
	if held(none.build(t), KeepFileToolName) {
		t.Error("a replay of a run with no room holds keep_file")
	}
}
