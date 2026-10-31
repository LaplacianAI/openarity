package store

import (
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Every agent that existed before the grant, and every one created without
// it, stays off the web: the column defaults to false and nothing sets it.
func TestAnAgentIsNotOnTheWebUntilGranted(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	a := mustCreateAgent(t, s, team.ID, "triage")
	if a.Web {
		t.Error("an agent created without the grant is on the web")
	}
	got, err := s.GetAgent(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Web {
		t.Error("an agent read back without the grant is on the web")
	}
}

// A replace is whole, as delegation is: the grant is what the replace says,
// kept when it says so and taken away when it does not.
func TestAGrantedAgentKeepsItsWebThroughAnUpdate(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform")

	p := agentParams(team.ID, "researcher")
	p.Web = true
	a, err := s.CreateAgent(t.Context(), p)
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if !a.Web {
		t.Fatal("CreateAgent with the grant returned an agent off the web")
	}

	update := func(web bool) db.Agent {
		t.Helper()
		got, err := s.UpdateAgent(t.Context(), db.UpdateAgentParams{
			ID: a.ID, Name: a.Name, Description: "Reads the web", ModelName: a.ModelName,
			MaxTokens: a.MaxTokens, Pattern: a.Pattern, MaxSteps: a.MaxSteps,
			Delegation: a.Delegation, Web: web,
		})
		if err != nil {
			t.Fatalf("UpdateAgent: %v", err)
		}
		return got
	}

	if got := update(true); !got.Web || got.Description != "Reads the web" {
		t.Errorf("web %v description %q after a replace that kept the grant", got.Web, got.Description)
	}
	if got := update(false); got.Web {
		t.Error("a replace without the grant left the agent on the web")
	}
}
