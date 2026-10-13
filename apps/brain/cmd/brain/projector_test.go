package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func liveFalkorURL(t *testing.T) string {
	t.Helper()

	u := os.Getenv("BRAIN_TEST_FALKOR_URL")
	if u == "" {
		t.Skip("BRAIN_TEST_FALKOR_URL is not set")
	}
	return u
}

// deadFalkorURL is an address nothing listens on, carrying a password that
// must not come back in an error.
func deadFalkorURL(t *testing.T) string {
	t.Helper()
	return "redis://default:hunter2@" + freeAddr(t)
}

// --- the key ---

// No key is no function at all, which is what makes the embedder send no
// Authorization header: a local model wants none, and "Bearer " with nothing
// after it is refused by servers that would have answered without it.
func TestNoKeySetMeansNoKeyIsSent(t *testing.T) {
	t.Parallel()

	if key := embeddingKey(&config.Config{}); key != nil {
		t.Error("with EMBEDDING_API_KEY empty there is still a key function")
	}
}

func TestTheKeySentIsTheOneConfigured(t *testing.T) {
	t.Parallel()

	// The LLM's key is a different credential for a different endpoint.
	key := embeddingKey(&config.Config{EmbeddingAPIKey: "sk-embedding", LLMAPIKey: "sk-llm"})
	if key == nil {
		t.Fatal("no key function for a configured key")
	}
	if got, err := key(t.Context()); err != nil || got != "sk-embedding" {
		t.Errorf("key = %q, %v", got, err)
	}
}

// --- the graph ---

// The worker does not start against a FalkorDB it cannot reach: a drain that
// fails every minute looks like a slow graph, and this looks like what it is.
func TestAProjectorIsNotBuiltOnAFalkorDBThatIsNotThere(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{FalkorDBURL: deadFalkorURL(t), EmbeddingModel: "test-model", EmbeddingDimensions: 3}
	started := time.Now()
	p, closeGraph, err := newProjector(t.Context(), cfg, discardLogger(), nil)
	if err == nil || p != nil || closeGraph != nil {
		t.Fatal("newProjector succeeded with nothing listening")
	}
	if !strings.Contains(err.Error(), "FalkorDB unreachable at startup") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error carries the password from FALKOR_DB_URL: %v", err)
	}
	if took := time.Since(started); took > graphStartTimeout+2*time.Second {
		t.Errorf("giving up took %v, want within the start timeout of %v", took, graphStartTimeout)
	}
}

// A server that takes the connection and never answers is the one a timeout
// is for: a refused connection fails at once whatever the deadline.
func TestAFalkorDBThatSaysNothingDoesNotHoldTheStart(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	cfg := &config.Config{FalkorDBURL: "redis://" + l.Addr().String(), EmbeddingModel: "test-model", EmbeddingDimensions: 3}
	started := time.Now()
	_, _, err = newProjector(t.Context(), cfg, discardLogger(), nil)
	if err == nil || !strings.Contains(err.Error(), "FalkorDB") || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want FalkorDB named and the deadline", err)
	}
	if took := time.Since(started); took > graphStartTimeout+2*time.Second {
		t.Errorf("giving up took %v, want within the start timeout of %v", took, graphStartTimeout)
	}
}

func TestAProjectorIsNotBuiltOnAWidthNoIndexCanHave(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{FalkorDBURL: deadFalkorURL(t), EmbeddingModel: "test-model"}
	if _, _, err := newProjector(t.Context(), cfg, discardLogger(), nil); err == nil ||
		!strings.Contains(err.Error(), "EMBEDDING_DIMENSIONS") {
		t.Errorf("err = %v, want the width refused by name", err)
	}
}

// --- which jobs the worker runs ---

// With no embedding model there is nothing to project with. The worker says
// so once and runs the rest; it does not dial a FalkorDB it will not use.
func TestTheWorkerDoesNotDrainWhenNoModelIsSet(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	cfg := &config.Config{FalkorDBURL: deadFalkorURL(t), EmbeddingDimensions: 3}

	jobs, closeJobs, err := newJobs(t.Context(), cfg, logger, nil, nil)
	if err != nil {
		t.Fatalf("newJobs: %v", err)
	}
	closeJobs()

	if len(jobs) != 1 || jobs[0].Name() != "reaper.sweep" {
		t.Errorf("%d jobs, want only the reaper's", len(jobs))
	}
	if !strings.Contains(logs.String(), "EMBEDDING_MODEL is not set") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("the worker did not say the graph is not being projected:\n%s", logs.String())
	}
}

func TestTheWorkerDrainsWhenAModelIsSet(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	cfg := &config.Config{FalkorDBURL: liveFalkorURL(t), EmbeddingModel: "test-model", EmbeddingDimensions: 3}

	jobs, closeJobs, err := newJobs(t.Context(), cfg, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil)
	if err != nil {
		t.Fatalf("newJobs: %v", err)
	}
	t.Cleanup(closeJobs)

	if len(jobs) != 2 || jobs[0].Name() != "reaper.sweep" || jobs[1].Name() != "graph.drain" {
		t.Errorf("jobs = %d, want the reaper's and the drain", len(jobs))
	}
	if strings.Contains(logs.String(), "not being projected") {
		t.Error("the worker said the graph is not projected, with a model set")
	}
}

// A model with no FalkorDB behind it fails the start; it does not quietly
// run the reaper alone.
func TestTheWorkerDoesNotStartWithAModelAndNoFalkorDB(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{FalkorDBURL: deadFalkorURL(t), EmbeddingModel: "test-model", EmbeddingDimensions: 3}
	jobs, closeJobs, err := newJobs(t.Context(), cfg, discardLogger(), nil, nil)
	if err == nil || jobs != nil || closeJobs != nil {
		t.Errorf("newJobs = %d jobs, %v; want the failure", len(jobs), err)
	}
}

// --- the whole of it ---

// modelRouter records what it was asked and answers with a vector of the
// width requested by the test.
type modelRouter struct {
	*httptest.Server
	mu   sync.Mutex
	seen []modelRequest

	// refuse fails any request whose text contains it; onRequest runs once,
	// before the first answer.
	refuse    string
	onRequest func()
}

type modelRequest struct {
	path, auth, model string
	input             []string
}

func newModelRouter(t *testing.T, dims int) *modelRouter {
	t.Helper()

	r := &modelRouter{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)

		r.mu.Lock()
		r.seen = append(r.seen, modelRequest{req.URL.Path, req.Header.Get("Authorization"), body.Model, body.Input})
		hook := r.onRequest
		r.onRequest = nil
		refused := r.refuse != "" && strings.Contains(strings.Join(body.Input, "\n"), r.refuse)
		r.mu.Unlock()

		if hook != nil {
			hook()
		}
		if refused {
			http.Error(w, `{"error":{"message":"refused by the test"}}`, http.StatusBadGateway)
			return
		}

		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]item, len(body.Input))
		for i := range data {
			data[i] = item{Index: i, Embedding: make([]float32, dims)}
			data[i].Embedding[0] = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *modelRouter) requests() []modelRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]modelRequest(nil), r.seen...)
}

// registrySchema is a migrated schema of its own, so a drain here sees only
// this test's outbox.
func registrySchema(t *testing.T) *store.Store {
	t.Helper()

	dsn := liveDSN(t)
	schema := "cmd_test_" + strings.ToLower(t.Name())
	if len(schema) > 63 {
		t.Fatalf("the schema name %s is %d bytes, and Postgres keeps 63", schema, len(schema))
	}

	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for _, statement := range []string{"DROP SCHEMA IF EXISTS " + schema + " CASCADE", "CREATE SCHEMA " + schema} {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	_ = admin.Close(t.Context())

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("BRAIN_TEST_POSTGRES_DSN is not a URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := store.New(t.Context(), u.String())
	if err != nil {
		t.Fatalf("connect to the schema: %v", err)
	}
	t.Cleanup(func() {
		s.Close()

		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "SET lock_timeout = '5s'"); err != nil {
			t.Errorf("set lock_timeout: %v", err)
		}
		if _, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// Every setting reaches the thing it configures. A skill written to Postgres
// is embedded at EMBEDDING_BASE_URL, as EMBEDDING_MODEL, with
// EMBEDDING_API_KEY, and lands in the FalkorDB at FALKOR_DB_URL at the width
// of EMBEDDING_DIMENSIONS.
//
// The LLM's address and key are set to something else on purpose: they are
// the same shape, one field name away, and an embedding sent there with that
// key is a credential handed to the wrong service.
//
// Not parallel: it drains under the lock every worker shares.
func TestTheWiredProjectorUsesEverySetting(t *testing.T) {
	router := newModelRouter(t, 5)
	llm := newModelRouter(t, 5)
	dbStore := registrySchema(t)
	cfg := &config.Config{
		FalkorDBURL: liveFalkorURL(t),
		LLMBaseURL:  llm.URL + "/v1", LLMAPIKey: "sk-llm",
		EmbeddingBaseURL: router.URL + "/v1", EmbeddingAPIKey: "sk-wired",
		EmbeddingModel: "wired-model", EmbeddingDimensions: 5,
	}

	p, closeGraph, err := newProjector(t.Context(), cfg, discardLogger(), dbStore)
	if err != nil {
		t.Fatalf("newProjector: %v", err)
	}

	team, err := dbStore.CreateTeam(t.Context(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := dbStore.CreateSkill(t.Context(), db.CreateSkillParams{
		TeamID: team.ID, Name: "pdf", Description: "Extract text", Body: "# pdf",
		Metadata: []byte(`{}`), Source: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A second client onto the same FalkorDB, to read what the first wrote.
	reader, err := graph.Dial(t.Context(), cfg.FalkorDBURL, 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.DropTeam(context.WithoutCancel(t.Context()), team.ID)
		_ = reader.Close()
	})

	res, err := p.Drain(t.Context(), 100)
	if err != nil || res.Projected != 1 || res.Failed != 0 {
		t.Fatalf("Drain = %+v, %v", res, err)
	}

	asked := router.requests()
	if len(asked) != 1 {
		t.Fatalf("the router saw %d requests, want 1", len(asked))
	}
	if got := asked[0]; got.path != "/v1/embeddings" || got.auth != "Bearer sk-wired" || got.model != "wired-model" {
		t.Errorf("the router was asked %+v", got)
	}
	if sent := llm.requests(); len(sent) != 0 {
		t.Errorf("the LLM endpoint was asked for an embedding: %+v", sent)
	}

	snapshot, err := reader.Snapshot(t.Context(), team.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, n := range snapshot.Nodes {
		if n.ID == skill.ID {
			found = true
			if len(n.Embedding) != 5 || n.Model != "wired-model" {
				t.Errorf("the skill's vector has %d dimensions under %q", len(n.Embedding), n.Model)
			}
		}
	}
	if !found {
		t.Errorf("the skill is not in the graph: %+v", snapshot.Nodes)
	}

	// The function handed back is what closes the connection the projector
	// uses: after it, a change can no longer be projected.
	closeGraph()
	if _, err := dbStore.CreateSkill(t.Context(), db.CreateSkillParams{
		TeamID: team.ID, Name: "docx", Description: "Write documents", Body: "# docx",
		Metadata: []byte(`{}`), Source: "upload",
	}); err != nil {
		t.Fatal(err)
	}
	if res, err := p.Drain(t.Context(), 100); err != nil || res.Failed != 1 {
		t.Errorf("after closing the graph, Drain = %+v, %v; want the row left", res, err)
	}
}

// --- the graph the API reads ---

func TestNoModelMeansNoGraphView(t *testing.T) {
	t.Parallel()

	// A width is set, so the missing model is the only reason not to dial.
	cfg := &config.Config{FalkorDBURL: deadFalkorURL(t), EmbeddingDimensions: 3}
	viewer, closeGraph := newGraphViewer(t.Context(), cfg, discardLogger())
	defer closeGraph()
	if viewer != nil {
		t.Error("a graph view was built with no embedding model")
	}
}

// The API serves without its graph. A FalkorDB that cannot be reached at
// start leaves the graph route answering 503, and serve still starts.
func TestAGraphStoreThatSaysNothingLeavesTheViewOff(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	var logs bytes.Buffer
	cfg := &config.Config{FalkorDBURL: "redis://" + l.Addr().String(), EmbeddingModel: "m", EmbeddingDimensions: 3}
	viewer, closeGraph := newGraphViewer(t.Context(), cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	defer closeGraph()

	if viewer != nil {
		t.Error("a view was built on a FalkorDB that never answered")
	}
	if !strings.Contains(logs.String(), "graph view is off") {
		t.Errorf("serve did not say the view is off:\n%s", logs.String())
	}
}

func TestTheGraphViewReadsTheConfiguredFalkorDB(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{FalkorDBURL: liveFalkorURL(t), EmbeddingModel: "m", EmbeddingDimensions: 3}
	viewer, closeGraph := newGraphViewer(t.Context(), cfg, discardLogger())
	defer closeGraph()
	if viewer == nil {
		t.Fatal("no view on a FalkorDB that is there")
	}
	if _, _, err := viewer.View(t.Context(), uuid.New(), 10); err != nil {
		t.Errorf("View: %v", err)
	}
}
