package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// None of these is parallel: a rebuild takes the lock every worker shares.

type rebuildWorld struct {
	t      *testing.T
	cfg    *config.Config
	store  *store.Store
	reader *graph.Graph
	router *modelRouter
	logs   bytes.Buffer
}

func newRebuildWorld(t *testing.T) *rebuildWorld {
	t.Helper()

	w := &rebuildWorld{t: t, router: newModelRouter(t, 3), store: registrySchema(t)}
	w.cfg = &config.Config{
		FalkorDBURL: liveFalkorURL(t), OmniRouteURL: w.router.URL + "/v1",
		EmbeddingModel: "test-model", EmbeddingDimensions: 3,
		SecretsBackend: config.SecretsBackendStatic,
	}

	reader, err := graph.Dial(t.Context(), w.cfg.FalkorDBURL, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	w.reader = reader
	return w
}

// team is a team holding one skill, with its graph dropped afterwards.
func (w *rebuildWorld) team(name string) uuid.UUID {
	w.t.Helper()

	team, err := w.store.CreateTeam(w.t.Context(), name)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.store.CreateSkill(w.t.Context(), db.CreateSkillParams{
		TeamID: team.ID, Name: name + "-skill", Description: "Does things for " + name, Body: "# skill",
		Metadata: []byte(`{}`), Source: "upload",
	}); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = w.reader.DropTeam(context.WithoutCancel(w.t.Context()), team.ID) })
	return team.ID
}

func (w *rebuildWorld) run(team uuid.UUID) error {
	logger := slog.New(slog.NewTextHandler(&w.logs, nil))
	return rebuildGraph(w.t.Context(), w.cfg, logger, w.store, team)
}

// skills is how many skill nodes a team's graph holds.
func (w *rebuildWorld) skills(team uuid.UUID) int {
	w.t.Helper()

	s, err := w.reader.Snapshot(w.t.Context(), team)
	if err != nil {
		w.t.Fatal(err)
	}
	n := 0
	for _, node := range s.Nodes {
		if node.Label == graph.LabelSkill {
			n++
		}
	}
	return n
}

func TestRebuildingOneTeamBuildsThatTeamOnly(t *testing.T) {
	w := newRebuildWorld(t)
	acme, globex := w.team("acme"), w.team("globex")

	if err := w.run(acme); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, w.logs.String())
	}
	if w.skills(acme) != 1 || w.skills(globex) != 0 {
		t.Errorf("acme has %d skills in the graph and globex %d, want 1 and 0", w.skills(acme), w.skills(globex))
	}
	if !strings.Contains(w.logs.String(), "Graph rebuilt") || !strings.Contains(w.logs.String(), acme.String()) ||
		strings.Contains(w.logs.String(), globex.String()) {
		t.Errorf("the log does not say which team was rebuilt:\n%s", w.logs.String())
	}
}

func TestRebuildingWithNoTeamBuildsEveryTeam(t *testing.T) {
	w := newRebuildWorld(t)
	acme, globex := w.team("acme"), w.team("globex")

	if err := w.run(uuid.Nil); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, w.logs.String())
	}
	if w.skills(acme) != 1 || w.skills(globex) != 1 {
		t.Errorf("acme has %d skills in the graph and globex %d, want 1 each", w.skills(acme), w.skills(globex))
	}
	if n := strings.Count(w.logs.String(), "Graph rebuilt"); n != 2 {
		t.Errorf("%d teams logged as rebuilt, want 2:\n%s", n, w.logs.String())
	}
}

// One team that will not rebuild does not stop the others, and the command
// fails: its exit code is what a deploy job reads.
func TestOneFailingTeamFailsTheCommandButNotTheRest(t *testing.T) {
	w := newRebuildWorld(t)
	ids := map[string]uuid.UUID{"acme": w.team("acme"), "globex": w.team("globex")}

	// Teams are rebuilt in the order of their ids. The one that fails is the
	// first, so a command that stopped at a failure would build nothing.
	broken, sound := "acme", "globex"
	if ids["globex"].String() < ids["acme"].String() {
		broken, sound = "globex", "acme"
	}
	w.router.refuse = broken + "-skill"

	err := w.run(uuid.Nil)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 teams") {
		t.Errorf("rebuild: err = %v, want one of two teams reported", err)
	}
	if w.skills(ids[sound]) != 1 {
		t.Errorf("the team after the one that failed was not rebuilt:\n%s", w.logs.String())
	}

	var failure string
	for _, line := range strings.Split(w.logs.String(), "\n") {
		if strings.Contains(line, "Graph not rebuilt") {
			failure = line
		}
	}
	if !strings.Contains(failure, "level=ERROR") || !strings.Contains(failure, ids[broken].String()) {
		t.Errorf("the failure is not an error naming its team: %q", failure)
	}
	if strings.Count(w.logs.String(), "Graph rebuilt") != 1 {
		t.Errorf("want exactly one team logged as rebuilt:\n%s", w.logs.String())
	}
}

// The team on the command line is the team rebuilt. The path from the parsed
// command to the rebuild is one argument, and passing the wrong one would
// turn "this team" into "every team".
func TestExecuteRebuildsTheTeamItWasGiven(t *testing.T) {
	w := newRebuildWorld(t)
	acme, globex := w.team("acme"), w.team("globex")

	err := execute(t.Context(), w.cfg, discardLogger(), w.store, command{name: commandGraph, team: globex})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if w.skills(globex) != 1 || w.skills(acme) != 0 {
		t.Errorf("globex has %d skills in the graph and acme %d, want 1 and 0", w.skills(globex), w.skills(acme))
	}
}

func TestRebuildingATeamThatDoesNotExistFailsTheCommand(t *testing.T) {
	w := newRebuildWorld(t)
	w.team("acme")
	ghost := uuid.New()

	if err := w.run(ghost); err == nil {
		t.Error("a rebuild of no team succeeded")
	}
	if !strings.Contains(w.logs.String(), ghost.String()) {
		t.Errorf("the log does not name the team:\n%s", w.logs.String())
	}
}

// With no model there is nothing to embed with, and a rebuild would drop
// every graph and build none. It is refused before anything is connected to.
func TestRebuildingWithNoModelIsRefusedBeforeAnythingIsDropped(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{FalkorDBURL: deadFalkorURL(t), EmbeddingDimensions: 3}
	err := rebuildGraph(t.Context(), cfg, discardLogger(), nil, uuid.Nil)
	if err == nil || !strings.Contains(err.Error(), "EMBEDDING_MODEL") {
		t.Errorf("err = %v, want the missing setting named", err)
	}
}

func TestRebuildingWithNoFalkorDBFailsTheCommand(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		FalkorDBURL: deadFalkorURL(t), EmbeddingModel: "test-model", EmbeddingDimensions: 3,
		SecretsBackend: config.SecretsBackendStatic,
	}
	err := rebuildGraph(t.Context(), cfg, discardLogger(), nil, uuid.Nil)
	if err == nil || !strings.Contains(err.Error(), "FalkorDB") {
		t.Errorf("err = %v", err)
	}
}

// Not being able to list the teams is not the same as there being none.
func TestRebuildingEveryTeamFailsWhenTheTeamsCannotBeListed(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		FalkorDBURL: liveFalkorURL(t), EmbeddingModel: "test-model", EmbeddingDimensions: 3,
		SecretsBackend: config.SecretsBackendStatic,
	}
	err := rebuildGraph(t.Context(), cfg, discardLogger(), closedStore(t), uuid.Nil)
	if err == nil || !strings.Contains(err.Error(), "listing the teams") {
		t.Errorf("err = %v, want the failed listing", err)
	}
}

func TestACancelledRebuildIsTheCancellation(t *testing.T) {
	w := newRebuildWorld(t)
	w.team("acme")
	w.team("globex")

	ctx, cancel := context.WithCancel(t.Context())
	w.router.onRequest = cancel
	err := rebuildGraph(ctx, w.cfg, slog.New(slog.NewTextHandler(&w.logs, nil)), w.store, uuid.Nil)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %v, want the cancellation and not a count of teams", err)
	}
	if strings.Contains(w.logs.String(), "Graph rebuilt") {
		t.Errorf("a team was reported rebuilt after the command was cancelled:\n%s", w.logs.String())
	}
}
