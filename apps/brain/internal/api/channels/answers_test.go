package channels

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/telegram"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func answersProviders() Providers {
	return providers{"custom": custom.New(), "telegram": telegram.New(), "whatsapp": whatsapp.New()}
}

func decodeChannel(t *testing.T, body []byte) channel {
	t.Helper()
	var c channel
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("not a channel: %v\n%s", err, body)
	}
	return c
}

// A number that is a person's own is made to answer in groups alone, and to
// tell strangers nothing, at the moment it is connected.
func TestAChannelIsMadeWithWhereItAnswersAndItsNotices(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &pairStore{fakeStore: &fakeStore{}}
	rec := callWith(t, answersProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, "/teams/"+teamID.String()+"/channels",
		`{"provider":"whatsapp","name":"mine","answers_in":"groups","notify_strangers":false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	arg := s.created[0]
	if arg.AnswersIn == nil || *arg.AnswersIn != "groups" || !arg.NotifyStrangers.Valid || arg.NotifyStrangers.Bool {
		t.Fatalf("created %+v; want groups, notices off", arg)
	}
	if c := decodeChannel(t, rec.Body.Bytes()); c.AnswersIn != "groups" || c.NotifyStrangers {
		t.Errorf("the channel = %+v; want it to say groups, notices off", c)
	}
}

// Left out, a channel answers everywhere and tells strangers, as before.
func TestAChannelMadeWithoutThemAnswersEverywhere(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &pairStore{fakeStore: &fakeStore{}}
	rec := callWith(t, answersProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, "/teams/"+teamID.String()+"/channels", `{"provider":"whatsapp","name":"bot"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	if arg := s.created[0]; arg.AnswersIn != nil || arg.NotifyStrangers.Valid {
		t.Errorf("created %+v; want both left to the defaults", arg)
	}
	if c := decodeChannel(t, rec.Body.Bytes()); c.AnswersIn != "everywhere" || !c.NotifyStrangers {
		t.Errorf("the channel = %+v; want everywhere, notices on", c)
	}
}

// Refused before anything is made, and before the provider is asked: a
// Telegram bot's token is never checked for a request that cannot stand.
func TestAChannelAskedWrongIsRefusedBeforeTheProviderIsAsked(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"a place it cannot answer":     `{"provider":"telegram","name":"x","credentials":{"bot_token":"1:a"},"answers_in":"nowhere"}`,
		"notices it cannot send":       `{"provider":"custom","name":"x","notify_strangers":true}`,
		"a place that is not a string": `{"provider":"whatsapp","name":"x","answers_in":3}`,
	} {
		teamID := uuid.New()
		s := &pairStore{fakeStore: &fakeStore{}}
		rec := callWith(t, answersProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
			http.MethodPost, "/teams/"+teamID.String()+"/channels", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: POST = %d %s, want 400", name, rec.Code, rec.Body)
		}
		if len(s.created) != 0 {
			t.Errorf("%s: a channel was made", name)
		}
	}
}

func TestWhereAChannelAnswersCanBeChanged(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "whatsapp", Name: "mine", AnswersIn: "everywhere"}
	s := &pairStore{fakeStore: &fakeStore{channels: []db.Channel{ch}}}
	rec := callWith(t, answersProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPatch, channelPath(teamID, ch.ID), `{"answers_in":"direct"}`)
	if rec.Code != http.StatusOK || decodeChannel(t, rec.Body.Bytes()).AnswersIn != "direct" {
		t.Fatalf("PATCH = %d %s, want direct", rec.Code, rec.Body)
	}

	rec = callWith(t, answersProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPatch, channelPath(teamID, ch.ID), `{"answers_in":"Groups"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "everywhere") {
		t.Errorf("PATCH Groups = %d %q, want 400 naming the places", rec.Code, rec.Body)
	}
	if got := s.channels[0].AnswersIn; got != "direct" {
		t.Errorf("a refused change left %q, want direct", got)
	}
}
