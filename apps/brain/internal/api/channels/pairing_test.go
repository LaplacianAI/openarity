package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// pairStore is the channels store with a WhatsApp account beside it.
type pairStore struct {
	*fakeStore
	account  *db.GetWhatsAppAccountRow
	err      error
	requests []db.RequestWhatsAppPairingParams
	reads    int
	// sharers are the other channels on the number; held counts the times
	// the number was held for a word change.
	sharers []db.ListWhatsAppSharersRow
	held    int
}

func (p *pairStore) ListWhatsAppSharers(context.Context, uuid.UUID) ([]db.ListWhatsAppSharersRow, error) {
	return p.sharers, nil
}

func (p *pairStore) UpdateChannelOnItsNumber(ctx context.Context, arg db.UpdateChannelParams,
	check func([]db.ListWhatsAppSharersRow) error,
) (db.Channel, error) {
	p.held++
	if err := check(p.sharers); err != nil {
		return db.Channel{}, err
	}
	return p.UpdateChannel(ctx, arg)
}

func (p *pairStore) RequestWhatsAppPairing(_ context.Context, arg db.RequestWhatsAppPairingParams) error {
	if p.err != nil {
		return p.err
	}
	p.requests = append(p.requests, arg)
	return nil
}

func (p *pairStore) GetWhatsAppAccount(_ context.Context, arg db.GetWhatsAppAccountParams) (db.GetWhatsAppAccountRow, error) {
	p.reads++
	if arg.StaleSeconds != leaseStale.Seconds() {
		return db.GetWhatsAppAccountRow{}, errAny
	}
	if p.account == nil {
		return db.GetWhatsAppAccountRow{}, pgx.ErrNoRows
	}
	return *p.account, nil
}

func pairingProviders() Providers {
	return providers{"custom": custom.New(), "whatsapp": whatsapp.New()}
}

func waTeam(t *testing.T) (uuid.UUID, db.Channel, *pairStore) {
	t.Helper()
	teamID := uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "whatsapp", Name: "support"}
	return teamID, ch, &pairStore{fakeStore: &fakeStore{channels: []db.Channel{ch}}}
}

func pairingPath(teamID, channel uuid.UUID) string {
	return channelPath(teamID, channel) + "/pairing"
}

func TestAskingToPairByQRIsAcceptedAndShowsThePairing(t *testing.T) {
	t.Parallel()

	teamID, ch, s := waTeam(t)
	at := time.Unix(1_763_190_000, 0).UTC()
	s.account = &db.GetWhatsAppAccountRow{State: "pairing", PairMethod: new("qr"), UpdatedAt: at, Held: true}
	a := &fakeAuthz{allowed: true}

	rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), a, memberOf(teamID, "admin"),
		http.MethodPost, pairingPath(teamID, ch.ID), `{"method":"qr"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d %s, want 202", rec.Code, rec.Body)
	}
	if len(s.requests) != 1 || s.requests[0].ChannelID != ch.ID || *s.requests[0].Method != "qr" || s.requests[0].Phone != nil {
		t.Fatalf("asked %+v; want a QR pairing of this channel, with no number", s.requests)
	}
	var got pairing
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "pairing" || got.Method != "qr" || !got.Held || got.UpdatedAt == nil || !got.UpdatedAt.Equal(at) {
		t.Errorf("pairing = %+v", got)
	}
	if !slices.Equal(a.asked, []authz.Action{"channel:write"}) {
		t.Errorf("the guard asked for %v, want channel:write", a.asked)
	}
}

func TestAskingForACodeTakesTheNumberAsPeopleWriteIt(t *testing.T) {
	t.Parallel()

	teamID, ch, s := waTeam(t)
	rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, pairingPath(teamID, ch.ID), `{"method":"code","phone":"+91 (98765) 432-10"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	if p := s.requests[0].Phone; p == nil || *p != "919876543210" {
		t.Fatalf("asked a code for %v; want the digits alone", p)
	}
}

func TestAPairingAskedWrongIsRefusedAndAsksNothing(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{}`, `{"method":"sms"}`, `{"method":"code"}`, `{"method":"qr","phone":"+919876543210"}`,
		`{"method":"code","phone":"12345"}`, `{"method":"code","phone":"0987654321"}`,
		`{"method":"code","phone":"+91 98765 abcde"}`, `{"method":"code","phone":"1234567890123456"}`,
		`{"method":"qr","extra":1}`, `{"method":7}`,
	} {
		teamID, ch, s := waTeam(t)
		rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
			http.MethodPost, pairingPath(teamID, ch.ID), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d %s, want 400", body, rec.Code, rec.Body)
		}
		if len(s.requests) != 0 {
			t.Errorf("POST %s asked for a pairing", body)
		}
	}
}

func TestAChannelNotLinkedByPairingIsRefused(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "support"}
	s := &pairStore{fakeStore: &fakeStore{channels: []db.Channel{ch}}}
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
			method, pairingPath(teamID, ch.ID), `{"method":"qr"}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not linked by pairing") {
			t.Errorf("%s = %d %q, want 400 saying so", method, rec.Code, rec.Body)
		}
	}
	if len(s.requests) != 0 || s.reads != 0 {
		t.Errorf("asked %d pairings and read %d accounts for a custom channel", len(s.requests), s.reads)
	}
}

// The guard checked the team in the path, not the channel: another team's
// channel answers as one that does not exist.
func TestAnotherTeamsChannelCannotBePairedOrRead(t *testing.T) {
	t.Parallel()

	_, ch, s := waTeam(t)
	mine := uuid.New()
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(mine, "admin"),
			method, pairingPath(mine, ch.ID), `{"method":"qr"}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", method, rec.Code)
		}
	}
	if len(s.requests) != 0 || s.reads != 0 {
		t.Error("another team's account was touched")
	}
}

// The QR is what lets a phone link the channel: someone who may only read
// channels never sees it.
func TestWithoutChannelWriteTheQRIsNeverRead(t *testing.T) {
	t.Parallel()

	teamID, ch, s := waTeam(t)
	s.account = &db.GetWhatsAppAccountRow{State: "pairing", PairQr: new("2@secret")}
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: false}, memberOf(teamID, "member"),
			method, pairingPath(teamID, ch.ID), `{"method":"qr"}`)
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "2@secret") {
			t.Errorf("%s = %d %q, want 403 and no QR", method, rec.Code, rec.Body)
		}
	}
	if len(s.requests) != 0 || s.reads != 0 {
		t.Errorf("asked %d pairings and read %d accounts for a caller refused", len(s.requests), s.reads)
	}
}

func TestReadingAPairingShowsWhatTheRoleWrote(t *testing.T) {
	t.Parallel()

	teamID, ch, s := waTeam(t)
	s.account = &db.GetWhatsAppAccountRow{State: "connected", Phone: new("919876543210"), Held: true, UpdatedAt: time.Now()}
	rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodGet, pairingPath(teamID, ch.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	var fields map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for k := range fields {
		if !slices.Contains([]string{"state", "method", "qr", "code", "phone", "error", "held", "updated_at"}, k) {
			t.Errorf("the pairing carries %q, which is not in its contract", k)
		}
	}
	if fields["state"] != "connected" || fields["phone"] != "919876543210" || fields["held"] != true {
		t.Errorf("pairing = %v", fields)
	}

	s.account = nil
	rec = callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodGet, pairingPath(teamID, ch.ID), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"unpaired"`) || !strings.Contains(rec.Body.String(), `"held":false`) {
		t.Errorf("a channel nobody has looked at = %d %s; want unpaired, held by nobody", rec.Code, rec.Body)
	}
}

func TestAPairingTheStoreCannotWriteIsAServerError(t *testing.T) {
	t.Parallel()

	teamID, ch, s := waTeam(t)
	s.err = errAny
	rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, pairingPath(teamID, ch.ID), `{"method":"qr"}`)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), errAny.Error()) {
		t.Errorf("POST = %d %q, want 500 telling nothing", rec.Code, rec.Body)
	}
}

func TestAChannelSaysWhetherItIsLinkedByPairing(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	wa := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "whatsapp", Name: "wa"}
	web := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "web"}
	s := &pairStore{fakeStore: &fakeStore{channels: []db.Channel{wa, web}}}
	for _, ch := range []db.Channel{wa, web} {
		rec := callWith(t, pairingProviders(), "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
			http.MethodPatch, channelPath(teamID, ch.ID), `{"reply_mode":"final"}`)
		want := `"can_pair":` + map[bool]string{true: "true", false: "false"}[ch.Provider == "whatsapp"]
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s channel = %d %s; want %s", ch.Provider, rec.Code, rec.Body, want)
		}
	}
}
