package graph

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// liveGraph is a client at the given width and a team of its own, dropped
// afterwards, so tests cannot see each other's nodes and may run in parallel.
func liveGraph(t *testing.T, dims int) (*Graph, uuid.UUID) {
	t.Helper()

	g, err := Dial(t.Context(), liveFalkorURL(t), dims)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	team := uuid.New()
	t.Cleanup(func() {
		_ = g.DropTeam(context.WithoutCancel(t.Context()), team)
		_ = g.Close()
	})
	return g, team
}

func skill(name string, vector []float32) Node {
	sum := sha256.Sum256([]byte(name))
	return Node{
		Ref:       Ref{Label: LabelSkill, ID: uuid.New()},
		Props:     map[string]any{"name": name, "description": "Does " + name},
		Embedding: vector, Model: "test-model", ContentSHA256: sum[:],
	}
}

func bare(label Label) Node { return Node{Ref: Ref{Label: label, ID: uuid.New()}} }

func mustPut(t *testing.T, g *Graph, team uuid.UUID, nodes ...Node) {
	t.Helper()
	for _, n := range nodes {
		if err := g.PutNode(t.Context(), team, n); err != nil {
			t.Fatalf("PutNode(%s %s): %v", n.Label, n.ID, err)
		}
	}
}

func mustSnapshot(t *testing.T, g *Graph, team uuid.UUID) Snapshot {
	t.Helper()
	s, err := g.Snapshot(t.Context(), team)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return s
}

func find(t *testing.T, s Snapshot, id uuid.UUID) Node {
	t.Helper()
	for _, n := range s.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %s is not in the snapshot of %d nodes", id, len(s.Nodes))
	return Node{}
}

func targets(s Snapshot, typ EdgeType) []uuid.UUID {
	var out []uuid.UUID
	for _, e := range s.Edges {
		if e.Type == typ {
			out = append(out, e.To.ID)
		}
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

func sorted(ids ...uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

// --- connecting ---

// Making the client proves nothing: FromURL returns no error with nothing
// listening. Ping is the proof, as it is for the Postgres pool.
func TestDialOfADeadServerSucceedsAndPingFails(t *testing.T) {
	t.Parallel()

	g, err := Dial(t.Context(), "redis://127.0.0.1:1", 3)
	if err != nil {
		t.Fatalf("Dial of a dead address failed, and it is expected to be lazy: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := g.Ping(ctx); err == nil {
		t.Error("Ping succeeded against nothing")
	}
}

// Making the client is not free of the network after all: the library asks
// the server whether it is a sentinel before it returns, on a context of its
// own, and a server that accepts and says nothing holds it for the whole read
// timeout. Measured, ten seconds. The caller's deadline has to cover it.
func TestDialOfAServerThatSaysNothingHonoursItsDeadline(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	g, err := Dial(ctx, "redis://"+l.Addr().String(), 3)
	if !errors.Is(err, context.DeadlineExceeded) || g != nil {
		t.Errorf("Dial = %v, %v; want the deadline", g != nil, err)
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("Dial returned after %v with a deadline of 500ms", took)
	}
}

func TestDialWithItsContextAlreadyOverDialsNothing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if g, err := Dial(ctx, "redis://127.0.0.1:1", 3); !errors.Is(err, context.Canceled) || g != nil {
		t.Errorf("Dial = %v, %v", g != nil, err)
	}
}

func TestPingProvesTheServerIsThere(t *testing.T) {
	t.Parallel()

	g, _ := liveGraph(t, 3)
	if err := g.Ping(t.Context()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

// FalkorDB creates a vector index of dimension 0 without complaint, and
// nothing put in it is ever found.
func TestDialRefusesAWidthNoIndexCanHave(t *testing.T) {
	t.Parallel()

	for _, dims := range []int{0, -1} {
		if _, err := Dial(t.Context(), "redis://127.0.0.1:6379", dims); err == nil || !strings.Contains(err.Error(), "EMBEDDING_DIMENSIONS") {
			t.Errorf("Dial with %d dimensions: err = %v", dims, err)
		}
	}
}

// A DSN can carry a password, and an error reaches the log.
func TestDialNeverRepeatsTheURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"redis://user:s3cret@127.0.0.1:6379/not-a-number",
		"http://user:s3cret@127.0.0.1:6379",
		"redis://user:s3cret@[::1",
		"redis://user:s3cret@127.0.0.1:6379/0?nope=1",
		"redis://user:s3cret@127.0.0.1:6379?db=abc",
		"redis://user:s3cret@127.0.0.1:6379?dial_timeout=soon",
		"redis://user:s3cret@127.0.0.1:6379/0/1",
		"redis://user:s3cret@127.0.0.1:notaport",
		"unix://user:s3cret@/tmp/x.sock?db=q",
	} {
		_, err := Dial(t.Context(), raw, 3)
		if err == nil {
			t.Errorf("Dial(t.Context(), %q) succeeded", raw)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("the error repeats the password: %v", err)
		}
	}
}

// --- indexes ---

func TestEnsureIndexesTwiceIsFine(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	for range 3 {
		if err := g.EnsureIndexes(t.Context(), team); err != nil {
			t.Fatalf("EnsureIndexes: %v", err)
		}
	}
}

// Creating a second vector index of another width answers "already indexed",
// which reads as success. Every vector of the new width would then be stored
// and never found. So the width of the index that is there is read back.
func TestEnsureIndexesRefusesAnIndexBuiltForAnotherWidth(t *testing.T) {
	t.Parallel()

	g3, team := liveGraph(t, 3)
	if err := g3.EnsureIndexes(t.Context(), team); err != nil {
		t.Fatalf("EnsureIndexes at 3: %v", err)
	}

	g5, err := Dial(t.Context(), liveFalkorURL(t), 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g5.Close() })

	err = g5.EnsureIndexes(t.Context(), team)
	if err == nil {
		t.Fatal("an index of 3 dimensions was accepted for vectors of 5")
	}
	for _, want := range []string{"3 dimensions", "EMBEDDING_DIMENSIONS is 5", "brain graph rebuild"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

// The index is usable, not merely created: the nearest vector comes back
// first, and a deleted node leaves the index with its node.
func TestAVectorQueryFindsTheNearestSkill(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	if err := g.EnsureIndexes(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	pdf, docx, xlsx := skill("pdf", []float32{1, 0, 0}), skill("docx", []float32{0, 1, 0}), skill("xlsx", []float32{0, 0, 1})
	mustPut(t, g, team, pdf, docx, xlsx)

	nearest := func() []string {
		res, err := g.run(t.Context(), team, true,
			"CALL db.idx.vector.queryNodes('Skill', 'embedding', 2, vecf32($v)) YIELD node, score RETURN node.id, score ORDER BY score",
			map[string]any{"v": []float32{0.9, 0.1, 0}})
		if err != nil {
			t.Fatalf("vector query: %v", err)
		}
		var ids []string
		for res.Next() {
			id, _ := res.Record().Values()[0].(string)
			ids = append(ids, id)
		}
		return ids
	}

	if got := nearest(); len(got) != 2 || got[0] != pdf.ID.String() || got[1] != docx.ID.String() {
		t.Errorf("nearest = %v, want pdf then docx", got)
	}

	if err := g.DeleteNode(t.Context(), team, pdf.Ref); err != nil {
		t.Fatal(err)
	}
	if got := nearest(); slices.Contains(got, pdf.ID.String()) || len(got) != 2 {
		t.Errorf("after deleting pdf, nearest = %v", got)
	}
}

// --- nodes ---

func TestPutNodeTwiceKeepsOneNode(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	n := skill("pdf", []float32{1, 0, 0})
	mustPut(t, g, team, n)

	n.Props = map[string]any{"name": "pdf-tools", "description": "Changed"}
	mustPut(t, g, team, n)

	s := mustSnapshot(t, g, team)
	if len(s.Nodes) != 1 {
		t.Fatalf("the graph holds %d nodes, want 1", len(s.Nodes))
	}
	if got := s.Nodes[0]; got.Props["name"] != "pdf-tools" || got.Props["description"] != "Changed" {
		t.Errorf("props = %v", got.Props)
	}
}

// Renaming a skill does not change what its description means, so the vector
// it already has — and the fingerprint that says what made it — stay put.
func TestPutNodeWithNoEmbeddingKeepsTheStoredOne(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	first := skill("pdf", []float32{0.5, 1e-7, -1})
	mustPut(t, g, team, first)

	renamed := Node{Ref: first.Ref, Props: map[string]any{"name": "pdf-tools"}}
	mustPut(t, g, team, renamed)

	got := find(t, mustSnapshot(t, g, team), first.ID)
	if got.Props["name"] != "pdf-tools" {
		t.Errorf("name = %v, want it changed", got.Props["name"])
	}
	if !slices.Equal(got.Embedding, first.Embedding) || got.Model != "test-model" || !bytes.Equal(got.ContentSHA256, first.ContentSHA256) {
		t.Errorf("the vector or its fingerprint changed: %v %q %x", got.Embedding, got.Model, got.ContentSHA256)
	}
}

func TestANodeIsStoredAsItWasPut(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 4)
	n := skill("pdf", []float32{0.1, -0.25, 1e-7, 3.4e38})
	n.Props = map[string]any{"name": "say \"hi\"", "description": "non\u00a0breaking, new\nline, back\\slash, 漢字 🙂", "kind": "custom"}
	mustPut(t, g, team, n)

	got := find(t, mustSnapshot(t, g, team), n.ID)
	if got.Label != LabelSkill || !slices.Equal(got.Embedding, n.Embedding) || got.Model != n.Model || !bytes.Equal(got.ContentSHA256, n.ContentSHA256) {
		t.Errorf("got %+v", got)
	}
	for k, want := range n.Props {
		if got.Props[k] != want {
			t.Errorf("%s = %q, want %q", k, got.Props[k], want)
		}
	}
	// The graph's own properties are not handed back as the caller's.
	for _, k := range reserved {
		if _, leaked := got.Props[k]; leaked {
			t.Errorf("Props carries the reserved %s", k)
		}
	}
}

// A property set to nil is removed, so a description that was cleared in
// Postgres does not linger in the graph.
func TestAPropertySetToNilIsRemoved(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	n := bare(LabelAgent)
	n.Props = map[string]any{"name": "triage", "description": "Sorts issues"}
	mustPut(t, g, team, n)

	n.Props = map[string]any{"name": "triage", "description": nil}
	mustPut(t, g, team, n)

	got := find(t, mustSnapshot(t, g, team), n.ID)
	if _, still := got.Props["description"]; still || got.Props["name"] != "triage" {
		t.Errorf("props = %v, want the description gone", got.Props)
	}
}

// FalkorDB stores a vector of the wrong width without error and leaves it out
// of every search. It is refused here, and nothing is written.
func TestAVectorOfTheWrongWidthIsRefusedAndNothingIsStored(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	for _, width := range []int{2, 4, 0} {
		err := g.PutNode(t.Context(), team, skill("pdf", make([]float32, width)))
		if width == 0 {
			// An empty, non-nil vector is still a vector of the wrong width.
			if err == nil {
				t.Error("an empty vector was accepted")
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "index of 3") {
			t.Errorf("width %d: err = %v", width, err)
		}
	}
	if s := mustSnapshot(t, g, team); len(s.Nodes) != 0 {
		t.Errorf("%d nodes were written", len(s.Nodes))
	}
}

// Only agents, tools and skills have a vector index. A vector on anything
// else would be stored and could never be searched.
func TestAVectorOnALabelWithNoIndexIsRefused(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	for _, label := range []Label{LabelTeam, LabelToolkit} {
		n := bare(label)
		n.Embedding = []float32{1, 0, 0}
		if err := g.PutNode(t.Context(), team, n); err == nil || !strings.Contains(err.Error(), "no vector index") {
			t.Errorf("%s: err = %v", label, err)
		}
	}
	for _, label := range embeddedLabels {
		n := bare(label)
		n.Embedding = []float32{1, 0, 0}
		if err := g.PutNode(t.Context(), team, n); err != nil {
			t.Errorf("%s: %v", label, err)
		}
	}
}

// The id, the vector and its fingerprint are the graph's to set. A caller's
// props naming one would overwrite it through SET n += $props.
func TestReservedPropertiesAreRefused(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	for _, key := range []string{"id", "embedding", "model", "content_sha256"} {
		n := bare(LabelAgent)
		n.Props = map[string]any{"name": "x", key: "mine"}
		if err := g.PutNode(t.Context(), team, n); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	if s := mustSnapshot(t, g, team); len(s.Nodes) != 0 {
		t.Errorf("%d nodes were written", len(s.Nodes))
	}
}

// --- fingerprints ---

func TestFingerprintReadsBackWhatProducedTheVector(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	n := skill("pdf", []float32{1, 0, 0})
	mustPut(t, g, team, n)

	model, sha, found, err := g.Fingerprint(t.Context(), team, n.Ref)
	if err != nil || !found || model != "test-model" || !bytes.Equal(sha, n.ContentSHA256) {
		t.Errorf("Fingerprint = %q, %x, %v, %v", model, sha, found, err)
	}
}

func TestFingerprintOfAMissingNodeIsNotFound(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)

	// No graph at all: a team nothing has been projected for.
	if _, _, found, err := g.Fingerprint(t.Context(), team, Ref{LabelSkill, uuid.New()}); found || err != nil {
		t.Errorf("with no graph: found %v, err %v", found, err)
	}

	other := skill("pdf", []float32{1, 0, 0})
	mustPut(t, g, team, other)

	// A graph, and no such node.
	if _, _, found, err := g.Fingerprint(t.Context(), team, Ref{LabelSkill, uuid.New()}); found || err != nil {
		t.Errorf("with no such node: found %v, err %v", found, err)
	}
	// The id exists under another label, which is another node.
	if _, _, found, err := g.Fingerprint(t.Context(), team, Ref{LabelAgent, other.ID}); found || err != nil {
		t.Errorf("under another label: found %v, err %v", found, err)
	}
}

// A node with no vector yet is found, with nothing to compare against — so
// the projector embeds it.
func TestFingerprintOfANodeWithNoVectorIsEmpty(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	n := bare(LabelSkill)
	mustPut(t, g, team, n)

	model, sha, found, err := g.Fingerprint(t.Context(), team, n.Ref)
	if err != nil || !found || model != "" || len(sha) != 0 {
		t.Errorf("Fingerprint = %q, %x, %v, %v", model, sha, found, err)
	}
}

// --- edges ---

func TestSetOutEdgesRemovesARevokedGrant(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	agent, github, linear := bare(LabelAgent), bare(LabelToolkit), bare(LabelToolkit)
	mustPut(t, g, team, agent, github, linear)

	both := []Edge{{To: github.ID, Props: map[string]any{"allow": []string{"search", "create"}}}, {To: linear.ID}}
	if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeGranted, LabelToolkit, both); err != nil {
		t.Fatalf("grant both: %v", err)
	}
	if got := targets(mustSnapshot(t, g, team), EdgeGranted); !slices.Equal(got, sorted(github.ID, linear.ID)) {
		t.Fatalf("granted %v, want both", got)
	}

	if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeGranted, LabelToolkit, []Edge{{To: linear.ID}}); err != nil {
		t.Fatalf("revoke github: %v", err)
	}
	s := mustSnapshot(t, g, team)
	if got := targets(s, EdgeGranted); !slices.Equal(got, []uuid.UUID{linear.ID}) {
		t.Errorf("granted %v, want only linear", got)
	}
	if e := s.Edges[0]; e.From != agent.Ref || e.To != github.Ref && e.To != linear.Ref || e.Type != EdgeGranted {
		t.Errorf("edge = %+v", e)
	}
}

func TestAnEdgeKeepsItsProperties(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	agent, github := bare(LabelAgent), bare(LabelToolkit)
	mustPut(t, g, team, agent, github)

	edges := []Edge{{To: github.ID, Props: map[string]any{"allow": []string{"search_code", "create-issue"}}}}
	if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeGranted, LabelToolkit, edges); err != nil {
		t.Fatal(err)
	}

	s := mustSnapshot(t, g, team)
	if len(s.Edges) != 1 {
		t.Fatalf("%d edges", len(s.Edges))
	}
	allow, _ := s.Edges[0].Props["allow"].([]any)
	if len(allow) != 2 || allow[0] != "search_code" || allow[1] != "create-issue" {
		t.Errorf("allow = %v", s.Edges[0].Props["allow"])
	}
}

// Replacing an agent's skills must not touch its granted servers, and
// replacing its edges to one label must not touch the same type to another.
func TestSetOutEdgesReplacesOnlyItsOwnTypeAndTarget(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	agent, kit, pdf, docx := bare(LabelAgent), bare(LabelToolkit), bare(LabelSkill), bare(LabelSkill)
	other := bare(LabelAgent)
	mustPut(t, g, team, agent, other, kit, pdf, docx)

	set := func(from Ref, typ EdgeType, to Label, ids ...uuid.UUID) {
		t.Helper()
		edges := make([]Edge, len(ids))
		for i, id := range ids {
			edges[i] = Edge{To: id}
		}
		if err := g.SetOutEdges(t.Context(), team, from, typ, to, edges); err != nil {
			t.Fatalf("SetOutEdges %s: %v", typ, err)
		}
	}
	set(agent.Ref, EdgeGranted, LabelToolkit, kit.ID)
	set(agent.Ref, EdgeHasSkill, LabelSkill, pdf.ID)
	set(agent.Ref, EdgeOwns, LabelToolkit, kit.ID)
	set(agent.Ref, EdgeOwns, LabelSkill, pdf.ID)
	set(other.Ref, EdgeHasSkill, LabelSkill, pdf.ID)

	set(agent.Ref, EdgeHasSkill, LabelSkill, docx.ID)
	set(agent.Ref, EdgeOwns, LabelSkill)

	s := mustSnapshot(t, g, team)
	var got []string
	for _, e := range s.Edges {
		who := "agent"
		if e.From == other.Ref {
			who = "other"
		}
		what := map[uuid.UUID]string{kit.ID: "kit", pdf.ID: "pdf", docx.ID: "docx"}[e.To.ID]
		got = append(got, who+" "+string(e.Type)+" "+what)
	}
	slices.Sort(got)
	want := []string{"agent GRANTED kit", "agent HAS_SKILL docx", "agent OWNS kit", "other HAS_SKILL pdf"}
	if !slices.Equal(got, want) {
		t.Errorf("edges = %v, want %v", got, want)
	}
}

func TestSetOutEdgesWithNoneRemovesThemAll(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	agent, pdf := bare(LabelAgent), bare(LabelSkill)
	mustPut(t, g, team, agent, pdf)
	if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeHasSkill, LabelSkill, []Edge{{To: pdf.ID}}); err != nil {
		t.Fatal(err)
	}

	for _, none := range [][]Edge{nil, {}} {
		if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeHasSkill, LabelSkill, none); err != nil {
			t.Fatalf("clear: %v", err)
		}
	}
	if s := mustSnapshot(t, g, team); len(s.Edges) != 0 || len(s.Nodes) != 2 {
		t.Errorf("%d edges and %d nodes, want 0 and 2", len(s.Edges), len(s.Nodes))
	}
}

// FalkorDB skips an edge whose end is not there, silently. Here it is an
// error that says so, and the projector tries again once the node exists.
func TestAnEdgeToOrFromANodeThatIsNotThereIsAnError(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	agent, pdf := bare(LabelAgent), bare(LabelSkill)
	mustPut(t, g, team, agent, pdf)

	err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeHasSkill, LabelSkill, []Edge{{To: pdf.ID}, {To: uuid.New()}})
	if err == nil || !strings.Contains(err.Error(), "1 of 2 HAS_SKILL edges") {
		t.Errorf("a missing target: err = %v", err)
	}

	ghost := Ref{LabelAgent, uuid.New()}
	err = g.SetOutEdges(t.Context(), team, ghost, EdgeHasSkill, LabelSkill, []Edge{{To: pdf.ID}})
	if err == nil || !strings.Contains(err.Error(), "0 of 1") {
		t.Errorf("a missing source: err = %v", err)
	}

	// The target exists, under another label: that is not the node meant.
	err = g.SetOutEdges(t.Context(), team, agent.Ref, EdgeGranted, LabelToolkit, []Edge{{To: pdf.ID}})
	if err == nil {
		t.Error("an edge to a skill was written as a grant of a toolkit")
	}

	// Nothing to write from nothing is not an error: an agent deleted before
	// its grants were projected.
	if err := g.SetOutEdges(t.Context(), team, ghost, EdgeHasSkill, LabelSkill, nil); err != nil {
		t.Errorf("no edges from a missing node: %v", err)
	}
}

// Two edges to one node would be two parallel grants. Refused before the
// query runs, so the edges already there are untouched.
func TestTheSameTargetTwiceIsRefusedBeforeAnythingChanges(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	agent, pdf, docx := bare(LabelAgent), bare(LabelSkill), bare(LabelSkill)
	mustPut(t, g, team, agent, pdf, docx)
	if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeHasSkill, LabelSkill, []Edge{{To: docx.ID}}); err != nil {
		t.Fatal(err)
	}

	err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeHasSkill, LabelSkill, []Edge{{To: pdf.ID}, {To: pdf.ID}})
	if err == nil || !strings.Contains(err.Error(), "two HAS_SKILL edges") {
		t.Errorf("err = %v", err)
	}
	if got := targets(mustSnapshot(t, g, team), EdgeHasSkill); !slices.Equal(got, []uuid.UUID{docx.ID}) {
		t.Errorf("edges are now %v, want the one that was there", got)
	}
}

// --- one edge at a time ---

func mustLink(t *testing.T, g *Graph, team uuid.UUID, from Ref, typ EdgeType, to Ref) {
	t.Helper()
	if err := g.Link(t.Context(), team, from, typ, to); err != nil {
		t.Fatalf("Link(%s %s -> %s): %v", from.Label, typ, to.Label, err)
	}
}

// An outbox row is replayed whenever a drain is interrupted, so linking what
// is already linked has to leave one edge, pointing the way it was asked to.
func TestLinkingTwiceKeepsOneEdge(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	kit, search := bare(LabelToolkit), bare(LabelTool)
	mustPut(t, g, team, kit, search)

	for range 3 {
		mustLink(t, g, team, kit.Ref, EdgeContains, search.Ref)
	}

	s := mustSnapshot(t, g, team)
	if len(s.Edges) != 1 {
		t.Fatalf("%d edges after linking the same pair three times, want 1: %+v", len(s.Edges), s.Edges)
	}
	if e := s.Edges[0]; e.Type != EdgeContains || e.From != kit.Ref || e.To != search.Ref {
		t.Errorf("edge = %+v, want CONTAINS from the toolkit to the tool", e)
	}
}

// Link adds; it does not replace. Each tool says which toolkit it is in, one
// outbox row at a time, and the second must not cost the first its edge.
func TestLinkLeavesTheOtherEdgesAlone(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	kit, search, create, agent := bare(LabelToolkit), bare(LabelTool), bare(LabelTool), bare(LabelAgent)
	mustPut(t, g, team, kit, search, create, agent)
	if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeGranted, LabelToolkit, []Edge{{To: kit.ID}}); err != nil {
		t.Fatal(err)
	}

	mustLink(t, g, team, kit.Ref, EdgeContains, search.Ref)
	mustLink(t, g, team, kit.Ref, EdgeContains, create.Ref)

	s := mustSnapshot(t, g, team)
	if got := targets(s, EdgeContains); !slices.Equal(got, sorted(search.ID, create.ID)) {
		t.Errorf("the toolkit contains %v, want both tools", got)
	}
	if got := targets(s, EdgeGranted); !slices.Equal(got, []uuid.UUID{kit.ID}) {
		t.Errorf("granted %v, want the grant that was there", got)
	}
}

// A toolkit is projected again every time its row changes. Its tools were
// linked by their own rows, which will not come round again, so writing the
// node a second time must keep the edges it has.
func TestPuttingANodeAgainKeepsItsLinks(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	kit, search := bare(LabelToolkit), bare(LabelTool)
	mustPut(t, g, team, kit, search)
	mustLink(t, g, team, kit.Ref, EdgeContains, search.Ref)

	kit.Props = map[string]any{"name": "renamed"}
	mustPut(t, g, team, kit, search)

	s := mustSnapshot(t, g, team)
	if got := targets(s, EdgeContains); !slices.Equal(got, []uuid.UUID{search.ID}) {
		t.Errorf("after the toolkit was written again it contains %v, want its tool", got)
	}
	if find(t, s, kit.ID).Props["name"] != "renamed" {
		t.Error("the second write of the toolkit did not land")
	}
}

// FalkorDB matches nothing and reports success. Here a link with an end that
// is not in the graph is an error, so the outbox row stays and the next drain
// tries again once the other node has been projected.
func TestALinkWithAMissingEndIsAnErrorAndWritesNothing(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	kit, search, pdf := bare(LabelToolkit), bare(LabelTool), bare(LabelSkill)
	mustPut(t, g, team, kit, search, pdf)

	for name, ends := range map[string][2]Ref{
		"the target is missing":            {kit.Ref, {LabelTool, uuid.New()}},
		"the source is missing":            {{LabelToolkit, uuid.New()}, search.Ref},
		"both are missing":                 {{LabelToolkit, uuid.New()}, {LabelTool, uuid.New()}},
		"the target is under another name": {kit.Ref, {LabelTool, pdf.ID}},
		"the source is under another name": {{LabelToolkit, pdf.ID}, search.Ref},
		"the ends are the wrong way round": {{LabelToolkit, search.ID}, {LabelTool, kit.ID}},
	} {
		err := g.Link(t.Context(), team, ends[0], EdgeContains, ends[1])
		if err == nil || !strings.Contains(err.Error(), "no CONTAINS edge was written") {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		// The message names both ends: whoever reads the log needs to know
		// which row to look for.
		for _, id := range []uuid.UUID{ends[0].ID, ends[1].ID} {
			if !strings.Contains(err.Error(), id.String()) {
				t.Errorf("%s: the error does not name %s: %v", name, id, err)
			}
		}
	}

	if s := mustSnapshot(t, g, team); len(s.Edges) != 0 || len(s.Nodes) != 3 {
		t.Errorf("%d edges and %d nodes after six refused links, want 0 and 3", len(s.Edges), len(s.Nodes))
	}
}

// A link in a team with no graph is the same refusal, and it must not leave
// an empty graph behind for a team that may since have been deleted.
func TestALinkInATeamWithNoGraphIsAnError(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	if err := g.Link(t.Context(), team, Ref{LabelToolkit, uuid.New()}, EdgeContains, Ref{LabelTool, uuid.New()}); err == nil {
		t.Error("a link between two nodes of an empty team succeeded")
	}
	if s := mustSnapshot(t, g, team); len(s.Nodes) != 0 || len(s.Edges) != 0 {
		t.Errorf("the refused link wrote %+v", s)
	}
}

// --- deleting ---

func TestDeleteNodeTakesItsEdges(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	agent, pdf, docx := bare(LabelAgent), bare(LabelSkill), bare(LabelSkill)
	mustPut(t, g, team, agent, pdf, docx)
	if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeHasSkill, LabelSkill, []Edge{{To: pdf.ID}, {To: docx.ID}}); err != nil {
		t.Fatal(err)
	}

	if err := g.DeleteNode(t.Context(), team, pdf.Ref); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	s := mustSnapshot(t, g, team)
	if len(s.Nodes) != 2 || !slices.Equal(targets(s, EdgeHasSkill), []uuid.UUID{docx.ID}) {
		t.Errorf("%d nodes, edges to %v; want 2 nodes and the edge to docx", len(s.Nodes), targets(s, EdgeHasSkill))
	}
}

func TestDeletingWhatIsNotThereIsNotAnError(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	// No graph yet.
	if err := g.DeleteNode(t.Context(), team, Ref{LabelSkill, uuid.New()}); err != nil {
		t.Errorf("with no graph: %v", err)
	}
	n := bare(LabelSkill)
	mustPut(t, g, team, n)
	// The id, under the wrong label: a different node, so nothing goes.
	if err := g.DeleteNode(t.Context(), team, Ref{LabelAgent, n.ID}); err != nil {
		t.Errorf("under another label: %v", err)
	}
	if s := mustSnapshot(t, g, team); len(s.Nodes) != 1 {
		t.Errorf("%d nodes, want the skill still there", len(s.Nodes))
	}
}

func TestDropTeamRemovesEverything(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	n := skill("pdf", []float32{1, 0, 0})
	mustPut(t, g, team, n)

	if err := g.DropTeam(t.Context(), team); err != nil {
		t.Fatalf("DropTeam: %v", err)
	}
	if s := mustSnapshot(t, g, team); len(s.Nodes) != 0 || len(s.Edges) != 0 {
		t.Errorf("%d nodes and %d edges survive", len(s.Nodes), len(s.Edges))
	}
	if _, _, found, _ := g.Fingerprint(t.Context(), team, n.Ref); found {
		t.Error("the node is still found")
	}
}

func TestDropTeamOfNoGraphIsNotAnError(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	for range 2 {
		if err := g.DropTeam(t.Context(), team); err != nil {
			t.Errorf("DropTeam: %v", err)
		}
	}
}

// --- isolation ---

func TestATeamsGraphCannotSeeAnothers(t *testing.T) {
	t.Parallel()

	g, mine := liveGraph(t, 3)
	theirs := uuid.New()
	t.Cleanup(func() { _ = g.DropTeam(context.WithoutCancel(t.Context()), theirs) })

	// The same id in both, so a query that leaked would find it.
	shared := uuid.New()
	a := Node{Ref: Ref{LabelSkill, shared}, Props: map[string]any{"name": "mine"}}
	b := Node{Ref: Ref{LabelSkill, shared}, Props: map[string]any{"name": "theirs"}}
	mustPut(t, g, mine, a)
	mustPut(t, g, theirs, b, bare(LabelAgent))

	sm, st := mustSnapshot(t, g, mine), mustSnapshot(t, g, theirs)
	if len(sm.Nodes) != 1 || sm.Nodes[0].Props["name"] != "mine" {
		t.Errorf("my graph holds %+v", sm.Nodes)
	}
	if len(st.Nodes) != 2 {
		t.Errorf("their graph holds %d nodes, want 2", len(st.Nodes))
	}

	if err := g.DropTeam(t.Context(), mine); err != nil {
		t.Fatal(err)
	}
	if st := mustSnapshot(t, g, theirs); len(st.Nodes) != 2 {
		t.Errorf("dropping my graph left theirs with %d nodes", len(st.Nodes))
	}
}

// --- snapshots ---

func TestSnapshotOfNoGraphIsEmpty(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	s, err := g.Snapshot(t.Context(), team)
	if err != nil || len(s.Nodes) != 0 || len(s.Edges) != 0 {
		t.Errorf("Snapshot = %+v, %v", s, err)
	}
}

// Two snapshots of one graph are equal, whatever order things were written
// in: that is what lets a rebuild be compared with incremental projection.
func TestSnapshotIsOrdered(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	var nodes []Node
	for _, label := range []Label{LabelSkill, LabelAgent, LabelToolkit, LabelSkill, LabelAgent} {
		nodes = append(nodes, bare(label))
	}
	mustPut(t, g, team, nodes...)

	s := mustSnapshot(t, g, team)
	if !slices.IsSortedFunc(s.Nodes, func(a, b Node) int {
		if c := strings.Compare(string(a.Label), string(b.Label)); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	}) {
		t.Errorf("nodes are not ordered by label then id: %+v", s.Nodes)
	}
}

// FalkorDB caps a result at 10000 rows by default, and would cut a large
// snapshot short without saying so. Paging reads past any cap.
func TestSnapshotReadsEveryPage(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	const n = 2*pageSize + 7
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	if _, err := g.run(t.Context(), team, false, "UNWIND $ids AS id CREATE (:Tool {id: id})", map[string]any{"ids": ids}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hub := bare(LabelToolkit)
	mustPut(t, g, team, hub)
	if _, err := g.run(t.Context(), team, false,
		"MATCH (k:Toolkit {id: $hub}) MATCH (t:Tool) CREATE (k)-[:CONTAINS]->(t)", map[string]any{"hub": hub.ID.String()}); err != nil {
		t.Fatalf("seed edges: %v", err)
	}

	s := mustSnapshot(t, g, team)
	if len(s.Nodes) != n+1 || len(s.Edges) != n {
		t.Fatalf("snapshot has %d nodes and %d edges, want %d and %d", len(s.Nodes), len(s.Edges), n+1, n)
	}
	seen := map[uuid.UUID]bool{}
	for _, node := range s.Nodes {
		if seen[node.ID] {
			t.Fatalf("node %s appears twice", node.ID)
		}
		seen[node.ID] = true
	}
}

// A node whose id is not a uuid was not written by this package. The
// snapshot says so rather than returning a zero id.
func TestSnapshotRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	for name, seed := range map[string]string{
		"an id that is not a uuid": `CREATE (:Skill {id: "not-a-uuid"})`,
		"a hash that is not hex":   `CREATE (:Skill {id: "11111111-1111-1111-1111-111111111111", content_sha256: "zz"})`,
		"an edge from a bad id":    `CREATE (:Agent {id: "nope"})-[:HAS_SKILL]->(:Skill {id: "11111111-1111-1111-1111-111111111111"})`,
	} {
		g, team := liveGraph(t, 3)
		if _, err := g.run(t.Context(), team, false, seed, nil); err != nil {
			t.Fatalf("%s: seed: %v", name, err)
		}
		if _, err := g.Snapshot(t.Context(), team); err == nil {
			t.Errorf("%s: the snapshot was returned", name)
		}
	}
}

// --- what goes into the query text ---

// A label and an edge type are written into the query, because Cypher takes
// neither as a parameter. Each must be a plain identifier; the type system
// alone would let Label("Skill) DETACH DELETE n //") through.
func TestANameThatIsNotAnIdentifierNeverReachesTheQuery(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	canary, pdf := bare(LabelAgent), bare(LabelSkill)
	mustPut(t, g, team, canary, pdf)

	for _, bad := range []string{"", "Skill) DETACH DELETE n //", "Sk ill", "Skill:Agent", "`Skill`", "1Skill", "Skill\n"} {
		evil := Ref{Label(bad), pdf.ID}
		if err := g.PutNode(t.Context(), team, Node{Ref: evil}); err == nil {
			t.Errorf("PutNode with label %q succeeded", bad)
		}
		if err := g.DeleteNode(t.Context(), team, evil); err == nil {
			t.Errorf("DeleteNode with label %q succeeded", bad)
		}
		if _, _, _, err := g.Fingerprint(t.Context(), team, evil); err == nil {
			t.Errorf("Fingerprint with label %q succeeded", bad)
		}
		for what, call := range map[string]func() error{
			"from":   func() error { return g.SetOutEdges(t.Context(), team, evil, EdgeHasSkill, LabelSkill, nil) },
			"type":   func() error { return g.SetOutEdges(t.Context(), team, canary.Ref, EdgeType(bad), LabelSkill, nil) },
			"target": func() error { return g.SetOutEdges(t.Context(), team, canary.Ref, EdgeHasSkill, Label(bad), nil) },
		} {
			if err := call(); err == nil {
				t.Errorf("SetOutEdges with %s %q succeeded", what, bad)
			}
		}
		// "Skill\n" is the one that matters here: as query text it is still
		// the label Skill, so without the check each of these links for real.
		for what, call := range map[string]func() error{
			"from":   func() error { return g.Link(t.Context(), team, evil, EdgeOwns, canary.Ref) },
			"type":   func() error { return g.Link(t.Context(), team, canary.Ref, EdgeType(bad), pdf.Ref) },
			"target": func() error { return g.Link(t.Context(), team, canary.Ref, EdgeHasSkill, evil) },
		} {
			if err := call(); err == nil {
				t.Errorf("Link with %s %q succeeded", what, bad)
			}
		}
	}

	if s := mustSnapshot(t, g, team); len(s.Nodes) != 2 || len(s.Edges) != 0 {
		t.Errorf("the graph holds %d nodes and %d edges after the attempts, want the 2 nodes it had and no edge",
			len(s.Nodes), len(s.Edges))
	}
}

// --- contexts ---

func TestACancelledContextReachesNoQuery(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := g.PutNode(ctx, team, bare(LabelAgent)); !errors.Is(err, context.Canceled) {
		t.Errorf("PutNode: err = %v, want context.Canceled", err)
	}
	if err := g.DropTeam(ctx, team); !errors.Is(err, context.Canceled) {
		t.Errorf("DropTeam: err = %v", err)
	}
	if err := g.Ping(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Ping: err = %v", err)
	}
	if s := mustSnapshot(t, g, team); len(s.Nodes) != 0 {
		t.Error("a cancelled write was made")
	}
}

// The library sends every command on a context of its own, so a query in
// flight cannot be cancelled. The caller is released the moment its context
// ends all the same, and a worker shutting down is not held for the query.
func TestTheCallerIsReleasedWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := ordered(ctx, make(chan struct{}, 1), func() (int, error) {
		<-release
		return 1, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the caller waited %v", time.Since(start))
	}
}

func TestOrderedReturnsWhatTheFunctionDid(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	if v, err := ordered(t.Context(), make(chan struct{}, 1), func() (int, error) { return 7, nil }); v != 7 || err != nil {
		t.Errorf("await = %v, %v", v, err)
	}
	if _, err := ordered(t.Context(), make(chan struct{}, 1), func() (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

// --- errors ---

// A syntax error from FalkorDB repeats the query, parameters and all. That
// can be a 16 KiB description, and it ends up in a log.
func TestBriefCapsAnErrorAndKeepsItOneOfOurs(t *testing.T) {
	t.Parallel()

	// One byte, then three-byte characters: the cut at 300 bytes lands in
	// the middle of one, which a two-byte character on an even cut would not.
	long := errors.New("x" + strings.Repeat("漢", 2000))
	got := brief(long)
	if !strings.HasPrefix(got.Error(), "graph: ") || len(got.Error()) > maxErrorText+20 {
		t.Errorf("brief gave %d bytes starting %.20q", len(got.Error()), got.Error())
	}
	if !strings.HasSuffix(got.Error(), "…") {
		t.Errorf("a cut error does not say it was cut: %.40q", got.Error())
	}
	for i, r := range got.Error() {
		if r == '�' {
			t.Fatalf("the cut left half a character at byte %d", i)
		}
	}

	if got := brief(errors.New("short")); got.Error() != "graph: short" {
		t.Errorf("brief = %q", got)
	}
	if brief(nil) != nil {
		t.Error("brief(nil) is not nil")
	}
	for _, ctxErr := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := brief(ctxErr); !errors.Is(got, ctxErr) {
			t.Errorf("brief lost %v", ctxErr)
		}
	}
}

// A real error from the server is capped too, not only a constructed one.
func TestAServerErrorIsCapped(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	_, err := g.run(t.Context(), team, false, "RETUR $v", map[string]any{"v": strings.Repeat("x", 5000)})
	if err == nil {
		t.Fatal("a syntax error was accepted")
	}
	if len(err.Error()) > maxErrorText+20 {
		t.Errorf("the error is %d bytes", len(err.Error()))
	}
	// FalkorDB repeats the query in a syntax error. It repeats the template,
	// with $v in it, and not the value: a team's text does not reach a log.
	if strings.Contains(err.Error(), "xxxxxxxx") {
		t.Errorf("the error repeats the parameter's value: %.120v", err)
	}
	// And it is marked as the graph's, so a log line says where it came from.
	if !strings.HasPrefix(err.Error(), "graph: ") {
		t.Errorf("err = %v, want it to begin graph:", err)
	}
}

// A read must leave no trace. FalkorDB's plain GRAPH.QUERY creates the graph
// it is asked about, so asking after a team nothing was projected for would
// leave an empty graph behind for every team that was ever looked at.
func TestReadingATeamWithNoGraphCreatesNone(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	if _, _, _, err := g.Fingerprint(t.Context(), team, Ref{LabelSkill, uuid.New()}); err != nil {
		t.Fatal(err)
	}
	mustSnapshot(t, g, team)

	graphs, err := g.db.ListGraphs()
	if err != nil {
		t.Fatalf("ListGraphs: %v", err)
	}
	if slices.Contains(graphs, graphName(team)) {
		t.Error("reading a team with no graph created one")
	}
}

// With its context already over, the work is never begun: the write must not
// happen in the background after the caller was told it was cancelled.
func TestNothingStartsWhenTheContextIsAlreadyOver(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	started := make(chan struct{}, 1)
	_, err := ordered(ctx, make(chan struct{}, 1), func() (int, error) {
		started <- struct{}{}
		return 1, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	select {
	case <-started:
		t.Error("the function ran although the context was already cancelled")
	case <-time.After(200 * time.Millisecond):
	}
}

// --- one team, one operation at a time ---

// The library cannot cancel a query, so a caller that gives up leaves its
// write in flight. FalkorDB runs queries on a thread pool, and two writes to
// one graph that are both in flight commit in whichever order takes the lock.
// If the abandoned one granted two servers and the next one revoked one of
// them, the old write landing last brings the revoked grant back.
//
// So the next operation on a team waits until the one before has finished,
// not merely been given up on.
func TestTheNextOperationWaitsForAnAbandonedOneToFinish(t *testing.T) {
	t.Parallel()

	turn := make(chan struct{}, 1)
	inFlight, finish := make(chan struct{}), make(chan struct{})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := ordered(ctx, turn, func() (int, error) {
		close(inFlight)
		<-finish
		return 1, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the first caller: err = %v, want it released by its deadline", err)
	}
	<-inFlight

	started := make(chan struct{})
	result := make(chan int, 1)
	go func() {
		v, _ := ordered(t.Context(), turn, func() (int, error) {
			close(started)
			return 2, nil
		})
		result <- v
	}()

	select {
	case <-started:
		t.Fatal("the second operation started while the abandoned first was still in flight")
	case <-time.After(150 * time.Millisecond):
	}

	close(finish)
	select {
	case v := <-result:
		if v != 2 {
			t.Errorf("the second operation returned %d", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second operation never ran after the first finished")
	}
}

// Waiting for a turn is still waiting on the caller's context: a team whose
// last query is stuck does not hold a shutting-down worker.
func TestACallerWaitingForItsTurnHonoursItsDeadline(t *testing.T) {
	t.Parallel()

	turn := make(chan struct{}, 1)
	finish := make(chan struct{})
	t.Cleanup(func() { close(finish) })
	go func() {
		_, _ = ordered(context.WithoutCancel(t.Context()), turn, func() (int, error) { <-finish; return 0, nil })
	}()
	// The holder has the turn once the channel is full.
	for len(turn) == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	ran := false
	start := time.Now()
	_, err := ordered(ctx, turn, func() (int, error) { ran = true; return 0, nil })

	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Errorf("err = %v, ran = %v; want the deadline and nothing run", err, ran)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("waited %v for a turn with an 80ms deadline", time.Since(start))
	}
}

// A turn that was cancelled before it began is handed back, or the team
// would be locked for good by a caller that never did anything.
func TestACancelledCallerGivesItsTurnBack(t *testing.T) {
	t.Parallel()

	turn := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 20 {
		if _, err := ordered(ctx, turn, func() (int, error) { return 0, nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	}

	if v, err := ordered(t.Context(), turn, func() (int, error) { return 9, nil }); v != 9 || err != nil {
		t.Errorf("after cancelled callers, ordered = %v, %v; the turn was not given back", v, err)
	}
}

// And a finished operation gives its turn back whether it succeeded or not.
func TestAFinishedOperationGivesItsTurnBack(t *testing.T) {
	t.Parallel()

	turn := make(chan struct{}, 1)
	for i := range 50 {
		_, err := ordered(t.Context(), turn, func() (int, error) {
			if i%2 == 0 {
				return 0, errors.New("failed")
			}
			return i, nil
		})
		if (err != nil) != (i%2 == 0) {
			t.Fatalf("call %d: err = %v", i, err)
		}
	}
}

// One team's stuck query is that team's problem alone.
func TestEachTeamHasItsOwnTurn(t *testing.T) {
	t.Parallel()

	g, err := Dial(t.Context(), "redis://127.0.0.1:1", 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })

	a, b := uuid.New(), uuid.New()
	once, again := g.turn(a), g.turn(a)
	if once != again {
		t.Error("one team was given two turns, so its operations are not ordered")
	}
	if g.turn(a) == g.turn(b) {
		t.Error("two teams share a turn, so one waits on the other")
	}

	// Many callers asking at once still get the one turn.
	first := g.turn(uuid.Nil)
	same := make(chan bool, 64)
	for range 64 {
		go func() { same <- g.turn(uuid.Nil) == first }()
	}
	for range 64 {
		if !<-same {
			t.Fatal("concurrent callers were given different turns for one team")
		}
	}
}

// Through the client itself: operations on one team from many goroutines all
// land, none lost and none doubled.
func TestConcurrentWritesToOneTeamAllLand(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	const n = 40
	errs := make(chan error, n)
	for range n {
		go func() { errs <- g.PutNode(t.Context(), team, bare(LabelTool)) }()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatalf("PutNode: %v", err)
		}
	}
	if s := mustSnapshot(t, g, team); len(s.Nodes) != n {
		t.Errorf("%d nodes, want %d", len(s.Nodes), n)
	}
}

// The pieces above are right and prove nothing about the client: one that
// made a fresh turn for every call would pass them all and order nothing. So
// this holds a team's turn from outside, as an operation still in flight
// does, and every operation on that team has to wait behind it.
func TestEveryOperationOnATeamWaitsForItsTurn(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	n := bare(LabelAgent)
	mustPut(t, g, team, n)

	g.turn(team) <- struct{}{}
	held := true
	release := func() {
		if held {
			<-g.turn(team)
			held = false
		}
	}
	t.Cleanup(release)

	short := func() context.Context {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	for name, op := range map[string]func(context.Context) error{
		"PutNode":       func(ctx context.Context) error { return g.PutNode(ctx, team, bare(LabelSkill)) },
		"DeleteNode":    func(ctx context.Context) error { return g.DeleteNode(ctx, team, n.Ref) },
		"SetOutEdges":   func(ctx context.Context) error { return g.SetOutEdges(ctx, team, n.Ref, EdgeHasSkill, LabelSkill, nil) },
		"Link":          func(ctx context.Context) error { return g.Link(ctx, team, n.Ref, EdgeOwns, n.Ref) },
		"EnsureIndexes": func(ctx context.Context) error { return g.EnsureIndexes(ctx, team) },
		"DropTeam":      func(ctx context.Context) error { return g.DropTeam(ctx, team) },
		"Fingerprint": func(ctx context.Context) error {
			_, _, _, err := g.Fingerprint(ctx, team, n.Ref)
			return err
		},
		"Snapshot": func(ctx context.Context) error {
			_, err := g.Snapshot(ctx, team)
			return err
		},
	} {
		if err := op(short()); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s went ahead while the team's turn was held: err = %v", name, err)
		}
	}

	// Another team is not held up by this one.
	other := uuid.New()
	t.Cleanup(func() { _ = g.DropTeam(context.WithoutCancel(t.Context()), other) })
	if err := g.PutNode(short(), other, bare(LabelAgent)); err != nil {
		t.Errorf("another team waited on this one: %v", err)
	}

	// Nothing above was done: the node is there, alone, and the graph was not dropped.
	release()
	if s := mustSnapshot(t, g, team); len(s.Nodes) != 1 || s.Nodes[0].ID != n.ID {
		t.Errorf("after the turn was released the graph holds %+v, want only the first node", s.Nodes)
	}
}

// --- indexes that are not there ---

// The width check used to pass when it found no vector index at all, and
// when it could not read the options of one it did find. Either way every
// vector written afterwards is stored and never searched, with no error
// anywhere. A missing index is now as loud as a wrong one.
func TestAVectorIndexThatIsMissingIsAnError(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	// A graph with a node and no index of any kind.
	mustPut(t, g, team, bare(LabelAgent))

	err := g.verifyIndexes(t.Context(), team)
	if err == nil || !strings.Contains(err.Error(), "vector index is missing") {
		t.Errorf("err = %v, want the missing index named", err)
	}
}

// One of the three missing is still an error: an index for skills does not
// make tools searchable.
func TestEveryEmbeddedLabelNeedsItsIndex(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	for _, l := range []Label{LabelAgent, LabelSkill} {
		if _, err := g.run(t.Context(), team, false,
			"CREATE VECTOR INDEX FOR (n:"+string(l)+") ON (n.embedding) OPTIONS {dimension:3, similarityFunction:'cosine'}", nil); err != nil {
			t.Fatal(err)
		}
	}

	// Tool has an index, on its id, and so a row in db.indexes with no
	// vector in it. That is a missing vector index, not one of width zero.
	if _, err := g.run(t.Context(), team, false, "CREATE INDEX FOR (n:Tool) ON (n.id)", nil); err != nil {
		t.Fatal(err)
	}

	err := g.verifyIndexes(t.Context(), team)
	if err == nil || !strings.Contains(err.Error(), "Tool vector index is missing") {
		t.Errorf("err = %v, want the Tool index named", err)
	}

	// EnsureIndexes creates what is missing and then finds all three.
	if err := g.EnsureIndexes(t.Context(), team); err != nil {
		t.Errorf("EnsureIndexes: %v", err)
	}
	if err := g.verifyIndexes(t.Context(), team); err != nil {
		t.Errorf("after EnsureIndexes: %v", err)
	}
}

// EnsureIndexes must be the thing that does this check: a function that is
// correct and never called protects nothing.
func TestEnsureIndexesVerifiesWhatItCreated(t *testing.T) {
	t.Parallel()

	g3, team := liveGraph(t, 3)
	// Two labels indexed at another width, the third not at all.
	for _, l := range []Label{LabelAgent, LabelSkill} {
		if _, err := g3.run(t.Context(), team, false,
			"CREATE VECTOR INDEX FOR (n:"+string(l)+") ON (n.embedding) OPTIONS {dimension:7, similarityFunction:'cosine'}", nil); err != nil {
			t.Fatal(err)
		}
	}
	err := g3.EnsureIndexes(t.Context(), team)
	if err == nil || !strings.Contains(err.Error(), "7 dimensions") {
		t.Errorf("err = %v, want the index of 7 dimensions refused", err)
	}
}

// --- reading rows ---

// The rows are FalkorDB's, and an id that is not a uuid means something
// other than this package wrote the node. Through the database the node pass
// always meets a bad id first, so the edge reader is checked here, on rows.
func TestEdgeOfRefusesAnEndThatIsNotAUUID(t *testing.T) {
	t.Parallel()

	good, other := uuid.NewString(), uuid.NewString()
	e, err := edgeOf([]any{"GRANTED", "Agent", good, "Toolkit", other, map[string]any{"allow": []any{"x"}}})
	if err != nil || e.Type != EdgeGranted || e.From != (Ref{LabelAgent, uuid.MustParse(good)}) ||
		e.To != (Ref{LabelToolkit, uuid.MustParse(other)}) || len(e.Props) != 1 {
		t.Errorf("edgeOf = %+v, %v", e, err)
	}

	if _, err := edgeOf([]any{"GRANTED", "Agent", "nope", "Toolkit", other, nil}); err == nil || !strings.Contains(err.Error(), "starts at") {
		t.Errorf("a bad source: err = %v", err)
	}
	if _, err := edgeOf([]any{"GRANTED", "Agent", good, "Toolkit", "nope", nil}); err == nil || !strings.Contains(err.Error(), "ends at") {
		t.Errorf("a bad target: err = %v", err)
	}
	// A null where an id belongs is not a uuid either.
	if _, err := edgeOf([]any{"GRANTED", "Agent", nil, "Toolkit", other, nil}); err == nil {
		t.Error("a null source was accepted")
	}
}

func TestNodeOfSeparatesTheGraphsPropertiesFromTheCallers(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	n, err := nodeOf([]any{"Skill", id.String(), map[string]any{
		"id": id.String(), "name": "pdf", "embedding": []float32{1, 2}, "model": "m", "content_sha256": "ab12",
	}})
	if err != nil {
		t.Fatalf("nodeOf: %v", err)
	}
	if n.Ref != (Ref{LabelSkill, id}) || !slices.Equal(n.Embedding, []float32{1, 2}) || n.Model != "m" || !bytes.Equal(n.ContentSHA256, []byte{0xab, 0x12}) {
		t.Errorf("node = %+v", n)
	}
	if len(n.Props) != 1 || n.Props["name"] != "pdf" {
		t.Errorf("Props = %v, want only the caller's", n.Props)
	}

	if _, err := nodeOf([]any{"Skill", "nope", map[string]any{}}); err == nil {
		t.Error("a bad id was accepted")
	}
	if _, err := nodeOf([]any{"Skill", id.String(), map[string]any{"content_sha256": "zz"}}); err == nil {
		t.Error("a hash that is not hex was accepted")
	}
	// A node with no properties at all reads as an empty node, not a panic.
	if n, err := nodeOf([]any{"Agent", id.String(), nil}); err != nil || n.Embedding != nil || n.Model != "" {
		t.Errorf("nodeOf with no props = %+v, %v", n, err)
	}
}

// --- the view the API serves ---

// What a dashboard is shown: every node's label, id and the three properties
// it displays, which model embedded it, and the edges between the nodes
// returned. Never a vector: 768 floats a node is the whole cost of the read.
func TestViewReturnsNodesWithoutVectorsAndTheirEdges(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	agent := Node{
		Ref: Ref{LabelAgent, uuid.New()}, Props: map[string]any{"name": "triage", "description": "Sorts", "kind": "custom"},
		Embedding: []float32{1, 0, 0}, Model: "m", ContentSHA256: []byte{1},
	}
	kit := Node{Ref: Ref{LabelToolkit, uuid.New()}, Props: map[string]any{"name": "github", "kind": "mcp"}}
	mustPut(t, g, team, Node{Ref: Ref{LabelTeam, team}}, agent, kit)
	if err := g.EnsureIndexes(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	mustLink(t, g, team, Ref{LabelTeam, team}, EdgeOwns, agent.Ref)
	if err := g.SetOutEdges(t.Context(), team, agent.Ref, EdgeGranted, LabelToolkit,
		[]Edge{{To: kit.ID, Props: map[string]any{"only": []string{"search"}}}}); err != nil {
		t.Fatal(err)
	}

	view, truncated, err := g.View(t.Context(), team, 10)
	if err != nil || truncated {
		t.Fatalf("View = %v, %v", truncated, err)
	}
	if len(view.Nodes) != 3 || len(view.Edges) != 2 {
		t.Fatalf("%d nodes and %d edges", len(view.Nodes), len(view.Edges))
	}

	got := find(t, view, agent.ID)
	if got.Label != LabelAgent || got.Props["name"] != "triage" || got.Props["description"] != "Sorts" ||
		got.Props["kind"] != "custom" || got.Model != "m" {
		t.Errorf("agent = %+v", got)
	}
	if got.Embedding != nil || got.ContentSHA256 != nil {
		t.Error("the view carries a vector")
	}
	if _, has := find(t, view, kit.ID).Props["description"]; has {
		t.Error("a property the node does not have came back")
	}

	for _, e := range view.Edges {
		if e.Type == EdgeGranted {
			only, _ := e.Props["only"].([]any)
			if e.From != agent.Ref || e.To != kit.Ref || len(only) != 1 || only[0] != "search" {
				t.Errorf("granted = %+v", e)
			}
		}
	}
}

// At most limit nodes, truncated saying when there were more, and only the
// edges whose two ends were both returned.
func TestViewIsBoundedAndDropsEdgesToNodesItDidNotReturn(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	kit := bare(LabelToolkit)
	mustPut(t, g, team, kit)
	for range 5 {
		tool := bare(LabelTool)
		mustPut(t, g, team, tool)
		mustLink(t, g, team, kit.Ref, EdgeContains, tool.Ref)
	}

	view, truncated, err := g.View(t.Context(), team, 3)
	if err != nil || !truncated || len(view.Nodes) != 3 {
		t.Fatalf("View = %d nodes, truncated %v, %v", len(view.Nodes), truncated, err)
	}
	returned := map[uuid.UUID]bool{}
	for _, n := range view.Nodes {
		returned[n.ID] = true
	}
	for _, e := range view.Edges {
		if !returned[e.From.ID] || !returned[e.To.ID] {
			t.Errorf("an edge to a node that was not returned: %+v", e)
		}
	}

	all, truncated, err := g.View(t.Context(), team, 6)
	if err != nil || truncated || len(all.Nodes) != 6 || len(all.Edges) != 5 {
		t.Errorf("exactly the limit: %d nodes, %d edges, truncated %v, %v", len(all.Nodes), len(all.Edges), truncated, err)
	}
}

func TestViewOfATeamWithNoGraphIsEmptyAndMakesNone(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	view, truncated, err := g.View(t.Context(), team, 10)
	if err != nil || truncated || len(view.Nodes) != 0 || len(view.Edges) != 0 {
		t.Errorf("View = %+v, %v, %v", view, truncated, err)
	}
	if graphs, _ := g.db.ListGraphs(); slices.Contains(graphs, graphName(team)) {
		t.Error("looking at a team with no graph made one")
	}
}

func TestViewRefusesALimitBelowOne(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	for _, limit := range []int{0, -1} {
		if _, _, err := g.View(t.Context(), team, limit); err == nil {
			t.Errorf("View with limit %d succeeded", limit)
		}
	}
}

func TestViewWaitsForItsTurn(t *testing.T) {
	t.Parallel()

	g, team := liveGraph(t, 3)
	g.turn(team) <- struct{}{}
	t.Cleanup(func() { <-g.turn(team) })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := g.View(ctx, team, 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("View went ahead while the team's turn was held: %v", err)
	}
}
