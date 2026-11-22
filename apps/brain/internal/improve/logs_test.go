package improve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// logbook keeps what was logged, so a test can read the lines back.
type logbook struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logbook) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logbook) logger() *slog.Logger { return slog.New(slog.NewJSONHandler(l, nil)) }

// said is every line logged with msg, as its fields.
func (l *logbook) said(t *testing.T, msg string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line is not JSON: %q", line)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// field is a logged value as text: 120, true, or the string.
func field(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

// A judged run is logged once, with what it came to; judging it again
// writes nothing and logs nothing. Its digest is logged once too.
func TestAJudgedAndDigestedRunIsLoggedOnce(t *testing.T) {
	w := newWorld(t)
	var book logbook
	w.checker.logger = book.logger()
	run := w.finished(t, w.session(t), "11 minutes")
	w.model.replies = []reply{{text: partlyAnswer}, {text: digestAnswerText}}

	for range 2 {
		if err := w.checker.Check(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	// A second worker that judged the run at the same moment records
	// nothing, and so logs nothing.
	late := db.RecordRunOutcomeParams{RunID: run, TeamID: w.team, Verdict: verdictDone, Implicit: implicitNone, Asked: []byte("[]"), Model: "team/model"}
	if err := w.checker.record(t.Context(), late, nil); err != nil {
		t.Fatal(err)
	}
	judged := book.said(t, "A run was judged")
	if len(judged) != 1 {
		t.Fatalf("judged lines = %d, want 1", len(judged))
	}
	j := judged[0]
	if field(j, "run") != run.String() || field(j, "team") != w.team.String() || field(j, "verdict") != "partly" ||
		field(j, "effect") != string(Bad) || field(j, "digest") != digestPending || field(j, "input_tokens") != "120" {
		t.Errorf("judged = %v", j)
	}

	for range 2 {
		if err := w.checker.Digest(t.Context(), run, 0); err != nil {
			t.Fatal(err)
		}
	}
	// A digest finished by another worker meanwhile writes and logs nothing.
	if err := w.checker.finishDigest(t.Context(), run, 0, nil, digestDone); err != nil {
		t.Fatal(err)
	}
	digested := book.said(t, "A run was digested")
	if len(digested) != 1 || field(digested[0], "run") != run.String() ||
		field(digested[0], "agents") != "1" {
		t.Errorf("digested = %v", digested)
	}
}

// A pass is logged when it begins and when it finishes, with what it came
// to; a pass failed as lost before it finished logs no finish.
func TestAPassIsLoggedFromBeginToFinish(t *testing.T) {
	w := newWorld(t)
	im := w.improver(t)
	var book logbook
	im.logger = book.logger()
	for range 3 {
		w.digested(t, skipped, "wrong", false)
	}
	pass := w.begin(t, im, patternPasses(w.patterns(t))[0])
	w.model.replies = []reply{{text: "Nothing to change."}}
	if err := im.Run(t.Context(), pass.ID); err != nil {
		t.Fatal(err)
	}

	began := book.said(t, "An improver pass began")
	if len(began) != 1 || field(began[0], "pass") != pass.ID.String() || field(began[0], "agent") != w.agent.String() ||
		field(began[0], "trigger") != triggerPattern || field(began[0], "patterns") != "1" || field(began[0], "runs") == "0" {
		t.Errorf("began = %v", began)
	}
	finished := book.said(t, "An improver pass finished")
	if len(finished) != 1 || field(finished[0], "pass") != pass.ID.String() || field(finished[0], "status") != passDone ||
		field(finished[0], "proposed") != "0" || field(finished[0], "cut_short") != "false" {
		t.Errorf("finished = %v", finished)
	}

	lost := w.begin(t, im, plannedPass{TeamID: w.team, AgentID: w.agent, Trigger: triggerDaily})
	w.exec(t, `UPDATE improver_passes SET status = 'failed', finished_at = now(), error = 'lost' WHERE id = $1`, lost.ID)
	if err := im.done(t.Context(), lost.ID, outcome{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range book.said(t, "An improver pass finished") {
		if field(f, "pass") == lost.ID.String() {
			t.Errorf("a pass failed as lost logged a finish: %v", f)
		}
	}
}

// A digest the model could not study is logged as skipped, not as digested.
func TestASkippedDigestSaysSo(t *testing.T) {
	w := newWorld(t)
	var book logbook
	w.checker.logger = book.logger()
	run := w.finished(t, w.session(t), "11 minutes")
	w.model.replies = []reply{{text: partlyAnswer}}
	if err := w.checker.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := w.checker.finishDigest(t.Context(), run, 0, nil, digestSkipped); err != nil {
		t.Fatal(err)
	}
	if got := book.said(t, "A run's digest was skipped"); len(got) != 1 || field(got[0], "run") != run.String() {
		t.Errorf("skipped = %v", got)
	}
	if got := book.said(t, "A run was digested"); len(got) != 0 {
		t.Errorf("a skipped digest was logged as digested: %v", got)
	}
}
