package whatsapp

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// The lease: renewed every 15 seconds, taken over by another role after 60.
// Replies are claimed every two seconds, since a run's finish cannot kick a
// process it does not know; devices no account names are looked for every
// half minute.
const (
	leaseEvery   = 15 * time.Second
	leaseStale   = 60 * time.Second
	drainEvery   = 2 * time.Second
	orphansEvery = 30 * time.Second
	pruneEvery   = time.Hour
)

// Store is what holding accounts reads and writes.
type Store interface {
	EnsureWhatsAppAccounts(ctx context.Context) error
	LeaseWhatsAppAccounts(ctx context.Context, arg db.LeaseWhatsAppAccountsParams) ([]db.LeaseWhatsAppAccountsRow, error)
	ReleaseWhatsAppAccounts(ctx context.Context, holder string) error
	SetWhatsAppPairing(ctx context.Context, arg db.SetWhatsAppPairingParams) (int64, error)
	SetWhatsAppLinked(ctx context.Context, arg db.SetWhatsAppLinkedParams) (int64, error)
	SetWhatsAppUnpaired(ctx context.Context, arg db.SetWhatsAppUnpairedParams) (int64, error)
	ListWhatsAppJIDs(ctx context.Context) ([]string, error)
	WhatsAppSharedWords(ctx context.Context, channelID uuid.UUID) ([]string, error)
	WhatsAppPartSent(ctx context.Context, arg db.WhatsAppPartSentParams) (string, error)
	RecordWhatsAppPart(ctx context.Context, arg db.RecordWhatsAppPartParams) error
	PruneWhatsAppSent(ctx context.Context) error
}

// Receiver is the gateway's: what a webhook's handler hands its messages to.
type Receiver interface {
	Receive(ctx context.Context, p gateway.Provider, ch db.Channel, msgs []gateway.Inbound, files gateway.Files) error
}

// Keeper keeps a message's files under the gateway's checks.
type Keeper interface {
	Keep(ctx context.Context, provider string, get func(context.Context, gateway.Attachment) ([]byte, error),
		perFile, total time.Duration, teamID uuid.UUID, claimed []gateway.Attachment) ([]gateway.Stored, []string)
}

type Config struct {
	// Holder names this process in the lease; unique among the roles running.
	Holder    string
	Store     Store
	Devices   Devices
	NewClient func(*store.Device) Client
	Provider  gateway.Provider
	Receiver  Receiver
	Ingest    Keeper
	Logger    *slog.Logger
}

// Accounts holds the connections of every WhatsApp channel this process
// leases, and sends their replies.
type Accounts struct {
	holder    string
	store     Store
	devices   Devices
	newClient func(*store.Device) Client
	provider  gateway.Provider
	receiver  Receiver
	ingest    Keeper
	logger    *slog.Logger
	pace      *pacer

	// The clock and the jitter, apart so a test runs without waiting.
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func() time.Duration

	mu      sync.Mutex
	conns   map[uuid.UUID]*conn
	orphans map[string]bool
}

func New(cfg Config) *Accounts {
	if cfg.Logger == nil {
		panic("whatsapp: a nil logger")
	}
	return &Accounts{
		holder: cfg.Holder, store: cfg.Store, devices: cfg.Devices, newClient: cfg.NewClient,
		provider: cfg.Provider, receiver: cfg.Receiver, ingest: cfg.Ingest, logger: cfg.Logger,
		pace: newPacer(), now: time.Now, sleep: sleep,
		jitter:  func() time.Duration { return time.Duration(rand.Int64N(int64(typingJitter))) }, //nolint:gosec // G404: pacing, not a secret
		conns:   map[uuid.UUID]*conn{},
		orphans: map[string]bool{},
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Held is the channels this process holds, for the sender's claims.
func (a *Accounts) Held() []uuid.UUID {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]uuid.UUID, 0, len(a.conns))
	for id := range a.conns {
		out = append(out, id)
	}
	return out
}

func (a *Accounts) conn(channel uuid.UUID) *conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conns[channel]
}

// Run holds accounts until ctx ends, then lets every connection and lease go
// so another role takes them at once. drain sends the held channels' replies.
func (a *Accounts) Run(ctx context.Context, drain func(context.Context) (int, error)) error {
	defer a.release()

	a.lease(ctx)
	// Sending paces itself for seconds a part, so it runs apart from the
	// lease: a long reply must never let the lease lapse mid-send.
	sending := make(chan struct{})
	go func() {
		defer close(sending)
		a.drainLoop(ctx, drain)
	}()
	defer func() { <-sending }()

	lease := time.NewTicker(leaseEvery)
	orphans, prune := time.NewTicker(orphansEvery), time.NewTicker(pruneEvery)
	defer lease.Stop()
	defer orphans.Stop()
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-lease.C:
			a.lease(ctx)
		case <-orphans.C:
			a.letGoOrphans(ctx)
		case <-prune.C:
			if err := a.store.PruneWhatsAppSent(ctx); err != nil {
				a.logger.Warn("whatsapp: pruning sent parts", "error", err)
			}
		}
	}
}

func (a *Accounts) drainLoop(ctx context.Context, drain func(context.Context) (int, error)) {
	t := time.NewTicker(drainEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := drain(ctx); err != nil && ctx.Err() == nil {
				a.logger.Error("whatsapp: sending replies", "error", err)
			}
		}
	}
}

// lease takes and renews this process's accounts, connects those newly held,
// lets go of those no longer held, and starts any pairing asked for.
func (a *Accounts) lease(ctx context.Context) {
	if err := a.store.EnsureWhatsAppAccounts(ctx); err != nil {
		a.logger.Error("whatsapp: listing channels", "error", err)
		return
	}
	rows, err := a.store.LeaseWhatsAppAccounts(ctx, db.LeaseWhatsAppAccountsParams{
		Holder: a.holder, StaleSeconds: leaseStale.Seconds(),
	})
	if err != nil {
		// Holding on would be holding without a lease: another role takes
		// these after 60 seconds, and two connections fight.
		a.logger.Error("whatsapp: renewing the lease; letting every connection go", "error", err)
		a.closeAll()
		return
	}
	a.held(ctx, rows)
}

func (a *Accounts) held(ctx context.Context, rows []db.LeaseWhatsAppAccountsRow) {
	keep := make(map[uuid.UUID]bool, len(rows))
	for _, r := range rows {
		keep[r.ChannelID] = true
	}
	a.mu.Lock()
	var gone []*conn
	for id, c := range a.conns {
		if !keep[id] {
			gone = append(gone, c)
			delete(a.conns, id)
		}
	}
	a.mu.Unlock()
	for _, c := range gone {
		a.logger.Info("whatsapp: no longer held here", "channel_id", c.channel.ID)
		c.close()
	}

	for _, r := range rows {
		c := a.conn(r.ChannelID)
		if c == nil {
			c = a.open(ctx, r)
		}
		c.setSettings(settings{notifyStrangers: r.NotifyStrangers, answersIn: r.AnswersIn, triggerWord: r.TriggerWord, sharedWords: r.SharedWords})
		if r.PairSeq > r.PairSeen {
			c.pair(ctx, r)
		}
	}
}

// open starts holding a channel, connecting the device it was linked to.
func (a *Accounts) open(ctx context.Context, r db.LeaseWhatsAppAccountsRow) *conn {
	c := a.newConn(ctx, db.Channel{ID: r.ChannelID, TeamID: r.TeamID})
	c.setSettings(settings{notifyStrangers: r.NotifyStrangers, answersIn: r.AnswersIn, triggerWord: r.TriggerWord, sharedWords: r.SharedWords})
	a.mu.Lock()
	a.conns[r.ChannelID] = c
	a.mu.Unlock()
	if r.Jid == nil {
		return c
	}

	jid, err := types.ParseJID(*r.Jid)
	var dev *store.Device
	if err == nil {
		dev, err = a.deviceOf(ctx, jid)
	}
	switch {
	case errors.Is(err, errDeviceAmbiguous):
		c.unlink(ctx, "This number's channels were linked before each kept a device of its own. Pair this channel again.")
	case err != nil:
		a.logger.Error("whatsapp: reading the linked device", "channel_id", r.ChannelID, "error", err)
	case dev == nil:
		c.unlink(ctx, "The linked device's keys are gone. Pair the number again.")
	default:
		if err := c.attach(dev); err != nil {
			a.logger.Warn("whatsapp: connecting; whatsmeow keeps trying", "channel_id", r.ChannelID, "error", err)
		}
	}
	return c
}

// errDeviceAmbiguous is an account that names only a number, as accounts did
// before each kept its device's address, on a number linked as more than one
// device: which one is this channel's cannot be told.
var errDeviceAmbiguous = errors.New("the account names a number linked as more than one device")

// deviceOf is the device an account names: by its full address, number and
// device, since a number can be linked to several channels, each its own
// device. An account from before names the number alone, and is matched to
// the one device of that number, or refused when there are several. Nil
// when its keys are gone.
func (a *Accounts) deviceOf(ctx context.Context, jid types.JID) (*store.Device, error) {
	devs, err := a.devices.GetAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	if jid.Device != 0 {
		for _, d := range devs {
			if d.ID != nil && sameDevice(*d.ID, jid) {
				return d, nil
			}
		}
		return nil, nil
	}
	want := jid.ToNonAD()
	var found *store.Device
	for _, d := range devs {
		if d.ID == nil || d.ID.ToNonAD() != want {
			continue
		}
		if found != nil {
			return nil, errDeviceAmbiguous
		}
		found = d
	}
	return found, nil
}

func sameDevice(a, b types.JID) bool {
	return a.User == b.User && a.Device == b.Device && a.Server == b.Server
}

// forget deletes a device's keys, unless logging it out already did:
// whatsmeow deletes them itself once WhatsApp accepts the logout, and leaves
// the device with no address.
func (a *Accounts) forget(ctx context.Context, d *store.Device) error {
	if d.ID == nil {
		return nil
	}
	return a.devices.DeleteDevice(ctx, d)
}

func (a *Accounts) closeAll() {
	a.mu.Lock()
	conns := a.conns
	a.conns = map[uuid.UUID]*conn{}
	a.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

func (a *Accounts) release() {
	a.closeAll()
	// Its own context: the one Run was given has ended.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.store.ReleaseWhatsAppAccounts(ctx, a.holder); err != nil {
		a.logger.Warn("whatsapp: letting the leases go; they lapse in a minute", "error", err)
	}
}

// letGoOrphans logs out and deletes a device no account names: its channel
// or team was deleted, or a pairing was abandoned. A device is let go only
// when it was an orphan at the last look too, so one another role has just
// linked, whose account is a moment from naming it, is not taken for one.
func (a *Accounts) letGoOrphans(ctx context.Context) {
	devs, err := a.devices.GetAllDevices(ctx)
	if err != nil {
		a.logger.Error("whatsapp: listing devices", "error", err)
		return
	}
	named, err := a.store.ListWhatsAppJIDs(ctx)
	if err != nil {
		a.logger.Error("whatsapp: listing linked accounts", "error", err)
		return
	}
	// A device is known by its own address, or, for an account from before
	// that names only the number, by the number: such an account keeps every
	// device of its number until it is paired again.
	known := make(map[string]bool, len(named))
	for _, raw := range named {
		if j, err := types.ParseJID(raw); err == nil && j.Device != 0 {
			known[j.String()] = true
		} else {
			known[raw] = true
		}
	}
	a.mu.Lock()
	for _, c := range a.conns {
		if d := c.device(); d != nil && d.ID != nil {
			known[d.ID.String()] = true
		}
	}
	last := a.orphans
	a.orphans = map[string]bool{}
	a.mu.Unlock()

	for _, d := range devs {
		if d.ID == nil {
			continue
		}
		id := d.ID.String()
		if known[id] || known[d.ID.ToNonAD().String()] {
			continue
		}
		if !last[id] {
			a.mu.Lock()
			a.orphans[id] = true
			a.mu.Unlock()
			continue
		}
		a.letGo(ctx, d)
	}
}

// letGo logs a device out, so the phone stops listing it, and deletes its
// keys whether or not WhatsApp could be reached.
func (a *Accounts) letGo(ctx context.Context, d *store.Device) {
	a.logger.Info("whatsapp: letting go of a device no channel holds", "jid", d.ID.ToNonAD().String())
	cli := a.newClient(d)
	if err := cli.Connect(); err == nil {
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := cli.Logout(lctx); err != nil {
			a.logger.Warn("whatsapp: logging an orphan out; deleting its keys anyway", "error", err)
		}
		cancel()
		cli.Disconnect()
	}
	if err := a.forget(ctx, d); err != nil && !errors.Is(err, context.Canceled) {
		a.logger.Error("whatsapp: deleting an orphan's keys", "error", err)
	}
}
