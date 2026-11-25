package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
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
	// queued is what FileReplyExists answers for a reply none was written for.
	queued    bool
	existsErr error
	// before is how many files the run sent before it was built.
	before   int64
	countErr error

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

func (s *fileSends) FileReplyExists(context.Context, db.FileReplyExistsParams) (bool, error) {
	return s.queued, s.existsErr
}

func (s *fileSends) CountRunFilesSent(context.Context, uuid.UUID) (int64, error) {
	return s.before, s.countErr
}

func (s *fileSends) written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.replies)
}

type sentNote struct {
	kind    string
	payload any
}

// sendFixture is a top-level run in a conversation holding one file, whose
// builder sends files where reach says, writing notes to notes and kicks to
// kicked.
type sendFixture struct {
	*fixture
	sends   *fileSends
	file    db.Attachment
	log     *bytes.Buffer
	mu      sync.Mutex
	notes   []sentNote
	noteErr error
	kicked  []string
}

func newSendFixture(reach db.GetSessionFileReachRow) *sendFixture {
	f := &sendFixture{fixture: newFixture(), log: &bytes.Buffer{}}
	f.run.SessionID = uuid.New()
	f.run.ReplyToChannel = !reach.Streamed
	f.file = attachedFile(f.run.SessionID, "report.pdf", "application/pdf", 9)
	f.store.attached = []db.Attachment{f.file}
	f.sends = &fileSends{fakeStore: f.store, reach: reach, inserted: 1}
	f.builder.WithFileSends(FileSends{
		Store:      f.sends,
		SendsFiles: func(p string) bool { return p == "telegram" || p == "custom" },
		Logger:     slog.New(slog.NewTextHandler(f.log, nil)),
	})
	f.builder.note = func(_ context.Context, _ uuid.UUID, _, kind string, p any) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.notes = append(f.notes, sentNote{kind, p})
		return f.noteErr
	}
	f.builder.kickFile = func(_ uuid.UUID, call string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.kicked = append(f.kicked, call)
	}
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

func (f *sendFixture) args() json.RawMessage {
	return json.RawMessage(`{"id":"` + f.file.ID.String() + `"}`)
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
		"the dashboard":                     {dashboard, nil, true},
		"a telegram chat":                   {telegram, nil, true},
		"a custom channel with a reply URL": {db.GetSessionFileReachRow{Provider: "custom", Postable: true}, nil, true},
		"a provider that sends none":        {db.GetSessionFileReachRow{Provider: "slack", Postable: true}, nil, false},
		"a channel with no reply URL":       {db.GetSessionFileReachRow{Provider: "custom"}, nil, false},
		"a channel the run answers not":     {telegram, func(f *sendFixture) { f.run.ReplyToChannel = false }, false},
		"a delegate":                        {dashboard, func(f *sendFixture) { f.run.ParentRunID = new(uuid.New()) }, false},
		"no file to send":                   {dashboard, func(f *sendFixture) { f.store.attached = nil }, false},
		"no file sends":                     {dashboard, func(f *sendFixture) { f.builder.sends = nil }, false},
		"no recorder":                       {dashboard, func(f *sendFixture) { f.builder.note = nil }, false},
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
	b := f.build(t)
	if !held(b, SendFileToolName) {
		t.Error("a replay of a run that could send files holds no send_file")
	}
	// Not a read: a replay never makes it live, as it would hand a file over.
	if b.reads[SendFileToolName] {
		t.Error("a replay may make send_file live")
	}
	f.run.ParentRunID = new(uuid.New())
	if held(f.build(t), SendFileToolName) {
		t.Error("a replay of a delegate holds send_file")
	}
}

func TestWhereFilesCanGoOrHowManyWentThatCannotBeReadFailsTheBuild(t *testing.T) {
	for name, fail := range map[string]func(*fileSends, error){
		"where they go": func(s *fileSends, err error) { s.reachErr = err },
		"how many went": func(s *fileSends, err error) { s.countErr = err },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSendFixture(dashboard)
			down := errors.New("connection refused")
			fail(f.sends, down)
			if err := f.buildErr(t); !errors.Is(err, down) {
				t.Fatalf("Build = %v, want %v", err, down)
			}
		})
	}
}

// On the dashboard the file is the run's file_sent event alone, as the
// stream writes it: no reply.
func TestOnTheDashboardAFileSentIsAnEvent(t *testing.T) {
	f := newSendFixture(dashboard)
	out, err := f.send(t, `{"id":"`+f.file.ID.String()+`","caption":"  Q3 numbers  "}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Sent report.pdf") {
		t.Errorf("out = %q", out)
	}
	want := stream.FileSent{
		ToolCallID: "c1", Caption: "Q3 numbers",
		File: stream.File{ID: f.file.ID.String(), Filename: "report.pdf", MediaType: "application/pdf", Size: 9},
	}
	if len(f.notes) != 1 || f.notes[0].kind != "file_sent" || f.notes[0].payload != want {
		t.Errorf("notes = %+v, want %+v", f.notes, want)
	}
	if f.sends.written() != 0 || len(f.kicked) != 0 {
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

// A call made again finds its reply already queued: sent, and not kicked
// again.
func TestACallMadeAgainIsSentOnceAndNotKickedAgain(t *testing.T) {
	f := newSendFixture(telegram)
	f.sends.inserted, f.sends.queued = 0, true
	if _, err := f.send(t, string(f.args())); err != nil {
		t.Fatal(err)
	}
	if len(f.kicked) != 0 {
		t.Errorf("kicked %v for a reply already written", f.kicked)
	}
}

// No reply written and none queued is a channel that can no longer be posted
// to: the model is told the file did not go, and nothing says it did.
func TestAFileNotQueuedIsNotSaidToBeSent(t *testing.T) {
	f := newSendFixture(telegram)
	f.sends.inserted = 0
	_, err := f.send(t, string(f.args()))
	if err == nil || !strings.Contains(err.Error(), "can no longer be posted to") {
		t.Fatalf("err = %v, want the file not sent", err)
	}
	if len(f.notes)+len(f.kicked) != 0 {
		t.Errorf("notes %v, kicks %v for a file not sent", f.notes, f.kicked)
	}

	f = newSendFixture(telegram)
	f.sends.inserted, f.sends.existsErr = 0, errors.New("connection reset")
	if _, err := f.send(t, string(f.args())); err == nil || !strings.Contains(err.Error(), "connection reset") || len(f.notes) != 0 {
		t.Errorf("a check that failed: err %v, notes %v; want the failure named", err, f.notes)
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
		"a caption not a string": {
			func(f *sendFixture) string { return `{"id":"` + f.file.ID.String() + `","caption":5}` },
			nil, "both strings",
		},
		"no call id": {
			func(f *sendFixture) string { return string(f.args()) },
			func(context.Context) context.Context { return context.Background() }, "no id",
		},
		"a call id too long": {
			func(f *sendFixture) string { return string(f.args()) },
			func(ctx context.Context) context.Context {
				return agent.WithToolCallID(ctx, strings.Repeat("c", maxCallID+1))
			}, "no id",
		},
		"not JSON": {func(*sendFixture) string { return `{` }, nil, "not what send_file takes"},
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
	if _, err := f.send(t, string(f.args())); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("err = %v", err)
	}
	if len(f.notes)+len(f.kicked) != 0 {
		t.Errorf("notes %v, kicks %v for a file not sent", f.notes, f.kicked)
	}
}

// On the dashboard the event is the delivery: one not written is the file
// not sent.
func TestOnTheDashboardAnEventNotWrittenIsAFileNotSent(t *testing.T) {
	f := newSendFixture(dashboard)
	f.noteErr = errors.New("the recorder stopped")
	if _, err := f.send(t, string(f.args())); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("err = %v", err)
	}
}

// On a channel the reply is already queued: an event not written is logged,
// and the call is still the file sent, so the model does not send it again.
func TestOnAChannelAFileQueuedIsNotUndoneByItsEvent(t *testing.T) {
	f := newSendFixture(telegram)
	f.noteErr = errors.New("the recorder stopped")
	out, err := f.send(t, string(f.args()))
	if err != nil || !strings.Contains(out, "Sent report.pdf") {
		t.Fatalf("out %q, err %v; want the file sent", out, err)
	}
	if len(f.kicked) != 1 || !strings.Contains(f.log.String(), "file_sent event was not recorded") {
		t.Errorf("kicked %v, log %q", f.kicked, f.log.String())
	}
}

// A run sends ten files: the eleventh is refused, whether the ten went or
// failed, and the count is one for all its calls at once.
func TestARunSendsTenFilesAtMost(t *testing.T) {
	for name, fail := range map[string]bool{"ten sent": false, "ten failed": true} {
		t.Run(name, func(t *testing.T) {
			f := newSendFixture(telegram)
			if fail {
				f.sends.insertErr = errors.New("connection reset")
			}
			send := tool(t, f.build(t), SendFileToolName)
			for i := range filesPerRun {
				if _, err := send.Invoke(agent.WithToolCallID(t.Context(), "c"+strconv.Itoa(i)), f.args()); (err != nil) != fail {
					t.Fatalf("send %d: %v", i+1, err)
				}
			}
			f.sends.insertErr = nil
			_, err := send.Invoke(agent.WithToolCallID(t.Context(), "last"), f.args())
			if err == nil || !strings.Contains(err.Error(), "10 files") {
				t.Fatalf("the eleventh send = %v, want it refused", err)
			}
			if f.sends.written() != filesPerRun {
				t.Errorf("%d replies tried, want %d", f.sends.written(), filesPerRun)
			}
		})
	}
}

func TestElevenCallsAtOnceSendTen(t *testing.T) {
	f := newSendFixture(telegram)
	send := tool(t, f.build(t), SendFileToolName)
	var wg sync.WaitGroup
	var mu sync.Mutex
	sent := 0
	for i := range filesPerRun + 1 {
		wg.Go(func() {
			if _, err := send.Invoke(agent.WithToolCallID(t.Context(), "c"+strconv.Itoa(i)), f.args()); err == nil {
				mu.Lock()
				sent++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if sent != filesPerRun || f.sends.written() != filesPerRun {
		t.Errorf("%d sent and %d written, want %d", sent, f.sends.written(), filesPerRun)
	}
}

// A wrong id costs no slot: the ten are for files found.
func TestAWrongIDCostsNoSlot(t *testing.T) {
	f := newSendFixture(telegram)
	send := tool(t, f.build(t), SendFileToolName)
	missing := json.RawMessage(`{"id":"` + uuid.NewString() + `"}`)
	for i := range filesPerRun {
		if _, err := send.Invoke(agent.WithToolCallID(t.Context(), "x"+strconv.Itoa(i)), missing); err == nil {
			t.Fatal("a file not in the conversation was sent")
		}
	}
	if _, err := send.Invoke(agent.WithToolCallID(t.Context(), "c1"), f.args()); err != nil {
		t.Fatalf("a file after ten wrong ids: %v", err)
	}
}

// FileSends missing what it needs offers nothing, rather than a tool that
// panics on the path that uses it.
func TestFileSendsMissingAPartOffersNothing(t *testing.T) {
	for name, drop := range map[string]func(*FileSends){
		"store":       func(s *FileSends) { s.Store = nil },
		"sends files": func(s *FileSends) { s.SendsFiles = nil },
		"logger":      func(s *FileSends) { s.Logger = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSendFixture(telegram)
			drop(f.builder.sends)
			if held(f.build(t), SendFileToolName) {
				t.Error("send_file was offered")
			}
		})
	}
}

// A run resumed is built again, and keeps to the ten it may send, counting
// those it sent before.
func TestAResumedRunCountsTheFilesItSentBefore(t *testing.T) {
	f := newSendFixture(telegram)
	f.sends.before = filesPerRun - 1
	send := tool(t, f.build(t), SendFileToolName)
	if _, err := send.Invoke(agent.WithToolCallID(t.Context(), "c1"), f.args()); err != nil {
		t.Fatalf("the tenth send: %v", err)
	}
	if _, err := send.Invoke(agent.WithToolCallID(t.Context(), "c2"), f.args()); err == nil {
		t.Error("an eleventh send across a resume went")
	}
}

// A server's tool may not take send_file's name: the run would hold two.
func TestSendFileIsReservedOnEveryRun(t *testing.T) {
	if !reserved(SendFileToolName, reservedFor{}) {
		t.Error("an MCP tool may be named send_file")
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

// The tool says its limits as its constants are.
func TestSendFileSaysItsLimits(t *testing.T) {
	tl := (&sendFile{}).tool()
	if !strings.Contains(tl.Description, "At most "+strconv.Itoa(filesPerRun)+" a run") ||
		!strings.Contains(string(tl.Schema), `"maxLength":`+strconv.Itoa(maxCaption)) {
		t.Errorf("description %q, schema %s", tl.Description, tl.Schema)
	}
	if !json.Valid(tl.Schema) {
		t.Errorf("schema %s is not JSON", tl.Schema)
	}
}

// ruled is the approval desk of a team whose person may approve their own
// calls, with the team's rules.
type ruled struct {
	*desk
	rules []db.ApprovalRule
}

func (*ruled) FindTeamMember(context.Context, db.FindTeamMemberParams) (string, error) {
	return "member", nil
}

func (*ruled) ListRolePermissions(context.Context, string) ([]string, error) {
	return []string{"approval:self"}, nil
}

func (r *ruled) ListApprovalRules(context.Context, uuid.UUID) ([]db.ApprovalRule, error) {
	return r.rules, nil
}

// sendOutcome is what came of a send_file call: made, asked, refused with nobody
// present, or denied by a rule.
func sendOutcome(err error) string {
	switch {
	case err == nil:
		return "made"
	case err.Error() == askedTop:
		return "asks"
	case strings.Contains(err.Error(), "Nobody is present"):
		return "refused"
	case strings.Contains(err.Error(), "does not allow send_file"):
		return "denied"
	}
	return err.Error()
}

// gatedSend is a send fixture behind the approval gate in mode, for who: a
// guest (no person on the run), a member who may approve their own, or that
// member's run with nobody present; with rule, if any, on send_file.
func gatedSend(reach db.GetSessionFileReachRow, mode approval.Mode, who, rule string) *sendFixture {
	f := newSendFixture(reach)
	f.store.settings = &db.TeamSetting{TeamID: f.run.TeamID, ApprovalMode: string(mode), ApprovalTtlMinutes: 60}
	d := &ruled{desk: &desk{byCall: map[string]db.Approval{}}}
	if rule != "" {
		d.rules = []db.ApprovalRule{{TargetKind: "tool", Target: SendFileToolName, Mode: rule}}
	}
	f.run.UserID = nil
	if who != "a guest" {
		person := uuid.New()
		f.run.UserID = &person
		f.run.Unattended = who == "nobody present"
	}
	f.builder.WithApprovals(d)
	return f
}

// send_file is judged as reaching out is: a read for a member who is there,
// a write for a guest, who is asked in every mode but off, and nobody present
// is refused where it would ask. A rule decides for a member; a guest's write
// asks however a rule allows it. Nothing is sent unless the call is made.
func TestSendFileIsBehindTheApprovalGate(t *testing.T) {
	for _, tc := range []struct {
		mode approval.Mode
		who  string
		rule string
		want string
	}{
		{approval.Off, "a guest", "deny", "made"},
		{approval.Writes, "a member", "", "made"},
		{approval.Writes, "a guest", "", "asks"},
		{approval.Writes, "nobody present", "", "refused"},
		{approval.All, "a member", "", "asks"},
		{approval.All, "a guest", "", "asks"},
		{approval.All, "nobody present", "", "refused"},
		{approval.DestructiveOnly, "a member", "", "made"},
		{approval.DestructiveOnly, "a guest", "", "asks"},
		{approval.Writes, "a member", "ask", "asks"},
		{approval.Writes, "a member", "deny", "denied"},
		{approval.Writes, "a guest", "allow", "asks"},
		{approval.Writes, "nobody present", "ask", "refused"},
	} {
		t.Run(fmt.Sprintf("%s, %s, rule %q", tc.mode, tc.who, tc.rule), func(t *testing.T) {
			f := gatedSend(dashboard, tc.mode, tc.who, tc.rule)
			_, err := f.send(t, string(f.args()))
			if got := sendOutcome(err); got != tc.want {
				t.Fatalf("%s, want %s", got, tc.want)
			}
			sent := slices.ContainsFunc(f.notes, func(n sentNote) bool { return n.kind == "file_sent" })
			if sent != (tc.want == "made") {
				t.Errorf("file_sent written: %v", sent)
			}
		})
	}
}

// A rule holds wherever the file would go: on a channel too, where a call
// not made queues no reply.
func TestARuleOnSendFileHoldsOnEveryChannel(t *testing.T) {
	for _, tc := range []struct {
		rule string
		want string
	}{
		{"", "made"},
		{"ask", "asks"},
		{"deny", "denied"},
	} {
		t.Run(fmt.Sprintf("telegram, rule %q", tc.rule), func(t *testing.T) {
			f := gatedSend(telegram, approval.Writes, "a member", tc.rule)
			_, err := f.send(t, string(f.args()))
			if got := sendOutcome(err); got != tc.want {
				t.Fatalf("%s, want %s", got, tc.want)
			}
			wantReplies := 0
			if tc.want == "made" {
				wantReplies = 1
			}
			if f.sends.written() != wantReplies {
				t.Errorf("%d replies, want %d", f.sends.written(), wantReplies)
			}
		})
	}
}

// A send_file a person approved is made on the resume, by the tool as it
// was built, so the run must keep it answerable.
func TestAnApprovedSendFileCanBeMadeOnResume(t *testing.T) {
	f := newSendFixture(telegram)
	f.store.settings = &db.TeamSetting{TeamID: f.run.TeamID, ApprovalMode: string(approval.All), ApprovalTtlMinutes: 60}
	f.builder.WithApprovals(&desk{byCall: map[string]db.Approval{}})
	b := f.build(t)
	send, ok := b.answerable[SendFileToolName]
	if !ok {
		t.Fatal("an approved send_file would find no tool to make it")
	}
	if _, err := callAs(t, send, "c1", string(f.args())); err != nil {
		t.Fatalf("the approved call: %v", err)
	}
	if f.sends.written() != 1 {
		t.Errorf("%d replies written, want the file", f.sends.written())
	}
}

// A pattern that plans every call first cannot wait for a person: a
// send_file that would ask is left out, and the run is told why.
func TestASendFileThatWouldAskIsLeftOutOfAPlannedRun(t *testing.T) {
	f := newSendFixture(dashboard)
	a := f.store.agents[*f.run.AgentID]
	a.Pattern = string(agent.PatternReWOO)
	f.store.agents[*f.run.AgentID] = a
	f.store.settings = &db.TeamSetting{TeamID: f.run.TeamID, ApprovalMode: string(approval.All), ApprovalTtlMinutes: 60}
	f.builder.WithApprovals(&desk{byCall: map[string]db.Approval{}})
	b := f.build(t)
	if held(b, SendFileToolName) || !slices.ContainsFunc(b.Unavailable, func(u Unavailable) bool { return u.Name == SendFileToolName }) {
		t.Errorf("send_file held %v, unavailable %+v", held(b, SendFileToolName), b.Unavailable)
	}
}
