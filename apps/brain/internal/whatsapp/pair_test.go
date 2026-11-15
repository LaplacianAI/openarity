package whatsapp

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func asked(ch uuid.UUID, method, phone string, seq, seen int64) db.LeaseWhatsAppAccountsRow {
	r := row(ch, nil)
	r.PairSeq, r.PairSeen, r.PairMethod = seq, seen, &method
	if phone != "" {
		r.PairPhone = &phone
	}
	return r
}

func TestAQRPairingShowsEachCodeAndTakesTheRequestOnce(t *testing.T) {
	f := newFixture(t, newDevices())
	ch := uuid.New()
	f.store.rows = []db.LeaseWhatsAppAccountsRow{asked(ch, "qr", "", 4, 3)}
	f.a.lease(t.Context())
	cli := f.client(t, 0)
	cli.qr <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "2@first"}
	cli.qr <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "2@second"}

	eventually(t, "both codes shown", func() bool { return len(f.store.snapshot().pairing) == 3 })
	p := f.store.snapshot().pairing
	if p[0].Seq != 4 || p[0].Qr != nil || *p[1].Qr != "2@first" || *p[2].Qr != "2@second" {
		t.Fatalf("pairing writes %+v; want the request taken, then each QR", p)
	}
	if f.devices.news != 1 || cli.connects != 1 {
		t.Errorf("new devices %d, connects %d; want one of each", f.devices.news, cli.connects)
	}

	f.a.lease(t.Context()) // the same request, still unseen in this fake
	if f.devices.news != 1 {
		t.Error("a pairing in progress was started again")
	}
}

func TestAQRThatExpiresUnpairsSayingSo(t *testing.T) {
	f := newFixture(t, newDevices())
	f.store.rows = []db.LeaseWhatsAppAccountsRow{asked(uuid.New(), "qr", "", 1, 0)}
	f.a.lease(t.Context())
	f.client(t, 0).qr <- whatsmeow.QRChannelTimeout
	close(f.client(t, 0).qr)

	eventually(t, "unpaired", func() bool { return len(f.store.snapshot().unpaired) == 1 })
	if why := *f.store.snapshot().unpaired[0].Error; !strings.Contains(why, "expired before it was scanned") {
		t.Fatalf("unpaired saying %q", why)
	}
}

func TestAPairingByCodeShowsTheCodeForTheNumberAsked(t *testing.T) {
	f := newFixture(t, newDevices())
	f.prepare = func(c *fakeClient) { c.pairCode = "ABCD-1234" }
	f.store.rows = []db.LeaseWhatsAppAccountsRow{asked(uuid.New(), "code", "919876543210", 1, 0)}
	f.a.lease(t.Context())

	eventually(t, "the code shown", func() bool { return len(f.store.snapshot().pairing) == 2 })
	if p := f.store.snapshot().pairing[1]; p.Code == nil || *p.Code != "ABCD-1234" {
		t.Fatalf("pairing %+v; want the code", p)
	}
	if got := f.client(t, 0).pairPhone; got != "919876543210" {
		t.Errorf("asked a code for %q", got)
	}
	// The fake clock does not wait: the code expires at once, unentered.
	eventually(t, "the code expired", func() bool { return len(f.store.snapshot().unpaired) == 1 })
	if why := *f.store.snapshot().unpaired[0].Error; !strings.Contains(why, "expired before it was entered") {
		t.Errorf("unpaired saying %q", why)
	}
}

func TestANumberWhatsAppGivesNoCodeForIsSaid(t *testing.T) {
	f := newFixture(t, newDevices())
	f.prepare = func(c *fakeClient) { c.pairErr = errors.New("bad number") }
	f.store.rows = []db.LeaseWhatsAppAccountsRow{asked(uuid.New(), "code", "123", 1, 0)}
	f.a.lease(t.Context())

	eventually(t, "unpaired", func() bool { return len(f.store.snapshot().unpaired) == 1 })
	if why := *f.store.snapshot().unpaired[0].Error; !strings.Contains(why, "country code") {
		t.Fatalf("unpaired saying %q", why)
	}
}

// Pairing again replaces the number: the device linked before is logged out
// and its keys deleted.
func TestPairingAgainLetsTheOldDeviceGo(t *testing.T) {
	f := newFixture(t, newDevices(linkedJID))
	ch := uuid.New()
	f.store.rows = []db.LeaseWhatsAppAccountsRow{row(ch, &linkedJID)}
	f.a.lease(t.Context())
	r := row(ch, &linkedJID)
	r.PairSeq, r.PairMethod = 1, new("qr")
	f.store.rows = []db.LeaseWhatsAppAccountsRow{r}
	f.a.lease(t.Context())

	if f.client(t, 0).logouts != 1 || len(f.devices.deleted) != 1 || f.devices.deleted[0] != linkedJID.String() {
		t.Fatalf("logouts %d, deleted %v; want the old device logged out and deleted", f.client(t, 0).logouts, f.devices.deleted)
	}
	if strings.Contains(f.logText(), "deleting the device paired before") {
		t.Errorf("deleting keys the logout already deleted was logged as a failure:\n%s", f.logText())
	}
}
