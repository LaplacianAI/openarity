package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/roomfile"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// keptCount is the store's count of the files a run kept before.
type keptCount struct {
	n   int64
	err error
}

func (c keptCount) CountRunKeptFiles(_ context.Context, arg db.CountRunKeptFilesParams) (int64, error) {
	if arg.Tool != KeepFileToolName {
		return 0, fmt.Errorf("counted %q's files, not keep_file's", arg.Tool)
	}
	return c.n, c.err
}

// serverRooms are command servers' rooms: each server's files as its room
// serves them, a room not running, or one that cannot be reached.
type serverRooms struct {
	t      *testing.T
	mu     sync.Mutex
	files  map[string]map[string]string // server name, path, bytes
	said   map[string]int64             // a path's Content-Length, when it is not the bytes'
	down   map[string]error
	asked  []string
	served *httptest.Server
}

func newServerRooms(t *testing.T) *serverRooms {
	r := &serverRooms{t: t, files: map[string]map[string]string{}, said: map[string]int64{}, down: map[string]error{}}
	r.served = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server := strings.TrimPrefix(req.URL.Path, "/")
		server = strings.TrimSuffix(server, "/file")
		r.mu.Lock()
		data, ok := r.files[server][req.URL.Query().Get("path")]
		said, lies := r.said[req.URL.Query().Get("path")]
		r.mu.Unlock()
		if !ok {
			w.Header().Set("X-Room-Problem", "1")
			http.Error(w, req.URL.Query().Get("path")+" does not exist", http.StatusNotFound)
			return
		}
		if lies {
			w.Header().Set("Content-Length", strconv.FormatInt(said, 10))
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
		"no path":              {args: `{"path":"  ","server":"mail"}`, says: "path is empty", noAsked: true},
		"another server":       {args: `{"path":"a.pdf","server":"github"}`, says: `no command MCP server named "github"`, noAsked: true},
		"no workspace":         {args: `{"path":"a.pdf"}`, says: "no workspace", noAsked: true},
		"a missing file":       {args: `{"path":"nope.pdf","server":"mail"}`, says: "nope.pdf does not exist"},
		"a room not running":   {setup: func(k *keepFixture) { k.rooms.down["mail"] = sandbox.ErrNoRoom }, args: `{"path":"a.pdf","server":"mail"}`, says: "room is not running"},
		"an empty file":        {setup: func(k *keepFixture) { k.rooms.files["mail"] = map[string]string{"e.txt": ""} }, args: `{"path":"e.txt","server":"mail"}`, says: "e.txt is empty"},
		"a room unreachable":   {setup: func(k *keepFixture) { k.rooms.down["mail"] = errors.New("provider down") }, args: `{"path":"a.pdf","server":"mail"}`, says: "could not be reached"},
		"a workspace replaced": {work: true, setup: func(k *keepFixture) { k.ws.ensureErr = sandbox.ErrReplaced }, args: `{"path":"a.png"}`, says: "is gone", noAsked: true},
		"not the arguments":    {args: `{"path":5}`, says: "arguments are not", noAsked: true},
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

// A file is kept under its path's last part, without what could end a line
// of the tool's answer, and named something when nothing is left.
func TestAKeptFileIsNamedCleanly(t *testing.T) {
	for path, want := range map[string]string{
		"out/chart.png":                   "chart.png",
		"out/a\nb\u202e.png":              "ab.png",
		"out/[x].csv":                     "x.csv",
		"out/" + strings.Repeat("é", 300): strings.Repeat("é", maxName),
		"/":                               "file",
		"..":                              "file",
	} {
		if got := nameOf(path); got != want {
			t.Errorf("nameOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// A run that holds keep_file holds the attachment tools with it, though its
// conversation has no file yet and it holds no MCP tool: the tool list is
// fixed for the run, and the file it keeps is read in that same run.
func TestKeepFileComesWithTheAttachmentTools(t *testing.T) {
	for name, tc := range map[string]struct {
		k    func(t *testing.T) *keepFixture
		want bool
	}{
		"a workspace":  {func(t *testing.T) *keepFixture { return newKeepFixture(t, true).withRoomFiles() }, true},
		"no keep_file": {func(t *testing.T) *keepFixture { return newKeepFixture(t, true) }, false},
		"a replay of a run": {func(t *testing.T) *keepFixture {
			k := newKeepFixture(t, true).withRoomFiles()
			k.builder.replay = true
			return k
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			b := tc.k(t).build(t)
			if held(b, KeepFileToolName) != tc.want {
				t.Fatalf("keep_file held %v, want %v", held(b, KeepFileToolName), tc.want)
			}
			for _, n := range []string{AttachmentsToolName, ReadAttachmentToolName} {
				if held(b, n) != tc.want {
					t.Errorf("%s held %v, want %v", n, held(b, n), tc.want)
				}
			}
		})
	}
}

// The workspace is the session's own room, so keeping from it is a read. A
// command server's room is shared by the team's sessions, so a run that can
// reach one is judged as send_file is: a guest is asked, a run nobody watches
// is refused where it would ask.
func TestKeepFileFromASharedRoomIsJudgedAsSharing(t *testing.T) {
	for _, tc := range []struct {
		servers []string
		who     string
		want    string
	}{
		{nil, "a member", "made"},
		{nil, "a guest", "made"},
		{nil, "nobody present", "made"},
		{[]string{"mail"}, "a member", "made"},
		{[]string{"mail"}, "a guest", "asks"},
		{[]string{"mail"}, "nobody present", "refused"},
	} {
		t.Run(fmt.Sprintf("%v, %s", tc.servers, tc.who), func(t *testing.T) {
			k := newKeepFixture(t, true, tc.servers...).withRoomFiles()
			k.ws.room.answer("/file", http.StatusOK, "%PDF-1.7\n")
			k.store.settings.ApprovalMode = string(approval.Writes)
			k.store.settings.ApprovalTtlMinutes = 60
			k.run.UserID = nil
			if tc.who != "a guest" {
				person := uuid.New()
				k.run.UserID = &person
				k.run.Unattended = tc.who == "nobody present"
			}
			k.builder.WithApprovals(&ruled{desk: &desk{byCall: map[string]db.Approval{}}})

			_, err := k.keep(t, `{"path":"a.pdf"}`)
			if got := sendOutcome(err); got != tc.want {
				t.Fatalf("%s, want %s", got, tc.want)
			}
			if kept := len(k.rows.rows) == 1; kept != (tc.want == "made") {
				t.Errorf("kept %v", kept)
			}
		})
	}
}

// The name kept is the cleaned one, in the row as in the answer.
func TestAFileIsKeptUnderItsCleanName(t *testing.T) {
	k := newKeepFixture(t, true).withRoomFiles()
	k.ws.room.answer("/file", http.StatusOK, "%PDF-1.7\n")
	out, err := k.keep(t, `{"path":"out/a\nb.pdf"}`)
	if err != nil {
		t.Fatalf("keep_file: %v", err)
	}
	if len(k.rows.rows) != 1 || k.rows.rows[0].Filename != "ab.pdf" || !strings.HasPrefix(out, "Kept ab.pdf ") {
		t.Errorf("kept %+v, told %q", k.rows.rows, out)
	}
}

// A call waiting its turn behind another's read can be given up on: it
// returns the caller's error and asks no room.
func TestAKeepWaitingItsTurnCanBeGivenUpOn(t *testing.T) {
	rooms := newServerRooms(t)
	k := &keepFile{
		servers: map[string]db.McpServer{"mail": {Name: "mail"}},
		rooms:   rooms, client: http.DefaultClient, one: make(chan struct{}, 1),
	}
	k.one <- struct{}{} // another call is reading
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := k.invoke(ctx, []byte(`{"path":"a.pdf","server":"mail"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("invoke = %v, want context.Canceled", err)
	}
	if len(rooms.asked) != 0 {
		t.Errorf("a room was asked: %v", rooms.asked)
	}
}

// A file over the old 25 MiB is kept, streamed from the room. One the room
// says is over 100 MiB is refused by its length, and one that ends short of
// the length it said keeps nothing.
func TestALargeFileIsKeptAndOneTooLargeOrCutShortIsNot(t *testing.T) {
	k := newKeepFixture(t, false, "mail").withRoomFiles()
	big := "%PDF-1.7\n" + strings.Repeat("x", 30<<20)
	k.rooms.files["mail"] = map[string]string{"big.pdf": big, "huge.zip": "", "cut.zip": "PK\x03\x04"}
	k.rooms.said["huge.zip"] = 101 << 20
	k.rooms.said["cut.zip"] = 1 << 20
	keep := tool(t, k.build(t), KeepFileToolName)

	out, err := callAs(t, keep, "c1", `{"path":"big.pdf","server":"mail"}`)
	if err != nil || !strings.HasPrefix(out.Output, "Kept big.pdf (application/pdf, 30.0 MB) as file ") {
		t.Fatalf("keep_file = %q, %v", out.Output, err)
	}
	if len(k.rows.rows) != 1 || k.rows.rows[0].SizeBytes != int64(len(big)) {
		t.Fatalf("kept %+v", k.rows.rows)
	}

	if _, err := callAs(t, keep, "c2", `{"path":"huge.zip","server":"mail"}`); err == nil || !strings.Contains(err.Error(), "is 105906176 bytes") {
		t.Errorf("a file over 100 MiB = %v, want it refused by its length", err)
	}
	if _, err := callAs(t, keep, "c3", `{"path":"cut.zip","server":"mail"}`); err == nil || !strings.Contains(err.Error(), "nothing was kept") {
		t.Errorf("a file cut short = %v, want nothing kept", err)
	}
	if len(k.rows.rows) != 1 {
		t.Errorf("%d rows, want the large file's alone", len(k.rows.rows))
	}
}

// stubKeeper fails a keep as it is told.
type stubKeeper struct{ err error }

func (s stubKeeper) KeepStream(_ context.Context, _, _ string, r io.Reader) (streamed, error) {
	_, _ = io.Copy(io.Discard, r)
	return streamed{}, s.err
}

// A room's refusal that comes while the file streams, such as one past the
// most, reaches the model in the room's words; any other failure says
// nothing was kept, without the store's.
func TestAFailureWhileStreamingSaysWhy(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		says string
	}{
		"the room's refusal": {&roomfile.Problem{Msg: "out.bin is over 104857600 bytes"}, "out.bin is over 104857600 bytes"},
		"nothing in it":      {errEmpty, "out.bin is empty"},
		"the store":          {errors.New("s3: access denied for bucket openarity"), "could not be read and kept whole"},
	} {
		t.Run(name, func(t *testing.T) {
			rooms := newServerRooms(t)
			rooms.files["mail"] = map[string]string{"out.bin": "x"}
			k := &keepFile{
				keep:    stubKeeper{tc.err},
				servers: map[string]db.McpServer{"mail": {Name: "mail"}},
				rooms:   rooms, client: http.DefaultClient, one: make(chan struct{}, 1),
			}
			_, err := k.invoke(t.Context(), []byte(`{"path":"out.bin","server":"mail"}`))
			if err == nil || !strings.Contains(err.Error(), tc.says) || strings.Contains(err.Error(), "s3:") {
				t.Errorf("invoke = %v, want it to say %q", err, tc.says)
			}
		})
	}
}
