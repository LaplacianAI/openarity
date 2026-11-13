package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// scripted answers a summary request with a summary and anything else with
// "ok", recording every request and counting what it was asked.
type scripted struct {
	mu   sync.Mutex
	sent []Request

	summary string
	usage   Usage
	fail    error // returned to a summary request
	refuse  int   // refuse this many ordinary requests as too long first
}

func isSummaryRequest(req Request) bool {
	n := len(req.Messages)
	return n > 0 && strings.HasPrefix(req.Messages[n-1].Text(), "Pause the task.")
}

func (s *scripted) Complete(_ context.Context, req Request) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, req)
	if isSummaryRequest(req) {
		if s.fail != nil {
			return Response{Usage: s.usage}, s.fail
		}
		text := s.summary
		if text == "" {
			text = "Goal: the task."
		}
		return Response{Message: say(RoleAssistant, text), Finish: FinishStop, Usage: s.usage}, nil
	}
	if s.refuse > 0 {
		s.refuse--
		return Response{}, &ModelError{Status: 400, Code: "context_length_exceeded"}
	}
	return Response{Message: say(RoleAssistant, "ok"), Finish: FinishStop, Usage: s.usage}, nil
}

func (s *scripted) Stream(ctx context.Context, req Request) (Stream, error) {
	return nil, errors.New("not streamed here")
}

func (s *scripted) requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.sent...)
}

func (s *scripted) summaries() int {
	n := 0
	for _, r := range s.requests() {
		if isSummaryRequest(r) {
			n++
		}
	}
	return n
}

func say(role Role, text string) Message {
	return Message{Role: role, Content: []Content{{Type: ContentText, Text: text}}}
}

func called(id, name string) Message {
	return Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(`{}`)}}}
}

func result(id, text string) Message {
	return Message{Role: RoleTool, ToolCallID: id, Content: []Content{{Type: ContentText, Text: text}}}
}

// conversation is a task and then n tool calls with their results.
func conversation(n int, out func(i int) string) []Message {
	msgs := []Message{say(RoleUser, "review the pull request")}
	for i := range n {
		id := fmt.Sprintf("call_%d", i)
		msgs = append(msgs, called(id, "read_file"), result(id, out(i)))
	}
	return msgs
}

type compactRig struct {
	model  *scripted
	client *compactingClient
	handle *compactHandle

	mu      sync.Mutex
	events  []CompactedEvent
	usages  []UsageEvent
	counted Usage
}

func rig(cfg Compaction) *compactRig {
	r := &compactRig{model: &scripted{}, handle: &compactHandle{}}
	r.client = newCompactingClient(r.model, cfg, r.handle,
		func(u Usage) { r.mu.Lock(); r.counted.Add(u); r.mu.Unlock() },
		func(e Event) {
			r.mu.Lock()
			defer r.mu.Unlock()
			switch e := e.(type) {
			case CompactedEvent:
				r.events = append(r.events, e)
			case UsageEvent:
				r.usages = append(r.usages, e)
			}
		})
	return r
}

func (r *compactRig) send(t *testing.T, msgs []Message) []Message {
	t.Helper()
	if _, err := r.client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}
	reqs := r.model.requests()
	return reqs[len(reqs)-1].Messages
}

func (r *compactRig) compacted() []CompactedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CompactedEvent(nil), r.events...)
}

func TestNothingCompactsUnderTheThresholds(t *testing.T) {
	r := rig(Compaction{AfterMessages: 100, AfterInputTokens: 100_000})
	msgs := conversation(5, func(int) string { return "small" })

	sent := r.send(t, msgs)

	if digest(sent) != digest(msgs) {
		t.Fatalf("an uncompacted request changed on its way to the model")
	}
	if r.model.summaries() != 0 || len(r.compacted()) != 0 {
		t.Fatalf("compacted under the thresholds")
	}
}

func TestPastTheMessageCountOldTurnsFoldIntoASummary(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10, KeepTokens: 10})
	r.model.summary = "Goal: review it. Findings: two bugs."
	msgs := conversation(10, func(i int) string { return fmt.Sprintf("file %d", i) })

	sent := r.send(t, msgs)

	if text(sent[0]) != "review the pull request" {
		t.Fatalf("the task is not first word for word: %q", text(sent[0]))
	}
	if !strings.Contains(text(sent[1]), "two bugs") {
		t.Fatalf("the summary is not second: %q", text(sent[1]))
	}
	if len(sent) >= len(msgs) {
		t.Fatalf("sent %d messages from %d; nothing was folded", len(sent), len(msgs))
	}
	last := msgs[len(msgs)-1]
	if got := sent[len(sent)-1]; got.ToolCallID != last.ToolCallID || text(got) != text(last) {
		t.Fatalf("the latest result is not kept word for word")
	}
	events := r.compacted()
	if len(events) != 1 || events[0].Reason != "messages" || events[0].Folded == 0 {
		t.Fatalf("events = %+v, want one messages compaction that folded", events)
	}
}

func TestTheSummaryRequestStartsWithWhatTheModelLastSaw(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10, KeepTokens: 10})
	system := System("you review code")
	tools := []Tool{{Name: "read_file", Schema: json.RawMessage(`{}`)}}

	small := conversation(3, func(int) string { return "x" })
	if _, err := r.client.Complete(t.Context(), Request{System: system, Tools: tools, Messages: small}); err != nil {
		t.Fatal(err)
	}
	more := conversation(10, func(int) string { return "x" })
	if _, err := r.client.Complete(t.Context(), Request{System: system, Tools: tools, Messages: more}); err != nil {
		t.Fatal(err)
	}

	var ask Request
	for _, req := range r.model.requests() {
		if isSummaryRequest(req) {
			ask = req
		}
	}
	if len(ask.Tools) != 1 || len(ask.System) != 1 {
		t.Fatalf("the summary request has %d tools and %d system blocks; it must carry the run's own, for the cache",
			len(ask.Tools), len(ask.System))
	}
	prefix := ask.Messages[:len(ask.Messages)-1]
	if digest(prefix) != digest(more) {
		t.Fatalf("the summary request does not start with the conversation the model was about to see")
	}
}

// Small messages never reach KeepTokens, so the message count alone has to
// bound what is kept, or the count triggers a compaction that folds nothing.
func TestManySmallMessagesFoldWithTheDefaultKeep(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10})
	msgs := conversation(15, func(int) string { return "x" })

	sent := r.send(t, msgs)

	// The task, the summary, five recent messages, and one more when the
	// fifth is a result, so the part starts at its call.
	if r.model.summaries() != 1 || len(sent) > 2+5+1 {
		t.Fatalf("sent %d of %d messages after %d summaries; past ten messages, about five stay word for word",
			len(sent), len(msgs), r.model.summaries())
	}
}

func TestTheCutNeverStartsOnAToolResult(t *testing.T) {
	// Two results per call: the recent part must start at the call, not between them.
	msgs := []Message{say(RoleUser, "task")}
	for i := range 8 {
		a, b := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		msgs = append(msgs,
			Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: a, Name: "t"}, {ID: b, Name: "t"}}},
			result(a, strings.Repeat("y", 40)), result(b, strings.Repeat("y", 40)))
	}
	r := rig(Compaction{AfterMessages: 5, KeepTokens: 5})

	sent := r.send(t, msgs)

	if sent[2].Role == RoleTool {
		t.Fatalf("the recent part starts on a tool result, split from its call")
	}
}

func TestRecentTurnsAreKeptBySizeNotCount(t *testing.T) {
	c := &compactingClient{cfg: Compaction{KeepTokens: 1000}}
	msgs := conversation(6, func(int) string { return strings.Repeat("z", 1600) }) // 400 tokens each

	from := c.keepFrom(msgs)

	kept := msgs[from:]
	if n := tokensOf(kept); n < 1000 || n > 1000+400+100 {
		t.Fatalf("kept %d tokens over %d messages, want just over 1000", n, len(kept))
	}
}

func TestClearingOldResultsIsEnoughWithoutAModelCall(t *testing.T) {
	r := rig(Compaction{AfterInputTokens: 20_000, KeepTokens: 1_000})
	msgs := conversation(12, func(int) string { return strings.Repeat("d", 8000) }) // 2k tokens each

	sent := r.send(t, msgs)

	if r.model.summaries() != 0 {
		t.Fatalf("summarised when clearing old results was enough")
	}
	if !strings.Contains(text(sent[2]), "cleared to save room") || !strings.Contains(text(sent[2]), ReadOutputToolName) {
		t.Fatalf("an old result was not cleared to a handle: %.80q", text(sent[2]))
	}
	if text(sent[len(sent)-1]) != text(msgs[len(msgs)-1]) {
		t.Fatalf("the latest result was cleared")
	}
	if e := r.compacted(); len(e) != 1 || e[0].Cleared == 0 || e[0].Folded != 0 {
		t.Fatalf("events = %+v, want one that cleared and folded nothing", e)
	}
}

func TestTheEstimateCountsWhatArrivedSinceTheLastCall(t *testing.T) {
	r := rig(Compaction{AfterInputTokens: 50_000, KeepTokens: 1_000})
	r.model.usage = Usage{InputTokens: 1_000}
	msgs := conversation(4, func(int) string { return "small" })
	r.send(t, msgs)

	// Three 90 KiB results arrive, each under the preview line: about 69k
	// tokens the last call never read, so only the estimate can see them.
	for i := range 3 {
		id := fmt.Sprintf("medium_%d", i)
		msgs = append(msgs, called(id, "get_diff"), result(id, strings.Repeat("+", 90<<10)))
	}
	msgs = append(msgs, called("next", "read_file"), result("next", "small"))
	r.send(t, msgs)

	if e := r.compacted(); len(e) == 0 || e[0].Reason != "tokens" {
		t.Fatalf("events = %+v; the large result should compact before the call that would read it", e)
	}
}

func TestALargeResultIsPreviewedWithAHandle(t *testing.T) {
	r := rig(Compaction{AfterMessages: 1000})
	big := strings.Repeat("line of the diff\n", 20_000) // 340 KiB
	msgs := []Message{say(RoleUser, "task"), called("call_7", "get_diff"), result("call_7", big)}

	sent := r.send(t, msgs)

	got := text(sent[2])
	if len(got) > 16<<10 {
		t.Fatalf("the preview is %d bytes, want about 12 KiB", len(got))
	}
	for _, want := range []string{"call_7", "get_diff", ReadOutputToolName, "from_line", "match"} {
		if !strings.Contains(got, want) {
			t.Errorf("the preview does not mention %q", want)
		}
	}
	if text(msgs[2]) != big {
		t.Fatalf("previewing changed the caller's messages")
	}
}

func TestAPreviewCutsOnRuneBoundaries(t *testing.T) {
	big := "a" + strings.Repeat("é", 120<<10) + "b"
	got := rig(Compaction{AfterMessages: 10}).client.shown(result("c", big), "t")

	if !utf8.ValidString(text(got)) {
		t.Fatalf("the preview cut a rune in half")
	}
}

func TestTheSessionKeepsALargeResultWhole(t *testing.T) {
	big := strings.Repeat("x", 300<<10)
	store := newFakeStore()
	session, err := Open("s", store)
	if err != nil {
		t.Fatal(err)
	}
	model := &scripted{}
	pattern := &callingPattern{msgs: []Message{say(RoleUser, "task"), called("c", "t"), result("c", big)}}
	runner, err := New(func(Endpoint) (ModelClient, error) { return model, nil }, pattern)
	if err != nil {
		t.Fatal(err)
	}

	_, err = runner.Run(t.Context(), Spec{
		Pattern: "calling", MaxSteps: 1, Session: session,
		Compaction: &Compaction{AfterMessages: 1000},
	}, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	saved, _ := session.Messages(t.Context())
	if len(saved) < 3 || text(saved[2]) != big {
		t.Fatalf("the session does not hold the result whole")
	}
	if got := text(model.requests()[0].Messages[2]); len(got) >= len(big) {
		t.Fatalf("the model was sent the whole result")
	}
}

func TestAFailedSummarySendsTheRequestWholeAndWaits(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10, KeepTokens: 10})
	r.model.fail = errors.New("the gateway is down")
	msgs := conversation(10, func(int) string { return "x" })

	sent := r.send(t, msgs)
	if len(sent) != len(msgs) {
		t.Fatalf("a failed summary still changed the request: %d messages from %d", len(sent), len(msgs))
	}
	if e := r.compacted(); len(e) != 1 || !strings.Contains(e[0].Err, "the gateway is down") {
		t.Fatalf("events = %+v, want the failure said", e)
	}

	msgs = append(msgs, called("again", "t"), result("again", "x"))
	r.send(t, msgs)
	if n := r.model.summaries(); n != 1 {
		t.Fatalf("tried %d summaries; a failed one waits %d messages before trying again", n, retryGap)
	}
}

func TestTheSummaryCallIsCountedAndAnnounced(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10, KeepTokens: 10})
	r.model.usage = Usage{InputTokens: 300, OutputTokens: 40}

	r.send(t, conversation(10, func(int) string { return "x" }))

	if r.counted != (Usage{InputTokens: 300, OutputTokens: 40}) {
		t.Fatalf("counted %+v; the summary call's tokens must join the run's", r.counted)
	}
	if len(r.usages) != 1 || !r.usages[0].Summary {
		t.Fatalf("usage events %+v; the summary call must be announced as one, so the budget counts it "+
			"and a wrap-up does not read its size as the next call's", r.usages)
	}
}

func TestATooLongRequestCompactsAndIsTriedOnceMore(t *testing.T) {
	r := rig(Compaction{AfterMessages: 1000, KeepTokens: 10})
	r.model.refuse = 1
	msgs := conversation(10, func(int) string { return "x" })

	if _, err := r.client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatalf("Complete() = %v; one refusal should compact and retry", err)
	}
	if e := r.compacted(); len(e) != 1 || e[0].Reason != "overflow" {
		t.Fatalf("events = %+v, want an overflow compaction", e)
	}

	r.model.refuse = 2
	if _, err := r.client.Complete(t.Context(), Request{Messages: msgs}); !overflowed(err) {
		t.Fatalf("Complete() = %v; a second refusal must come back, not loop", err)
	}
}

func TestCompactNowFoldsBelowTheThresholds(t *testing.T) {
	r := rig(Compaction{AfterMessages: 1000, KeepTokens: 10})
	msgs := conversation(6, func(int) string { return "x" })

	r.handle.force.Store(true)
	sent := r.send(t, msgs)

	if len(sent) >= len(msgs) || r.model.summaries() != 1 {
		t.Fatalf("asked to compact, it sent %d of %d messages after %d summaries", len(sent), len(msgs), r.model.summaries())
	}
	if e := r.compacted(); e[0].Reason != "asked" {
		t.Fatalf("reason = %q, want asked", e[0].Reason)
	}
}

func TestAConversationThatNoLongerStartsTheSameStartsOver(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10, KeepTokens: 10})
	r.send(t, conversation(10, func(int) string { return "x" }))

	// As long as the first, so only the prefix can tell them apart.
	other := conversation(10, func(int) string { return "a different run" })
	other[0] = say(RoleUser, "a different task")
	r.handle.force.Store(false)
	r.client.cfg.AfterMessages = 1000
	sent := r.send(t, other)

	if digest(sent) != digest(other) {
		t.Fatalf("a summary of another conversation was applied to this one")
	}
}

func TestTwoCompactionsLeaveTwoSummariesAndTheModelSeesTheLatest(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10, KeepTokens: 10})
	r.model.summary = "Goal: the first summary."
	r.send(t, conversation(10, func(int) string { return "x" }))
	r.model.summary = "Goal: the second summary."
	longer := conversation(20, func(int) string { return "x" })
	sent := r.send(t, longer)

	marks := summariesIn(r.handle.splice(longer))
	if len(marks) != 2 || marks[0].Summary != "Goal: the first summary." || marks[1].Summary != "Goal: the second summary." {
		t.Fatalf("the transcript holds %+v; want both summaries, in order", marks)
	}
	if s := textOf(sent); strings.Contains(s, "the first summary") || !strings.Contains(s, "the second summary") {
		t.Fatalf("the model was sent %q; want the latest summary alone", s)
	}
}

func TestTheUsersWordsAndReferencesSurviveTheSummary(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10, KeepTokens: 10})
	r.model.summary = "Goal: something vague."
	msgs := []Message{
		say(RoleUser, "review the pull request"),
		say(RoleUser, "The user sent this while you were working:\nonly the brain, not the CLI"),
		called("c1", "get"), result("c1", "see https://github.com/LaplacianAI/openarity/pull/49 and apps/brain/internal/runtime/report.go"),
	}
	msgs = append(msgs, conversation(10, func(int) string { return "x" })[1:]...)

	sent := r.send(t, msgs)

	s := text(sent[1])
	for _, want := range []string{
		"only the brain, not the CLI",
		"https://github.com/LaplacianAI/openarity/pull/49",
		"apps/brain/internal/runtime/report.go",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the summary message lost %q", want)
		}
	}
}

func TestSkillsLoadedEarlierComeBackWordForWord(t *testing.T) {
	r := rig(Compaction{AfterMessages: 10, KeepTokens: 10})
	body := "Severity rubric: P0 when data is lost."
	msgs := []Message{say(RoleUser, "task"), called("s1", SkillToolName), result("s1", body)}
	msgs = append(msgs, conversation(10, func(int) string { return "x" })[1:]...)

	sent := r.send(t, msgs)

	if !strings.Contains(text(sent[1]), body) {
		t.Fatalf("a skill loaded before the cut did not come back word for word")
	}
}

func TestASkillIsNeverCleared(t *testing.T) {
	r := rig(Compaction{AfterInputTokens: 20_000, KeepTokens: 1_000})
	skill := strings.Repeat("rule ", 2000)
	msgs := []Message{say(RoleUser, "task"), called("s1", SkillToolName), result("s1", skill)}
	msgs = append(msgs, conversation(12, func(int) string { return strings.Repeat("d", 8000) })[1:]...)

	sent := r.send(t, msgs)

	if text(sent[2]) != skill {
		t.Fatalf("clearing reduced a skill to a handle")
	}
}

func TestReadOutputReadsARangeAndAMatch(t *testing.T) {
	h := &compactHandle{}
	var lines []string
	for i := 1; i <= 500; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines[299] = "func checkDelegation(ctx context.Context)"
	h.setFull([]Message{called("c7", "get"), result("c7", strings.Join(lines, "\n"))})
	tool := readOutputTool(h)

	got, err := tool.Invoke(t.Context(), json.RawMessage(`{"call":"c7","from_line":10,"lines":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "10: line 10\n11: line 11\n12: line 12\n") || strings.Contains(got, "13: line 13") {
		t.Fatalf("range read = %q", got)
	}

	got, err = tool.Invoke(t.Context(), json.RawMessage(`{"call":"c7","match":"func checkDelegation"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "300: func checkDelegation") {
		t.Fatalf("match read = %q", got)
	}
}

func TestReadOutputReadsTheLatestResultWithAReusedID(t *testing.T) {
	h := &compactHandle{}
	h.setFull([]Message{result("call_1", "the first turn's"), result("call_1", "the latest")})

	got, err := readOutputTool(h).Invoke(t.Context(), json.RawMessage(`{"call":"call_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "the latest") {
		t.Fatalf("read %q, want the latest result with the id", got)
	}
}

func TestOneReadOutputReadIsCapped(t *testing.T) {
	h := &compactHandle{}
	h.setFull([]Message{result("c", strings.Repeat(strings.Repeat("a", 3000)+"\n", 1000))})

	got, err := readOutputTool(h).Invoke(t.Context(), json.RawMessage(`{"call":"c","lines":2000}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > readBytes+1024 || !strings.Contains(got, "read on from line") {
		t.Fatalf("one read returned %d bytes, want at most about %d and where to read on", len(got), readBytes)
	}
}

func TestReadOutputSplitsAVeryLongLine(t *testing.T) {
	h := &compactHandle{}
	h.setFull([]Message{result("c", strings.Repeat("m", 20<<10))}) // one minified line

	got, err := readOutputTool(h).Invoke(t.Context(), json.RawMessage(`{"call":"c","from_line":3,"lines":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "3: m") {
		t.Fatalf("a 20 KiB line was not readable in parts: %.100q", got)
	}
}

func TestReadOutputRefusesWhatItCannotRead(t *testing.T) {
	h := &compactHandle{}
	h.setFull([]Message{result("c", "x")})
	tool := readOutputTool(h)

	for _, args := range []string{
		`{"call":"nope"}`,
		`{"call":"c","match":"("}`,
		`{"call":"c","lines":5000}`,
		`{"call":"c","extra":1}`,
	} {
		if _, err := tool.Invoke(t.Context(), json.RawMessage(args)); err == nil {
			t.Errorf("Invoke(%s) succeeded, want a refusal", args)
		}
	}
}

func TestACompactionThatCouldNeverTriggerIsRefused(t *testing.T) {
	for _, c := range []Compaction{{}, {AfterMessages: -1}, {AfterInputTokens: 10, KeepTokens: -1}} {
		if checkCompaction(&c, nil) == nil {
			t.Errorf("checkCompaction(%+v) = nil", c)
		}
	}
}

func TestACompactingRunRefusesAToolNamedReadOutput(t *testing.T) {
	runner, err := New(func(Endpoint) (ModelClient, error) { return &scripted{}, nil }, &callingPattern{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), Spec{
		Pattern: "calling", MaxSteps: 1, Tools: []Tool{{Name: ReadOutputToolName}},
		Compaction: &Compaction{AfterMessages: 10},
	}, nil, Endpoint{}, nil)
	if err == nil || !strings.Contains(err.Error(), ReadOutputToolName) {
		t.Fatalf("Run() = %v, want the clash named", err)
	}
}

func TestACompactingRunHasReadOutput(t *testing.T) {
	pattern := &callingPattern{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return &scripted{}, nil }, pattern)
	if _, err := runner.Run(t.Context(), Spec{
		Pattern: "calling", MaxSteps: 1, Compaction: &Compaction{AfterMessages: 10},
	}, nil, Endpoint{}, nil); err != nil {
		t.Fatal(err)
	}
	if !hasTool(pattern.tools, ReadOutputToolName) {
		t.Fatalf("a compacting run has no %s", ReadOutputToolName)
	}

	pattern = &callingPattern{}
	runner, _ = New(func(Endpoint) (ModelClient, error) { return &scripted{}, nil }, pattern)
	if _, err := runner.Run(t.Context(), Spec{Pattern: "calling", MaxSteps: 1}, nil, Endpoint{}, nil); err != nil {
		t.Fatal(err)
	}
	if hasTool(pattern.tools, ReadOutputToolName) {
		t.Fatalf("a run that does not compact has %s", ReadOutputToolName)
	}
}

func hasTool(tools []Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// callingPattern sends its messages to the model once and keeps the tools it
// was handed.
type callingPattern struct {
	msgs  []Message
	tools []Tool
}

func (*callingPattern) Name() PatternName { return "calling" }

func (p *callingPattern) Run(ctx context.Context, in Input) (Result, error) {
	p.tools = in.Spec.Tools
	msgs := p.msgs
	if msgs == nil {
		msgs = []Message{say(RoleUser, "task")}
	}
	resp, err := in.Model.Complete(ctx, Request{Model: in.Spec.Model, Messages: msgs, Tools: in.Spec.Tools})
	if err != nil {
		return Result{}, err
	}
	return Result{Output: resp.Message.Text(), Messages: append(msgs, resp.Message), Steps: 1}, nil
}

// Reasoning a turn sends back is input the model reads, so it counts towards
// when a run compacts.
func TestReasoningSentBackIsCounted(t *testing.T) {
	t.Parallel()

	r := &Reasoning{Fields: map[string]json.RawMessage{"reasoning_details": json.RawMessage(strings.Repeat("x", 400))}}
	if got, want := tokensOf([]Message{{Role: RoleAssistant, Reasoning: r}}), 400/charsPerToken; got != want {
		t.Errorf("counted %d tokens, want %d", got, want)
	}
}
