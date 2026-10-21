package runs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/clitest"
)

// A whole run as the brain streams it, every kind once, ending well.
const succeededStream = ": following run " + runID + "\n\n" +
	"id: 1\nevent: started\ndata: {\"agent\":\"triage\",\"model\":\"anthropic/claude-sonnet-5-5\",\"pattern\":\"react\",\"tools\":[\"github.search_code\"],\"skills\":[\"pdf\"]}\n\n" +
	"id: 2\nevent: tool_unavailable\ndata: {\"name\":\"jira.search\",\"reason\":\"the server is unreachable\"}\n\n" +
	"id: 3\nevent: step\ndata: {\"step\":1}\n\n" +
	"id: 4\nevent: tool_call\ndata: {\"id\":\"c1\",\"name\":\"github.search_code\",\"arguments\":\"{\\\"q\\\":\\\"label:bug\\\"}\",\"truncated\":false}\n\n" +
	": keep-alive\n\n" +
	"id: 5\nevent: tool_result\ndata: {\"id\":\"c1\",\"name\":\"github.search_code\",\"output\":\"4 issues\",\"truncated\":true,\"duration_ms\":812}\n\n" +
	"id: 6\nevent: usage\ndata: {\"model\":\"anthropic/claude-sonnet-5-5\",\"input_tokens\":120,\"output_tokens\":45,\"cached_input_tokens\":30,\"spent\":165}\n\n" +
	"id: 7\nevent: finished\ndata: {\"status\":\"succeeded\"}\n\n" +
	"event: end\ndata: {\"status\":\"succeeded\"}\n\n"

func ending(status, finished string) string {
	return "id: 1\nevent: step\ndata: {\"step\":1}\n\n" +
		"id: 2\nevent: finished\ndata: " + finished + "\n\n" +
		"event: end\ndata: {\"status\":\"" + status + "\"}\n\n"
}

func eventsRoute(status int, body string) map[string]clitest.Reply {
	return with("GET /teams/{id}/runs/{runID}/events", clitest.Reply{Status: status, Body: body})
}

func TestEventsPrintsEveryKindReadably(t *testing.T) {
	routes := eventsRoute(http.StatusOK, succeededStream)
	routes["GET /teams/{id}/runs/{runID}"] = clitest.Reply{Status: http.StatusOK, Body: finishedRun}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err != nil {
		t.Fatalf("runs events: %v\n%s", err, out)
	}
	if n := script.Calls(http.MethodGet, eventsPath); n != 1 {
		t.Errorf("the events were asked for %d times", n)
	}

	want := "triage · anthropic/claude-sonnet-5-5 · react\n" +
		"  tools github.search_code\n" +
		"  skills pdf\n" +
		"\n" +
		"⚠ jira.search unavailable: the server is unreachable\n" +
		"\n" +
		"⏺ github.search_code(q: \"label:bug\")\n" +
		"  ⎿  4 issues · 812ms · truncated by the brain\n" +
		"\n" +
		"⏺ Labelled 4 issues.\n" +
		"\n" +
		"✓ succeeded · 1 step · 165 tokens · 8.0s\n"
	if out != want {
		t.Errorf("transcript =\n%s\nwant\n%s", out, want)
	}
	if strings.Contains(out, "keep-alive") || strings.Contains(out, "following run") {
		t.Errorf("a comment was printed:\n%s", out)
	}
}

// One object per line is what lets `| jq` or a `while read` loop handle each
// event as it arrives, rather than waiting for a document that ends only when
// the run does.
func TestEventsAsJSONIsOneObjectPerLine(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusOK, succeededStream))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID, "-o", "json")
	if err != nil {
		t.Fatalf("runs events -o json: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 8 {
		t.Fatalf("%d lines, want one per frame:\n%s", len(lines), out)
	}
	for i, line := range lines {
		var got struct {
			ID    *int64         `json:"id"`
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not a json object: %v\n%s", i, err, line)
		}
		if got.Data == nil {
			t.Errorf("line %d carries its data as something other than the object recorded: %s", i, line)
		}
		if i < 7 && (got.ID == nil || *got.ID != int64(i+1)) {
			t.Errorf("line %d id = %v, want %d", i, got.ID, i+1)
		}
		if i == 7 && (got.Event != "end" || got.ID != nil || got.Data["status"] != "succeeded") {
			t.Errorf("the last line = %s, want the end frame with no id", line)
		}
	}
	if !strings.Contains(lines[4], `"output":"4 issues"`) {
		t.Errorf("the tool's output was not passed through whole: %s", lines[4])
	}
}

func TestEventsAsYAMLIsADocumentPerEvent(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusOK, succeededStream))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID, "-o", "yaml")
	if err != nil {
		t.Fatalf("runs events -o yaml: %v\n%s", err, out)
	}

	decoder := yaml.NewDecoder(strings.NewReader(out))
	n := 0
	for {
		var got eventView
		if err := decoder.Decode(&got); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("document %d: %v\n%s", n, err, out)
			}
			break
		}
		if _, ok := got.Data.(map[string]any); !ok {
			t.Errorf("document %d's data is a %T, want a mapping", n, got.Data)
		}
		n++
	}
	if n != 8 {
		t.Errorf("%d documents, want 8:\n%s", n, out)
	}
}

// Following a run is how a script waits for it. Exiting zero when it failed
// would let the next step run on an answer that never came.
func TestEventsExitsByHowTheRunEnded(t *testing.T) {
	for _, tc := range []struct {
		status, finished string
		wantErr          []string
	}{
		{"succeeded", `{"status":"succeeded"}`, nil},
		{"failed", `{"status":"failed","error":"the run has spent its token budget"}`, []string{"failed", "token budget"}},
		{"failed", `{"status":"failed"}`, []string{"failed", "oa runs get platform " + runID}},
		{"cancelled", `{"status":"cancelled"}`, []string{"cancelled"}},
		{"running", `{"status":"running"}`, []string{"still running"}},
		{"exploded", `{"status":"exploded"}`, []string{"exploded"}},
	} {
		t.Run(tc.status+" "+tc.finished, func(t *testing.T) {
			clitest.Routes(t, eventsRoute(http.StatusOK, ending(tc.status, tc.finished)))

			out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("a %s run exited with %v\n%s", tc.status, err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("a %s run exited zero:\n%s", tc.status, out)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want %q in it", err, want)
				}
			}
		})
	}
}

// The failure reason is the model's or a tool's text, and it is printed to
// stderr by main — outside any table, so nothing else would quote it.
func TestAFailureReasonCannotDriveTheTerminal(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusOK, ending("failed", `{"status":"failed","error":"boom \u001b]0;pwned\u0007"}`)))

	_, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err == nil {
		t.Fatal("a failed run exited zero")
	}
	if strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("a control character reached the error: %q", err.Error())
	}
}

// The end frame is the only proof the run is over. A stream that closes
// without one — a proxy timing out, the brain restarting — must not read as a
// run that finished, and the message says how to carry on from the last event.
func TestAStreamThatClosesEarlyIsNotAnEnding(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusOK, "id: 1\nevent: step\ndata: {\"step\":1}\n\nid: 2\nevent: step\ndata: {\"step\":2}\n\n"))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err == nil {
		t.Fatalf("a stream with no end frame exited zero:\n%s", out)
	}
	if !strings.Contains(err.Error(), "oa runs events platform "+runID+" --after 2") {
		t.Errorf("err = %v, want it to name the command that resumes after event 2", err)
	}
}

func TestEventsResumesAfterAGivenEvent(t *testing.T) {
	script := clitest.Routes(t, eventsRoute(http.StatusOK, ending("succeeded", `{"status":"succeeded"}`)))

	if out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID, "--after", "5"); err != nil {
		t.Fatalf("runs events --after: %v\n%s", err, out)
	}
	if query := queryFor(t, script, eventsPath); query != "after=5" {
		t.Errorf("query = %q, want after=5", query)
	}

	script = clitest.Routes(t, eventsRoute(http.StatusOK, ending("succeeded", `{"status":"succeeded"}`)))
	if out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID); err != nil {
		t.Fatalf("runs events: %v\n%s", err, out)
	}
	if query := queryFor(t, script, eventsPath); query != "" {
		t.Errorf("query = %q, want nothing without --after", query)
	}

	script = clitest.Routes(t, eventsRoute(http.StatusOK, ""))
	if _, err := clitest.Execute(t, commands, "runs", "events", "platform", runID, "--after", "-1"); err == nil {
		t.Error("a negative --after was accepted")
	}
	if n := script.Calls(http.MethodGet, eventsPath); n != 0 {
		t.Errorf("the stream was opened %d times for a refused --after", n)
	}
}

func TestEventsOfARunItCannotSeeSaysSo(t *testing.T) {
	clitest.Routes(t, eventsRoute(http.StatusNotFound, "not found"))

	_, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err == nil || !strings.Contains(err.Error(), "not found, or not visible to you") {
		t.Errorf("err = %v, want the 404 sentence", err)
	}
}

func TestEventsRefusesARunNameWithoutAskingTheBrain(t *testing.T) {
	script := clitest.Routes(t, teamRoute())

	_, err := clitest.Execute(t, commands, "runs", "events", "platform", "triage")
	if err == nil || !strings.Contains(err.Error(), "not a run id") {
		t.Fatalf("err = %v, want it to say a run id is wanted", err)
	}
	if n := script.Calls(http.MethodGet, "/teams/"+teamID+"/runs/triage/events"); n != 0 {
		t.Errorf("the brain was asked anyway")
	}
}

// Event data is whatever a tool returned or a model wrote. Printed raw, an
// escape sequence in a web page an agent fetched clears the screen of whoever
// is following the run, and a newline forges a second event line.
func TestEventDataCannotDriveTheTerminal(t *testing.T) {
	hostile := "id: 1\nevent: tool_result\ndata: {\"name\":\"web.fetch\",\"output\":\"page \\u001b[2J\\u001b[H\\n  9  finished  succeeded\",\"duration_ms\":3}\n\n" +
		"id: 2\nevent: tool_call\ndata: {\"name\":\"x\\u009b31m\",\"arguments\":\"\\u202eevil\"}\n\n" +
		"id: 3\nevent: \u001b[31mweird\ndata: not json \u001b[2J\n\n" +
		"event: end\ndata: {\"status\":\"succeeded\"}\n\n"
	clitest.Routes(t, eventsRoute(http.StatusOK, hostile))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err != nil {
		t.Fatalf("runs events: %v\n%s", err, out)
	}
	if strings.ContainsAny(out, "\x1b\u009b\u202e") {
		t.Errorf("a control sequence reached the terminal:\n%q", out)
	}
	// The forged line stays under the result it came in, never at the left
	// edge where a real event would be.
	if !strings.Contains(out, "\n       9  finished  succeeded\n") || strings.Contains(out, "\n  9  finished") {
		t.Errorf("a line of a tool's output escaped its result:\n%s", out)
	}
	if !strings.Contains(out, "⏺ web.fetch()\n  ⎿  2 lines · 3ms\n     page\n") ||
		!strings.Contains(out, "⏺ x(evil)") || !strings.Contains(out, "weird not json") {
		t.Errorf("the readable part was lost:\n%s", out)
	}

	out, err = clitest.Execute(t, commands, "runs", "events", "platform", runID, "-o", "json")
	if err != nil {
		t.Fatalf("runs events -o json: %v\n%s", err, out)
	}
	var first struct {
		Data struct {
			Output string `json:"output"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &first); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if first.Data.Output != "page \x1b[2J\x1b[H\n  9  finished  succeeded" {
		t.Errorf("output = %q, want it exactly as recorded", first.Data.Output)
	}
	if !strings.Contains(out, `"data":"not json \u001b[2J"`) {
		t.Errorf("data that is not json was not carried as a string:\n%s", out)
	}
}

// A long tool output is the common case — a fetched page, a file. The
// transcript shows the start of it, one terminal line wide, and -o json has
// the rest.
func TestALongOutputIsClippedToTheLine(t *testing.T) {
	long := strings.Repeat("ab", 500)
	clitest.Routes(t, eventsRoute(http.StatusOK,
		"id: 1\nevent: tool_result\ndata: {\"name\":\"read\",\"output\":\""+long+"\"}\n\nevent: end\ndata: {\"status\":\"succeeded\"}\n\n"))

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID)
	if err != nil {
		t.Fatalf("runs events: %v\n%s", err, out)
	}
	want := "  ⎿  " + strings.Repeat("ab", 44) + "… · 0ms\n"
	if !strings.Contains(out, want) {
		t.Errorf("the output was not clipped to %d columns:\n%s", fallbackWidth, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if n := utf8.RuneCountInString(line); n > fallbackWidth {
			t.Errorf("a line is %d columns wide, past %d: %s", n, fallbackWidth, line)
		}
	}
}

// streamBrain is a brain whose events endpoint is a real stream: the handler
// writes and flushes as it likes, so a test controls exactly where the bytes
// break and when the connection is held open.
func streamBrain(t *testing.T, events http.HandlerFunc) *atomic.Int32 {
	t.Helper()

	var others atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /teams", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(oneTeam))
	})
	mux.HandleFunc("POST /teams/{id}/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(queuedRun))
	})
	mux.HandleFunc("GET "+eventsPath, events)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		others.Add(1)
		http.NotFound(w, nil)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	clitest.Isolate(t)
	t.Setenv("OPENARITY_SERVER", server.URL)
	t.Setenv("OPENARITY_TOKEN", "a-token")
	return &others
}

func flushed(w http.ResponseWriter, parts ...string) {
	for _, part := range parts {
		_, _ = io.WriteString(w, part)
		_ = http.NewResponseController(w).Flush()
		time.Sleep(5 * time.Millisecond)
	}
}

// Over a real connection the bytes arrive however TCP and the brain's flushes
// cut them — mid-field, between \r and \n — and an event must still be one
// event.
func TestEventsSurviveFramesSplitAcrossReads(t *testing.T) {
	streamBrain(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flushed(w,
			": following\r\n\r\nid: 1\r\nev", "ent: step\r", "\ndata: {\"st", "ep\":4}\r\n\r", "\n",
			"event: end\r\ndata: {\"status\":", "\"succeeded\"}\r\n\r\n")
	})

	out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID, "-o", "json")
	if err != nil {
		t.Fatalf("runs events: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != `{"id":1,"event":"step","data":{"step":4}}` {
		t.Errorf("events = %q", lines)
	}
}

// Ctrl-C cancels the context, and a stream held open by a running run must
// end with it — promptly, without cancelling the run, and saying how to
// carry on following.
func TestCtrlCStopsFollowingAndNotTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	others := streamBrain(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flushed(w, "id: 3\nevent: step\ndata: {\"step\":1}\n\n")
		cancel()
		<-r.Context().Done()
	})

	done := make(chan error, 1)
	go func() {
		_, err := clitest.ExecuteContext(ctx, t, commands, "runs", "events", "platform", runID)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an interrupted stream exited zero, as if the run had finished")
		}
		if !strings.Contains(err.Error(), "stopped following") ||
			!strings.Contains(err.Error(), "oa runs events platform "+runID+" --after 3") {
			t.Errorf("err = %v, want it to say following stopped and how to resume", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not stop the stream")
	}
	if n := others.Load(); n != 0 {
		t.Errorf("%d other requests were made — the run must be left alone", n)
	}
}

// The ordinary client gives up 30 seconds into any response, body included,
// so a run followed on it is cut off mid-stream — but only after 30 seconds,
// which no test waits for. What a test can see is the connection: the
// streaming client has a transport of its own, so its request never rides the
// keep-alive connection the team lookup just used.
func TestEventsAreReadOnTheStreamingClient(t *testing.T) {
	var lookup, stream atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("GET /teams", func(w http.ResponseWriter, r *http.Request) {
		lookup.Store(r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(oneTeam))
	})
	mux.HandleFunc("GET "+eventsPath, func(w http.ResponseWriter, r *http.Request) {
		stream.Store(r.RemoteAddr)
		_, _ = io.WriteString(w, ending("succeeded", `{"status":"succeeded"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	clitest.Isolate(t)
	t.Setenv("OPENARITY_SERVER", server.URL)
	t.Setenv("OPENARITY_TOKEN", "a-token")

	if out, err := clitest.Execute(t, commands, "runs", "events", "platform", runID); err != nil {
		t.Fatalf("runs events: %v\n%s", err, out)
	}
	if lookup.Load() == nil || stream.Load() == nil {
		t.Fatal("the team lookup or the stream never arrived")
	}
	if lookup.Load() == stream.Load() {
		t.Errorf("the events were read on the ordinary client's connection, %v", stream.Load())
	}
}

// --follow is start and events in one: the run is printed first so its id is
// on screen before anything else happens, then its events until it ends.
func TestStartFollowStreamsTheRunsEvents(t *testing.T) {
	routes := with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun})
	routes["GET /teams/{id}/runs/{runID}/events"] = clitest.Reply{Status: http.StatusOK, Body: succeededStream}
	script := clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "runs", "start", "platform", "triage", "go", "--follow")
	if err != nil {
		t.Fatalf("runs start --follow: %v\n%s", err, out)
	}

	all := script.All()
	if len(all) != 4 || all[1].Path != runsPath || all[2].Path != eventsPath || all[3].Path != runPath {
		t.Fatalf("requests = %+v, want the team, the start, the run's events, then the run for its answer", all)
	}
	started := strings.Index(out, "started")
	step := strings.Index(out, "⏺ github.search_code")
	if started < 0 || step < 0 || started > step || !strings.Contains(out, runID) {
		t.Errorf("the run is not printed before its events:\n%s", out)
	}
}

// Under -o json the whole output is a stream, so the run itself is one more
// line rather than an indented document a line reader cannot parse.
func TestStartFollowAsJSONIsOneObjectPerLine(t *testing.T) {
	routes := with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun})
	routes["GET /teams/{id}/runs/{runID}/events"] = clitest.Reply{Status: http.StatusOK, Body: succeededStream}
	clitest.Routes(t, routes)

	out, err := clitest.Execute(t, commands, "runs", "start", "platform", "triage", "go", "--follow", "-o", "json")
	if err != nil {
		t.Fatalf("runs start --follow -o json: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 9 {
		t.Fatalf("%d lines, want the run and 8 events:\n%s", len(lines), out)
	}
	var run runView
	if err := json.Unmarshal([]byte(lines[0]), &run); err != nil || run.ID != runID || run.Status != "queued" {
		t.Errorf("the first line is not the run: %v\n%s", err, lines[0])
	}
	for i, line := range lines[1:] {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d is not json: %s", i+1, line)
		}
	}
}

func TestStartFollowExitsByHowTheRunEnded(t *testing.T) {
	routes := with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun})
	routes["GET /teams/{id}/runs/{runID}/events"] = clitest.Reply{
		Status: http.StatusOK, Body: ending("failed", `{"status":"failed","error":"no model is set"}`),
	}
	clitest.Routes(t, routes)

	_, err := clitest.Execute(t, commands, "runs", "start", "platform", "triage", "go", "--follow")
	if err == nil || !strings.Contains(err.Error(), "no model is set") {
		t.Errorf("err = %v, want the run's failure", err)
	}
}

// Without --follow, start must return once the run is queued: a script that
// starts many runs in a loop cannot be held for each one to finish.
func TestStartWithoutFollowDoesNotStream(t *testing.T) {
	routes := with("POST /teams/{id}/runs", clitest.Reply{Status: http.StatusAccepted, Body: queuedRun})
	routes["GET /teams/{id}/runs/{runID}/events"] = clitest.Reply{Status: http.StatusOK, Body: succeededStream}
	script := clitest.Routes(t, routes)

	if out, err := clitest.Execute(t, commands, "runs", "start", "platform", "triage", "go"); err != nil {
		t.Fatalf("runs start: %v\n%s", err, out)
	}
	if n := script.Calls(http.MethodGet, eventsPath); n != 0 {
		t.Errorf("the events were followed %d times without --follow", n)
	}
}
