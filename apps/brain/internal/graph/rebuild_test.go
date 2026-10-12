package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func (w *world) rebuild(team uuid.UUID) int {
	w.t.Helper()

	n, err := w.p.Rebuild(w.t.Context(), team)
	if err != nil {
		w.t.Fatalf("Rebuild: %v; logs:\n%s", err, w.logs.String())
	}
	return n
}

// describe is a snapshot as text: every node with its properties and what
// produced its vector, every edge with its properties, sorted. Two graphs
// with the same description are the same graph.
func (w *world) describe(s Snapshot) []string {
	w.t.Helper()

	var out []string
	for _, n := range s.Nodes {
		keys := make([]string, 0, len(n.Props))
		for k := range n.Props {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		line := fmt.Sprintf("%s %s", n.Label, n.ID)
		for _, k := range keys {
			line += fmt.Sprintf(" %s=%v", k, n.Props[k])
		}
		out = append(out, fmt.Sprintf("%s model=%q sha=%x vector=%v", line, n.Model, n.ContentSHA256, n.Embedding))
	}
	for _, e := range s.Edges {
		out = append(out, fmt.Sprintf("%s %s -%s-> %s %s %v", e.From.Label, e.From.ID, e.Type, e.To.Label, e.To.ID, e.Props))
	}
	slices.Sort(out)
	return out
}

// The proof that the graph is rebuildable from Postgres: a history of
// creates, grants, revokes, renames and deletes, drained as it happened, and
// then the same team rebuilt from nothing. The two graphs are the same graph.
func TestARebuildEqualsTheIncrementalGraph(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()

	linear := w.server(a.team, "linear")
	w.tool(linear, "issues", "List issues")
	docx := w.skill(a.team, "docx", "Write Word documents")
	w.grant(a.triage, linear, "issues")
	w.teach(a.triage, docx)
	w.drained()

	// Rename, revoke, re-grant wider, delete.
	p := skillParams(a.pdf)
	p.Description = "Extract text and tables from PDFs"
	w.updateSkill(p)
	u := agentUpdate(a.triage)
	u.Name, u.Instructions = "triager", "Label everything."
	if _, err := w.store.UpdateAgent(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	w.names[a.triage.ID] = "triager"
	w.must(w.store.ClearAgentMCPServers(t.Context(), a.triage.ID))
	w.grant(a.triage, linear)
	w.must(w.store.DeleteMCPToolsNotIn(t.Context(), db.DeleteMCPToolsNotInParams{McpServerID: a.github.ID, Names: []string{"search"}}))
	w.must(w.store.DeleteMCPServer(t.Context(), a.github.ID))
	reviewer := w.agent(a.team, "reviewer", "Reads pull requests")
	w.teach(reviewer, a.pdf)
	w.drained()

	incremental := w.describe(w.snapshot(a.team.ID))
	if len(incremental) < 10 {
		t.Fatalf("the incremental graph has only %d lines, so equality would prove little:\n%s", len(incremental), strings.Join(incremental, "\n"))
	}

	n := w.rebuild(a.team.ID)
	rebuilt := w.describe(w.snapshot(a.team.ID))
	if !slices.Equal(rebuilt, incremental) {
		t.Errorf("the rebuilt graph is not the incremental one:\n--- incremental\n%s\n--- rebuilt\n%s",
			strings.Join(incremental, "\n"), strings.Join(rebuilt, "\n"))
	}
	// linear and its tool, pdf and docx, triager and reviewer.
	if n != 6 {
		t.Errorf("Rebuild projected %d, want the 6 things the team holds", n)
	}
}

// A rebuild starts from nothing, so it embeds everything: one request for
// each node that carries a vector, and none for the two kinds that do not.
func TestARebuildReembedsEveryNode(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()
	asked := len(w.embedded())

	w.rebuild(a.team.ID)

	got := w.embedded()[asked:]
	slices.Sort(got)
	want := []string{"create\nOpen an issue", "pdf\nExtract text from PDFs", "search\nSearch code", "triage\nSorts incoming issues"}
	if !slices.Equal(got, want) {
		t.Errorf("a rebuild embedded %q, want each of %q once", got, want)
	}
}

// What is in the graph and not in Postgres is gone afterwards. That is the
// difference between rebuilding and projecting everything again.
func TestARebuildRemovesWhatPostgresDoesNotHold(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()

	stray := bare(LabelSkill)
	mustPut(t, w.graph, a.team.ID, stray)
	mustLink(t, w.graph, a.team.ID, Ref{LabelTeam, a.team.ID}, EdgeOwns, stray.Ref)

	w.rebuild(a.team.ID)
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

// A team that was never projected, or whose graph was lost, is built whole.
func TestARebuildOfATeamWithNoGraphBuildsIt(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.exec("DELETE FROM graph_outbox")

	if n := w.rebuild(a.team.ID); n != 5 {
		t.Errorf("Rebuild projected %d, want 5", n)
	}
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

// The query happens to list a team's things in an order that works: servers
// before their tools, agents last. Nothing promises that of a UNION ALL, so
// the rebuild sorts them itself. Here they arrive agents first.
func TestARebuildDoesNotDependOnTheOrderOfTheQuery(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.exec("DELETE FROM graph_outbox")
	w.store.backwards = true

	w.rebuild(a.team.ID)
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

func TestARebuildOfAnEmptyTeamLeavesItsNodeAndIndexes(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")

	if n := w.rebuild(team.ID); n != 0 {
		t.Errorf("Rebuild projected %d", n)
	}
	w.wantGraph(team.ID, []string{"Team:acme"}, nil)
	if err := w.graph.verifyIndexes(t.Context(), team.ID); err != nil {
		t.Errorf("after a rebuild the indexes are not there: %v", err)
	}
}

// Changing the model is the reason a rebuild exists. The old graph's index
// is the wrong width for every new vector; the rebuilt one is not.
func TestARebuildMovesAGraphToANewWidth(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()

	wide, err := Dial(t.Context(), liveFalkorURL(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wide.Close() })
	w.router.Close()
	w.router = newRouter(t, inOrder(4))
	w.p = w.projector("a-wider-model", wide)

	if _, err := w.p.Rebuild(t.Context(), a.team.ID); err != nil {
		t.Fatalf("Rebuild at the new width: %v; logs:\n%s", err, w.logs.String())
	}

	s := mustSnapshot(t, wide, a.team.ID)
	if n := find(t, s, a.pdf.ID); len(n.Embedding) != 4 || n.Model != "a-wider-model" {
		t.Errorf("the skill has a vector of %d under %q", len(n.Embedding), n.Model)
	}
	if err := wide.verifyIndexes(t.Context(), a.team.ID); err != nil {
		t.Errorf("the rebuilt graph's indexes: %v", err)
	}
}

func TestARebuildTouchesNoOtherTeam(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	globex := w.team("globex")
	w.skill(globex, "docx", "Write Word documents")
	w.drained()
	asked := len(w.embedded())
	before := w.describe(w.snapshot(globex.ID))

	w.rebuild(a.team.ID)

	if after := w.describe(w.snapshot(globex.ID)); !slices.Equal(after, before) {
		t.Errorf("rebuilding one team changed another:\n%v\n%v", before, after)
	}
	for _, text := range w.embedded()[asked:] {
		if strings.HasPrefix(text, "docx") {
			t.Error("rebuilding one team embedded another's skill")
		}
	}
}

// The outbox is the drain's. A rebuild that cleared it would lose a change
// made while it ran; one that read it would not be a rebuild.
func TestARebuildLeavesTheOutboxAlone(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	waiting := w.outstanding()

	w.rebuild(a.team.ID)

	var attempted int
	if err := w.sql.QueryRow(t.Context(), "SELECT count(*) FROM graph_outbox WHERE attempts > 0").Scan(&attempted); err != nil {
		t.Fatal(err)
	}
	if w.outstanding() != waiting || attempted != 0 {
		t.Errorf("a rebuild left %d of %d outbox rows and claimed %d", w.outstanding(), waiting, attempted)
	}

	// And the drain that follows changes nothing and costs no embedding.
	asked := len(w.embedded())
	before := w.describe(w.snapshot(a.team.ID))
	w.drained()
	if after := w.describe(w.snapshot(a.team.ID)); !slices.Equal(after, before) || len(w.embedded()) != asked {
		t.Error("the drain after a rebuild changed the graph or embedded again")
	}
}

// --- refusals ---

// A mistyped id is the likeliest way to reach this, and it must not pass
// for a team with nothing in it.
func TestARebuildOfATeamThatDoesNotExistIsAnError(t *testing.T) {
	w := newWorld(t)
	ghost := uuid.New()

	n, err := w.p.Rebuild(t.Context(), ghost)
	if err == nil || n != 0 || !strings.Contains(err.Error(), ghost.String()) {
		t.Errorf("Rebuild = %d, %v; want an error naming the team", n, err)
	}
	if graphs, _ := w.graph.db.ListGraphs(); slices.Contains(graphs, graphName(ghost)) {
		t.Error("a rebuild of no team made a graph")
	}
}

// Nothing is dropped until everything needed to build it again has been
// read: a rebuild that cannot start leaves the graph that was there.
func TestARebuildThatCannotReadTheTeamDropsNothing(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()
	w.store.getTeam = errors.New("the database went away")

	if _, err := w.p.Rebuild(t.Context(), a.team.ID); err == nil || !strings.Contains(err.Error(), "the database went away") {
		t.Errorf("Rebuild: err = %v", err)
	}
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

func TestARebuildThatCannotListTheTeamDropsNothing(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.drained()
	w.store.entities = errors.New("the database went away")

	if _, err := w.p.Rebuild(t.Context(), a.team.ID); err == nil || !strings.Contains(err.Error(), "the database went away") {
		t.Errorf("Rebuild: err = %v", err)
	}
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

// One thing that will not project does not stop the rest, and the rebuild
// says how many it missed: an exit code of zero would mean a whole graph.
func TestARebuildReportsWhatItCouldNotProject(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	w.down.Store(true)

	n, err := w.p.Rebuild(t.Context(), a.team.ID)
	if err == nil || !strings.Contains(err.Error(), "4 of the 5") || !strings.Contains(err.Error(), a.team.ID.String()) {
		t.Errorf("Rebuild = %d, %v; want four of five reported", n, err)
	}
	if n != 1 {
		t.Errorf("Rebuild projected %d, want the server, which needs no embedding", n)
	}
	w.wantGraph(a.team.ID, []string{"Team:acme", "Toolkit:github"}, []string{"Team:acme OWNS Toolkit:github"})
	if got := len(w.logged("graph: a row was not rebuilt")); got != 4 {
		t.Errorf("%d failures logged, want 4:\n%s", got, w.logs.String())
	}
}

func TestACancelledRebuildStops(t *testing.T) {
	w := newWorld(t)
	a := w.acme()

	ctx, cancel := context.WithCancel(t.Context())
	w.store.onGetSkill = cancel
	if _, err := w.p.Rebuild(ctx, a.team.ID); !errors.Is(err, context.Canceled) {
		t.Errorf("Rebuild: err = %v, want the cancellation", err)
	}
	if n := len(w.logged("graph: a row was not rebuilt")); n > 1 {
		t.Errorf("%d failures logged after the cancellation, want at most the one in flight", n)
	}
}

// --- one writer at a time ---

// A rebuild drops a graph and writes it again. A drain running through the
// middle of that would be writing into the half that is not there yet, so
// the two take the same lock, and a rebuild waits for it where a drain would
// skip its turn.
func TestARebuildWaitsForADrainToFinish(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	holder := w.store.Store

	locked, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		_, err := holder.WithAdvisoryLock(context.WithoutCancel(t.Context()), w.p.lockID, func(context.Context) error {
			close(locked)
			<-release
			return nil
		})
		held <- err
	}()
	<-locked

	done := make(chan error, 1)
	go func() {
		_, err := w.p.Rebuild(context.WithoutCancel(t.Context()), a.team.ID)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("Rebuild ran while another held the lock: %v", err)
	case <-time.After(lockRetry + 500*time.Millisecond):
	}
	if graphs, _ := w.graph.db.ListGraphs(); slices.Contains(graphs, graphName(a.team.ID)) {
		t.Error("the waiting rebuild wrote to the graph")
	}

	close(release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Rebuild after the lock was free: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Rebuild never ran after the lock was released")
	}
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))
}

func TestARebuildWaitingForTheLockHonoursItsDeadline(t *testing.T) {
	w := newWorld(t)
	a := w.acme()

	_, err := w.store.WithAdvisoryLock(t.Context(), w.p.lockID, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()

		started := time.Now()
		if _, err := w.p.Rebuild(ctx, a.team.ID); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Rebuild: err = %v, want the deadline", err)
		}
		if took := time.Since(started); took > lockRetry {
			t.Errorf("Rebuild gave up after %v with a deadline of 300ms", took)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if graphs, _ := w.graph.db.ListGraphs(); slices.Contains(graphs, graphName(a.team.ID)) {
		t.Error("a rebuild that never got the lock wrote to the graph")
	}
}

// --- the query ---

// The query names things as the outbox does. A name the projector does not
// know would fail every rebuild, and a table left out would leave its nodes
// out of every rebuilt graph with no error at all.
func TestTheRebuildQueryListsEverythingTheTriggersEnqueue(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	globex := w.team("globex")
	w.skill(globex, "docx", "Write Word documents")

	rows, err := w.store.ListGraphEntities(t.Context(), a.team.ID)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, r := range rows {
		listed = append(listed, r.Entity+" "+r.ID.String())
	}
	slices.Sort(listed)

	// What the triggers wrote for this team is, with duplicates removed,
	// what the team holds: nothing here has been deleted.
	enqueued, err := w.sql.Query(t.Context(),
		"SELECT DISTINCT entity || ' ' || entity_id FROM graph_outbox WHERE team_id = $1 ORDER BY 1", a.team.ID)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for enqueued.Next() {
		var line string
		if err := enqueued.Scan(&line); err != nil {
			t.Fatal(err)
		}
		want = append(want, line)
	}

	if !slices.Equal(listed, want) || len(want) != 5 {
		t.Errorf("the rebuild query lists\n%v\nand the triggers enqueued\n%v", listed, want)
	}
}
