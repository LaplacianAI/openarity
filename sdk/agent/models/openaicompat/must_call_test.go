package openaicompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openai/openai-go/v3/option"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// choosing is a server that records each request's tool_choice and, when
// refuses is set, answers one that names a tool as Claude does while it
// thinks: 400. Otherwise it answers with done, streamed when asked to.
type choosing struct {
	mu      sync.Mutex
	choices []any
	refuses bool
	status  int
}

func (c *choosing) serve(t *testing.T, done http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("the request was not JSON: %v", err)
		}
		c.mu.Lock()
		c.choices = append(c.choices, body["tool_choice"])
		refuse := c.refuses && body["tool_choice"] != nil
		status := c.status
		c.mu.Unlock()
		if refuse {
			if status == 0 {
				status = http.StatusBadRequest
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"Thinking may not be enabled when tool_choice forces tool use.","type":"invalid_request_error"}}`)
			return
		}
		done(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "sk-test", noRetries())
}

func (c *choosing) seen() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.choices...)
}

func critiqueRequest() agent.Request {
	return agent.Request{
		Model:    agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{userSays("review it")},
		Tools:    []agent.Tool{{Name: "submit_critique", Schema: json.RawMessage(`{"type":"object"}`)}},
		MustCall: "submit_critique",
	}
}

var named = map[string]any{"type": "function", "function": map[string]any{"name": "submit_critique"}}

func sameChoice(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// The tool a pattern must have called is named on the wire, as the chat
// completions API names one.
func TestATurnThatMustCallAToolNamesIt(t *testing.T) {
	t.Parallel()

	srv := &choosing{}
	c := srv.serve(t, answer(t, completion("", nil, "stop")))
	if _, err := c.Complete(t.Context(), critiqueRequest()); err != nil {
		t.Fatal(err)
	}
	if got := srv.seen(); len(got) != 1 || !sameChoice(got[0], named) {
		t.Errorf("tool_choice %v, want %v", got, named)
	}

	free := critiqueRequest()
	free.MustCall = ""
	if _, err := c.Complete(t.Context(), free); err != nil {
		t.Fatal(err)
	}
	if got := srv.seen(); len(got) != 2 || got[1] != nil {
		t.Errorf("a turn left to the model sent tool_choice %v", got[1])
	}
}

// A model that refuses to be told which tool to call is asked again, once,
// without being told: the turn is answered rather than failed.
func TestARefusedToolChoiceIsAskedAgainWithoutIt(t *testing.T) {
	t.Parallel()

	srv := &choosing{refuses: true}
	c := srv.serve(t, answer(t, completion("looks fine", nil, "stop")))
	resp, err := c.Complete(t.Context(), critiqueRequest())
	if err != nil {
		t.Fatalf("Complete: %v, want the answer asked for without the choice", err)
	}
	if resp.Message.Text() != "looks fine" {
		t.Errorf("answer %q", resp.Message.Text())
	}
	if got := srv.seen(); len(got) != 2 || !sameChoice(got[0], named) || got[1] != nil {
		t.Errorf("tool_choice asked %v, want the tool then nothing", got)
	}
}

// Only a refusal of a turn that named a tool is asked again: any other 400 is
// the request's own fault, and asking twice would only hide it.
func TestOtherRefusalsAreNotAskedAgain(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		status int
		must   string
	}{
		"a 400 for a turn left to the model": {http.StatusBadRequest, ""},
		"a 401 for a turn that named a tool": {http.StatusUnauthorized, "submit_critique"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				fmt.Fprint(w, `{"error":{"message":"no","type":"invalid_request_error"}}`)
			}))
			t.Cleanup(srv.Close)
			calls := 0
			counted := New(srv.URL, "sk-test", noRetries(), countRequests(&calls))
			req := critiqueRequest()
			req.MustCall = c.must
			_, err := counted.Complete(t.Context(), req)
			var me *agent.ModelError
			if !errors.As(err, &me) || me.Status != c.status {
				t.Errorf("err = %v, want the %d", err, c.status)
			}
			if calls != 1 {
				t.Errorf("asked %d times, want once", calls)
			}
		})
	}
}

// A stream is refused before its first chunk: it is opened again without the
// choice, once, and reads as one stream.
func TestARefusedStreamIsOpenedAgainWithoutTheChoice(t *testing.T) {
	t.Parallel()

	srv := &choosing{refuses: true}
	c := srv.serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "content": "looks"}, ""),
		chunk(map[string]any{"content": " fine"}, ""),
		chunk(map[string]any{}, "stop"),
	))
	s, err := c.Stream(t.Context(), critiqueRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text, final := drain(t, s)
	if text != "looks fine" || final == nil || final.Message.Text() != "looks fine" {
		t.Errorf("streamed %q, final %+v", text, final)
	}
	if got := srv.seen(); len(got) != 2 || !sameChoice(got[0], named) || got[1] != nil {
		t.Errorf("tool_choice asked %v, want the tool then nothing", got)
	}
}

// A stream refused for anything else is reported, not opened again.
func TestAStreamRefusedForAnythingElseIsReported(t *testing.T) {
	t.Parallel()

	srv := &choosing{refuses: true, status: http.StatusUnauthorized}
	c := srv.serve(t, sse(t, chunk(map[string]any{}, "stop")))
	s, err := c.Stream(t.Context(), critiqueRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for s.Next() {
	}
	var me *agent.ModelError
	if !errors.As(s.Err(), &me) || me.Status != http.StatusUnauthorized {
		t.Errorf("err = %v, want the 401", s.Err())
	}
	if got := srv.seen(); len(got) != 1 {
		t.Errorf("opened %d times, want once", len(got))
	}
}

// noRetries turns off openai-go's own retries, so a count of requests is the
// client's.
func noRetries() option.RequestOption { return option.WithMaxRetries(0) }

func countRequests(n *int) option.RequestOption {
	return option.WithMiddleware(func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		*n++
		return next(r)
	})
}
