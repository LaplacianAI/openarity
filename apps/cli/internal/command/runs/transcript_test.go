package runs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

func frameOf(id, event, data string) string {
	head := ""
	if id != "" {
		head = "id: " + id + "\n"
	}
	return head + "event: " + event + "\ndata: " + data + "\n\n"
}

// A real run, recorded: an agent asked to find who should reconcile payments
// searches the team's graph twice, then answers.
var sampleStream = frameOf("1", "started", `{"agent":"graph-check-finder","model":"antigravity/gemini-3.8-flash-medium","pattern":"react","tools":["graph"]}`) +
	frameOf("2", "step", `{"step":1}`) +
	frameOf("3", "usage", `{"model":"antigravity/gemini-3.8-flash-medium","input_tokens":344,"output_tokens":121,"cached_input_tokens":0,"spent":465}`) +
	frameOf("4", "tool_call", `{"id":"c1","name":"graph","arguments":"{\"query\":\"reconcile payments against invoices\"}","truncated":false}`) +
	frameOf("5", "tool_result", `{"id":"c1","name":"graph","output":"{\"results\":[{\"kind\":\"agent\",\"name\":\"graph-check-clerk\",\"description\":\"Keeps the books.\",\"score\":0.447,\"path\":[{\"kind\":\"agent\",\"name\":\"graph-check-clerk\"}]}]}","truncated":false,"duration_ms":83}`) +
	frameOf("6", "step", `{"step":2}`) +
	frameOf("7", "usage", `{"model":"antigravity/gemini-3.8-flash-medium","input_tokens":432,"output_tokens":89,"cached_input_tokens":0,"spent":986}`) +
	frameOf("8", "tool_call", `{"id":"c2","name":"graph","arguments":"{\"hops\":1,\"query\":\"reconcile payments against invoices\"}","truncated":false}`) +
	frameOf("9", "tool_result", `{"id":"c2","name":"graph","output":"{\"results\":[{\"kind\":\"agent\",\"name\":\"graph-check-clerk\",\"description\":\"Keeps the books.\",\"score\":0.849,\"path\":[{\"kind\":\"agent\",\"name\":\"graph-check-clerk\"},{\"kind\":\"skill\",\"name\":\"invoice-reconciliation\"}]}]}","truncated":false,"duration_ms":38}`) +
	frameOf("10", "step", `{"step":3}`) +
	frameOf("11", "usage", `{"model":"antigravity/gemini-3.8-flash-medium","input_tokens":600,"output_tokens":91,"cached_input_tokens":256,"spent":1677}`) +
	frameOf("12", "finished", `{"status":"succeeded"}`) +
	frameOf("", "end", `{"status":"succeeded"}`)

var sampleRun = `{"id":"` + runID + `","session_id":"` + sessionID + `","agent":"graph-check-finder","status":"succeeded",
  "input":"Who should reconcile payments against invoices?","input_tokens":1376,"output_tokens":301,
  "output":"The agent best suited to this task is **graph-check-clerk**.\nIt holds the ` + "`invoice-reconciliation`" + ` skill.",
  "created_at":"2026-10-21T09:00:00Z","started_at":"2026-10-21T09:00:01Z","finished_at":"2026-10-21T09:00:07.1Z"}`

func sampleRoutes(stream string) map[string]clitest.Reply {
	routes := eventsRoute(http.StatusOK, stream)
	routes["GET /teams/{id}/runs/{runID}"] = clitest.Reply{Status: http.StatusOK, Body: sampleRun}
	return routes
}

const sampleTranscript = "graph-check-finder · antigravity/gemini-3.8-flash-medium · react\n" +
	"  tools graph\n" +
	"\n" +
	"⏺ graph(query: \"reconcile payments against invoices\")\n" +
	"  ⎿  1 result · 83ms\n" +
	"     agent  graph-check-clerk  0.447\n" +
	"\n" +
	"⏺ graph(hops: 1, query: \"reconcile payments against invoices\")\n" +
	"  ⎿  1 result · 38ms\n" +
	"     agent  graph-check-clerk  0.849  via skill invoice-reconciliation\n" +
	"\n" +
	"⏺ The agent best suited to this task is graph-check-clerk.\n" +
	"  It holds the invoice-reconciliation skill.\n" +
	"\n" +
	"✓ succeeded · 3 steps · 1,677 tokens · 6.1s\n"

// The transcript is the whole point of the table format: someone following a
// run reads the calls, what came back and the answer, not a log of frames.
// Compared exactly, because every space in it is a choice.
func TestTheSampleRunReadsLikeATranscript(t *testing.T) {
	script := clitest.Routes(t, sampleRoutes(sampleStream))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err != nil {
		t.Fatalf("runs events: %v\n%s", err, out)
	}
	if out != sampleTranscript {
		t.Errorf("transcript =\n%s\nwant\n%s", out, sampleTranscript)
	}
	if n := script.Calls(http.MethodGet, runPath); n != 1 {
		t.Errorf("the run was read %d times for its answer, want once", n)
	}
}

// --verbose is for finding out where the tokens went: each step and its usage
// as a dim line, and every result whole.
func TestVerboseShowsEveryStepAndItsTokens(t *testing.T) {
	clitest.Routes(t, sampleRoutes(sampleStream))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID, "--verbose")
	if err != nil {
		t.Fatalf("runs events --verbose: %v\n%s", err, out)
	}
	want := "graph-check-finder · antigravity/gemini-3.8-flash-medium · react\n" +
		"  tools graph\n" +
		"\n" +
		"step 1 · 344 in / 121 out\n" +
		"\n" +
		"⏺ graph(query: \"reconcile payments against invoices\")\n" +
		"  ⎿  1 result · 83ms\n" +
		"     agent  graph-check-clerk  0.447\n" +
		"\n" +
		"step 2 · 432 in / 89 out\n" +
		"\n" +
		"⏺ graph(hops: 1, query: \"reconcile payments against invoices\")\n" +
		"  ⎿  1 result · 38ms\n" +
		"     agent  graph-check-clerk  0.849  via skill invoice-reconciliation\n" +
		"\n" +
		"step 3 · 600 in / 91 out · 256 cached\n" +
		"\n" +
		"⏺ The agent best suited to this task is graph-check-clerk.\n" +
		"  It holds the invoice-reconciliation skill.\n" +
		"\n" +
		"✓ succeeded · 3 steps · 1,677 tokens · 6.1s\n"
	if out != want {
		t.Errorf("transcript =\n%s\nwant\n%s", out, want)
	}
}

// A step whose usage never arrived still shows under --verbose, before what
// the step did, and never without --verbose.
func TestAStepWithNoUsageIsShownOnlyWhenVerbose(t *testing.T) {
	stream := frameOf("1", "step", `{"step":1}`) +
		frameOf("2", "tool_call", `{"id":"c1","name":"clock","arguments":"{}"}`) +
		frameOf("3", "tool_result", `{"id":"c1","name":"clock","output":"09:00","duration_ms":1}`) +
		frameOf("4", "step", `{"step":2}`) +
		frameOf("5", "finished", `{"status":"cancelled"}`) +
		frameOf("", "end", `{"status":"cancelled"}`)
	clitest.Routes(t, eventsRoute(http.StatusOK, stream))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID, "--verbose")
	want := "step 1\n\n⏺ clock()\n  ⎿  09:00 · 1ms\n\nstep 2\n\n■ cancelled\n"
	if out != want {
		t.Errorf("verbose =\n%s\nwant\n%s", out, want)
	}

	out, _ = clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if strings.Contains(out, "step") {
		t.Errorf("a step was shown without --verbose:\n%s", out)
	}
}

// --follow takes --verbose too, and the transcript follows the started row.
func TestStartFollowVerboseShowsTheSteps(t *testing.T) {
	routes := sampleRoutes(sampleStream)
	routes["POST /teams/{id}/runs"] = clitest.Reply{Status: http.StatusAccepted, Body: queuedRun}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "runs", "start", "platform", "graph-check-finder", "go", "--follow", "--verbose")
	if err != nil {
		t.Fatalf("runs start --follow --verbose: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "started  triage  "+runID+"\n\ngraph-check-finder · ") {
		t.Errorf("the transcript does not follow the started row after a blank line:\n%s", out)
	}
	if !strings.Contains(out, "step 2 · 432 in / 89 out") {
		t.Errorf("--verbose did not reach the transcript:\n%s", out)
	}
}

func resultStream(name, output string) string {
	return frameOf("1", "tool_call", `{"id":"c1","name":"`+name+`","arguments":"{}"}`) +
		frameOf("2", "tool_result", `{"id":"c1","name":"`+name+`","output":`+output+`,"duration_ms":1200}`) +
		frameOf("3", "finished", `{"status":"failed","error":"stop"}`) +
		frameOf("", "end", `{"status":"failed"}`)
}

// A graph search that finds nothing says so in words; an empty table under
// "0 results" reads like the search never ran.
func TestAnEmptyGraphResultSaysNothingMatched(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusOK, resultStream("graph", `"{\"results\":[]}"`)))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if !strings.Contains(out, "⏺ graph()\n  ⎿  nothing in reach matched · 1.2s\n\n✗ failed: stop\n") {
		t.Errorf("an empty search =\n%s", out)
	}
}

// Rows line up whatever the names' lengths, and a path is shown only when it
// says something the row does not: how the match was reached.
func TestGraphRowsAlignAndShowTheirPath(t *testing.T) {
	output := `"{\"results\":[` +
		`{\"kind\":\"agent\",\"name\":\"clerk\",\"score\":0.7071,\"path\":[{\"kind\":\"agent\",\"name\":\"clerk\"},{\"kind\":\"skill\",\"name\":\"invoices\"},{\"kind\":\"tool\",\"name\":\"ledger\"}]},` +
		`{\"kind\":\"skill\",\"name\":\"invoices\",\"score\":0.5,\"path\":[{\"kind\":\"skill\",\"name\":\"invoices\"}]},` +
		`{\"kind\":\"tool\",\"name\":\"ledger.read\",\"score\":0.25}]}"`
	clitest.Routes(t, eventsRoute(http.StatusOK, resultStream("graph", output)))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	want := "  ⎿  3 results · 1.2s\n" +
		"     agent  clerk        0.707  via skill invoices, tool ledger\n" +
		"     skill  invoices     0.500\n" +
		"     tool   ledger.read  0.250\n"
	if !strings.Contains(out, want) {
		t.Errorf("graph rows =\n%s\nwant\n%s", out, want)
	}
}

// A tool named graph whose output is not the graph's shape is shown like any
// other output, not as an empty search.
func TestAGraphResultOfAnotherShapeIsShownAsItIs(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusOK, resultStream("graph", `"the graph is not built yet"`)))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if !strings.Contains(out, "  ⎿  the graph is not built yet · 1.2s\n") {
		t.Errorf("a plain graph output =\n%s", out)
	}
}

// A long result shows its first lines and how many it left out; --verbose
// shows the rest. JSON is pretty-printed first so the preview has lines.
func TestALongResultIsPreviewedAndVerboseShowsAll(t *testing.T) {
	lines := []string{"one", "two", "three", "four", "five", "six", "seven"}
	clitest.Routes(t, eventsRoute(http.StatusOK, resultStream("read", `"`+strings.Join(lines, `\n`)+`"`)))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	want := "  ⎿  7 lines · 1.2s\n" +
		"     one\n     two\n     three\n     four\n     five\n" +
		"     … +2 lines (oa runs events --verbose to see all)\n"
	if !strings.Contains(out, want) {
		t.Errorf("preview =\n%s\nwant\n%s", out, want)
	}

	out, _ = clitest.Execute(t, commands, "runs", "events", "platform", runID, "--verbose")
	if !strings.Contains(out, "     five\n     six\n     seven\n\n") || strings.Contains(out, "… +") {
		t.Errorf("--verbose did not show the whole result:\n%s", out)
	}

	clitest.Routes(t, eventsRoute(http.StatusOK, resultStream("fetch", `"{\"a\":1,\"b\":[true]}"`)))
	out, _ = clitest.Execute(t, commands, "runs", "events", "platform", runID)
	want = "  ⎿  6 lines · 1.2s\n     {\n       \"a\": 1,\n       \"b\": [\n         true\n       ]\n" +
		"     … +1 lines (oa runs events --verbose to see all)\n"
	if !strings.Contains(out, want) {
		t.Errorf("json preview =\n%s\nwant\n%s", out, want)
	}
}

// Exactly five lines is not cut: the hint would cost the line it saves.
func TestFiveLinesAreShownWhole(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusOK, resultStream("read", `"1\n2\n3\n4\n5\n\n"`)))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if !strings.Contains(out, "  ⎿  5 lines · 1.2s\n     1\n     2\n     3\n     4\n     5\n\n") || strings.Contains(out, "… +") {
		t.Errorf("five lines =\n%s", out)
	}
}

// A tool that failed says why, under its call, and the transcript goes on.
func TestAFailedToolResultShowsItsError(t *testing.T) {
	stream := frameOf("1", "tool_call", `{"id":"c1","name":"fetch","arguments":"{\"url\":\"https://example.com\"}"}`) +
		frameOf("2", "tool_result", `{"id":"c1","name":"fetch","output":"","error":"timeout\nafter 3 tries","duration_ms":9,"truncated":true}`) +
		frameOf("", "end", `{"status":"cancelled"}`)
	clitest.Routes(t, eventsRoute(http.StatusOK, stream))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	want := "⏺ fetch(url: \"https://example.com\")\n" +
		"  ⎿  timeout · 9ms · truncated by the brain\n" +
		"     after 3 tries\n" +
		"\n■ cancelled\n"
	if out != want {
		t.Errorf("a failed result =\n%s\nwant\n%s", out, want)
	}
}

// Two calls at once: the second is printed before the first's result, so
// each result repeats its call rather than sitting under the wrong one.
func TestParallelCallsKeepEachResultWithItsCall(t *testing.T) {
	stream := frameOf("1", "tool_call", `{"id":"a","name":"left","arguments":"{}"}`) +
		frameOf("2", "tool_call", `{"id":"b","name":"right","arguments":"{}"}`) +
		frameOf("3", "tool_result", `{"id":"a","name":"left","output":"L","duration_ms":1}`) +
		frameOf("4", "tool_result", `{"id":"b","name":"right","output":"R","duration_ms":2}`) +
		frameOf("5", "tool_result", `{"id":"zz","name":"stray","output":"S","duration_ms":3}`) +
		frameOf("", "end", `{"status":"cancelled"}`)
	clitest.Routes(t, eventsRoute(http.StatusOK, stream))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	want := "⏺ left()\n\n⏺ right()\n\n⏺ left()\n  ⎿  L · 1ms\n\n⏺ right()\n  ⎿  R · 2ms\n\n⏺ stray()\n  ⎿  S · 3ms\n\n■ cancelled\n"
	if out != want {
		t.Errorf("parallel calls =\n%s\nwant\n%s", out, want)
	}
}

// Every ending has its own last line, and the exit status and the message a
// script sees are the ones they were before the transcript existed.
func TestEveryEndingHasItsLastLineAndExitStatus(t *testing.T) {
	for _, tc := range []struct {
		status, finished, last string
		wantErr                string
	}{
		{
			"failed", `{"status":"failed","error":"the run has spent its token budget"}`,
			"✗ failed: the run has spent its token budget\n", "the run failed: the run has spent its token budget",
		},
		{"failed", `{"status":"failed"}`, "✗ failed\n", "the run failed — `oa runs get platform " + runID + "` says why"},
		{"cancelled", `{"status":"cancelled"}`, "■ cancelled\n", "the run was cancelled"},
		{"running", `{"status":"running"}`, "? the stream ended with the run still running\n", "the stream ended with the run still running"},
		{"exploded", `{"status":"exploded"}`, "? the run ended as exploded\n", "the run ended with a status this oa does not know: exploded"},
	} {
		t.Run(tc.status+" "+tc.finished, func(t *testing.T) {
			script := clitest.Routes(t, eventsRoute(http.StatusOK, ending(tc.status, tc.finished)))

			out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if out != tc.last {
				t.Errorf("the transcript ends\n%s\nwant\n%s", out, tc.last)
			}
			if n := script.Calls(http.MethodGet, runPath); n != 0 {
				t.Errorf("a run that did not succeed was read %d times for an answer", n)
			}
		})
	}
}

// The answer is fetched, not streamed; losing it must not turn a success
// into a failure, and must say where the answer is.
func TestAnAnswerThatCannotBeReadStillSucceeds(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusOK, ending("succeeded", `{"status":"succeeded"}`)))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err != nil {
		t.Fatalf("a succeeded run exited with %v", err)
	}
	want := "the answer could not be read — `oa runs get platform " + runID + "` shows it\n\n✓ succeeded · 1 step\n"
	if !strings.HasSuffix(out, want) {
		t.Errorf("the transcript ends\n%s\nwant\n%s", out, want)
	}
}

// A run that succeeded with no answer, and no usage, still ends on one line.
func TestASucceededRunWithNothingToSayEndsOnOneLine(t *testing.T) {
	empty := runJSON(runID, "succeeded", `,"output":"  \n"`)
	empty = strings.Replace(empty, `"input_tokens":120,"output_tokens":45`, `"input_tokens":0,"output_tokens":0`, 1)
	routes := eventsRoute(http.StatusOK, frameOf("", "end", `{"status":"succeeded"}`))
	routes["GET /teams/{id}/runs/{runID}"] = clitest.Reply{Status: http.StatusOK, Body: empty}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err != nil || out != "✓ succeeded\n" {
		t.Errorf("out = %q, err = %v", out, err)
	}
}

// A tool the run could not reach is a warning, not a failure, and the name
// and reason are the brain's text like everything else.
func TestAnUnavailableToolIsAWarning(t *testing.T) {
	stream := frameOf("1", "tool_unavailable", `{"name":"jira\u001b[2J.search","reason":"the server\nis unreachable"}`) +
		frameOf("", "end", `{"status":"cancelled"}`)
	clitest.Routes(t, eventsRoute(http.StatusOK, stream))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if out != "⚠ jira.search unavailable: the server is unreachable\n\n■ cancelled\n" {
		t.Errorf("out = %q", out)
	}
}

// A resumed run was started before; its header is already above, so it says
// it resumed rather than printing the agent again.
func TestAResumedRunSaysSoAndKeepsOneHeader(t *testing.T) {
	stream := frameOf("1", "started", `{"agent":"triage","model":"m","pattern":"react","skills":["pdf"]}`) +
		frameOf("2", "resumed", `{"messages":12}`) +
		frameOf("3", "unknown_kind", `{"x":1}`) +
		frameOf("", "end", `{"status":"cancelled"}`)
	clitest.Routes(t, eventsRoute(http.StatusOK, stream))

	out, _ := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	want := "triage · m · react\n  skills pdf\n\n↻ resumed after 12 messages\n\nunknown_kind {\"x\":1}\n\n■ cancelled\n"
	if out != want {
		t.Errorf("out =\n%s\nwant\n%s", out, want)
	}
	if strings.Count(out, "triage") != 1 {
		t.Errorf("the header was repeated:\n%s", out)
	}
}

// --- the renderer on its own ---

func plain() paint {
	same := func(s string) string { return s }
	return paint{mark: same, strong: same, code: same, muted: same, ok: same, warn: same, err: same}
}

// tags makes every style visible, so a test can see exactly what it wraps.
func tags() paint {
	wrap := func(name string) func(string) string {
		return func(s string) string { return "<" + name + ">" + s + "</" + name + ">" }
	}
	return paint{
		mark: wrap("mark"), strong: wrap("b"), code: wrap("code"), muted: wrap("dim"),
		ok: wrap("ok"), warn: wrap("warn"), err: wrap("err"),
	}
}

func replay(t *testing.T, tr *transcript, stream string) {
	t.Helper()
	frames := newFrames(strings.NewReader(stream))
	for {
		f, err := frames.next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var p payload
		_ = json.Unmarshal([]byte(f.data), &p)
		if err := tr.event(f, p); err != nil {
			t.Fatal(err)
		}
	}
}

func testTranscript(w io.Writer, width int, tty bool) *transcript {
	return &transcript{w: w, paint: plain(), width: width, tty: tty, calls: map[string]string{}}
}

// On a terminal a call shows it is running until its result replaces it.
// Anywhere else — a pipe, a CI log, this buffer — there is no cursor to move,
// and \r or an erase would be garbage in the file.
func TestOnlyATerminalSeesTheRunningLine(t *testing.T) {
	t.Parallel()

	stream := frameOf("1", "tool_call", `{"id":"c1","name":"slow","arguments":"{}"}`) +
		frameOf("2", "tool_result", `{"id":"c1","name":"slow","output":"done","duration_ms":4000}`) +
		frameOf("3", "tool_call", `{"id":"c2","name":"cut","arguments":"{}"}`)

	var tty bytes.Buffer
	shown := testTranscript(&tty, 80, true)
	replay(t, shown, stream)
	want := "⏺ slow()\n  ⎿  running…" + clearLine + "  ⎿  done · 4.0s\n\n⏺ cut()\n  ⎿  running…"
	if tty.String() != want {
		t.Errorf("on a terminal =\n%q\nwant\n%q", tty.String(), want)
	}
	shown.close()
	if !strings.HasSuffix(tty.String(), "running…"+clearLine) {
		t.Errorf("stopping left the running line on screen: %q", tty.String())
	}

	var pipe bytes.Buffer
	replay(t, testTranscript(&pipe, 80, false), stream)
	if pipe.String() != "⏺ slow()\n  ⎿  done · 4.0s\n\n⏺ cut()\n" {
		t.Errorf("to a pipe = %q", pipe.String())
	}
	if strings.ContainsAny(pipe.String(), "\r\x1b") || strings.Contains(pipe.String(), "running") {
		t.Errorf("cursor control reached a pipe: %q", pipe.String())
	}
}

// Anything else that arrives while a call is running clears the running line
// first, so the two never share a line.
func TestTheRunningLineIsClearedBeforeAnythingElse(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	replay(t, testTranscript(&out, 80, true),
		frameOf("1", "tool_call", `{"id":"c1","name":"slow","arguments":"{}"}`)+
			frameOf("2", "tool_unavailable", `{"name":"x","reason":"gone"}`))
	if out.String() != "⏺ slow()\n  ⎿  running…"+clearLine+"\n⚠ x unavailable: gone\n" {
		t.Errorf("out = %q", out.String())
	}
}

// Every line a run writes fits the terminal: a call's arguments, a result's
// summary and its rows, a failure. The ellipsis says something was cut.
func TestEveryLineIsClippedToTheWidth(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 200)
	stream := frameOf("1", "tool_call", `{"id":"c1","name":"search","arguments":"{\"q\":\"`+long+`\",\"n\":5}"}`) +
		frameOf("2", "tool_result", `{"id":"c1","name":"search","output":"`+long+`\n`+long+`","duration_ms":5}`) +
		frameOf("3", "tool_call", `{"id":"c2","name":"漢字漢字漢字漢字漢字漢字漢字漢字漢字漢字漢字漢字","arguments":"{}"}`) +
		frameOf("4", "tool_result", `{"id":"c2","name":"x","error":"`+long+`","duration_ms":5}`) +
		frameOf("5", "tool_unavailable", `{"name":"x","reason":"`+long+`"}`)

	var out bytes.Buffer
	replay(t, testTranscript(&out, 40, false), stream)
	want := "⏺ search(q: \"" + strings.Repeat("x", 26) + "…\n" +
		"  ⎿  2 lines · 5ms\n" +
		"     xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx…\n" +
		"     xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx…\n" +
		"\n" +
		"⏺ 漢字漢字漢字漢字漢字漢字漢字漢字漢字…\n" +
		"  ⎿  " + strings.Repeat("x", 28) + "… · 5ms\n" +
		"\n" +
		"⚠ x unavailable: xxxxxxxxxxxxxxxxxxxxxx…\n"
	if out.String() != want {
		t.Errorf("clipped =\n%s\nwant\n%s", out.String(), want)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("%q is %d columns wide", line, w)
		}
	}
}

// An argument's value is clipped on its own, so one long query does not hide
// the arguments after it on a wide terminal.
func TestALongArgumentIsClippedOnItsOwn(t *testing.T) {
	t.Parallel()

	got := arguments(`{"q":"` + strings.Repeat("y", 100) + `","hops":2,"kinds":["agents","tools"],"deep":{"a":[1, 2]}}`)
	want := `q: "` + strings.Repeat("y", maxArg) + `…", hops: 2, kinds: ["agents","tools"], deep: {"a":[1,2]}`
	if got != want {
		t.Errorf("arguments = %q, want %q", got, want)
	}

	for raw, want := range map[string]string{
		"":                      "",
		"{}":                    "",
		` {"b":1,"a":2} `:       "b: 1, a: 2",
		`{"q":"cut off`:         `{"q":"cut off`,
		`[1,2]`:                 `[1,2]`,
		`{"a":1} {"b":2}`:       `{"a":1} {"b":2}`,
		`{"a":"x\u001b[2Jy"}`:   `a: "xy"`,
		`{"a\nb":"two\nlines"}`: `a b: "two lines"`,
	} {
		if got := arguments(raw); got != want {
			t.Errorf("arguments(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The answer is prose for a person: bold, code and headings are rendered,
// and anything else is left as written.
func TestTheAnswersMarkdownIsRendered(t *testing.T) {
	t.Parallel()

	text := "# Who\n" +
		"The **clerk** runs `reconcile` — **twice**.\n" +
		"  ## Sub heading ##\n" +
		"#hashtag stays, as do ** and ` alone, and ****\n" +
		"```go\n" +
		"x := **not bold**\n" +
		"```\n" +
		"    #### indented code is not a heading\n" +
		"####### seven is not a heading"
	want := []string{
		"<b>Who</b>",
		"The <b>clerk</b> runs <code>reconcile</code> — <b>twice</b>.",
		"<b>Sub heading</b>",
		"#hashtag stays, as do ** and ` alone, and ****",
		"<dim>```go</dim>",
		"<code>x := **not bold**</code>",
		"<dim>```</dim>",
		"    #### indented code is not a heading",
		"####### seven is not a heading",
	}
	got := answer(tags(), text)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("answer =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := answer(tags(), "# \n#"); strings.Join(got, "|") != "<b></b>|<b></b>" {
		t.Errorf("empty headings = %q", got)
	}
}

// Markdown wraps text in styles, and a style is an escape sequence of its
// own — so the sanitiser runs first, and nothing between ** or ` gets a
// control through by being styled.
func TestMarkdownNeverStylesAnEscapeThrough(t *testing.T) {
	t.Parallel()

	text := "**\x1b[31mred\x1b[0m** and `\x1b]52;c;cm0gLXJmIH4=\x07paste` and\n" +
		"# \x1b]8;;https://evil.example\x07click\x1b]8;;\x07\n" +
		"```\n\u009b2J\u202eboom\xff\n```"
	got := strings.Join(answer(tags(), text), "\n")
	want := "<b>red</b> and <code>paste</code> and\n<b>click</b>\n<dim>```</dim>\n<code>boom\uFFFD</code>\n<dim>```</dim>"
	if got != want {
		t.Errorf("answer =\n%q\nwant\n%q", got, want)
	}
}

func TestDurationsAndCountsReadNaturally(t *testing.T) {
	t.Parallel()

	for ms, want := range map[int64]string{0: "0ms", 83: "83ms", 999: "999ms", 1000: "1.0s", 6149: "6.1s", 59_999: "60.0s", 60_000: "1m 00s", 125_000: "2m 05s"} {
		if got := took(time.Duration(ms) * time.Millisecond); got != want {
			t.Errorf("took(%dms) = %q, want %q", ms, got, want)
		}
	}
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1677: "1,677", 1234567: "1,234,567", -1677: "-1,677"} {
		if got := grouped(n); got != want {
			t.Errorf("grouped(%d) = %q, want %q", n, got, want)
		}
	}
}

// The stats line counts from the run when it was read and from the usage
// events otherwise — a run followed with --after has missed some of them.
func TestTheLastLineCountsWhatItKnows(t *testing.T) {
	t.Parallel()

	shown := testTranscript(io.Discard, 80, false)
	shown.steps, shown.tokens = 1, 465
	if got := shown.stats(nil); got != " · 1 step · 465 tokens" {
		t.Errorf("stats from events = %q", got)
	}
	if got := shown.stats(&client.Run{InputTokens: 1000, OutputTokens: 677}); got != " · 1 step · 1,677 tokens" {
		t.Errorf("stats from the run = %q", got)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("the pipe is closed") }

// A closed stdout — `oa runs events … | head -1` — ends the command with
// the write's error rather than printing into nothing until the run ends.
func TestAWriteThatFailsStopsTheTranscript(t *testing.T) {
	t.Parallel()

	shown := testTranscript(brokenWriter{}, 80, false)
	f := frame{event: "started", data: `{"agent":"a"}`}
	if err := shown.event(f, payload{Agent: "a"}); err == nil || err.Error() != "the pipe is closed" {
		t.Errorf("event = %v, want the write's error", err)
	}
	if err := shown.finish("cancelled", "", nil, ""); err == nil {
		t.Error("finish wrote into a closed pipe without an error")
	}
}

// Usage reported before any step still shows under --verbose, named for what
// it is.
func TestUsageBeforeAnyStepIsStillShown(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	shown := testTranscript(&out, 80, false)
	shown.verbose = true
	replay(t, shown, frameOf("1", "usage", `{"input_tokens":5,"output_tokens":1}`))
	if out.String() != "usage · 5 in / 1 out\n" {
		t.Errorf("out = %q", out.String())
	}
}
