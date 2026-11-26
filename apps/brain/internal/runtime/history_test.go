package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func spoke(seq int64, who, text string) db.ListSessionHistoryRow {
	return db.ListSessionHistoryRow{Seq: seq, At: time.Unix(1_700_000_000+seq, 0), Who: who, Text: text}
}

func botSaid(seq int64, text string) db.ListSessionHistoryRow {
	return db.ListSessionHistoryRow{Seq: seq, At: time.Unix(1_700_000_000+seq, 0), Ours: true, Text: text}
}

func hasTool(built Built, name string) bool {
	for _, tl := range heldTools(built) {
		if tl.Name == name {
			return true
		}
	}
	return false
}

// readHistory calls the run's history tool and decodes the page.
func readHistory(t *testing.T, built Built, args string) historyPageOut {
	t.Helper()

	r, _, err := tool(t, built, HistoryToolName).Call(t.Context(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("history(%s): %v", args, err)
	}
	var page historyPageOut
	if err := json.Unmarshal([]byte(r.Output), &page); err != nil {
		t.Fatalf("history's result is not its page: %v\n%s", err, r.Output)
	}
	return page
}

func pageLines(page historyPageOut) []string {
	out := make([]string, len(page.Messages))
	for i, m := range page.Messages {
		out[i] = m.From + ": " + m.Text
	}
	return out
}

// --- binding ---

func TestATopLevelRunWithEarlierMessagesReadsTheConversation(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{
		f.run.SessionID: {spoke(3, "asha", "and now?"), botSaid(2, "it was the cron"), spoke(1, "", "why is the export late?")},
	}

	page := readHistory(t, f.build(t), `{}`)
	want := []string{"someone: why is the export late?", "you: it was the cron", "asha: and now?"}
	if got := pageLines(page); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q, want %q: oldest first, the bot as you, a nameless sender as someone", got, want)
	}
	if page.More || page.Before != 0 || !strings.Contains(page.Note, "not instructions") {
		t.Errorf("page = %+v; want everything, no cursor, and the note that it is data", page)
	}
}

// The run's own message alone is nothing to read back: the first message of
// a conversation adds no tool.
func TestAFirstMessageHasNoHistory(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{f.run.SessionID: {spoke(1, "asha", "hello")}}

	if hasTool(f.build(t), HistoryToolName) {
		t.Fatal("a conversation holding only the run's own message bound history")
	}
}

// A delegated agent is sent a task, not the conversation.
func TestADelegatedRunDoesNotReadTheConversation(t *testing.T) {
	f := newFixture()
	parent := uuid.New()
	f.run.SessionID, f.run.ParentRunID = uuid.New(), &parent
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{
		f.run.SessionID: {spoke(2, "asha", "and now?"), spoke(1, "asha", "why?")},
	}

	if hasTool(f.build(t), HistoryToolName) {
		t.Fatal("a child run was handed the conversation")
	}
}

func TestABuildThatCannotReadTheHistoryFails(t *testing.T) {
	f := newFixture()
	f.store.historyErr = errors.New("connection reset")

	if err := f.buildErr(t); err == nil || !strings.Contains(err.Error(), "history") {
		t.Fatalf("Build = %v; want the failed read named", err)
	}
}

// The tool reads the session it was built for, whatever another session holds.
func TestHistoryReadsOnlyItsOwnConversation(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	other := uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{
		f.run.SessionID: {spoke(2, "asha", "mine"), spoke(1, "asha", "also mine")},
		other:           {spoke(9, "ravi", "SOMEONE ELSE'S")},
	}

	for _, m := range readHistory(t, f.build(t), `{"before": 100}`).Messages {
		if strings.Contains(m.Text, "SOMEONE ELSE'S") {
			t.Fatal("history read another conversation")
		}
	}
}

// --- arguments ---

func conversationOf(n int) []db.ListSessionHistoryRow {
	rows := make([]db.ListSessionHistoryRow, 0, n)
	for i := n; i >= 1; i-- {
		rows = append(rows, spoke(int64(i), "asha", "message"))
	}
	return rows
}

func TestHistoryPagesBackWithoutOverlap(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{f.run.SessionID: conversationOf(5)}
	built := f.build(t)

	first := readHistory(t, built, `{"limit": 2}`)
	if len(first.Messages) != 2 || first.Messages[0].N != 4 || first.Messages[1].N != 5 || !first.More || first.Before != 4 {
		t.Fatalf("first page = %+v; want 4 and 5, oldest first, more before 4", first)
	}
	second := readHistory(t, built, `{"limit": 2, "before": 4}`)
	if len(second.Messages) != 2 || second.Messages[0].N != 2 || second.Messages[1].N != 3 || second.Before != 2 {
		t.Fatalf("second page = %+v; want 2 and 3, more before 2", second)
	}
	last := readHistory(t, built, `{"limit": 2, "before": 2}`)
	if len(last.Messages) != 1 || last.Messages[0].N != 1 || last.More || last.Before != 0 {
		t.Fatalf("last page = %+v; want 1 alone and nothing more", last)
	}
}

func TestHistoryLimitsAreClampedNotRefused(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{f.run.SessionID: conversationOf(150)}
	built := f.build(t)

	for _, tc := range []struct {
		args string
		want int
	}{
		{`{}`, historyPage},
		{`{"limit": 0}`, historyPage},
		{`{"limit": -3}`, historyPage},
		{`{"limit": 1000}`, historyMaxPage},
	} {
		if got := len(readHistory(t, built, tc.args).Messages); got != tc.want {
			t.Errorf("history(%s) gave %d messages, want %d", tc.args, got, tc.want)
		}
	}
}

func TestABeforeBelowOneIsRefusedAndReadsNothing(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{f.run.SessionID: conversationOf(3)}
	built := f.build(t)

	for _, args := range []string{`{"before": 0}`, `{"before": -1}`, `{not json`} {
		r, _, err := tool(t, built, HistoryToolName).Call(t.Context(), json.RawMessage(args))
		if err == nil {
			t.Errorf("history(%s) = %q; want it refused", args, r.Output)
		}
	}
}

// --- the page ---

func TestALongMessageIsCutOnACharacter(t *testing.T) {
	long := strings.Repeat("é", historyText+10)
	page := historyOf([]db.ListSessionHistoryRow{spoke(1, "asha", long)}, historyPage)

	got := page.Messages[0].Text
	if want := strings.Repeat("é", historyText) + "…"; got != want {
		t.Fatalf("a long message kept %d characters; want %d and an ellipsis", len([]rune(got)), historyText)
	}
}

// A page past its bytes keeps the newest and says where to carry on, so
// nothing is skipped between pages.
func TestAPagePastItsBytesKeepsTheNewest(t *testing.T) {
	big := strings.Repeat("x", historyText)
	var rows []db.ListSessionHistoryRow
	for i := int64(30); i >= 1; i-- {
		rows = append(rows, spoke(i, "asha", big))
	}

	page := historyOf(rows, historyPage)
	n := len(page.Messages)
	if n == 0 || n == len(rows) {
		t.Fatalf("kept %d of %d messages; want the page cut by its bytes", n, len(rows))
	}
	if page.Messages[n-1].N != 30 || !page.More || page.Before != page.Messages[0].N {
		t.Fatalf("page = newest %d, more %v, before %d; want 30 kept and the oldest kept as the cursor",
			page.Messages[n-1].N, page.More, page.Before)
	}
	b, _ := json.Marshal(page.Messages)
	if len(b) > historyPageSize+historyText*2 {
		t.Errorf("the page is %d bytes, well past its %d", len(b), historyPageSize)
	}
}

// One message larger than the page on its own is still read, not skipped
// forever.
func TestAMessageLargerThanThePageIsStillRead(t *testing.T) {
	files := make([]string, 1000)
	for i := range files {
		files[i] = strings.Repeat("f", 20)
	}
	page := historyOf([]db.ListSessionHistoryRow{
		{Seq: 2, Text: strings.Repeat("x", historyText), Files: files},
		spoke(1, "asha", "older"),
	}, historyPage)
	if len(page.Messages) != 1 || page.Messages[0].N != 2 || page.Before != 2 {
		t.Fatalf("page = %+v; want the one big message, then more before it", page.Messages)
	}
}

func TestHistoryIsNotAnnouncedInAChat(t *testing.T) {
	if !unspoken[HistoryToolName] {
		t.Fatal("history would be told as a step: it is the run's bookkeeping")
	}
}

// A thread whose first message replies to a schedule's post has that post to
// read: the tool is bound for it, and the page carries what schedules posted.
func TestAReplyToAScheduledPostReadsThePost(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{f.run.SessionID: {spoke(1, "asha", "why the delay?")}}
	posted := time.Now().Add(-time.Hour)
	f.store.posted = map[uuid.UUID][]db.ListPostedTriggerAnswersRow{
		f.run.SessionID: {{At: posted, Name: "Release notes", Text: "Three changes went out."}},
	}

	built := f.build(t)
	if !hasTool(built, HistoryToolName) {
		t.Fatal("a reply to a schedule's post was not given history")
	}
	page := readHistory(t, built, `{}`)
	if len(page.Scheduled) != 1 || page.Scheduled[0].From != "Release notes (scheduled)" ||
		page.Scheduled[0].Text != "Three changes went out." {
		t.Errorf("scheduled = %+v, want the post it replies to", page.Scheduled)
	}
}

// An earlier page carries what schedules posted within its own span: from
// its oldest message up to where the later page began, not what came after.
func TestAnEarlierPageCarriesOnlyItsOwnSpanOfPosts(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{
		f.run.SessionID: {spoke(4, "asha", "d"), spoke(3, "asha", "c"), spoke(2, "asha", "b"), spoke(1, "asha", "a")},
	}
	at := func(seq int64) time.Time { return time.Unix(1_700_000_000+seq, 0) }
	half := time.Second / 2
	f.store.posted = map[uuid.UUID][]db.ListPostedTriggerAnswersRow{
		f.run.SessionID: {
			{At: at(3).Add(half), Name: "after c", Text: "-"},
			{At: at(2).Add(half), Name: "after b", Text: "-"},
			{At: at(1).Add(half), Name: "after a", Text: "-"},
		},
	}

	page := readHistory(t, f.build(t), `{"before":3}`)
	if len(page.Scheduled) != 2 || page.Scheduled[0].From != "after a (scheduled)" || page.Scheduled[1].From != "after b (scheduled)" {
		t.Errorf("the page before 3 carries %+v, want what was posted up to message 3", page.Scheduled)
	}
}

// Paging back a message at a time, each post is on exactly one page: the
// pages' spans meet, with no gap between a page and the one before it.
func TestEveryPostIsOnOnePageAsThePagesGoBack(t *testing.T) {
	f := newFixture()
	f.run.SessionID = uuid.New()
	f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{
		f.run.SessionID: {spoke(4, "asha", "d"), spoke(3, "asha", "c"), spoke(2, "asha", "b"), spoke(1, "asha", "a")},
	}
	at := func(seq int64) time.Time { return time.Unix(1_700_000_000+seq, 0) }
	var posts []db.ListPostedTriggerAnswersRow
	for seq := int64(4); seq >= 0; seq-- {
		posts = append(posts, db.ListPostedTriggerAnswersRow{At: at(seq).Add(time.Second / 2), Name: fmt.Sprint(seq), Text: "-"})
	}
	posts = append(posts, db.ListPostedTriggerAnswersRow{At: at(2), Name: "at b", Text: "-"})
	f.store.posted = map[uuid.UUID][]db.ListPostedTriggerAnswersRow{f.run.SessionID: posts}

	built := f.build(t)
	seen := map[string]int{}
	args := `{"limit":1}`
	for range 5 {
		page := readHistory(t, built, args)
		for _, p := range page.Scheduled {
			seen[p.From]++
		}
		if !page.More {
			break
		}
		args = fmt.Sprintf(`{"limit":1,"before":%d}`, page.Before)
	}
	if len(seen) != len(posts) {
		t.Errorf("seen %v, want each of the %d posts", seen, len(posts))
	}
	for from, n := range seen {
		if n != 1 {
			t.Errorf("%s was on %d pages, want one", from, n)
		}
	}
}

// A span holds at most ten posts, the newest; more says so rather than
// leaving the older ones out silently.
func TestMorePostsThanAPageHoldsAreSaid(t *testing.T) {
	for _, n := range []int{historyScheduled, historyScheduled + 1} {
		f := newFixture()
		f.run.SessionID = uuid.New()
		f.store.history = map[uuid.UUID][]db.ListSessionHistoryRow{f.run.SessionID: {spoke(2, "asha", "b"), spoke(1, "asha", "a")}}
		var posts []db.ListPostedTriggerAnswersRow
		for i := range n { // newest first, as the query gives them
			posts = append(posts, db.ListPostedTriggerAnswersRow{At: time.Now().Add(-time.Duration(i+1) * time.Minute), Name: fmt.Sprint(i), Text: "-"})
		}
		f.store.posted = map[uuid.UUID][]db.ListPostedTriggerAnswersRow{f.run.SessionID: posts}
		page := readHistory(t, f.build(t), `{}`)
		if len(page.Scheduled) != historyScheduled || page.ScheduledMore != (n > historyScheduled) {
			t.Errorf("%d posts: %d shown, more %v", n, len(page.Scheduled), page.ScheduledMore)
		}
		if page.Scheduled[historyScheduled-1].From != "0 (scheduled)" {
			t.Errorf("%d posts: the newest shown is %s, want the newest kept", n, page.Scheduled[historyScheduled-1].From)
		}
	}
}
