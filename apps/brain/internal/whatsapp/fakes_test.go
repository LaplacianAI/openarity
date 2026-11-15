package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	gwa "github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// fakeClient stands in for WhatsApp: it records what it was asked and lets a
// test send it events.
type fakeClient struct {
	mu        sync.Mutex
	dev       *store.Device
	handlers  []whatsmeow.EventHandler
	connected bool
	connects  int
	discos    int
	logouts   int
	connErr   error
	sent      []sentMessage
	presences []types.ChatPresence
	sendErr   error
	qr        chan whatsmeow.QRChannelItem
	pairPhone string
	pairCode  string
	pairErr   error
	download  []byte
}

type sentMessage struct {
	to  types.JID
	msg *waE2E.Message
}

func (f *fakeClient) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	if f.connErr != nil {
		return f.connErr
	}
	f.connected = true
	return nil
}

func (f *fakeClient) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discos++
	f.connected = false
}

func (f *fakeClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeClient) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logouts++
	return nil
}

func (f *fakeClient) AddEventHandler(h whatsmeow.EventHandler) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append(f.handlers, h)
	return uint32(len(f.handlers)) //nolint:gosec // G115: a test's few handlers
}

func (f *fakeClient) emit(evt any) {
	f.mu.Lock()
	hs := append([]whatsmeow.EventHandler(nil), f.handlers...)
	f.mu.Unlock()
	for _, h := range hs {
		h(evt)
	}
}

func (f *fakeClient) GetQRChannel(context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return f.qr, nil
}

func (f *fakeClient) PairPhone(_ context.Context, phone string, _ bool, _ whatsmeow.PairClientType, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pairPhone = phone
	return f.pairCode, f.pairErr
}

func (f *fakeClient) SendMessage(_ context.Context, to types.JID, msg *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return whatsmeow.SendResponse{}, f.sendErr
	}
	f.sent = append(f.sent, sentMessage{to: to, msg: msg})
	return whatsmeow.SendResponse{ID: "SENT-" + types.MessageID(rune('0'+len(f.sent)))}, nil
}

func (f *fakeClient) SendChatPresence(_ context.Context, _ types.JID, state types.ChatPresence, _ types.ChatPresenceMedia) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presences = append(f.presences, state)
	return nil
}

func (f *fakeClient) DownloadAny(context.Context, *waE2E.Message) ([]byte, error) {
	return f.download, nil
}

func (f *fakeClient) ParseWebMessage(chat types.JID, m *waWeb.WebMessageInfo) (*events.Message, error) {
	if m == nil {
		return nil, errors.New("no message")
	}
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsGroup: chat.Server == types.GroupServer},
			ID:            m.GetKey().GetID(), Timestamp: time.Unix(1_700_000_000, 0),
		},
		Message: m.GetMessage(),
	}, nil
}

// fakeDevices keeps devices by their JID.
type fakeDevices struct {
	mu      sync.Mutex
	devs    map[string]*store.Device
	deleted []string
	news    int
}

func newDevices(jids ...types.JID) *fakeDevices {
	d := &fakeDevices{devs: map[string]*store.Device{}}
	for _, j := range jids {
		d.devs[j.String()] = &store.Device{ID: &j}
	}
	return d
}

func (d *fakeDevices) NewDevice() *store.Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.news++
	return &store.Device{}
}

func (d *fakeDevices) GetDevice(_ context.Context, jid types.JID) (*store.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.devs[jid.String()], nil
}

func (d *fakeDevices) GetAllDevices(context.Context) ([]*store.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*store.Device, 0, len(d.devs))
	for _, v := range d.devs {
		out = append(out, v)
	}
	return out, nil
}

func (d *fakeDevices) DeleteDevice(_ context.Context, dev *store.Device) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dev.ID != nil {
		delete(d.devs, dev.ID.String())
		d.deleted = append(d.deleted, dev.ID.String())
	}
	return nil
}

// fakeStore is the accounts table and the sent parts, in memory.
type fakeStore struct {
	mu       sync.Mutex
	rows     []db.LeaseWhatsAppAccountsRow
	leaseErr error
	pairing  []db.SetWhatsAppPairingParams
	linked   []db.SetWhatsAppLinkedParams
	unpaired []db.SetWhatsAppUnpairedParams
	released []string
	jids     []string
	sent     map[string]string
}

func (s *fakeStore) EnsureWhatsAppAccounts(context.Context) error { return nil }

func (s *fakeStore) LeaseWhatsAppAccounts(context.Context, db.LeaseWhatsAppAccountsParams) ([]db.LeaseWhatsAppAccountsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.LeaseWhatsAppAccountsRow(nil), s.rows...), s.leaseErr
}

func (s *fakeStore) ReleaseWhatsAppAccounts(_ context.Context, holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, holder)
	return nil
}

func (s *fakeStore) SetWhatsAppPairing(_ context.Context, arg db.SetWhatsAppPairingParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairing = append(s.pairing, arg)
	return 1, nil
}

func (s *fakeStore) SetWhatsAppLinked(_ context.Context, arg db.SetWhatsAppLinkedParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linked = append(s.linked, arg)
	return 1, nil
}

func (s *fakeStore) SetWhatsAppUnpaired(_ context.Context, arg db.SetWhatsAppUnpairedParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unpaired = append(s.unpaired, arg)
	return 1, nil
}

func (s *fakeStore) ListWhatsAppJIDs(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.jids...), nil
}

func (s *fakeStore) WhatsAppPartSent(_ context.Context, arg db.WhatsAppPartSentParams) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.sent[arg.ChannelID.String()+"/"+arg.PartID]; ok {
		return id, nil
	}
	return "", pgx.ErrNoRows
}

func (s *fakeStore) RecordWhatsAppPart(_ context.Context, arg db.RecordWhatsAppPartParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sent == nil {
		s.sent = map[string]string{}
	}
	s.sent[arg.ChannelID.String()+"/"+arg.PartID] = arg.MessageID
	return nil
}

func (s *fakeStore) PruneWhatsAppSent(context.Context) error { return nil }

func (s *fakeStore) snapshot() fakeStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fakeStore{
		pairing: append([]db.SetWhatsAppPairingParams(nil), s.pairing...), linked: append([]db.SetWhatsAppLinkedParams(nil), s.linked...),
		unpaired: append([]db.SetWhatsAppUnpairedParams(nil), s.unpaired...), released: append([]string(nil), s.released...),
	}
}

// fakeReceiver keeps what it was handed, and answers errs in turn.
type fakeReceiver struct {
	mu    sync.Mutex
	chans []db.Channel
	got   [][]gateway.Inbound
	files [][]gateway.Stored
	errs  []error
	calls int
}

func (r *fakeReceiver) Receive(ctx context.Context, _ gateway.Provider, ch db.Channel, msgs []gateway.Inbound, files gateway.Files) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.chans = append(r.chans, ch)
	if len(r.errs) > 0 {
		err := r.errs[0]
		r.errs = r.errs[1:]
		if err != nil {
			return err
		}
	}
	r.got = append(r.got, msgs)
	stored, _ := files(ctx, msgs[0].Attachments)
	r.files = append(r.files, stored)
	return nil
}

func (r *fakeReceiver) delivered() [][]gateway.Inbound {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]gateway.Inbound(nil), r.got...)
}

// fakeKeeper keeps each file by reading it, as the gateway's ingest would.
type fakeKeeper struct{}

func (fakeKeeper) Keep(ctx context.Context, _ string, get func(context.Context, gateway.Attachment) ([]byte, error),
	_, _ time.Duration, _ uuid.UUID, claimed []gateway.Attachment,
) ([]gateway.Stored, []string) {
	var out []gateway.Stored
	for _, a := range claimed {
		b, err := get(ctx, a)
		if err == nil {
			out = append(out, gateway.Stored{Filename: a.ClaimedFilename, SizeBytes: int64(len(b))})
		}
	}
	return out, nil
}

// fixture is Accounts over the fakes, with a clock that never waits and
// records every wait asked of it.
type fixture struct {
	a        *Accounts
	store    *fakeStore
	devices  *fakeDevices
	receiver *fakeReceiver
	log      *lockedBuffer

	mu      sync.Mutex
	clients []*fakeClient
	slept   []time.Duration
	// prepare sets up each new client before it is handed over.
	prepare func(*fakeClient)
}

func newFixture(t *testing.T, devices *fakeDevices) *fixture {
	t.Helper()
	f := &fixture{store: &fakeStore{}, devices: devices, receiver: &fakeReceiver{}, log: &lockedBuffer{b: &bytes.Buffer{}}}
	f.a = New(Config{
		Holder: "test/1", Store: f.store, Devices: devices, Provider: gwa.New(),
		Receiver: f.receiver, Ingest: fakeKeeper{},
		Logger: slog.New(slog.NewTextHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		NewClient: func(d *store.Device) Client {
			c := &fakeClient{dev: d, qr: make(chan whatsmeow.QRChannelItem, 8)}
			f.mu.Lock()
			if f.prepare != nil {
				f.prepare(c)
			}
			f.clients = append(f.clients, c)
			f.mu.Unlock()
			return c
		},
	})
	start := time.Unix(1_763_190_000, 0)
	f.a.now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		var waited time.Duration
		for _, d := range f.slept {
			waited += d
		}
		return start.Add(waited)
	}
	f.a.sleep = func(ctx context.Context, d time.Duration) error {
		f.mu.Lock()
		f.slept = append(f.slept, d)
		f.mu.Unlock()
		return ctx.Err()
	}
	f.a.jitter = func() time.Duration { return 0 }
	t.Cleanup(f.a.closeAll)
	return f
}

func (f *fixture) client(t *testing.T, i int) *fakeClient {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.clients) {
		t.Fatalf("client %d asked for; %d made", i, len(f.clients))
	}
	return f.clients[i]
}

func (f *fixture) madeClients() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clients)
}

// eventually polls until ok holds, since events are taken on the
// connection's own goroutine.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func row(channel uuid.UUID, jid *types.JID) db.LeaseWhatsAppAccountsRow {
	r := db.LeaseWhatsAppAccountsRow{ChannelID: channel, TeamID: uuid.New(), State: "unpaired"}
	if jid != nil {
		r.Jid, r.State = new(jid.String()), "connected"
	}
	return r
}
