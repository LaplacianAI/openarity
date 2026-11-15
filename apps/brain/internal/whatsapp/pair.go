package whatsapp

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// codeExpires is how long a pairing code is given before it is called
// expired: WhatsApp's own lasts about this long.
const codeExpires = 3 * time.Minute

// pair starts linking a new device for the channel, by QR or by code, as the
// request asked. A device already linked is let go first: pairing again is
// replacing the number. The request is marked taken before anything else, so
// a role that restarts does not take it twice.
func (c *conn) pair(ctx context.Context, r db.LeaseWhatsAppAccountsRow) {
	c.mu.Lock()
	if c.pairing {
		c.mu.Unlock()
		return
	}
	c.pairing = true
	c.mu.Unlock()

	n, err := c.a.store.SetWhatsAppPairing(ctx, db.SetWhatsAppPairingParams{ChannelID: c.channel.ID, Holder: c.a.holder, Seq: r.PairSeq})
	if err != nil || n == 0 {
		c.a.logger.Warn("whatsapp: taking a pairing request", "channel_id", c.channel.ID, "rows", n, "error", err)
		c.pairingDone()
		return
	}

	if old := c.device(); old != nil && old.ID != nil {
		if cli := c.client(); cli != nil && cli.IsConnected() {
			lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_ = cli.Logout(lctx)
			cancel()
		}
		if err := c.a.devices.DeleteDevice(ctx, old); err != nil {
			c.a.logger.Warn("whatsapp: deleting the device paired before", "channel_id", c.channel.ID, "error", err)
		}
	}
	if err := c.attach(c.a.devices.NewDevice()); err != nil {
		c.failed(ctx, "Could not start pairing.")
		return
	}

	method, phone := "", ""
	if r.PairMethod != nil {
		method = *r.PairMethod
	}
	if r.PairPhone != nil {
		phone = *r.PairPhone
	}
	// The connection's own context: a pairing ends with the connection,
	// never outlives it on a channel another role now holds.
	switch method {
	case "code":
		go c.byCode(c.ctx, r.PairSeq, phone)
	default:
		go c.byQR(c.ctx, r.PairSeq)
	}
}

func (c *conn) pairingDone() {
	c.mu.Lock()
	c.pairing = false
	c.mu.Unlock()
}

// failed ends a pairing that will not finish, saying why.
func (c *conn) failed(ctx context.Context, why string) {
	c.a.logger.Info("whatsapp: pairing ended", "channel_id", c.channel.ID, "why", why)
	c.unlink(ctx, why)
}

func (c *conn) byQR(ctx context.Context, seq int64) {
	cli := c.client()
	qr, err := cli.GetQRChannel(ctx)
	if err == nil {
		err = cli.Connect()
	}
	if err != nil {
		c.failed(ctx, "Could not reach WhatsApp to pair.")
		return
	}
	for item := range qr {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			code := item.Code
			if _, err := c.a.store.SetWhatsAppPairing(ctx, db.SetWhatsAppPairingParams{
				ChannelID: c.channel.ID, Holder: c.a.holder, Seq: seq, Qr: &code,
			}); err != nil {
				c.a.logger.Error("whatsapp: recording a QR code", "channel_id", c.channel.ID, "error", err)
			}
		case whatsmeow.QRChannelSuccess.Event:
			return // the PairSuccess event records the number
		case whatsmeow.QRChannelTimeout.Event:
			c.failed(ctx, "The QR code expired before it was scanned. Pair again.")
			return
		default:
			c.failed(ctx, "WhatsApp refused the pairing. Pair again.")
			return
		}
	}
}

func (c *conn) byCode(ctx context.Context, seq int64, phone string) {
	cli := c.client()
	if err := cli.Connect(); err != nil {
		c.failed(ctx, "Could not reach WhatsApp to pair.")
		return
	}
	code, err := cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		c.failed(ctx, "WhatsApp gave no pairing code for that number. Check it, with its country code.")
		return
	}
	if _, err := c.a.store.SetWhatsAppPairing(ctx, db.SetWhatsAppPairingParams{
		ChannelID: c.channel.ID, Holder: c.a.holder, Seq: seq, Code: &code,
	}); err != nil {
		c.a.logger.Error("whatsapp: recording a pairing code", "channel_id", c.channel.ID, "error", err)
	}
	if c.a.sleep(ctx, codeExpires) != nil {
		return
	}
	c.mu.Lock()
	still := c.pairing
	c.mu.Unlock()
	if still {
		c.failed(ctx, "The pairing code expired before it was entered. Pair again.")
	}
}
