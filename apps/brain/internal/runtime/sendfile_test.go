package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/stream"
)

// fileSends is the store send_file writes to: the conversation's files are
// the fixture's, and each file reply written is kept.
type fileSends struct {
	*fakeStore
	reach    db.GetSessionFileReachRow
	reachErr error
	// inserted answers each insert: 1 a reply written, 0 none.
	inserted  int64
	insertErr error

	mu      sync.Mutex
	replies []db.InsertFileReplyParams
}

func (s *fileSends) GetSessionFileReach(context.Context, uuid.UUID) (db.GetSessionFileReachRow, error) {
	return s.reach, s.reachErr
}

func (s *fileSends) InsertFileReply(_ context.Context, arg db.InsertFileReplyParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, arg)
	return s.inserted, s.insertErr
}

type sentNote struct {
	kind    string
	payload any
}

// sendFixture is a top-level run in a conversation holding one file, whose
// builder sends files where reach says, writing notes to sentNote and kicks to
// kicked.
type sendFixture struct {
	*fixture
	sends   *fileSends
	file    db.Attachment
	notes   []sentNote
	noteErr error
	kicked  []string
}

func newSendFixture(reach db.GetSessionFileReachRow) *sendFixture {
	f := &sendFixture{fixture: newFixture()}
	f.run.SessionID = uuid.New()
	f.run.ReplyToChannel = !reach.Streamed
	f.file = attachedFile(f.run.SessionID, "report.pdf", "application/pdf", 9)
	f.store.attached = []db.Attachment{f.file}
	f.sends = &fileSends{fakeStore: f.store, reach: reach, inserted: 1}
	f.builder.WithFileSends(FileSends{Store: f.sends, SendsFiles: func(p string) bool { return p == "telegram" }})
	f.builder.note = func(_ context.Context, _ uuid.UUID, _, kind string, p any) error {
		f.notes = append(f.notes, sentNote{kind, p})
		return f.noteErr
	}
	f.builder.kickFile = func(_ uuid.UUID, call string) { f.kicked = append(f.kicked, call) }
	return f
}

var (
	dashboard = db.GetSessionFileReachRow{Streamed: true, Provider: "dashboard", Postable: true}
	telegram  = db.GetSessionFileReachRow{Provider: "telegram", Postable: true}
)

func held(b Built, name string) bool {
	for _, tl := range b.Spec.Tools {
		if tl.Name == name {
			return true
		}
	}
	return false
}

// send calls send_file as call c1 would.
func (f *sendFixture) send(t *testing.T, args string) (string, error) {
	t.Helper()
	ctx := agent.WithToolCallID(t.Context(), "c1")
	return tool(t, f.build(t), SendFileToolName).Invoke(ctx, json.RawMessage(args))
}

// A top-level run is given send_file where its conversation takes a file:
// the dashboard's, or a channel it answers whose provider sends files and
// can be posted to. Nowhere else.
func TestSendFileIsGivenWhereTheFileCanArrive(t *testing.T) {
	for name, tc := range map[string]struct {
		reach  db.GetSessionFileReachRow
		mutate func(*sendFixture)
		want   bool
	}{
		"the dashboard":                 {dashboard, nil, true},
		"a telegram chat":               {telegram, nil, true},
		"a provider that sends none":    {db.GetSessionFileReachRow{Provider: "slack", Postable: true}, nil, false},
		"a channel with no reply URL":   {db.GetSessionFileReachRow{Provider: "telegram"}, nil, false},
		"a channel the run answers not": {telegram, func(f *sendFixture) { f.run.ReplyToChannel = false }, false},
		"a delegate":                    {dashboard, func(f *sendFixture) { f.run.ParentRunID = new(uuid.New()) }, false},
		"no file to send":               {dashboard, func(f *sendFixture) { f.store.attached = nil }, false},
		"no file sends":                 {dashboard, func(f *sendFixture) { f.builder.sends = nil }, false},
		"no recorder":                   {dashboard, func(f *sendFixture) { f.builder.note = nil }, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSendFixture(tc.reach)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			if got := held(f.build(t), SendFileToolName); got != tc.want {
				t.Errorf("send_file given = %v, want %v", got, tc.want)
			}
		})
	}
}

// A replay holds send_file wherever a top-level run might: it has no channel
// to ask, and a stand-in it never makes.
func TestAReplayHoldsSendFileAsAStandIn(t *testing.T) {
	f := newSendFixture(dashboard)
	f.builder.sends, f.builder.note, f.builder.replay = nil, nil, true
	if !held(f.build(t), SendFileToolName) {
		t.Error("a replay of a run that could send files holds no send_file")
	}
	f.run.ParentRunID = new(uuid.New())
	if held(f.build(t), SendFileToolName) {
		t.Error("a replay of a delegate holds send_file")
	}
}

func TestWhereFilesCanGoThatCannotBeReadFailsTheBuild(t *testing.T) {
	f := newSendFixture(dashboard)
	down := errors.New("connection refused")
	f.sends.reachErr = down
	if err := f.buildErr(t); !errors.Is(err, down) {
		t.Fatalf("Build = %v, want %v", err, down)
	}
}

// On the dashboard the file is the run's file_sent event alone: no reply.
func TestOnTheDashboardAFileSentIsAnEvent(t *testing.T) {
	f := newSendFixture(dashboard)
	out, err := f.send(t, `{"id":"`+f.file.ID.String()+`","caption":"  Q3 numbers  "}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Sent report.pdf") {
		t.Errorf("out = %q", out)
	}
	want := fileSentPayload{
		ToolCallID: "c1", ID: f.file.ID, Filename: "report.pdf", MediaType: "application/pdf", Size: 9, Caption: "Q3 numbers",
	}
	if len(f.notes) != 1 || f.notes[0].kind != "file_sent" || f.notes[0].payload != want {
		t.Errorf("notes = %+v, want %+v", f.notes, want)
	}
	if len(f.sends.replies) != 0 || len(f.kicked) != 0 {
		t.Errorf("replies %+v, kicked %v on the dashboard", f.sends.replies, f.kicked)
	}
}

// In a channel the file is also a reply, by the call that sent it, with its
// caption, kicked to go out now.
func TestInAChannelAFileSentIsAReplyKickedNow(t *testing.T) {
	f := newSendFixture(telegram)
	if _, err := f.send(t, `{"id":"`+f.file.ID.String()+`","caption":"Q3"}`); err != nil {
		t.Fatal(err)
	}
	if len(f.sends.replies) != 1 {
		t.Fatalf("replies = %+v", f.sends.replies)
	}
	r := f.sends.replies[0]
	if r.RunID != f.run.ID || r.AttachmentID != f.file.ID || r.CallID != "c1" || string(r.Detail) != `{"caption":"Q3"}` {
		t.Errorf("reply = %+v (detail %s)", r, r.Detail)
	}
	if len(f.kicked) != 1 || f.kicked[0] != "c1" || len(f.notes) != 1 {
		t.Errorf("kicked %v, notes %+v", f.kicked, f.notes)
	}

	// No caption is no detail.
	f = newSendFixture(telegram)
	if _, err := f.send(t, `{"id":"`+f.file.ID.String()+`"}`); err != nil {
		t.Fatal(err)
	}
	if d := f.sends.replies[0].Detail; d != nil {
		t.Errorf("detail = %s, want none", d)
	}
}

// A call made again writes no second reply, so it is not kicked again.
func TestACallMadeAgainIsNotKickedAgain(t *testing.T) {
	f := newSendFixture(telegram)
	f.sends.inserted = 0
	if _, err := f.send(t, `{"id":"`+f.file.ID.String()+`"}`); err != nil {
		t.Fatal(err)
	}
	if len(f.kicked) != 0 {
		t.Errorf("kicked %v for a reply already written", f.kicked)
	}
}

// What cannot be sent writes nothing at all.
func TestAFileThatCannotBeSentWritesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		args   func(f *sendFixture) string
		ctx    func(context.Context) context.Context
		errHas string
	}{
		"not an id":            {func(*sendFixture) string { return `{"id":"report.pdf"}` }, nil, "not a file's id"},
		"another conversation": {func(*sendFixture) string { return `{"id":"` + uuid.NewString() + `"}` }, nil, "no file"},
		"a caption too long": {func(f *sendFixture) string {
			return `{"id":"` + f.file.ID.String() + `","caption":"` + strings.Repeat("é", maxCaption+1) + `"}`
		}, nil, "1024"},
		"no call id": {
			func(f *sendFixture) string { return `{"id":"` + f.file.ID.String() + `"}` },
			func(context.Context) context.Context { return context.Background() }, "no id",
		},
		"a call id too long": {
			func(f *sendFixture) string { return `{"id":"` + f.file.ID.String() + `"}` },
			func(ctx context.Context) context.Context {
				return agent.WithToolCallID(ctx, strings.Repeat("c", maxCallID+1))
			}, "no id",
		},
		"not JSON": {func(*sendFixture) string { return `{` }, nil, "not valid JSON"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSendFixture(telegram)
			ctx := agent.WithToolCallID(t.Context(), "c1")
			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}
			_, err := tool(t, f.build(t), SendFileToolName).Invoke(ctx, json.RawMessage(tc.args(f)))
			if err == nil || !strings.Contains(err.Error(), tc.errHas) {
				t.Fatalf("err = %v, want one saying %q", err, tc.errHas)
			}
			if len(f.sends.replies)+len(f.notes)+len(f.kicked) != 0 {
				t.Errorf("wrote replies %v, notes %v, kicks %v", f.sends.replies, f.notes, f.kicked)
			}
		})
	}
}

// A reply that cannot be written is the call failing, and nothing is told
// the dashboard: the file did not go.
func TestAReplyThatCannotBeWrittenIsTheCallFailing(t *testing.T) {
	f := newSendFixture(telegram)
	f.sends.insertErr = errors.New("connection reset")
	if _, err := f.send(t, `{"id":"`+f.file.ID.String()+`"}`); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("err = %v", err)
	}
	if len(f.notes)+len(f.kicked) != 0 {
		t.Errorf("notes %v, kicks %v for a file not sent", f.notes, f.kicked)
	}
}

func TestAnEventThatCannotBeWrittenIsTheCallFailing(t *testing.T) {
	f := newSendFixture(dashboard)
	f.noteErr = errors.New("the recorder stopped")
	if _, err := f.send(t, `{"id":"`+f.file.ID.String()+`"}`); err == nil || !strings.Contains(err.Error(), "recorded") {
		t.Fatalf("err = %v", err)
	}
}

// A run sends ten files at most, each attempt counted, a failed one too.
func TestARunSendsTenFilesAtMost(t *testing.T) {
	f := newSendFixture(telegram)
	f.sends.insertErr = errors.New("connection reset")
	send := tool(t, f.build(t), SendFileToolName)
	args := json.RawMessage(`{"id":"` + f.file.ID.String() + `"}`)
	for i := range filesPerRun {
		if _, err := send.Invoke(agent.WithToolCallID(t.Context(), "c"+string(rune('a'+i))), args); err == nil {
			t.Fatalf("send %d succeeded with the store down", i+1)
		}
	}
	f.sends.insertErr = nil
	_, err := send.Invoke(agent.WithToolCallID(t.Context(), "last"), args)
	if err == nil || !strings.Contains(err.Error(), "10 files") {
		t.Fatalf("the eleventh send = %v, want it refused", err)
	}
	if len(f.sends.replies) != filesPerRun {
		t.Errorf("%d replies tried, want %d", len(f.sends.replies), filesPerRun)
	}
}

// A caption is what every platform takes: no control characters but line
// breaks, trimmed, and counted in characters, not bytes.
func TestACaptionIsCleanedAndCountedInCharacters(t *testing.T) {
	got, err := captionOf("  one\x00\u2028two\nthree\t ")
	if err != nil || got != "onetwo\nthree" {
		t.Errorf("caption = %q, %v", got, err)
	}
	if _, err := captionOf(strings.Repeat("é", maxCaption)); err != nil {
		t.Errorf("a caption of %d characters (%d bytes) refused: %v", maxCaption, 2*maxCaption, err)
	}
}

// The file_sent event is read back as the stream's file by its JSON names
// (api/sessions/stream.go): a field renamed here would reach the dashboard
// empty.
func TestTheFileSentEventIsTheStreamsFile(t *testing.T) {
	id := uuid.New()
	raw, err := json.Marshal(fileSentPayload{
		ToolCallID: "c1", ID: id, Filename: "report.pdf", MediaType: "application/pdf", Size: 9, Caption: "Q3",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ToolCallID string `json:"tool_call_id"`
		stream.File
		Caption string `json:"caption"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := stream.File{ID: id.String(), Filename: "report.pdf", MediaType: "application/pdf", Size: 9}
	if got.ToolCallID != "c1" || got.File != want || got.Caption != "Q3" {
		t.Errorf("read back %+v from %s", got, raw)
	}
}
