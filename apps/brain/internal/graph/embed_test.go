package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// router is a stand-in for the model router. It records what it was sent and
// answers with whatever the test hands it.
type router struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recorded
}

type recorded struct {
	method, path, auth, contentType string
	body                            embedRequest
	raw                             string
}

func newRouter(t *testing.T, answer func(w http.ResponseWriter, req embedRequest)) *router {
	t.Helper()

	r := &router{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		rec := recorded{
			method: req.Method, path: req.URL.Path, auth: req.Header.Get("Authorization"),
			contentType: req.Header.Get("Content-Type"), raw: string(raw),
		}
		_ = json.Unmarshal(raw, &rec.body)

		r.mu.Lock()
		r.requests = append(r.requests, rec)
		r.mu.Unlock()

		answer(w, rec.body)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *router) seen() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.requests)
}

// vectorFor is a vector that says which text it belongs to: every component
// is the text's position. A vector attached to the wrong text is then visible.
func vectorFor(position, dims int) []float32 {
	v := make([]float32, dims)
	for i := range v {
		v[i] = float32(position)
	}
	return v
}

// inOrder answers as a well-behaved router does.
func inOrder(dims int) func(http.ResponseWriter, embedRequest) {
	return func(w http.ResponseWriter, req embedRequest) {
		data := make([]embedding, len(req.Input))
		for i := range req.Input {
			data[i] = embedding{Index: i, Embedding: vectorFor(i, dims)}
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Data: data})
	}
}

func staticKey(key string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return key, nil }
}

func client() *http.Client { return &http.Client{Timeout: 10 * time.Second} }

// --- the request ---

func TestEmbedSendsTheModelAndEveryTextInOneRequest(t *testing.T) {
	t.Parallel()

	r := newRouter(t, inOrder(3))
	e := NewEmbedder(r.URL+"/v1", "text-embedding-3-small", 3, nil, client())

	if _, err := e.Embed(t.Context(), []string{"alpha", "beta", "gamma"}); err != nil {
		t.Fatalf("embed: %v", err)
	}

	got := r.seen()
	if len(got) != 1 {
		t.Fatalf("the router saw %d requests, want one for the whole batch", len(got))
	}
	req := got[0]
	if req.method != http.MethodPost || req.path != "/v1/embeddings" || req.contentType != "application/json" {
		t.Errorf("request was %s %s as %q", req.method, req.path, req.contentType)
	}
	if req.body.Model != "text-embedding-3-small" || !slices.Equal(req.body.Input, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("body = %s", req.raw)
	}
	// Not every model behind the router accepts OpenAI's dimensions parameter.
	if strings.Contains(req.raw, "dimensions") {
		t.Errorf("the request carries a dimensions parameter: %s", req.raw)
	}
}

// OMNI_ROUTE_URL is written with and without a trailing slash; both must
// reach the same path, and neither a doubled one.
func TestTheBaseURLMayEndInASlash(t *testing.T) {
	t.Parallel()

	for _, suffix := range []string{"/v1", "/v1/", "/v1//"} {
		r := newRouter(t, inOrder(2))
		if _, err := NewEmbedder(r.URL+suffix, "m", 2, nil, client()).Embed(t.Context(), []string{"a"}); err != nil {
			t.Fatalf("%q: %v", suffix, err)
		}
		if got := r.seen()[0].path; got != "/v1/embeddings" {
			t.Errorf("base %q reached %q, want /v1/embeddings", suffix, got)
		}
	}
}

// Nothing to embed is nothing to ask: no request, no error.
func TestAnEmptyInputMakesNoRequest(t *testing.T) {
	t.Parallel()

	r := newRouter(t, inOrder(2))
	e := NewEmbedder(r.URL, "m", 2, staticKey("k"), client())

	for _, texts := range [][]string{nil, {}} {
		got, err := e.Embed(t.Context(), texts)
		if err != nil || got != nil {
			t.Errorf("Embed(%v) = %v, %v; want nothing", texts, got, err)
		}
	}
	if n := len(r.seen()); n != 0 {
		t.Errorf("the router saw %d requests for an empty input", n)
	}
}

// --- the key ---

func TestTheKeyIsSentAsABearer(t *testing.T) {
	t.Parallel()

	r := newRouter(t, inOrder(2))
	if _, err := NewEmbedder(r.URL, "m", 2, staticKey("sk-live"), client()).Embed(t.Context(), []string{"a"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if got := r.seen()[0].auth; got != "Bearer sk-live" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestWithNoKeyFunctionNoAuthorizationIsSent(t *testing.T) {
	t.Parallel()

	r := newRouter(t, inOrder(2))
	if _, err := NewEmbedder(r.URL, "m", 2, nil, client()).Embed(t.Context(), []string{"a"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if got := r.seen()[0].auth; got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

// The key is read on every call, so rotating it in the secret store takes
// effect without restarting the worker.
func TestTheKeyIsReadOnEveryCall(t *testing.T) {
	t.Parallel()

	r := newRouter(t, inOrder(2))
	keys := []string{"first", "second"}
	calls := 0
	e := NewEmbedder(r.URL, "m", 2, func(context.Context) (string, error) {
		k := keys[calls]
		calls++
		return k, nil
	}, client())

	for range 2 {
		if _, err := e.Embed(t.Context(), []string{"a"}); err != nil {
			t.Fatalf("embed: %v", err)
		}
	}
	got := r.seen()
	if got[0].auth != "Bearer first" || got[1].auth != "Bearer second" {
		t.Errorf("sent %q then %q", got[0].auth, got[1].auth)
	}
}

func TestAKeyThatCannotBeReadFailsBeforeAnyRequest(t *testing.T) {
	t.Parallel()

	boom := errors.New("secret store unavailable")
	for name, key := range map[string]func(context.Context) (string, error){
		"an error":     func(context.Context) (string, error) { return "", boom },
		"an empty key": staticKey(""),
	} {
		r := newRouter(t, inOrder(2))
		_, err := NewEmbedder(r.URL, "m", 2, key, client()).Embed(t.Context(), []string{"a"})
		if err == nil {
			t.Errorf("%s: embedding succeeded", name)
		}
		if name == "an error" && !errors.Is(err, boom) {
			t.Errorf("err = %v, want the store's error kept", err)
		}
		if name == "an empty key" && !strings.Contains(err.Error(), "empty") {
			t.Errorf("err = %v, want the empty key named", err)
		}
		if n := len(r.seen()); n != 0 {
			t.Errorf("%s: the router was still asked %d times", name, n)
		}
	}
}

// The key function is given the caller's context, so a hung secret store is
// bounded by the same deadline as the embedding.
func TestTheKeyFunctionGetsTheCallersContext(t *testing.T) {
	t.Parallel()

	type marker struct{}
	ctx := context.WithValue(t.Context(), marker{}, "mine")
	var got any
	r := newRouter(t, inOrder(2))
	e := NewEmbedder(r.URL, "m", 2, func(c context.Context) (string, error) {
		got = c.Value(marker{})
		return "k", nil
	}, client())

	if _, err := e.Embed(ctx, []string{"a"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if got != "mine" {
		t.Errorf("the key function saw %v, want the caller's context", got)
	}
}

// --- the reply ---

// The contract allows a router to answer out of order. A vector placed by its
// position rather than its index would attach one tool's meaning to another,
// and nothing downstream could tell.
func TestVectorsAreOrderedByIndexNotByPosition(t *testing.T) {
	t.Parallel()

	r := newRouter(t, func(w http.ResponseWriter, req embedRequest) {
		data := make([]embedding, len(req.Input))
		for i := range req.Input {
			data[len(req.Input)-1-i] = embedding{Index: i, Embedding: vectorFor(i, 4)}
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Data: data})
	})

	got, err := NewEmbedder(r.URL, "m", 4, nil, client()).Embed(t.Context(), []string{"a", "b", "c", "d", "e"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	for i, v := range got {
		if !slices.Equal(v, vectorFor(i, 4)) {
			t.Errorf("text %d got the vector of text %v", i, v[0])
		}
	}
}

func TestValuesSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()

	r := newRouter(t, func(w http.ResponseWriter, _ embedRequest) {
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.5,-0.25,1e-3]}],"model":"m","usage":{"prompt_tokens":1}}`)
	})

	got, err := NewEmbedder(r.URL, "m", 3, nil, client()).Embed(t.Context(), []string{"a"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if !slices.Equal(got[0], []float32{0.5, -0.25, 0.001}) {
		t.Errorf("vector = %v", got[0])
	}
}

// The index is built for one width. A vector of another is refused here,
// naming the model and both numbers, rather than later by the graph with a
// message about an index.
func TestAVectorOfTheWrongWidthNamesTheModelAndBothNumbers(t *testing.T) {
	t.Parallel()

	for _, returned := range []int{767, 769, 0} {
		r := newRouter(t, inOrder(returned))
		_, err := NewEmbedder(r.URL, "nomic-embed-text", 768, nil, client()).Embed(t.Context(), []string{"a"})
		if err == nil {
			t.Errorf("a vector of %d was accepted for 768", returned)
			continue
		}
		for _, want := range []string{"nomic-embed-text", fmt.Sprint(returned), "768", "EMBEDDING_DIMENSIONS"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to mention %q", err, want)
			}
		}
	}
}

// One bad vector fails the batch: a partly embedded batch would leave some
// nodes searchable and others silently not.
func TestOneWrongWidthFailsTheWholeBatch(t *testing.T) {
	t.Parallel()

	r := newRouter(t, func(w http.ResponseWriter, _ embedRequest) {
		_ = json.NewEncoder(w).Encode(embedResponse{Data: []embedding{
			{Index: 0, Embedding: vectorFor(0, 3)}, {Index: 1, Embedding: vectorFor(1, 2)}, {Index: 2, Embedding: vectorFor(2, 3)},
		}})
	})
	got, err := NewEmbedder(r.URL, "m", 3, nil, client()).Embed(t.Context(), []string{"a", "b", "c"})
	if err == nil || got != nil {
		t.Errorf("got %v, %v; want nothing and an error", got, err)
	}
}

func TestAReplyThatDoesNotMatchWhatWasSentIsRefused(t *testing.T) {
	t.Parallel()

	v := vectorFor(0, 2)
	for name, tc := range map[string]struct {
		data []embedding
		want string
	}{
		"too few vectors":       {[]embedding{{Index: 0, Embedding: v}}, "1 vectors came back for 2 texts"},
		"too many vectors":      {[]embedding{{Index: 0, Embedding: v}, {Index: 1, Embedding: v}, {Index: 2, Embedding: v}}, "3 vectors came back for 2 texts"},
		"none at all":           {nil, "0 vectors came back for 2 texts"},
		"the same index twice":  {[]embedding{{Index: 1, Embedding: v}, {Index: 1, Embedding: v}}, "two vectors are numbered 1"},
		"an index past the end": {[]embedding{{Index: 0, Embedding: v}, {Index: 2, Embedding: v}}, "numbered 2, outside the 2 texts"},
		"a negative index":      {[]embedding{{Index: 0, Embedding: v}, {Index: -1, Embedding: v}}, "numbered -1, outside the 2 texts"},
	} {
		r := newRouter(t, func(w http.ResponseWriter, _ embedRequest) {
			_ = json.NewEncoder(w).Encode(embedResponse{Data: tc.data})
		})
		got, err := NewEmbedder(r.URL, "m", 2, nil, client()).Embed(t.Context(), []string{"a", "b"})
		if err == nil || !strings.Contains(err.Error(), tc.want) || got != nil {
			t.Errorf("%s: got %v, %v; want an error saying %q", name, got, err, tc.want)
		}
	}
}

func TestAReplyThatIsNotJSONIsAnError(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"", "<html>ok</html>", `{"data":[{"index":0,"embedding":[0.1`} {
		r := newRouter(t, func(w http.ResponseWriter, _ embedRequest) { _, _ = io.WriteString(w, body) })
		if _, err := NewEmbedder(r.URL, "m", 2, nil, client()).Embed(t.Context(), []string{"a"}); err == nil {
			t.Errorf("body %q was accepted", body)
		}
	}
}

// --- failures ---

// The router's own words reach the operator: its message, its code, and the
// id to search its logs for. This is the shape OmniRoute answers with.
func TestAFailureCarriesTheRoutersMessageCodeAndCorrelationID(t *testing.T) {
	t.Parallel()

	r := newRouter(t, func(w http.ResponseWriter, _ embedRequest) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"AUTH_002","message":"Invalid API key","correlation_id":"9f9a0d71"}}`)
	})

	_, err := NewEmbedder(r.URL, "text-embedding-3-small", 2, staticKey("wrong"), client()).Embed(t.Context(), []string{"a"})
	if err == nil {
		t.Fatal("a 401 was treated as success")
	}
	for _, want := range []string{"text-embedding-3-small", "401", "Invalid API key", "AUTH_002", "9f9a0d71"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("the error repeats the key: %v", err)
	}
}

// An OpenAI-style error has a message and no code or correlation id; nothing
// is invented for the parts that are absent.
func TestAnErrorWithOnlyAMessageAddsNothing(t *testing.T) {
	t.Parallel()

	r := newRouter(t, func(w http.ResponseWriter, _ embedRequest) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"model not found","type":"invalid_request_error"}}`)
	})
	_, err := NewEmbedder(r.URL, "m", 2, nil, client()).Embed(t.Context(), []string{"a"})
	if err == nil || !strings.HasSuffix(err.Error(), "answered 400: model not found") {
		t.Errorf("err = %v", err)
	}
}

// A proxy in front of the router answers in HTML. It is flattened to one
// line and capped, because it ends up in a log.
func TestAnErrorPageIsFlattenedAndCapped(t *testing.T) {
	t.Parallel()

	page := "<html>\n  <body>\n\t<h1>502 Bad Gateway</h1>\n" + strings.Repeat("<p>padding</p>\n", 2000) + "</body></html>"
	r := newRouter(t, func(w http.ResponseWriter, _ embedRequest) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, page)
	})

	_, err := NewEmbedder(r.URL, "m", 2, nil, client()).Embed(t.Context(), []string{"a"})
	if err == nil {
		t.Fatal("a 502 was treated as success")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") || !strings.Contains(msg, "<h1>502 Bad Gateway</h1>") {
		t.Errorf("err = %.200s", msg)
	}
	if strings.ContainsAny(msg, "\n\t") {
		t.Error("the error spans several lines")
	}
	if len(msg) > maxErrorBytes+200 {
		t.Errorf("the error is %d bytes, want it capped near %d", len(msg), maxErrorBytes)
	}
}

// Anything but 200 is a failure, including the statuses that carry a body
// shaped like success.
func TestOnly200IsSuccess(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusCreated, http.StatusAccepted, http.StatusTooManyRequests, http.StatusInternalServerError} {
		r := newRouter(t, func(w http.ResponseWriter, req embedRequest) {
			w.WriteHeader(status)
			inOrder(2)(w, req)
		})
		_, err := NewEmbedder(r.URL, "m", 2, nil, client()).Embed(t.Context(), []string{"a"})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestAnUnreachableRouterIsAnErrorNamingTheModel(t *testing.T) {
	t.Parallel()

	r := newRouter(t, inOrder(2))
	addr := r.URL
	r.Close()

	_, err := NewEmbedder(addr, "text-embedding-3-small", 2, nil, client()).Embed(t.Context(), []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "text-embedding-3-small") {
		t.Errorf("err = %v", err)
	}
}

func TestEmbedStopsWhenTheContextDoes(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	r := newRouter(t, func(http.ResponseWriter, embedRequest) { <-release })
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := NewEmbedder(r.URL, "m", 2, nil, client()).Embed(ctx, []string{"a"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v with a 200ms deadline", time.Since(start))
	}
}

// --- what it says about itself ---

func TestTheEmbedderReportsItsModelAndWidth(t *testing.T) {
	t.Parallel()

	e := NewEmbedder("http://router/v1", "nomic-embed-text", 768, nil, client())
	if e.Model() != "nomic-embed-text" || e.Dimensions() != 768 {
		t.Errorf("Model() = %q, Dimensions() = %d", e.Model(), e.Dimensions())
	}
}

// Go's decoder carries on after a type error and fills in what it can, so a
// reply whose index is a string leaves the index at zero with the vector in
// place. Read without checking the error, that is a valid-looking answer.
func TestAReplyOfTheWrongShapeIsRefusedEvenWhenItLooksUsable(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"an index that is a string": `{"data":[{"index":"0","embedding":[0.1,0.2]}]}`,
		"a vector holding a string": `{"data":[{"index":0,"embedding":[0.1,"0.2"]}]}`,
		"data that is an object":    `{"data":{"index":0,"embedding":[0.1,0.2]}}`,
		"a reply cut short":         `{"data":[{"index":0,"embedding":[0.1,0.2]}]`,
	} {
		r := newRouter(t, func(w http.ResponseWriter, _ embedRequest) { _, _ = io.WriteString(w, body) })
		got, err := NewEmbedder(r.URL, "m", 2, nil, client()).Embed(t.Context(), []string{"a"})
		if err == nil || got != nil || !strings.Contains(err.Error(), "not the JSON it should be") {
			t.Errorf("%s: got %v, %v; want it refused as malformed", name, got, err)
		}
	}
}
