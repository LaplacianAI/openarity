package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A channel has no word unless made with one; an empty word is none; a
// change of something else keeps it, and an empty one takes it away.
func TestAChannelsTriggerWordIsSetKeptAndTakenAway(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	team := mustCreate(t, s, "trigger "+uuid.NewString()[:8])

	for name, word := range map[string]*string{"none": nil, "empty": new("")} {
		ch, err := s.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: team.ID, Provider: "whatsapp", Name: name, TriggerWord: word})
		if err != nil {
			t.Fatal(err)
		}
		if ch.TriggerWord != nil {
			t.Errorf("made with %s = %q; want no word", name, *ch.TriggerWord)
		}
	}

	ch, err := s.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: team.ID, Provider: "whatsapp", Name: "mine", TriggerWord: new("@Fduv")})
	if err != nil || ch.TriggerWord == nil || *ch.TriggerWord != "@Fduv" {
		t.Fatalf("CreateChannel = %v, %v; want @Fduv", ch.TriggerWord, err)
	}
	kept, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{ID: ch.ID, TeamID: team.ID, AnswersIn: new("groups")})
	if err != nil || kept.TriggerWord == nil || *kept.TriggerWord != "@Fduv" {
		t.Fatalf("a change of answers_in left %v, %v; want the word kept", kept.TriggerWord, err)
	}
	gone, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{ID: ch.ID, TeamID: team.ID, TriggerWord: new("")})
	if err != nil || gone.TriggerWord != nil {
		t.Fatalf("an empty word left %v, %v; want none", gone.TriggerWord, err)
	}
	if _, err := s.UpdateChannel(t.Context(), db.UpdateChannelParams{ID: ch.ID, TeamID: team.ID, TriggerWord: new(strings.Repeat("a", 33))}); err == nil {
		t.Error("the table took a word of 33 characters")
	}
}

// The role reads the word from its lease, with the other settings.
func TestALeaseCarriesTheTriggerWord(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	team := mustCreate(t, s, "trigger-lease "+uuid.NewString()[:8])
	ch, err := s.CreateChannel(t.Context(), db.CreateChannelParams{TeamID: team.ID, Provider: "whatsapp", Name: "mine", TriggerWord: new("@Fduv")})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWhatsAppAccounts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r := leased(t, s, "a", 60, ch.ID)[ch.ID]; r.TriggerWord == nil || *r.TriggerWord != "@Fduv" {
		t.Errorf("the lease says %v; want @Fduv", r.TriggerWord)
	}
}

const beforeTriggerWord = 20261116090000

func TestATriggerWordRollsBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	team := mustCreate(t, s, "trigger-down")
	mustCreateChannel(t, s, team.ID, "whatsapp", "kept")

	downTo(t, s, beforeTriggerWord)
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'channels' AND column_name = 'trigger_word'`); n != 0 {
		t.Error("rolled back, channels still has trigger_word")
	}
	if n := count(t, s, `SELECT count(*) FROM channels WHERE name = 'kept'`); n != 1 {
		t.Errorf("rolling back lost the channel: %d left", n)
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after Rollback: %v", err)
	}
}
