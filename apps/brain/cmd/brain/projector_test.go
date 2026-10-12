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

	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const embeddingRef = "brain/embedding#api_key"

// secretsWith is a secret store already holding one value.
func secretsWith(t *testing.T, path, key, value string) secrets.Store {
	t.Helper()

	s := static.New()
	w, ok := s.(secrets.Writer)
	if !ok {
		t.Fatal("the static store cannot be written")
	}
	if err := w.Put(t.Context(), path, key, value); err != nil {
		t.Fatal(err)
	}
	return s
}

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

func TestNoKeyRefMeansNoKeyAndNoRead(t *testing.T) {
	t.Parallel()

	// A nil store: with no reference there is nothing to read, and reading
	// anyway would panic here.
	key, err := embeddingKey(t.Context(), &config.Config{}, nil)
	if err != nil || key != nil {
		t.Errorf("embeddingKey = %v, %v; want none", key != nil, err)
	}
}

// The key is read when it is used, not copied at startup: one rotated in the
// secret store reaches the next embedding without a restart.
func TestTheKeyIsReadFromTheSecretStoreEachTime(t *testing.T) {
	t.Parallel()

	s := secretsWith(t, "brain/embedding", "api_key", "first")
	key, err := embeddingKey(t.Context(), &config.Config{EmbeddingKeyRef: embeddingRef}, s)
	if err != nil {
		t.Fatalf("embeddingKey: %v", err)
	}
	if got, err := key(t.Context()); err != nil || got != "first" {
		t.Errorf("key = %q, %v", got, err)
	}

	w, _ := s.(secrets.Writer)
	if err := w.Put(t.Context(), "brain/embedding", "api_key", "rotated"); err != nil {
		t.Fatal(err)
	}
	if got, _ := key(t.Context()); got != "rotated" {
		t.Errorf("after a rotation the key is %q", got)
	}
}

// A reference to nothing fails the start, where it is read by whoever set
// it, and not at the first embedding as a row that never projects.
func TestAKeyRefThatCannotBeReadFailsTheStart(t *testing.T) {
	t.Parallel()

	for name, s := range map[string]secrets.Store{
		"nothing at the path":       static.New(),
		"the path, without the key": secretsWith(t, "brain/embedding", "another_key", "sk-secret-value"),
		"an empty value":            secretsWith(t, "brain/embedding", "api_key", ""),
		"the key, at another path":  secretsWith(t, "brain/embeddings", "api_key", "sk-secret-value"),
	} {
		key, err := embeddingKey(t.Context(), &config.Config{EmbeddingKeyRef: embeddingRef}, s)
		if err == nil || key != nil {
			t.Errorf("%s: embeddingKey succeeded", name)
			continue
		}
		if !strings.Contains(err.Error(), "EMBEDDING_KEY_REF") || !strings.Contains(err.Error(), embeddingRef) {
			t.Errorf("%s: the error does not say which setting and which reference: %v", name, err)
		}
		if strings.Contains(err.Error(), "sk-secret-value") {
			t.Errorf("%s: the error carries a secret: %v", name, err)
		}
	}
}

func TestAKeyReadDoesNotOutliveItsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := embeddingKey(ctx, &config.Config{EmbeddingKeyRef: embeddingRef}, blockingSecrets{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
	}
}

// blockingSecrets answers only when its context ends.
type blockingSecrets struct{}

func (blockingSecrets) Get(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// --- the graph ---

// The worker does not start against a FalkorDB it cannot reach: a drain that
// fails every minute looks like a slow graph, and this looks like what it is.
func TestAProjectorIsNotBuiltOnAFalkorDBThatIsNotThere(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{FalkorDBURL: deadFalkorURL(t), EmbeddingModel: "test-model", EmbeddingDimensions: 3}
	started := time.Now()
	p, closeGraph, err := newProjector(t.Context(), cfg, discardLogger(), nil, static.New())
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
	_, _, err = newProjector(t.Context(), cfg, discardLogger(), nil, static.New())
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
	if _, _, err := newProjector(t.Context(), cfg, discardLogger(), nil, static.New()); err == nil ||
		!strings.Contains(err.Error(), "EMBEDDING_DIMENSIONS") {
		t.Errorf("err = %v, want the width refused by name", err)
	}
}

// The key is checked before anything is dialled: the cheaper failure first,
// and it is the one a person fixes by editing a setting.
func TestABadKeyRefIsReportedBeforeFalkorDBIsTried(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		FalkorDBURL: deadFalkorURL(t), EmbeddingModel: "test-model", EmbeddingDimensions: 3,
		EmbeddingKeyRef: embeddingRef,
	}
	_, _, err := newProjector(t.Context(), cfg, discardLogger(), nil, static.New())
	if err == nil || !strings.Contains(err.Error(), "EMBEDDING_KEY_REF") {
		t.Errorf("err = %v, want the key reference named", err)
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

	jobs, closeJobs, err := newJobs(t.Context(), cfg, logger, nil, static.New(), nil)
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

	jobs, closeJobs, err := newJobs(t.Context(), cfg, slog.New(slog.NewTextHandler(&logs, nil)), nil, static.New(), nil)
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
	jobs, closeJobs, err := newJobs(t.Context(), cfg, discardLogger(), nil, static.New(), nil)
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
// is embedded through OMNI_ROUTE_URL, as EMBEDDING_MODEL, with the key that
// EMBEDDING_KEY_REF names, and lands in the FalkorDB at FALKOR_DB_URL at the
// width of EMBEDDING_DIMENSIONS.
//
// Not parallel: it drains under the lock every worker shares.
func TestTheWiredProjectorUsesEverySetting(t *testing.T) {
	router := newModelRouter(t, 5)
	dbStore := registrySchema(t)
	cfg := &config.Config{
		FalkorDBURL: liveFalkorURL(t), OmniRouteURL: router.URL + "/v1",
		EmbeddingModel: "wired-model", EmbeddingDimensions: 5, EmbeddingKeyRef: embeddingRef,
	}

	p, closeGraph, err := newProjector(t.Context(), cfg, discardLogger(),
		dbStore, secretsWith(t, "brain/embedding", "api_key", "sk-wired"))
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
