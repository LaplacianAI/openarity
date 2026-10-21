package store

import (
	"testing"

	"github.com/google/uuid"
)

func listDelegate(t *testing.T, s *Store, teamID, agentID, delegateID uuid.UUID) error {
	t.Helper()

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO agent_delegates (agent_id, delegate_id, team_id)
		VALUES ($1, $2, $3)`, agentID, delegateID, teamID)
	return err
}

func delegationOf(t *testing.T, s *Store, agentID uuid.UUID) string {
	t.Helper()

	var d string
	if err := s.pool.QueryRow(t.Context(), `SELECT delegation FROM agents WHERE id = $1`, agentID).Scan(&d); err != nil {
		t.Fatalf("read delegation: %v", err)
	}
	return d
}

// --- the setting ---

func TestAnAgentDelegatesToTheTeamByDefault(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID

	if got := delegationOf(t, s, insertAgent(t, s, team, "triage")); got != "team" {
		t.Errorf("delegation = %q, want team", got)
	}
}

func TestEveryKnownDelegationIsAccepted(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	a := insertAgent(t, s, team, "triage")

	for _, d := range []string{"team", "listed", "none"} {
		if _, err := s.pool.Exec(t.Context(), `UPDATE agents SET delegation = $2 WHERE id = $1`, a, d); err != nil {
			t.Errorf("%s refused: %v", d, err)
		}
	}
}

func TestAnUnknownDelegationIsRefused(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	a := insertAgent(t, s, team, "triage")

	for _, d := range []string{"some", "", "TEAM"} {
		_, err := s.pool.Exec(t.Context(), `UPDATE agents SET delegation = $2 WHERE id = $1`, a, d)
		wantConstraint(t, err, "23514", "agents_delegation_known")
	}
}

// --- the listing ---

func TestAnAgentCanListAnotherInItsTeam(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	a, b := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "clerk")

	if err := listDelegate(t, s, team, a, b); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := listDelegate(t, s, team, a, b); err == nil {
		t.Error("the same pair was listed twice")
	}
}

func TestAnAgentCannotListItself(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	a := insertAgent(t, s, team, "lead")

	wantConstraint(t, listDelegate(t, s, team, a, a), "23514", "agent_delegates_not_self")
}

// Both ends are in the row's team, whichever end is the stranger.
func TestADelegateMustBeInTheSameTeam(t *testing.T) {
	s := queryStore(t)
	mine, theirs := mustCreate(t, s, "platform").ID, mustCreate(t, s, "billing").ID
	a := insertAgent(t, s, mine, "lead")
	stranger := insertAgent(t, s, theirs, "clerk")

	wantConstraint(t, listDelegate(t, s, mine, a, stranger), "23503", "agent_delegates_delegate_in_team")
	wantConstraint(t, listDelegate(t, s, theirs, a, stranger), "23503", "agent_delegates_agent_in_team")
}

// Deleting either end removes the listing and nothing else: an agent is
// never kept alive by being somebody's delegate.
func TestDeletingEitherEndRemovesTheListing(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	lead, clerk, other := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "clerk"), insertAgent(t, s, team, "other")
	for _, p := range [][2]uuid.UUID{{lead, clerk}, {other, lead}, {other, clerk}} {
		if err := listDelegate(t, s, team, p[0], p[1]); err != nil {
			t.Fatalf("list: %v", err)
		}
	}

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM agents WHERE id = $1`, lead); err != nil {
		t.Fatalf("delete an agent that lists and is listed: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM agent_delegates`); n != 1 {
		t.Errorf("%d listings left, want only other→clerk", n)
	}
	if n := count(t, s, `SELECT count(*) FROM agents WHERE team_id = $1`, team); n != 2 {
		t.Errorf("%d agents left, want clerk and other", n)
	}
}

// --- the graph hears about it ---

func TestListingADelegateEnqueuesTheAgent(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	lead, clerk := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "clerk")
	clearOutbox(t, s)

	if err := listDelegate(t, s, team, lead, clerk); err != nil {
		t.Fatalf("list: %v", err)
	}
	got := outbox(t, s)
	if len(got) != 1 || got[0] != (outboxRow{TeamID: team, Entity: "agent", EntityID: lead}) {
		t.Errorf("outbox = %v, want the listing agent once", got)
	}
}

func TestUnlistingADelegateEnqueuesTheAgent(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	lead, clerk := insertAgent(t, s, team, "lead"), insertAgent(t, s, team, "clerk")
	if err := listDelegate(t, s, team, lead, clerk); err != nil {
		t.Fatalf("list: %v", err)
	}
	clearOutbox(t, s)

	exec(t, s, `DELETE FROM agent_delegates`)
	got := outbox(t, s)
	if len(got) != 1 || got[0] != (outboxRow{TeamID: team, Entity: "agent", EntityID: lead}) {
		t.Errorf("outbox = %v, want the listing agent once", got)
	}
}

// Changing the setting is an update of the agent, which already enqueues it;
// the projector draws the edges only for listed, so the switch must reach it.
func TestChangingTheSettingEnqueuesTheAgent(t *testing.T) {
	s := queryStore(t)
	team := mustCreate(t, s, "platform").ID
	lead := insertAgent(t, s, team, "lead")
	clearOutbox(t, s)

	if _, err := s.pool.Exec(t.Context(), `UPDATE agents SET delegation = 'listed' WHERE id = $1`, lead); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := outbox(t, s); len(got) == 0 || got[0].EntityID != lead {
		t.Errorf("outbox = %v, want the agent", got)
	}
}
