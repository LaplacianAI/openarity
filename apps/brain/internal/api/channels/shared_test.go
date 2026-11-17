package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// teamAuthz allows channel:write in the caller's own team only, so a test can
// tell a sharer the caller may edit from one it may not.
type teamAuthz struct{ team uuid.UUID }

func (teamAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (a teamAuthz) Can(_ context.Context, _ *auth.User, _ authz.Action, r authz.Resource) (bool, error) {
	return r.TeamID == a.team, nil
}

func (teamAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("a channels route used CanInAnyTeam")
}

// sharedTeam is a WhatsApp channel, linked, whose number another channel in
// another team links too.
func sharedTeam(t *testing.T, word, other *string) (uuid.UUID, db.Channel, *pairStore, uuid.UUID) {
	t.Helper()
	teamID, ch, s := waTeam(t)
	ch.TriggerWord = word
	s.channels[0] = ch
	s.account = &db.GetWhatsAppAccountRow{State: "connected", Phone: new("918939512050"), UpdatedAt: time.Now(), Held: true}
	otherTeam := uuid.New()
	s.sharers = []db.ListWhatsAppSharersRow{{ChannelID: uuid.New(), TeamID: otherTeam, Name: "shrijeeth-personal", TriggerWord: other}}
	return teamID, ch, s, otherTeam
}

func patchWord(t *testing.T, s *pairStore, teamID, channel uuid.UUID, body string) (int, string) {
	t.Helper()
	rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPatch, channelPath(teamID, channel), body)
	return rec.Code, rec.Body.String()
}

// On a number another channel links, a word must be something, and nothing a
// message for the other could hold too: refused, and nothing written.
func TestAWordOnASharedNumberMustBeItsOwn(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		body string
		says string
	}{
		"no word":          {`{"trigger_word":""}`, "needs a trigger word"},
		"the other's word": {`{"trigger_word":"@fduv"}`, "answers @Fduv"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			teamID, ch, s, _ := sharedTeam(t, new("@Dhars"), new("@Fduv"))
			code, body := patchWord(t, s, teamID, ch.ID, c.body)
			if code != http.StatusConflict || !strings.Contains(body, c.says) || !strings.Contains(body, "shrijeeth-personal") {
				t.Fatalf("PATCH = %d %q; want 409 naming the other channel and why", code, body)
			}
			if got := s.channels[0].TriggerWord; got == nil || *got != "@Dhars" {
				t.Errorf("the word is %v; a refused change wrote it", got)
			}
		})
	}
}

func TestAWordOfItsOwnOnASharedNumberIsTaken(t *testing.T) {
	t.Parallel()
	teamID, ch, s, _ := sharedTeam(t, nil, new("@Fduv"))
	if code, body := patchWord(t, s, teamID, ch.ID, `{"trigger_word":"@Dharshini"}`); code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", code, body)
	}
	if got := s.channels[0].TriggerWord; got == nil || *got != "@Dharshini" || s.held != 1 {
		t.Errorf("word %v, number held %d times; want it written with the number held", got, s.held)
	}
}

// A number of its own needs no word, and a change that is not to the word
// does not hold the number at all.
func TestOnlyAWordChangeOnASharedNumberIsChecked(t *testing.T) {
	t.Parallel()
	teamID, ch, s := waTeam(t)
	if code, body := patchWord(t, s, teamID, ch.ID, `{"trigger_word":""}`); code != http.StatusOK {
		t.Fatalf("clearing the word on a number of its own = %d %s", code, body)
	}
	teamID, ch, s, _ = sharedTeam(t, new("@Dhars"), nil)
	if code, body := patchWord(t, s, teamID, ch.ID, `{"answers_in":"direct"}`); code != http.StatusOK || s.held != 0 {
		t.Errorf("PATCH = %d %s, number held %d times; want another field changed as before", code, body, s.held)
	}
}

func readPairing(t *testing.T, s *pairStore, a api.Authorizer, teamID, channel uuid.UUID) pairing {
	t.Helper()
	mux := http.NewServeMux()
	New(discardLogger(), s, a, newFakeSecrets(), pairingProviders(), "", publicOnly, offline(t), nil, s).
		Register(mux, api.NewGuard(channelRoutes(t), a, liveTeams{}, discardLogger()))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, pairingPath(teamID, channel), nil)
	req = req.WithContext(auth.WithUser(req.Context(), memberOf(teamID, "admin")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	var got pairing
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// A linked channel says who shares its number, whether the caller may set
// each one's word, and whether it waits for words before it answers.
func TestALinkedChannelSaysWhoSharesItsNumber(t *testing.T) {
	t.Parallel()
	teamID, ch, s, _ := sharedTeam(t, new("@Dhars"), nil)

	got := readPairing(t, s, teamAuthz{team: teamID}, teamID, ch.ID)
	if len(got.SharedWith) != 1 || got.SharedWith[0].Name != "shrijeeth-personal" || got.SharedWith[0].Editable {
		t.Fatalf("shared_with = %+v; want the other channel, not editable from another team", got.SharedWith)
	}
	if !got.NeedsTriggerWord {
		t.Error("the other channel has no word, and this one was said to answer")
	}

	s.sharers[0].TriggerWord = new("@Fduv")
	got = readPairing(t, s, &fakeAuthz{allowed: true}, teamID, ch.ID)
	if got.NeedsTriggerWord || !got.SharedWith[0].Editable {
		t.Errorf("pairing = %+v; want it answering, and the other editable for one who may", got)
	}
}

func TestANumberOfItsOwnSaysNothingOfSharing(t *testing.T) {
	t.Parallel()
	teamID, ch, s, _ := sharedTeam(t, nil, nil)
	s.sharers = nil
	if got := readPairing(t, s, &fakeAuthz{allowed: true}, teamID, ch.ID); got.SharedWith != nil || got.NeedsTriggerWord {
		t.Errorf("pairing = %+v; want nothing about sharing", got)
	}
}
