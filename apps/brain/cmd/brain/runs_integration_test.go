package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	skillName   = "triage-guide"
	skillBody   = "Count the open issues before answering."
	lookupTool  = "tracker__lookup"
	lookupText  = "3 open issues"
	finalAnswer = "Three issues need you."
)

// scriptedLLM speaks /chat/completions. What it answers depends only on how
// many tool results the request carries, so a run resumed from its
// transcript is answered exactly as it would have been without the
// interruption: load the skill, call the tool, answer.
type scriptedLLM struct {
	*httptest.Server

	mu       sync.Mutex
	requests [][]byte

	// holdAnswer, when set, blocks the request for the final answer until it
	// is closed, and says so on held the first time.
	holdAnswer chan struct{}
	held       chan struct{}
	heldOnce   sync.Once
}

func newScriptedLLM(t *testing.T) *scriptedLLM {
	t.Helper()

	m := &scriptedLLM{}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	return m
}

func (m *scriptedLLM) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.requests = append(m.requests, raw)
	m.mu.Unlock()

	var req struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.Error(w, "not a chat completion", http.StatusBadRequest)
		return
	}
	results := 0
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			results++
		}
	}

	switch results {
	case 0:
		writeCompletion(w, "", "Skill", fmt.Sprintf(`{"name":%q}`, skillName))
	case 1:
		writeCompletion(w, "", lookupTool, `{}`)
	default:
		if m.holdAnswer != nil {
			m.heldOnce.Do(func() { close(m.held) })
			select {
			case <-m.holdAnswer:
			case <-r.Context().Done():
				return
			}
		}
		writeCompletion(w, finalAnswer, "", "")
	}
}

func writeCompletion(w http.ResponseWriter, content, tool, args string) {
	msg := map[string]any{"role": "assistant", "content": content}
	finish := "stop"
	if tool != "" {
		msg["tool_calls"] = []map[string]any{{
			"id": "call_" + strings.ToLower(tool), "type": "function",
			"function": map[string]any{"name": tool, "arguments": args},
		}}
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion", "model": "test/model",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
	})
}

func (m *scriptedLLM) saw(s string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.requests {
		if bytes.Contains(r, []byte(s)) {
			return true
		}
	}
	return false
}

// tracker is an MCP server with one tool, counting how often it ran.
func tracker(t *testing.T) (url string, calls *atomic.Int32) {
	t.Helper()

	calls = &atomic.Int32{}
	server := sdk.NewServer(&sdk.Implementation{Name: "tracker", Version: "v1"}, nil)
	server.AddTool(&sdk.Tool{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: lookupText}}}, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
	})
	return httpServer.URL, calls
}

type brain struct {
	api   string
	raw   *pgx.Conn
	llm   *scriptedLLM
	calls *atomic.Int32
	team  uuid.UUID
}

// newBrain is serve, on a fresh database, with a team the development user
// is a member of, and an agent there granted one MCP tool and one skill.
// No worker yet: each test starts its own.
func newBrain(t *testing.T, llm *scriptedLLM) *brain {
	t.Helper()

	dsn, apiBind := runtimeEnv(t)
	mcpURL, calls := tracker(t)
	t.Setenv("OPENARITY_LLM_BASE_URL", llm.URL+"/v1")
	t.Setenv("OPENARITY_LLM_API_KEY", "sk-test")
	t.Setenv("OPENARITY_MCP_PRIVATE_NETWORKS", "127.0.0.0/8")
	mustMigrate(t)

	ctx, stop := context.WithCancel(t.Context())
	var log syncBuffer
	done := make(chan error, 1)
	go func() { done <- run(ctx, &log, nil) }()
	t.Cleanup(func() {
		stop()
		stopped(t, "serve", done, &log)
	})
	waitForHealthz(t, apiBind)

	raw, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(context.WithoutCancel(t.Context())) })

	b := &brain{api: "http://" + apiBind, raw: raw, llm: llm, calls: calls}

	// The first authenticated request is what creates the user.
	if status, _ := b.call(t, http.MethodGet, "/whoami", nil); status != http.StatusOK {
		t.Fatalf("whoami: %d", status)
	}

	var user, agentID, server, skill uuid.UUID
	for _, q := range []struct {
		sql  string
		args []any
		into *uuid.UUID
	}{
		{`SELECT id FROM users WHERE issuer = 'dev' AND subject = 'dev'`, nil, &user},
		{`INSERT INTO teams (name) VALUES ('platform') RETURNING id`, nil, &b.team},
		{
			`INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps, instructions)
		  VALUES ($1, 'triage', 'test/model', 1024, 'react', 6, 'You sort issues.') RETURNING id`,
			[]any{&b.team},
			&agentID,
		},
		{
			`INSERT INTO mcp_servers (team_id, name, url) VALUES ($1, 'tracker', $2) RETURNING id`,
			[]any{&b.team, mcpURL},
			&server,
		},
		{
			`INSERT INTO skills (team_id, name, description, body) VALUES ($1, $2, 'How to triage', $3) RETURNING id`,
			[]any{&b.team, skillName, skillBody},
			&skill,
		},
	} {
		args := make([]any, len(q.args))
		for i, a := range q.args {
			if id, ok := a.(*uuid.UUID); ok {
				a = *id
			}
			args[i] = a
		}
		if err := raw.QueryRow(t.Context(), q.sql, args...).Scan(q.into); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member')`, []any{b.team, user}},
		{
			`INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id, allow) VALUES ($1, $2, $3, '{lookup}')`,
			[]any{agentID, server, b.team},
		},
		{`INSERT INTO agent_skills (agent_id, skill_id, team_id) VALUES ($1, $2, $3)`, []any{agentID, skill, b.team}},
	} {
		if _, err := raw.Exec(t.Context(), q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	return b
}

// call makes one request as the development user and reads the whole reply.
func (b *brain) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()

	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, b.api+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer integration-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading the reply: %v", method, path, err)
	}
	return resp.StatusCode, raw
}

type runView struct {
	ID           uuid.UUID `json:"id"`
	Status       string    `json:"status"`
	Output       *string   `json:"output"`
	Error        *string   `json:"error"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
}

func (b *brain) start(t *testing.T) runView {
	t.Helper()

	status, raw := b.call(t, http.MethodPost, "/teams/"+b.team.String()+"/runs",
		map[string]string{"agent": "triage", "input": "What needs me today?"})
	if status != http.StatusAccepted {
		t.Fatalf("POST runs: %d %s", status, raw)
	}
	var r runView
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return r
}

func (b *brain) get(t *testing.T, id uuid.UUID) runView {
	t.Helper()

	status, raw := b.call(t, http.MethodGet, "/teams/"+b.team.String()+"/runs/"+id.String(), nil)
	if status != http.StatusOK {
		t.Fatalf("GET run: %d %s", status, raw)
	}
	var r runView
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return r
}

type sseFrame struct{ event, data string }

// follow reads the run's event stream to its end frame.
func (b *brain) follow(t *testing.T, id uuid.UUID, within time.Duration) []sseFrame {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), within)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		b.api+"/teams/"+b.team.String()+"/runs/"+id.String()+"/events", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer integration-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET events: %d %s", resp.StatusCode, raw)
	}

	var frames []sseFrame
	var f sseFrame
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		case line == "" && f.event != "":
			frames = append(frames, f)
			if f.event == "end" {
				return frames
			}
			f = sseFrame{}
		}
	}
	t.Fatalf("the stream stopped without an end frame (%v) after %+v", scanner.Err(), frames)
	return nil
}

// startWorker runs `brain worker` until stop is called, and returns once it
// says it is running.
func startWorker(t *testing.T) (stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	var log syncBuffer
	done := make(chan error, 1)
	go func() { done <- run(ctx, &log, []string{"worker"}) }()

	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(log.String(), "Worker running") {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("the worker stopped while starting: %v\n%s", err, log.String())
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("the worker never said it was running:\n%s", log.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			stopped(t, "worker", done, &log)
		})
	}
	t.Cleanup(stop)
	return stop
}

// kinds is the event names in order, with each tool event named by its tool.
func kinds(frames []sseFrame) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		name := f.event
		if f.event == "tool_call" || f.event == "tool_result" {
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal([]byte(f.data), &p)
			name += ":" + p.Name
		}
		out = append(out, name)
	}
	return out
}

// inOrder reports whether want appears in got as a subsequence.
func inOrder(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

var aWholeRun = []string{
	"started",
	"tool_call:Skill", "tool_result:Skill",
	"tool_call:" + lookupTool, "tool_result:" + lookupTool,
	"finished", "end",
}

func (b *brain) count(t *testing.T, query string, args ...any) int {
	t.Helper()

	var n int
	if err := b.raw.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// Everything slice 1 promises, through the binary as it ships: the API takes
// the run, the worker builds the agent from its row with its granted tool and
// skill, the model is reached at OPENARITY_LLM_BASE_URL, and what happens is
// streamed back as it happens.
func TestARunStartedThroughTheAPIIsExecutedAndStreamed(t *testing.T) {
	b := newBrain(t, newScriptedLLM(t))
	stopWorker := startWorker(t)

	r := b.start(t)
	if r.Status != "queued" {
		t.Errorf("a new run is %q, want queued", r.Status)
	}

	frames := b.follow(t, r.ID, 60*time.Second)
	if got := kinds(frames); !inOrder(got, aWholeRun) {
		t.Errorf("events %v\nwant in order %v", got, aWholeRun)
	}
	if end := frames[len(frames)-1]; end.data != `{"status":"succeeded"}` {
		t.Errorf("end = %s", end.data)
	}
	for _, f := range frames {
		if f.event == "tool_result" && strings.Contains(f.data, lookupTool) && !strings.Contains(f.data, lookupText) {
			t.Errorf("the tool's result is not what the server answered: %s", f.data)
		}
	}

	got := b.get(t, r.ID)
	if got.Status != "succeeded" || got.Output == nil || *got.Output != finalAnswer {
		t.Errorf("run = %+v, want succeeded with the model's answer", got)
	}
	if got.InputTokens != 300 || got.OutputTokens != 60 {
		t.Errorf("tokens = %d in, %d out; want three turns of 100 and 20", got.InputTokens, got.OutputTokens)
	}
	if !b.llm.saw(skillBody) || !b.llm.saw(lookupText) {
		t.Error("the model was never shown the skill's body and the tool's answer")
	}
	if n := b.calls.Load(); n != 1 {
		t.Errorf("the MCP tool ran %d times, want once", n)
	}

	// Deleting the team takes everything the run left, including the
	// transcript, which the SDK keys by a string no foreign key can follow.
	stopWorker()
	transcript := b.count(t, `SELECT count(*) FROM agent_messages WHERE session = $1`, r.ID.String())
	if transcript == 0 {
		t.Fatal("the run left no transcript, so its deletion proves nothing")
	}
	if _, err := b.raw.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, b.team); err != nil {
		t.Fatalf("delete the team: %v", err)
	}
	for _, table := range []string{"runs", "run_events", "agent_messages", "agent_steers"} {
		if n := b.count(t, "SELECT count(*) FROM "+table); n != 0 {
			t.Errorf("%d rows left in %s after the team was deleted", n, table)
		}
	}
}

// A worker that stops mid-run leaves it running with a heartbeat that stops
// moving. Another worker's sweep takes it over and carries on from the
// transcript: the tool that already ran is not run again, and the record
// reads as one run.
func TestARunWhoseWorkerStopsIsFinishedByAnother(t *testing.T) {
	llm := newScriptedLLM(t)
	llm.holdAnswer, llm.held = make(chan struct{}), make(chan struct{})
	b := newBrain(t, llm)

	stopFirst := startWorker(t)
	r := b.start(t)

	select {
	case <-llm.held:
	case <-time.After(60 * time.Second):
		t.Fatal("the run never reached its last turn")
	}
	stopFirst()
	close(llm.holdAnswer)

	if got := b.get(t, r.ID); got.Status != "running" {
		t.Fatalf("after its worker stopped the run is %q, want running for another to take over", got.Status)
	}
	// What a dead worker looks like after a while, without waiting the while.
	if _, err := b.raw.Exec(t.Context(), `
		UPDATE runs SET heartbeat_at = now() - interval '10 minutes',
		    started_at = now() - interval '10 minutes', created_at = now() - interval '10 minutes'
		WHERE id = $1`, r.ID); err != nil {
		t.Fatalf("age the run: %v", err)
	}

	startWorker(t)
	// The sweep runs once a minute, on the minute.
	frames := b.follow(t, r.ID, 90*time.Second)

	got := kinds(frames)
	if !inOrder(got, aWholeRun) || !inOrder(got, []string{"tool_result:" + lookupTool, "resumed", "finished"}) {
		t.Errorf("events %v\nwant the whole run, resumed after the tool", got)
	}
	if end := frames[len(frames)-1]; end.data != `{"status":"succeeded"}` {
		t.Errorf("end = %s", end.data)
	}
	if got := b.get(t, r.ID); got.Status != "succeeded" || got.Output == nil || *got.Output != finalAnswer {
		t.Errorf("run = %+v, want succeeded with the model's answer", got)
	}
	if n := b.calls.Load(); n != 1 {
		t.Errorf("the MCP tool ran %d times; a takeover must not run it again", n)
	}
}
