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
	// sharedWords are the other channels' on this number, "" for one with
	// none; empty when the number is this channel's alone.
	sharedWords []string
}

// words is the trigger word messages are mapped with, whether the number is
// shared, and whether this channel answers at all: on a shared number it
// waits until every channel has a word of its own.
func (c *conn) words() (trigger string, shared, answers bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.triggerWord != nil {
		trigger = *c.settings.triggerWord
	}
	shared = len(c.settings.sharedWords) > 0
	return trigger, shared, !shared || gateway.SharedAnswers(trigger, c.settings.sharedWords)
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
		if e.Message.GetPollUpdateMessage() != nil {
			c.vote(ctx, e)
			return
		}
		if e.Info.IsFromMe && c.sentHere(e.Info.ID) {
			return
		}
		trigger, shared, answers := c.words()
		if !answers {
			return
		}
		in, ok := gwa.Map(e, c.self(), trigger, false, shared)
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
		// Another client connected as this device, and this connection is
		// over: WhatsApp does not hand it back. Said on the account, so the
		// channel shows it needs pairing rather than "linked" over nothing.
		log.Error("whatsapp: another connection took this device over")
		c.replaced(ctx)
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

// vote decrypts a poll vote and offers it to the gateway as an answer. The
// client does the decrypting, so it is here and not in the adapter. A vote
// that cannot be decrypted (a poll this device did not send, or one whose
// secret is gone) or that chose nothing (a retracted choice) is logged and
// dropped: WhatsApp will not send it again, and nothing could be done with
// it. Whether the voter may answer anything is the gateway's to decide.
func (c *conn) vote(ctx context.Context, e *events.Message) {
	log := c.a.logger.With("channel_id", c.channel.ID, "message_id", e.Info.ID)
	if _, _, answers := c.words(); !answers {
		return
	}
	cli := c.client()
	if cli == nil {
		return
	}
	v, err := cli.DecryptPollVote(ctx, e)
	if err != nil {
		// On a number several channels share, every channel's holder sees
		// the vote and only the one whose device sent the poll holds its
		// secret: the rest failing here is expected, not a warning.
		log.Debug("whatsapp: a poll vote was not decrypted; dropped", "error", err)
		return
	}
	in, ok := gwa.MapVote(e, c.self(), v.GetSelectedOptions())
	if !ok {
		log.Info("whatsapp: a poll vote chose nothing, or is not kept; dropped")
		return
	}
	c.receive(ctx, []gateway.Inbound{in}, func(context.Context, gateway.Attachment) ([]byte, error) {
		return nil, errVoteFiles
	})
}

var errVoteFiles = errors.New("a poll vote has no file")

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
	trigger, shared, answers := c.words()
	if !answers {
		return
	}
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
			// The owner's own from before are never kept, so the word only
			// matters on a shared number, where it is what everyone's needs.
			if in, ok := gwa.Map(m, self, trigger, true, shared); ok {
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
	// The device's own address, number and device both: a number can be
	// linked to several channels, each its own device, and by the number
	// alone every channel would connect as the first.
	n, err := c.a.store.SetWhatsAppLinked(ctx, db.SetWhatsAppLinkedParams{
		ChannelID: c.channel.ID, Holder: c.a.holder, Jid: new(e.ID.String()), Phone: new(e.ID.User),
	})
	// Whether the number is shared, now: the history WhatsApp sends next
	// comes before the next sweep, and on a shared number it is filtered.
	if err == nil && n > 0 {
		if words, err := c.a.store.WhatsAppSharedWords(ctx, c.channel.ID); err == nil {
			c.mu.Lock()
			c.settings.sharedWords = words
			c.mu.Unlock()
		} else {
			c.a.logger.Error("whatsapp: reading the number's other channels", "channel_id", c.channel.ID, "error", err)
		}
	}
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

// replaced lets a device go whose connection another took. Its keys are
// left: the account no longer names it, so the sweep logs it out.
func (c *conn) replaced(ctx context.Context) {
	c.mu.Lock()
	cli := c.cli
	c.dev, c.cli = nil, nil
	c.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
	why := "Another connection took this channel's linked device over. Pair the number again."
	if _, err := c.a.store.SetWhatsAppUnpaired(ctx, db.SetWhatsAppUnpairedParams{
		ChannelID: c.channel.ID, Holder: c.a.holder, Error: &why,
	}); err != nil {
		c.a.logger.Error("whatsapp: recording the device taken over", "channel_id", c.channel.ID, "error", err)
	}
}

func (c *conn) String() string { return fmt.Sprintf("whatsapp conn %s", c.channel.ID) }
