package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/stream"
)

func seq(n int64) *int64 { return &n }

func eventRow(n int64, kind string, run *uuid.UUID, payload string) db.ListSessionEventsRow {
	return db.ListSessionEventsRow{Seq: seq(n), At: time.Unix(1763000000+n, 0).UTC(), Kind: kind, RunID: run, Payload: []byte(payload)}
}

// Each kind the runtime records becomes the stream's event of that meaning,
// with the field names the runtime writes.
func TestEveryRecordedKindBecomesItsEvent(t *testing.T) {
	run, parent, msg, user := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	agent, output := "billing", "The migration timed out."
	cases := []struct {
		row  db.ListSessionEventsRow
		want stream.Event
	}{
		{
			eventRow(1, "message", nil, `{"id":"`+msg.String()+`","user_id":"`+user.String()+`","text":"why?","asks":true}`),
			stream.Message{ID: msg.String(), UserID: user.String(), Parts: []stream.Part{{Type: stream.PartText, Text: "why?"}}, State: stream.StatePending},
		},
		{
			eventRow(2, "message", nil, `{"id":"`+msg.String()+`","user_id":"`+user.String()+`","text":"aside","asks":false}`),
			stream.Message{ID: msg.String(), UserID: user.String(), Parts: []stream.Part{{Type: stream.PartText, Text: "aside"}}},
		},
		{func() db.ListSessionEventsRow {
			r := eventRow(3, "started", &run, `{"agent":"x"}`)
			r.Agent, r.ParentRunID = &agent, &parent
			return r
		}(), stream.RunStarted{Agent: agent, ParentRunID: parent.String()}},
		{eventRow(4, "step", &run, `{"step":2}`), stream.StepStarted{Step: 2}},
		{eventRow(5, "text", &run, `{"text":"The ","truncated":true}`), stream.TextMessageChunk{Delta: "The ", Truncated: true}},
		{
			eventRow(6, "tool_call", &run, `{"id":"c1","name":"search","arguments":"{\"q\":1}","truncated":false}`),
			stream.ToolCallChunk{ToolCallID: "c1", Name: "search", Arguments: `{"q":1}`},
		},
		{
			eventRow(7, "tool_result", &run, `{"id":"c1","name":"search","output":"3 hits","error":"slow","truncated":true,"duration_ms":40}`),
			stream.ToolCallResult{ToolCallID: "c1", Name: "search", Content: "3 hits", Error: "slow", Truncated: true, DurationMS: 40},
		},
		{
			eventRow(8, "usage", &run, `{"model":"m","input_tokens":1,"output_tokens":2,"cached_input_tokens":3,"spent":9}`),
			stream.Usage{Model: "m", InputTokens: 1, OutputTokens: 2, CachedInputTokens: 3},
		},
		{
			eventRow(9, "compacted", &run, `{"reason":"tokens","cleared":1,"folded":2,"before":3,"after":4,"summary":"s"}`),
			stream.Compacted{Reason: "tokens", Cleared: 1, Folded: 2, Before: 3, After: 4, Summary: "s"},
		},
		{func() db.ListSessionEventsRow {
			r := eventRow(10, "finished", &run, `{"status":"succeeded"}`)
			r.Output = &output
			return r
		}(), stream.RunFinished{Status: stream.StatusSucceeded, Output: output}},
		{eventRow(11, "finished", &run, `{"status":"failed","error":"budget"}`), stream.RunFinished{Status: stream.StatusFailed, Error: "budget"}},
		{eventRow(12, "consumed", &run, `{"message_ids":["`+msg.String()+`"]}`), stream.MessageConsumed{MessageIDs: []string{msg.String()}}},
		{eventRow(13, "delegated", &run, `{"agent":"billing"}`), stream.Custom{Name: "delegated", Value: json.RawMessage(`{"agent":"billing"}`)}},
		{eventRow(14, "wrap_up", &run, `{"reason":"budget","left":40}`), stream.Custom{Name: "wrap_up", Value: json.RawMessage(`{"reason":"budget","left":40}`)}},
	}
	for _, c := range cases {
		t.Run(c.row.Kind, func(t *testing.T) {
			env, err := envelopeOf(c.row)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(env.Event, c.want) {
				t.Fatalf("got %#v\nwant %#v", env.Event, c.want)
			}
			if env.Seq != *c.row.Seq || !env.At.Equal(c.row.At) {
				t.Fatalf("envelope %+v", env)
			}
			if (c.row.RunID == nil) != (env.RunID == "") {
				t.Fatalf("run %q for a row whose run is %v", env.RunID, c.row.RunID)
			}
			// Everything translated is something the stream can write.
			if _, err := stream.Frame(env); err != nil {
				t.Fatalf("not framed: %v", err)
			}
		})
	}
}

func TestARowThatCannotBeReadIsAnError(t *testing.T) {
	run := uuid.New()
	for name, row := range map[string]db.ListSessionEventsRow{
		"no number":          {Kind: "step", Payload: []byte(`{}`)},
		"a broken message":   eventRow(1, "message", nil, `{`),
		"a step that is not": eventRow(2, "step", &run, `{"step":"two"}`),
		"a broken tool call": eventRow(3, "tool_call", &run, `[`),
		"a broken result":    eventRow(4, "tool_result", &run, `[`),
		"a broken text":      eventRow(5, "text", &run, `[`),
		"a broken finish":    eventRow(6, "finished", &run, `[`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := envelopeOf(row); err == nil {
				t.Fatal("translated")
			}
		})
	}
}

// follow serves the session's events and reads them back, after a cursor
// when one is given, until want have arrived.
func (f *fixture) follow(t *testing.T, after string, want int) []stream.Envelope {
	t.Helper()
	mux := http.NewServeMux()
	New(discardLogger(), f.store, f.authz, f.objects, f.inbox).
		Register(mux, api.NewGuard(routes(t), f.authz, liveTeams{}, discardLogger()))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.caller)))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+f.session()+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	if after != "" {
		req.Header.Set("Last-Event-ID", after)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	r := stream.NewReader(res.Body)
	var got []stream.Envelope
	for len(got) < want {
		env, err := r.Next()
		if err != nil {
			t.Fatalf("after %d events: %v", len(got), err)
		}
		got = append(got, env)
	}
	return got
}

func TestFollowingASessionReplaysItAndResumes(t *testing.T) {
	f := newFixture(t)
	run := uuid.New()
	f.store.events = []db.ListSessionEventsRow{
		eventRow(-1, "message", nil, `{"id":"m0","user_id":"u","text":"from before","asks":true}`),
		eventRow(0, "finished", &run, `{"status":"succeeded"}`),
		eventRow(1, "message", nil, `{"id":"m1","user_id":"u","text":"why?","asks":true}`),
		eventRow(2, "started", &run, `{}`),
	}

	all := f.follow(t, "", 4)
	if all[0].Seq != -1 || all[3].Seq != 2 || all[3].RunID != run.String() {
		t.Fatalf("replayed %+v", all)
	}
	if f.store.eventArgs[0].After != stream.Start || f.store.eventArgs[0].SessionID != f.sessionID {
		t.Fatalf("read the log as %+v", f.store.eventArgs[0])
	}

	later := f.follow(t, "0", 2)
	if later[0].Seq != 1 || later[1].Seq != 2 {
		t.Fatalf("resumed at %+v", later)
	}
}

func TestFollowingIsRefused(t *testing.T) {
	cases := map[string]struct {
		setup  func(f *fixture, req *http.Request)
		status int
	}{
		"a cursor that is not a number": {func(_ *fixture, req *http.Request) { req.Header.Set("Last-Event-ID", "x") }, http.StatusBadRequest},
		"someone else's conversation": {func(f *fixture, _ *http.Request) {
			other := uuid.New()
			f.store.sessions[0].UserID = &other
		}, http.StatusNotFound},
		"another team's": {func(f *fixture, _ *http.Request) { f.store.sessions[0].TeamID = uuid.New() }, http.StatusNotFound},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			mux := http.NewServeMux()
			New(discardLogger(), f.store, f.authz, f.objects, f.inbox).
				Register(mux, api.NewGuard(routes(t), f.authz, liveTeams{}, discardLogger()))
			// Bounded: a refusal that let the stream through would follow it
			// for good, and should fail by name rather than by timeout.
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, f.session()+"/events", nil)
			req = req.WithContext(auth.WithUser(req.Context(), f.caller))
			c.setup(f, req)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d", rec.Code, c.status)
			}
			if len(f.store.eventArgs) != 0 {
				t.Fatal("read the log on a refusal")
			}
		})
	}
}

// A log that fails ends the stream; the handler logs it rather than writing
// a second status over the first.
func TestALogThatFailsEndsTheStream(t *testing.T) {
	f := newFixture(t)
	f.store.eventsErr = errors.New("connection reset")
	mux := http.NewServeMux()
	New(discardLogger(), f.store, f.authz, f.objects, f.inbox).
		Register(mux, api.NewGuard(routes(t), f.authz, liveTeams{}, discardLogger()))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, f.session()+"/events", nil)
	req = req.WithContext(auth.WithUser(req.Context(), f.caller))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(f.store.eventArgs) != 1 {
		t.Fatalf("status %d after %d reads", rec.Code, len(f.store.eventArgs))
	}
}
