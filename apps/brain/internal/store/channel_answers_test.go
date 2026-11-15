package store

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func TestAChannelAnswersEverywhereAndTellsStrangersUnlessMadeOtherwise(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	team := mustCreate(t, s, "answers "+uuid.NewString()[:8])

	plain, err := s.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: team.ID, Provider: "whatsapp", Name: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.AnswersIn != "everywhere" || !plain.NotifyStrangers {
		t.Errorf("a channel made plainly = %q, notices %v; want everywhere, on", plain.AnswersIn, plain.NotifyStrangers)
	}

	groups := "groups"
	mine, err := s.CreateChannel(t.Context(), db.CreateChannelParams{
		TeamID: team.ID, Provider: "whatsapp", Name: "mine", AnswersIn: &groups,
		NotifyStrangers: pgtype.Bool{Bool: false, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mine.AnswersIn != "groups" || mine.NotifyStrangers {
		t.Errorf("a channel made for a person's number = %q, notices %v; want groups, off", mine.AnswersIn, mine.NotifyStrangers)
	}

	direct := "direct"
	changed, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{ID: mine.ID, TeamID: team.ID, AnswersIn: &direct})
	if err != nil || changed.AnswersIn != "direct" || changed.NotifyStrangers {
		t.Errorf("UpdateChannel = %+v, %v; want direct, notices still off", changed, err)
	}

	nowhere := "nowhere"
	if _, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{ID: mine.ID, TeamID: team.ID, AnswersIn: &nowhere}); err == nil {
		t.Error("the table took a place no channel answers")
	}
}

// The role reads a channel's settings from its lease, at every renewal.
func TestALeaseCarriesTheChannelsSettings(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	team := mustCreate(t, s, "answers-lease "+uuid.NewString()[:8])
	groups := "groups"
	ch, err := s.CreateChannel(t.Context(), db.CreateChannelParams{
		TeamID: team.ID, Provider: "whatsapp", Name: "mine", AnswersIn: &groups,
		NotifyStrangers: pgtype.Bool{Bool: false, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWhatsAppAccounts(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := leased(t, s, "a", 60, ch.ID)[ch.ID]
	if r.AnswersIn != "groups" || r.NotifyStrangers {
		t.Errorf("the lease says %q, notices %v; want groups, off", r.AnswersIn, r.NotifyStrangers)
	}
}

const beforeAnswersIn = 20261115120000

func TestWhereAChannelAnswersRollsBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	team := mustCreate(t, s, "answers-down")
	mustCreateChannel(t, s, team.ID, "whatsapp", "kept")

	downTo(t, s, beforeAnswersIn)
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'channels' AND column_name = 'answers_in'`); n != 0 {
		t.Error("rolled back, channels still has answers_in")
	}
	if n := count(t, s, `SELECT count(*) FROM channels WHERE name = 'kept'`); n != 1 {
		t.Errorf("rolling back lost the channel: %d left", n)
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after Rollback: %v", err)
	}
}
