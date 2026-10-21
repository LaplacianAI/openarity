package graph

import (
	"math"
	"slices"
	"testing"

	"github.com/google/uuid"
)

const searchModel = "test-model"

func embedded(label Label, name string, vector []float32) Node {
	return Node{
		Ref:       Ref{Label: label, ID: uuid.New()},
		Props:     map[string]any{"name": name, "description": "Does " + name},
		Embedding: vector, Model: searchModel, ContentSHA256: []byte(name),
	}
}

func toolkit(name string) Node {
	return Node{Ref: Ref{Label: LabelToolkit, ID: uuid.New()}, Props: map[string]any{"name": name}}
}

func mustGrantToolkit(t *testing.T, g *Graph, team uuid.UUID, a, k Node, only []string) {
	t.Helper()
	e := Edge{To: k.ID}
	if only != nil {
		e.Props = map[string]any{"only": only}
	}
	if err := g.SetOutEdges(t.Context(), team, a.Ref, EdgeGranted, LabelToolkit, []Edge{e}); err != nil {
		t.Fatalf("granting %s to %s: %v", k.Props["name"], a.Props["name"], err)
	}
}

func idsOf(nodes ...Node) []uuid.UUID {
	out := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

func mustSearch(t *testing.T, g *Graph, team uuid.UUID, s Search) []Hit {
	t.Helper()
	if s.Model == "" {
		s.Model = searchModel
	}
	if s.Limit == 0 {
		s.Limit = 10
	}
	hits, err := g.Search(t.Context(), team, s)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	return hits
}

func names(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Name
	}
	return out
}

func hitNamed(t *testing.T, hits []Hit, name string) Hit {
	t.Helper()
	for _, h := range hits {
		if h.Name == name {
			return h
		}
	}
	t.Fatalf("%q is not among %v", name, names(hits))
	return Hit{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-5 }

func pathOf(h Hit) []Step { return h.Path }

func TestSearchRanksByCosineAndReportsTheScore(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	same := embedded(LabelSkill, "same", []float32{1, 0})
	slant := embedded(LabelSkill, "slant", []float32{0.6, 0.8})
	square := embedded(LabelSkill, "square", []float32{0, 1})
	mustPut(t, g, team, square, same, slant)

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindSkill},
		Reach: Reach{Skills: idsOf(same, slant, square)},
	})

	if got := names(hits); !slices.Equal(got, []string{"same", "slant", "square"}) {
		t.Fatalf("order = %v, want same, slant, square", got)
	}
	for i, want := range []float64{1, 0.6, 0} {
		if !near(hits[i].Score, want) {
			t.Errorf("%s scored %v, want %v (one minus the cosine distance)", hits[i].Name, hits[i].Score, want)
		}
	}
	if h := hits[0]; h.Kind != KindSkill || h.ID != same.ID || h.Description != "Does same" {
		t.Errorf("first hit = %+v, want the skill same with its id and description", h)
	}
	if p := pathOf(hits[0]); len(p) != 1 || p[0] != (Step{Kind: KindSkill, Name: "same"}) {
		t.Errorf("a direct match's path = %v, want only itself", p)
	}
}

// Review Focus 1, and the runtime spec's open item 7: twelve nodes nearer the
// query but out of reach must not use up a cut of ten.
func TestSearchNeverSpendsTheCutOutOfReach(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	var mine []Node
	for _, name := range []string{"mine-a", "mine-b", "mine-c"} {
		n := embedded(LabelSkill, name, []float32{0.6, 0.8})
		mustPut(t, g, team, n)
		mine = append(mine, n)
	}
	for i := range 12 {
		mustPut(t, g, team, embedded(LabelSkill, "theirs-"+string(rune('a'+i)), []float32{1, 0}))
	}

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindSkill}, Limit: 10,
		Reach: Reach{Skills: idsOf(mine...)},
	})

	if got := names(hits); !slices.Equal(got, []string{"mine-a", "mine-b", "mine-c"}) {
		t.Fatalf("hits = %v, want exactly the three in reach", got)
	}
}

func TestSearchCutsToTheLimitAndBreaksTiesByName(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	var all []Node
	for _, name := range []string{"delta", "alpha", "charlie", "bravo"} {
		n := embedded(LabelSkill, name, []float32{1, 0})
		mustPut(t, g, team, n)
		all = append(all, n)
	}

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindSkill}, Limit: 3,
		Reach: Reach{Skills: idsOf(all...)},
	})

	if got := names(hits); !slices.Equal(got, []string{"alpha", "bravo", "charlie"}) {
		t.Fatalf("hits = %v, want the first three by name among equal scores", got)
	}
}

// Review Focus 2: after EMBEDDING_MODEL changes and before a rebuild, a node
// still carries a vector from the old model's space. Comparing it would rank
// by noise.
func TestSearchSkipsAVectorFromAnotherModel(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	stale := embedded(LabelSkill, "stale", []float32{1, 0})
	stale.Model = "old-model"
	fresh := embedded(LabelSkill, "fresh", []float32{0, 1})
	mustPut(t, g, team, stale, fresh)

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindSkill},
		Reach: Reach{Skills: idsOf(stale, fresh)},
	})

	if got := names(hits); !slices.Equal(got, []string{"fresh"}) {
		t.Fatalf("hits = %v, want only fresh", got)
	}
}

func TestSearchOnlyReturnsTheKindsAskedFor(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "agent", []float32{1, 0})
	s := embedded(LabelSkill, "skill", []float32{1, 0})
	tool := embedded(LabelTool, "tool", []float32{1, 0})
	mustPut(t, g, team, a, s, tool)
	reach := Reach{Agents: idsOf(a), Skills: idsOf(s), Tools: idsOf(tool)}

	for _, c := range []struct {
		kinds []Kind
		want  []string
	}{
		{[]Kind{KindAgent}, []string{"agent"}},
		{[]Kind{KindTool}, []string{"tool"}},
		{[]Kind{KindSkill, KindTool}, []string{"skill", "tool"}},
		{nil, nil},
	} {
		hits := mustSearch(t, g, team, Search{Vector: []float32{1, 0}, Kinds: c.kinds, Reach: reach})
		got := names(hits)
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("kinds %v gave %v, want %v", c.kinds, got, c.want)
		}
	}
}

// An agent is found through the skill it holds: hops 1, the skill's score,
// and the path from the agent to the skill.
func TestSearchFindsAnAgentThroughItsSkill(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "clerk", []float32{0, 1})
	s := embedded(LabelSkill, "invoices", []float32{1, 0})
	mustPut(t, g, team, a, s)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, s.Ref)

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindAgent}, Hops: 1,
		Reach: Reach{Agents: idsOf(a)},
	})

	h := hitNamed(t, hits, "clerk")
	if !near(h.Score, 1) {
		t.Errorf("clerk scored %v, want the skill's 1", h.Score)
	}
	want := []Step{{KindAgent, "clerk"}, {KindSkill, "invoices"}}
	if !slices.Equal(h.Path, want) {
		t.Errorf("path = %v, want %v", h.Path, want)
	}
}

// At hops 0 the same agent is only its own match: its own score, its own path.
func TestSearchAtHopsZeroReturnsOnlyDirectMatches(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "clerk", []float32{0, 1})
	s := embedded(LabelSkill, "invoices", []float32{1, 0})
	mustPut(t, g, team, a, s)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, s.Ref)

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindAgent},
		Reach: Reach{Agents: idsOf(a)},
	})

	h := hitNamed(t, hits, "clerk")
	if !near(h.Score, 0) || len(h.Path) != 1 {
		t.Errorf("at hops 0 clerk = score %v path %v, want its own 0 and only itself", h.Score, h.Path)
	}
}

func TestSearchKeepsTheBestPathPerResult(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "clerk", []float32{0, 1})
	weak := embedded(LabelSkill, "weak", []float32{0.6, 0.8})
	strong := embedded(LabelSkill, "strong", []float32{1, 0})
	mustPut(t, g, team, a, weak, strong)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, weak.Ref)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, strong.Ref)

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindAgent}, Hops: 1,
		Reach: Reach{Agents: idsOf(a)},
	})

	if len(hits) != 1 {
		t.Fatalf("hits = %v, want clerk once", names(hits))
	}
	if h := hits[0]; !near(h.Score, 1) || h.Path[len(h.Path)-1].Name != "strong" {
		t.Errorf("clerk = score %v path %v, want 1 through strong", h.Score, h.Path)
	}
}

// The runtime spec: a walk from a tool to the agents holding it must not
// report an agent granted the server without that tool.
func TestSearchHonoursTheAllowListOnTheHop(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	limited := embedded(LabelAgent, "limited", []float32{0, 1})
	whole := embedded(LabelAgent, "whole", []float32{0, 1})
	files := toolkit("files")
	search := embedded(LabelTool, "search", []float32{0, 1})
	del := embedded(LabelTool, "delete", []float32{1, 0})
	mustPut(t, g, team, limited, whole, files, search, del)
	mustLink(t, g, team, files.Ref, EdgeContains, search.Ref)
	mustLink(t, g, team, files.Ref, EdgeContains, del.Ref)
	mustGrantToolkit(t, g, team, limited, files, []string{"search"})
	mustGrantToolkit(t, g, team, whole, files, nil)

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindAgent}, Hops: 2,
		Reach: Reach{Agents: idsOf(limited, whole)},
	})

	w := hitNamed(t, hits, "whole")
	wantPath := []Step{{KindAgent, "whole"}, {KindToolkit, "files"}, {KindTool, "delete"}}
	if !near(w.Score, 1) || !slices.Equal(w.Path, wantPath) {
		t.Errorf("whole = score %v path %v, want 1 through %v", w.Score, w.Path, wantPath)
	}
	for _, h := range hits {
		if h.Name == "limited" && (h.Score > 0.5 || slices.Contains(h.Path, Step{KindTool, "delete"})) {
			t.Errorf("limited reached delete, which its grant leaves out: score %v path %v", h.Score, h.Path)
		}
	}
}

// From the other end: the tools an agent reaches through its grant, and only
// those its allow-list keeps, even when the caller may reach both tools.
func TestSearchFindsAToolThroughTheAgentGrantedIt(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	limited := embedded(LabelAgent, "limited", []float32{1, 0})
	files := toolkit("files")
	search := embedded(LabelTool, "search", []float32{0, 1})
	del := embedded(LabelTool, "delete", []float32{0, 1})
	mustPut(t, g, team, limited, files, search, del)
	mustLink(t, g, team, files.Ref, EdgeContains, search.Ref)
	mustLink(t, g, team, files.Ref, EdgeContains, del.Ref)
	mustGrantToolkit(t, g, team, limited, files, []string{"search"})

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindTool}, Hops: 2,
		Reach: Reach{Tools: idsOf(search, del)},
	})

	s := hitNamed(t, hits, "search")
	wantPath := []Step{{KindTool, "search"}, {KindToolkit, "files"}, {KindAgent, "limited"}}
	if !near(s.Score, 1) || !slices.Equal(s.Path, wantPath) {
		t.Errorf("search = score %v path %v, want 1 through %v", s.Score, s.Path, wantPath)
	}
	if d := hitNamed(t, hits, "delete"); !near(d.Score, 0) {
		t.Errorf("delete scored %v through a grant that leaves it out, want only its own 0", d.Score)
	}
}

// Hops 2 between skills goes through an agent holding both, and a skill is
// never its own neighbour.
func TestSearchFindsASkillThroughAnotherSkillOfTheSameAgent(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "clerk", []float32{0, 1})
	match := embedded(LabelSkill, "match", []float32{1, 0})
	sibling := embedded(LabelSkill, "sibling", []float32{0, 1})
	mustPut(t, g, team, a, match, sibling)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, match.Ref)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, sibling.Ref)

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindSkill}, Hops: 2,
		Reach: Reach{Skills: idsOf(match, sibling)},
	})

	s := hitNamed(t, hits, "sibling")
	want := []Step{{KindSkill, "sibling"}, {KindAgent, "clerk"}, {KindSkill, "match"}}
	if !near(s.Score, 1) || !slices.Equal(s.Path, want) {
		t.Errorf("sibling = score %v path %v, want 1 through %v", s.Score, s.Path, want)
	}
	if m := hitNamed(t, hits, "match"); len(m.Path) != 1 {
		t.Errorf("match reached itself through clerk: path %v", m.Path)
	}
}

// Hops count: a skill two hops from the tool cannot be reached at hops 2.
func TestSearchStopsAtTheHopsAskedFor(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "clerk", []float32{0, 1})
	files := toolkit("files")
	tool := embedded(LabelTool, "read", []float32{1, 0})
	s := embedded(LabelSkill, "filing", []float32{0, 1})
	mustPut(t, g, team, a, files, tool, s)
	mustLink(t, g, team, files.Ref, EdgeContains, tool.Ref)
	mustGrantToolkit(t, g, team, a, files, nil)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, s.Ref)

	for hops, want := range map[int]float64{2: 0, 3: 1} {
		hits := mustSearch(t, g, team, Search{
			Vector: []float32{1, 0}, Kinds: []Kind{KindSkill}, Hops: hops,
			Reach: Reach{Skills: idsOf(s)},
		})
		if h := hitNamed(t, hits, "filing"); !near(h.Score, want) {
			t.Errorf("at hops %d filing scored %v, want %v", hops, h.Score, want)
		}
	}

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindSkill}, Hops: 3,
		Reach: Reach{Skills: idsOf(s)},
	})
	want := []Step{{KindSkill, "filing"}, {KindAgent, "clerk"}, {KindToolkit, "files"}, {KindTool, "read"}}
	if h := hitNamed(t, hits, "filing"); !slices.Equal(h.Path, want) {
		t.Errorf("filing's path = %v, want %v", h.Path, want)
	}
}

// The other three-hop walk: a tool found through a skill held by an agent
// granted the tool's server, cut by that grant's allow-list.
func TestSearchFindsAToolThroughASkillOfTheAgentGrantedIt(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "clerk", []float32{0, 1})
	s := embedded(LabelSkill, "filing", []float32{1, 0})
	files := toolkit("files")
	read := embedded(LabelTool, "read", []float32{0, 1})
	wipe := embedded(LabelTool, "wipe", []float32{0, 1})
	mustPut(t, g, team, a, s, files, read, wipe)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, s.Ref)
	mustLink(t, g, team, files.Ref, EdgeContains, read.Ref)
	mustLink(t, g, team, files.Ref, EdgeContains, wipe.Ref)
	mustGrantToolkit(t, g, team, a, files, []string{"read"})

	for hops, want := range map[int]float64{2: 0, 3: 1} {
		hits := mustSearch(t, g, team, Search{
			Vector: []float32{1, 0}, Kinds: []Kind{KindTool}, Hops: hops,
			Reach: Reach{Tools: idsOf(read, wipe)},
		})
		if h := hitNamed(t, hits, "read"); !near(h.Score, want) {
			t.Errorf("at hops %d read scored %v, want %v", hops, h.Score, want)
		}
	}

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindTool}, Hops: 3,
		Reach: Reach{Tools: idsOf(read, wipe)},
	})
	want := []Step{{KindTool, "read"}, {KindToolkit, "files"}, {KindAgent, "clerk"}, {KindSkill, "filing"}}
	if h := hitNamed(t, hits, "read"); !slices.Equal(h.Path, want) {
		t.Errorf("read's path = %v, want %v", h.Path, want)
	}
	if h := hitNamed(t, hits, "wipe"); !near(h.Score, 0) {
		t.Errorf("wipe scored %v through a grant that leaves it out, want only its own 0", h.Score)
	}
}

// Review Focus 3: the outbox has not reached this team yet, so it has no
// graph at all. That is no results, not a failure.
func TestSearchOnATeamWithNoGraphIsEmpty(t *testing.T) {
	t.Parallel()
	g, _ := liveGraph(t, 2)

	hits, err := g.Search(t.Context(), uuid.New(), Search{
		Vector: []float32{1, 0}, Model: searchModel, Kinds: []Kind{KindSkill}, Limit: 10,
		Reach: Reach{Skills: []uuid.UUID{uuid.New()}},
	})
	if err != nil || len(hits) != 0 {
		t.Fatalf("Search = %v, %v, want nothing and no error", hits, err)
	}
}

func TestSearchWithNothingInReachIsEmpty(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)
	mustPut(t, g, team, embedded(LabelSkill, "theirs", []float32{1, 0}))

	hits := mustSearch(t, g, team, Search{Vector: []float32{1, 0}, Kinds: []Kind{KindSkill, KindAgent, KindTool}})
	if len(hits) != 0 {
		t.Fatalf("hits = %v, want none with an empty reach", names(hits))
	}
}

func TestSearchRefusesALimitBelowOne(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	_, err := g.Search(t.Context(), team, Search{
		Vector: []float32{1, 0}, Model: searchModel, Kinds: []Kind{KindSkill},
		Reach: Reach{Skills: []uuid.UUID{uuid.New()}},
	})
	if err == nil {
		t.Fatal("Search with a limit of 0 succeeded, want a refusal")
	}
}

func TestSearchFindsASkillThroughTheAgentHoldingIt(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "clerk", []float32{1, 0})
	s := embedded(LabelSkill, "filing", []float32{0, 1})
	mustPut(t, g, team, a, s)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, s.Ref)

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindSkill}, Hops: 1,
		Reach: Reach{Skills: idsOf(s)},
	})

	h := hitNamed(t, hits, "filing")
	want := []Step{{KindSkill, "filing"}, {KindAgent, "clerk"}}
	if !near(h.Score, 1) || !slices.Equal(h.Path, want) {
		t.Errorf("filing = score %v path %v, want 1 through %v", h.Score, h.Path, want)
	}
}

// A skill is not found through a tool its agent's grant leaves out.
func TestSearchFindsASkillOnlyThroughAToolItsAgentMayUse(t *testing.T) {
	t.Parallel()
	g, team := liveGraph(t, 2)

	a := embedded(LabelAgent, "clerk", []float32{0, 1})
	s := embedded(LabelSkill, "filing", []float32{0, 1})
	files := toolkit("files")
	read := embedded(LabelTool, "read", []float32{0, 1})
	wipe := embedded(LabelTool, "wipe", []float32{1, 0})
	mustPut(t, g, team, a, s, files, read, wipe)
	mustLink(t, g, team, a.Ref, EdgeHasSkill, s.Ref)
	mustLink(t, g, team, files.Ref, EdgeContains, read.Ref)
	mustLink(t, g, team, files.Ref, EdgeContains, wipe.Ref)
	mustGrantToolkit(t, g, team, a, files, []string{"read"})

	hits := mustSearch(t, g, team, Search{
		Vector: []float32{1, 0}, Kinds: []Kind{KindSkill}, Hops: 3,
		Reach: Reach{Skills: idsOf(s)},
	})

	if h := hitNamed(t, hits, "filing"); !near(h.Score, 0) {
		t.Errorf("filing scored %v through wipe, which clerk's grant leaves out: path %v", h.Score, h.Path)
	}
}
