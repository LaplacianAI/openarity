package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	gwa "github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	// historyPerChat bounds what is kept of each chat's history when the
	// number is linked.
	historyPerChat = 50
	// A file read from WhatsApp: no ack window waits on it, so it may take
	// longer than a webhook's.
	filePerFile = 30 * time.Second
	fileTotal   = 60 * time.Second
	// receiveTries is how often a message is offered to the gateway before
	// it is logged as lost: WhatsApp does not send it again.
	receiveTries = 3
	queueDepth   = 256
	quoteMemory  = 500
)

// conn is one channel's linked device and its connection. Its events are
// taken one at a time, in order, off the client's goroutine, so a slow file
// never holds up WhatsApp's socket.
type conn struct {
	a       *Accounts
	channel db.Channel
	events  chan any
	done    chan struct{}
	ctx     context.Context //nolint:containedctx // the connection's lifetime, for what it starts
	stop    context.CancelFunc

	mu sync.Mutex
	// settings is what the channel says about answering, from the lease:
	// it can change on the dashboard while the connection is held.
	settings settings
	dev      *store.Device
	cli      Client
	pairing  bool
	quotes   map[types.MessageID]quoted
	quoteFIF []types.MessageID
	// own is what this connection sent, so "Message yourself", where the
	// bot's answers arrive as the owner's, does not answer them.
	own    map[types.MessageID]struct{}
	ownFIF []types.MessageID
}

// settings is what a channel says about answering: whether a stranger is
// told they wait, where it answers, and the word that calls it. The lease
// carries them, so a change reaches the connection at its next renewal.
type settings struct {
	notifyStrangers bool
	answersIn       string
	triggerWord     *string
}

func (c *conn) trigger() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.triggerWord == nil {
		return ""
	}
	return *c.settings.triggerWord
}

func (c *conn) setSettings(s settings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = s
}

// current is the channel as the gateway takes a message for it now.
func (c *conn) current() db.Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.channel
	ch.NotifyStrangers, ch.AnswersIn, ch.TriggerWord = c.settings.notifyStrangers, c.settings.answersIn, c.settings.triggerWord
	return ch
}

// quoted is a message the device received, kept so an answer can quote it:
// WhatsApp draws a quote from its sender and words, which a reply only names
// by id.
type quoted struct {
	sender types.JID
	text   string
}

func (a *Accounts) newConn(ctx context.Context, ch db.Channel) *conn {
	ctx, stop := context.WithCancel(ctx)
	c := &conn{
		a: a, channel: ch, events: make(chan any, queueDepth), done: make(chan struct{}), ctx: ctx, stop: stop,
		quotes: map[types.MessageID]quoted{}, own: map[types.MessageID]struct{}{},
	}
	go c.run(ctx)
	return c
}

// attach makes dev this channel's device and connects it. An earlier client
// is let go first.
func (c *conn) attach(dev *store.Device) error {
	cli := c.a.newClient(dev)
	cli.AddEventHandler(func(evt any) {
		select {
		case c.events <- evt:
		case <-c.done:
		}
	})
	c.mu.Lock()
	old := c.cli
	c.dev, c.cli = dev, cli
	c.mu.Unlock()
	if old != nil {
		old.Disconnect()
	}
	if dev.ID == nil {
		return nil // a new device connects when its pairing starts
	}
	return cli.Connect()
}

func (c *conn) client() Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cli
}

func (c *conn) device() *store.Device {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dev
}

// close lets the connection go, leaving the device linked: another role may
// take the channel over and connect it.
func (c *conn) close() {
	c.stop()
	if cli := c.client(); cli != nil {
		cli.Disconnect()
	}
	<-c.done
}

func (c *conn) run(ctx context.Context) {
	defer close(c.done)
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-c.events:
			c.handle(ctx, evt)
		}
	}
}

func (c *conn) handle(ctx context.Context, evt any) {
	log := c.a.logger.With("channel_id", c.channel.ID)
	switch e := evt.(type) {
	case *events.Message:
		if e.Info.IsFromMe && c.sentHere(e.Info.ID) {
			return
		}
		in, ok := gwa.Map(e, c.self(), c.trigger(), false)
		if !ok {
			return
		}
		c.remember(e)
		c.receive(ctx, []gateway.Inbound{in}, func(ctx context.Context, _ gateway.Attachment) ([]byte, error) {
			return c.client().DownloadAny(ctx, e.Message)
		})
	case *events.HistorySync:
		c.history(ctx, e)
	case *events.PairSuccess:
		c.linked(ctx, e)
	case *events.LoggedOut:
		log.Warn("whatsapp: the number logged this device out", "on_connect", e.OnConnect, "reason", e.Reason.String())
		c.unlink(ctx, "WhatsApp logged this device out. Pair the number again.")
	case *events.StreamReplaced:
		// Another client connected as this device: two holders, which the
		// lease exists to prevent.
		log.Error("whatsapp: another connection took this device over")
	case *events.TemporaryBan:
		log.Error("whatsapp: the number is banned for a while", "ban", e.String())
	case *events.ConnectFailure:
		log.Error("whatsapp: connecting failed", "reason", e.Reason.String(), "message", e.Message)
	case *events.ClientOutdated:
		log.Error("whatsapp: WhatsApp refuses this client as outdated; whatsmeow needs updating")
	case *events.Connected:
		log.Info("whatsapp: connected")
	}
}

func (c *conn) self() gwa.Self {
	dev := c.device()
	if dev == nil || dev.ID == nil {
		return gwa.Self{}
	}
	return gwa.Self{PN: dev.ID.ToNonAD(), LID: dev.LID.ToNonAD()}
}

// remember keeps the newest messages, so an answer can quote the one it
// answers.
func (c *conn) remember(e *events.Message) {
	text, _ := quoteText(e)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.quotes[e.Info.ID]; !ok {
		c.quoteFIF = append(c.quoteFIF, e.Info.ID)
	}
	c.quotes[e.Info.ID] = quoted{sender: e.Info.Sender.ToNonAD(), text: text}
	if len(c.quoteFIF) > quoteMemory {
		delete(c.quotes, c.quoteFIF[0])
		c.quoteFIF = c.quoteFIF[1:]
	}
}

// sending names a message before it goes, so its echo is known whenever it
// arrives.
func (c *conn) sending(id types.MessageID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.own[id]; ok {
		return
	}
	c.own[id] = struct{}{}
	c.ownFIF = append(c.ownFIF, id)
	if len(c.ownFIF) > quoteMemory {
		delete(c.own, c.ownFIF[0])
		c.ownFIF = c.ownFIF[1:]
	}
}

func (c *conn) sentHere(id types.MessageID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.own[id]
	return ok
}

func (c *conn) quote(id string) (quoted, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q, ok := c.quotes[id]
	return q, ok
}

func quoteText(e *events.Message) (string, bool) {
	m := e.Message
	switch {
	case m.GetConversation() != "":
		return m.GetConversation(), true
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText(), true
	default:
		return "", false
	}
}

// receive offers messages to the gateway, a few times when it fails: the
// gateway's writes are idempotent, and WhatsApp will not send them again.
func (c *conn) receive(ctx context.Context, msgs []gateway.Inbound, get func(context.Context, gateway.Attachment) ([]byte, error)) {
	files := func(ctx context.Context, claimed []gateway.Attachment) ([]gateway.Stored, []string) {
		return c.a.ingest.Keep(ctx, gwa.Name, get, filePerFile, fileTotal, c.channel.TeamID, claimed)
	}
	var err error
	for try := range receiveTries {
		err = c.a.receiver.Receive(ctx, c.a.provider, c.current(), msgs, files)
		if err == nil || errors.Is(err, gateway.ErrTeamDeleted) || ctx.Err() != nil {
			break
		}
		if c.a.sleep(ctx, time.Duration(try+1)*time.Second) != nil {
			break
		}
	}
	if err != nil && !errors.Is(err, gateway.ErrTeamDeleted) {
		c.a.logger.Error("whatsapp: a message was not taken", "channel_id", c.channel.ID,
			"messages", len(msgs), "first", msgs[0].ExternalID, "error", err)
	}
}

// history keeps what WhatsApp sent of each chat when the number was linked:
// the newest of each, never answered, and without their files, which are
// often gone from WhatsApp's servers by now.
func (c *conn) history(ctx context.Context, e *events.HistorySync) {
	cli, self := c.client(), c.self()
	for _, conv := range e.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil || chat.User == "" {
			continue
		}
		var msgs []gateway.Inbound
		for _, hm := range conv.GetMessages() {
			if len(msgs) == historyPerChat {
				break
			}
			m, err := cli.ParseWebMessage(chat, hm.GetMessage())
			if err != nil {
				continue
			}
			// The owner's own from before are never kept, so no word is needed.
			if in, ok := gwa.Map(m, self, "", true); ok {
				in.Attachments = nil
				msgs = append(msgs, in)
			}
		}
		if len(msgs) > 0 {
			c.receive(ctx, msgs, func(context.Context, gateway.Attachment) ([]byte, error) {
				return nil, errHistoryFiles
			})
		}
	}
}

var errHistoryFiles = errors.New("a file from before the number was linked is not kept")

func (c *conn) linked(ctx context.Context, e *events.PairSuccess) {
	c.mu.Lock()
	c.pairing = false
	c.mu.Unlock()
	n, err := c.a.store.SetWhatsAppLinked(ctx, db.SetWhatsAppLinkedParams{
		ChannelID: c.channel.ID, Holder: c.a.holder, Jid: new(e.ID.ToNonAD().String()), Phone: new(e.ID.User),
	})
	switch {
	case err != nil:
		c.a.logger.Error("whatsapp: recording the linked number", "channel_id", c.channel.ID, "error", err)
	case n == 0:
		c.a.logger.Warn("whatsapp: linked a number for a channel this role no longer holds", "channel_id", c.channel.ID)
	default:
		c.a.logger.Info("whatsapp: linked", "channel_id", c.channel.ID)
	}
}

// unlink forgets the device: its keys deleted, the account unpaired with
// why. The connection stays held, so pairing again needs no new lease.
func (c *conn) unlink(ctx context.Context, why string) {
	c.mu.Lock()
	dev, cli := c.dev, c.cli
	c.dev, c.cli, c.pairing = nil, nil, false
	c.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
	if dev != nil {
		if err := c.a.forget(ctx, dev); err != nil {
			c.a.logger.Error("whatsapp: deleting a device's keys", "channel_id", c.channel.ID, "error", err)
		}
	}
	if _, err := c.a.store.SetWhatsAppUnpaired(ctx, db.SetWhatsAppUnpairedParams{
		ChannelID: c.channel.ID, Holder: c.a.holder, Error: &why,
	}); err != nil {
		c.a.logger.Error("whatsapp: recording the device unlinked", "channel_id", c.channel.ID, "error", err)
	}
}

func (c *conn) String() string { return fmt.Sprintf("whatsapp conn %s", c.channel.ID) }
