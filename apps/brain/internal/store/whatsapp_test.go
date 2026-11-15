package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// waChannel is a live team's WhatsApp channel, with its account row.
func waChannel(t *testing.T, s *Store) db.Channel {
	t.Helper()
	team := mustCreate(t, s, "wa "+uuid.NewString()[:8])
	ch := mustCreateChannel(t, s, team.ID, "whatsapp", "number")
	if err := s.EnsureWhatsAppAccounts(t.Context()); err != nil {
		t.Fatalf("EnsureWhatsAppAccounts: %v", err)
	}
	return ch
}

// leased is the channels of want that holder's lease returned, since other
// tests' accounts share the table.
func leased(t *testing.T, s *Store, holder string, stale float64, want ...uuid.UUID) map[uuid.UUID]db.LeaseWhatsAppAccountsRow {
	t.Helper()
	rows, err := s.LeaseWhatsAppAccounts(t.Context(), db.LeaseWhatsAppAccountsParams{Holder: holder, StaleSeconds: stale})
	if err != nil {
		t.Fatalf("LeaseWhatsAppAccounts: %v", err)
	}
	mine := map[uuid.UUID]bool{}
	for _, id := range want {
		mine[id] = true
	}
	got := map[uuid.UUID]db.LeaseWhatsAppAccountsRow{}
	for _, r := range rows {
		if mine[r.ChannelID] {
			got[r.ChannelID] = r
		}
	}
	return got
}

func TestOnlyAWhatsAppChannelHasAnAccount(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	team := mustCreate(t, s, "wa-only")
	tg := mustCreateChannel(t, s, team.ID, "telegram", "bot")
	wa := waChannel(t, s)
	if err := s.EnsureWhatsAppAccounts(t.Context()); err != nil { // twice changes nothing
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM whatsapp_accounts WHERE channel_id = '`+tg.ID.String()+`'`); n != 0 {
		t.Error("a Telegram channel has a WhatsApp account")
	}
	if n := count(t, s, `SELECT count(*) FROM whatsapp_accounts WHERE channel_id = '`+wa.ID.String()+`'`); n != 1 {
		t.Errorf("the WhatsApp channel has %d accounts; want one", n)
	}
}

// A lease held and renewed is not another's; one whose holder stopped
// renewing is taken over.
func TestALeaseIsOnesUntilItGoesStale(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	ch := waChannel(t, s)

	if got := leased(t, s, "a", 60, ch.ID); len(got) != 1 {
		t.Fatalf("a took %d; want the channel", len(got))
	}
	if got := leased(t, s, "b", 60, ch.ID); len(got) != 0 {
		t.Fatal("b took a lease a holds and renews")
	}
	if got := leased(t, s, "a", 60, ch.ID); len(got) != 1 {
		t.Fatal("a could not renew its own lease")
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE whatsapp_accounts SET heartbeat_at = now() - interval '2 minutes' WHERE channel_id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	if got := leased(t, s, "b", 60, ch.ID); len(got) != 1 {
		t.Fatal("b did not take over a stale lease")
	}
	if got := leased(t, s, "a", 60, ch.ID); len(got) != 0 {
		t.Fatal("a still renews a lease b took over")
	}
}

// Two roles leasing at once: each channel goes to exactly one.
func TestTwoRolesAtOnceNeverHoldOneChannel(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	var ids []uuid.UUID
	for range 8 {
		ids = append(ids, waChannel(t, s).ID)
	}
	var mu sync.Mutex
	holders := map[uuid.UUID][]string{}
	var wg sync.WaitGroup
	for _, h := range []string{"a", "b", "c"} {
		wg.Go(func() {
			got := leased(t, s, h, 60, ids...)
			mu.Lock()
			defer mu.Unlock()
			for id := range got {
				holders[id] = append(holders[id], h)
			}
		})
	}
	wg.Wait()
	for _, id := range ids {
		if len(holders[id]) != 1 {
			t.Errorf("channel %s held by %v; want exactly one", id, holders[id])
		}
	}
}

func TestADeletedTeamsAccountIsNotLeased(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	ch := waChannel(t, s)
	softDelete(t, s, ch.TeamID)
	if got := leased(t, s, "a", 60, ch.ID); len(got) != 0 {
		t.Fatal("a deleted team's channel was leased")
	}
}

func TestReleasingLetsAnotherTakeAtOnce(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	ch := waChannel(t, s)
	holder := "rel-" + uuid.NewString()
	leased(t, s, holder, 60, ch.ID)
	if err := s.ReleaseWhatsAppAccounts(t.Context(), holder); err != nil {
		t.Fatal(err)
	}
	if got := leased(t, s, "other", 60, ch.ID); len(got) != 1 {
		t.Fatal("a released lease was not free to take")
	}
}

// Only the holder writes the pairing; a request it has taken stays taken,
// and linking records the number and clears what pairing showed.
func TestPairingIsWrittenByItsHolderAlone(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	ch := waChannel(t, s)
	leased(t, s, "a", 60, ch.ID)
	if _, err := s.pool.Exec(t.Context(), `UPDATE whatsapp_accounts SET pair_seq = 3, pair_method = 'qr' WHERE channel_id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}

	qr := "2@code"
	if n, err := s.SetWhatsAppPairing(t.Context(), db.SetWhatsAppPairingParams{ChannelID: ch.ID, Holder: "b", Seq: 3, Qr: &qr}); err != nil || n != 0 {
		t.Fatalf("another role's write = %d, %v; want nothing written", n, err)
	}
	if n, err := s.SetWhatsAppPairing(t.Context(), db.SetWhatsAppPairingParams{ChannelID: ch.ID, Holder: "a", Seq: 3, Qr: &qr}); err != nil || n != 1 {
		t.Fatalf("the holder's write = %d, %v", n, err)
	}
	if n, _ := s.SetWhatsAppPairing(t.Context(), db.SetWhatsAppPairingParams{ChannelID: ch.ID, Holder: "a", Seq: 1}); n != 1 {
		t.Fatal("a later write of the same pairing was refused")
	}
	r := leased(t, s, "a", 60, ch.ID)[ch.ID]
	if r.PairSeen != 3 {
		t.Errorf("pair_seen = %d; want 3, never moved back", r.PairSeen)
	}

	jid, phone := "919000000001@s.whatsapp.net", "919000000001"
	if n, err := s.SetWhatsAppLinked(t.Context(), db.SetWhatsAppLinkedParams{ChannelID: ch.ID, Holder: "a", Jid: &jid, Phone: &phone}); err != nil || n != 1 {
		t.Fatalf("SetWhatsAppLinked = %d, %v", n, err)
	}
	var state string
	var leftQR *string
	if err := s.pool.QueryRow(t.Context(), `SELECT state, pair_qr FROM whatsapp_accounts WHERE channel_id = $1`, ch.ID).Scan(&state, &leftQR); err != nil {
		t.Fatal(err)
	}
	if state != "connected" || leftQR != nil {
		t.Errorf("state %q, QR %v; want connected and the QR cleared", state, leftQR)
	}
	jids, err := s.ListWhatsAppJIDs(t.Context())
	if err != nil || !containsString(jids, jid) {
		t.Errorf("ListWhatsAppJIDs = %v, %v; want the linked device named", jids, err)
	}
}

// The table refuses what no code should write: a connected account with no
// device, and a code pairing with no number to give the code for.
func TestAnAccountsRulesAreTheTables(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	ch := waChannel(t, s)
	for name, q := range map[string]string{
		"connected, no device": `UPDATE whatsapp_accounts SET state = 'connected' WHERE channel_id = $1`,
		"a code, no number":    `UPDATE whatsapp_accounts SET pair_method = 'code' WHERE channel_id = $1`,
		"an unknown state":     `UPDATE whatsapp_accounts SET state = 'linked' WHERE channel_id = $1`,
	} {
		if _, err := s.pool.Exec(t.Context(), q, ch.ID); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestAPartSentIsRememberedOnce(t *testing.T) {
	t.Parallel()
	s := queryStore(t)
	ch := waChannel(t, s)
	_, err := s.WhatsAppPartSent(t.Context(), db.WhatsAppPartSentParams{ChannelID: ch.ID, PartID: "run:0"})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("an unsent part = %v; want no row", err)
	}
	for _, id := range []string{"M1", "M2"} {
		if err := s.RecordWhatsAppPart(t.Context(), db.RecordWhatsAppPartParams{ChannelID: ch.ID, PartID: "run:0", MessageID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.WhatsAppPartSent(t.Context(), db.WhatsAppPartSentParams{ChannelID: ch.ID, PartID: "run:0"}); err != nil || got != "M1" {
		t.Fatalf("WhatsAppPartSent = %q, %v; want the first send kept", got, err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE whatsapp_sent SET sent_at = now() - interval '2 days' WHERE channel_id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneWhatsAppSent(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WhatsAppPartSent(t.Context(), db.WhatsAppPartSentParams{ChannelID: ch.ID, PartID: "run:0"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a part sent two days ago is still kept: %v", err)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

const beforeWhatsAppAccounts = 20261115090000

func TestTheWhatsAppAccountsRollBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	waChannel(t, s)
	if _, err := s.pool.Exec(t.Context(), `CREATE TABLE whatsmeow.whatsmeow_device (jid text)`); err != nil {
		t.Fatal(err)
	}

	downTo(t, s, beforeWhatsAppAccounts)
	for _, q := range []string{
		`SELECT count(*) FROM information_schema.tables WHERE table_name IN ('whatsapp_accounts', 'whatsapp_sent')`,
		`SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'whatsmeow'`,
	} {
		if n := count(t, s, q); n != 0 {
			t.Errorf("rolled back, %q still finds %d", q, n)
		}
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after Rollback: %v", err)
	}
}
