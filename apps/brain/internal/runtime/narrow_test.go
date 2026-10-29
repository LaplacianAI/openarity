package runtime

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func skillNames(built Built) []string {
	out := make([]string, 0, len(built.Spec.Skills))
	for _, s := range built.Spec.Skills {
		out = append(out, s.Name)
	}
	slices.Sort(out)
	return out
}

// buildNarrowed is Build with a lineage that narrows the run.
func (f *fixture) buildNarrowed(t *testing.T, n *Narrowing) Built {
	t.Helper()

	built, err := f.builder.Build(t.Context(), f.run, noSession{}, Lineage{Narrow: n})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)
	return built
}

// narrowFixture is an agent granted three tools on a bare server, two on a
// prefixed one, and two skills.
func narrowFixture(t *testing.T) (*fixture, db.Skill, db.Skill) {
	t.Helper()

	f, writing, charts := skillFixture()
	f.store.grants = []db.ListGrantedMCPServersRow{
		urlServer("web", serve(t, "search", "fetch", "crawl").url, true),
		urlServer("github", serve(t, "issues", "pulls").url, false),
	}
	return f, writing, charts
}

func TestANarrowedChildHasOnlyWhatItWasGiven(t *testing.T) {
	f, writing, _ := narrowFixture(t)

	built := f.buildNarrowed(t, &Narrowing{Tools: []string{"fetch", "github__pulls"}, Skills: []uuid.UUID{writing.ID}})

	if got := toolNames(built); !slices.Equal(got, []string{"fetch", "github__pulls"}) {
		t.Errorf("tools = %v, want fetch and github__pulls", got)
	}
	if got := skillNames(built); !slices.Equal(got, []string{"writing"}) {
		t.Errorf("skills = %v, want writing", got)
	}
}

// A name the agent was never granted is ignored: narrowing takes away, it
// never gives.
func TestNarrowingNeverAddsAGrant(t *testing.T) {
	f, writing, _ := narrowFixture(t)

	built := f.buildNarrowed(t, &Narrowing{
		Tools:  []string{"search", "delete_repo", "issues", "web__fetch"},
		Skills: []uuid.UUID{writing.ID, uuid.New()},
	})

	if got := toolNames(built); !slices.Equal(got, []string{"search"}) {
		t.Errorf("tools = %v, want search alone: names as the agent sees them, and only granted ones", got)
	}
	if got := skillNames(built); !slices.Equal(got, []string{"writing"}) {
		t.Errorf("skills = %v, want writing alone", got)
	}
}

func TestNoNarrowingIsEveryGrant(t *testing.T) {
	f, _, _ := narrowFixture(t)

	built := f.buildNarrowed(t, nil)

	want := []string{"crawl", "fetch", "github__issues", "github__pulls", "search"}
	if got := toolNames(built); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	if got := skillNames(built); !slices.Equal(got, []string{"charts", "writing"}) {
		t.Errorf("skills = %v, want both", got)
	}
}

// An empty narrowing is a child given nothing of its own, which is not the
// same as no narrowing at all.
func TestAnEmptyNarrowingLeavesNoGrant(t *testing.T) {
	f, _, _ := narrowFixture(t)

	built := f.buildNarrowed(t, &Narrowing{})

	if got, skills := toolNames(built), skillNames(built); len(got) != 0 || len(skills) != 0 {
		t.Errorf("tools %v, skills %v; want none", got, skills)
	}
}

// The brain's own tools are how a run works at all; the outfitter narrows
// what the team granted, never those.
func TestTheBrainsOwnToolsAreNeverNarrowed(t *testing.T) {
	f, _, _ := searchFixture("brave", "k")
	f.withTeam(1)
	f.builder.note = (&noted{}).note

	built := f.buildNarrowed(t, &Narrowing{})

	var own []string
	for _, tl := range built.Spec.Tools {
		own = append(own, tl.Name)
	}
	slices.Sort(own)
	if want := []string{"delegate", "graph", "plan", "web_search"}; !slices.Equal(own, want) {
		t.Errorf("tools = %v, want the brain's own %v", own, want)
	}
}
