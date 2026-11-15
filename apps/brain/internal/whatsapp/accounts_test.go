package whatsapp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

var linkedJID = types.NewJID("919000000001", types.DefaultUserServer)

func TestAChannelLeasedWithALinkedDeviceConnects(t *testing.T) {
	f := newFixture(t, newDevices(linkedJID))
	ch := uuid.New()
	f.store.rows = []db.LeaseWhatsAppAccountsRow{row(ch, &linkedJID)}
	f.a.lease(t.Context())

	if got := f.a.Held(); len(got) != 1 || got[0] != ch {
		t.Fatalf("Held = %v; want the channel leased", got)
	}
	if c := f.client(t, 0); c.connects != 1 || c.dev.ID.String() != linkedJID.String() {
		t.Fatalf("connected %d times as %v; want once, as the linked device", c.connects, c.dev.ID)
	}
}

// A lease the store no longer gives is a channel another role holds, or one
// deleted: its connection goes at once, the device left linked for whoever
// holds it now.
func TestAChannelNoLongerLeasedIsDisconnected(t *testing.T) {
	f := newFixture(t, newDevices(linkedJID))
	ch := uuid.New()
	f.store.rows = []db.LeaseWhatsAppAccountsRow{row(ch, &linkedJID)}
	f.a.lease(t.Context())
	f.store.rows = nil
	f.a.lease(t.Context())

	if len(f.a.Held()) != 0 || f.client(t, 0).discos == 0 {
		t.Fatalf("held %v, disconnects %d; want none held and the connection let go", f.a.Held(), f.client(t, 0).discos)
	}
	if len(f.devices.deleted) != 0 {
		t.Error("a device was deleted for a lease lost; another role may hold it now")
	}
}

// Holding on without a lease is two connections fighting once another role
// takes the channel over.
func TestALeaseThatCannotBeRenewedLetsEveryConnectionGo(t *testing.T) {
	f := newFixture(t, newDevices(linkedJID))
	f.store.rows = []db.LeaseWhatsAppAccountsRow{row(uuid.New(), &linkedJID)}
	f.a.lease(t.Context())
	f.store.leaseErr = errors.New("connection reset")
	f.a.lease(t.Context())

	if len(f.a.Held()) != 0 || f.client(t, 0).discos == 0 {
		t.Fatal("a connection was kept with no lease behind it")
	}
}

func TestADeviceWhoseKeysAreGoneIsUnpairedWithWhy(t *testing.T) {
	f := newFixture(t, newDevices())
	ch := uuid.New()
	f.store.rows = []db.LeaseWhatsAppAccountsRow{row(ch, &linkedJID)}
	f.a.lease(t.Context())

	s := f.store.snapshot()
	if len(s.unpaired) != 1 || s.unpaired[0].ChannelID != ch || !strings.Contains(*s.unpaired[0].Error, "Pair the number again") {
		t.Fatalf("unpaired %+v; want the channel unpaired, saying to pair again", s.unpaired)
	}
	if f.madeClients() != 0 {
		t.Error("a client was made for a device that is not there")
	}
}

// A device no account names is let go: logged out, so the phone stops
// listing it, and its keys deleted. Only at the second look, so one another
// role is a moment from naming is not taken for an orphan; and never one
// this process holds.
func TestADeviceNoAccountNamesIsLetGoAtTheSecondLook(t *testing.T) {
	orphan := types.NewJID("919000000099", types.DefaultUserServer)
	named := types.NewJID("919000000002", types.DefaultUserServer)
	f := newFixture(t, newDevices(orphan, named, linkedJID))
	f.store.jids = []string{named.String()}
	f.store.rows = []db.LeaseWhatsAppAccountsRow{row(uuid.New(), &linkedJID)}
	f.a.lease(t.Context())

	f.a.letGoOrphans(t.Context())
	if len(f.devices.deleted) != 0 {
		t.Fatalf("deleted %v at the first look", f.devices.deleted)
	}
	f.a.letGoOrphans(t.Context())
	if !slices.Equal(f.devices.deleted, []string{orphan.String()}) {
		t.Fatalf("deleted %v; want the orphan alone", f.devices.deleted)
	}
	if c := f.client(t, f.madeClients()-1); c.logouts != 1 {
		t.Errorf("the orphan was logged out %d times; want once", c.logouts)
	}
}

func TestStoppingReleasesTheLeases(t *testing.T) {
	f := newFixture(t, newDevices(linkedJID))
	f.store.rows = []db.LeaseWhatsAppAccountsRow{row(uuid.New(), &linkedJID)}
	ctx, cancel := contextWithCancel(t)
	done := make(chan error, 1)
	go func() { done <- f.a.Run(ctx, func(context.Context) (int, error) { return 0, nil }) }()
	eventually(t, "the lease taken", func() bool { return len(f.a.Held()) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if s := f.store.snapshot(); !slices.Equal(s.released, []string{"test/1"}) || len(f.a.Held()) != 0 {
		t.Fatalf("released %v, held %v; want this holder's leases let go", s.released, f.a.Held())
	}
}
