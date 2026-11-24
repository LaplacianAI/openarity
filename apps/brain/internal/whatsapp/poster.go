package whatsapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	gwa "github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Post sends one part through the channel's connection, at a person's pace:
// a pause after the last message to that chat, then typing, then the words.
// A part sent before, whose answer was lost, is not sent again. Over a cap,
// or not connected, is an error the outbox retries.
func (a *Accounts) Post(ctx context.Context, channel uuid.UUID, req gateway.OutboundRequest) error {
	p, err := gwa.ReadPart(req)
	if err != nil {
		return fmt.Errorf("%w: %w", gateway.ErrUnsendable, err)
	}
	// ParseJID is lenient: "@@@" parses, as nobody. Only a person or a group
	// is someone to send to.
	to, err := types.ParseJID(p.To)
	if err != nil || to.User == "" || !sendable[to.Server] {
		return fmt.Errorf("%w: %q is not a person or a group", gateway.ErrUnsendable, p.To)
	}
	c := a.conn(channel)
	if c == nil {
		return errors.New("this role no longer holds the channel")
	}

	_, err = a.store.WhatsAppPartSent(ctx, db.WhatsAppPartSentParams{ChannelID: channel, PartID: p.ID})
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("reading whether the part was sent: %w", err)
	}

	cli := c.client()
	if cli == nil || !cli.IsConnected() {
		return errors.New("the number is not connected")
	}
	wait, err := a.pace.take(channel, to.String(), a.now())
	if err != nil {
		return err
	}
	if err := a.sleep(ctx, wait); err != nil {
		return err
	}
	_ = cli.SendChatPresence(ctx, to, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	if err := a.sleep(ctx, typing(spoken(p), a.jitter())); err != nil {
		return err
	}
	msg, err := a.message(ctx, c, cli, p)
	if err != nil {
		return err
	}
	id := cli.GenerateMessageID()
	c.sending(id)
	resp, err := cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{ID: id})
	_ = cli.SendChatPresence(ctx, to, types.ChatPresencePaused, types.ChatPresenceMediaText)
	if err != nil {
		return fmt.Errorf("sending: %w", err)
	}
	if err := a.store.RecordWhatsAppPart(ctx, db.RecordWhatsAppPartParams{
		ChannelID: channel, PartID: p.ID, MessageID: resp.ID,
	}); err != nil {
		// It went: an error here would send it again.
		a.logger.Error("whatsapp: recording a part sent; a retry may repeat it", "channel_id", channel, "error", err)
	}
	return nil
}

// message is a part as WhatsApp sends it: a poll, a document, or extended
// text with the people it mentions and the message it quotes when this
// connection still remembers it. A quote it cannot draw is left out rather
// than shown empty.
func (a *Accounts) message(ctx context.Context, c *conn, cli Client, p gwa.Part) (*waE2E.Message, error) {
	switch {
	case p.Poll != nil:
		// Single choice. whatsmeow keeps the poll's secret when it sends it,
		// which is what lets a vote on it be decrypted (SendMessage, send.go).
		return cli.BuildPollCreation(p.Poll.Question, p.Poll.Options, 1), nil
	case p.File != nil:
		return document(ctx, cli, p.File)
	}
	ext := &waE2E.ExtendedTextMessage{Text: &p.Text}
	ctxInfo := &waE2E.ContextInfo{}
	set := false
	if len(p.Mentions) > 0 {
		ctxInfo.MentionedJID, set = p.Mentions, true
	}
	if q, ok := c.quote(p.Quote); p.Quote != "" && ok {
		ctxInfo.StanzaID = &p.Quote
		ctxInfo.Participant = new(q.sender.String())
		ctxInfo.QuotedMessage = &waE2E.Message{Conversation: &q.text}
		set = true
	}
	if set {
		ext.ContextInfo = ctxInfo
	}
	return &waE2E.Message{ExtendedTextMessage: ext}, nil
}

// document uploads a file and is the message naming it. WhatsApp keeps
// what was uploaded only as long as a message names it, so an upload whose
// send fails is uploaded again on the retry.
func document(ctx context.Context, cli Client, f *gwa.File) (*waE2E.Message, error) {
	up, err := cli.Upload(ctx, f.Data, whatsmeow.MediaDocument)
	if err != nil {
		return nil, fmt.Errorf("uploading %s: %w", f.Name, err)
	}
	doc := &waE2E.DocumentMessage{
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength,
		Mimetype: &f.MediaType, FileName: &f.Name, Title: &f.Name,
	}
	if f.Caption != "" {
		doc.Caption = &f.Caption
	}
	return &waE2E.Message{DocumentMessage: doc}, nil
}

// spoken is the words of a part, for how long typing them takes.
func spoken(p gwa.Part) string {
	switch {
	case p.Poll != nil:
		return p.Poll.Question
	case p.File != nil:
		return p.File.Caption
	}
	return p.Text
}

var sendable = map[string]bool{types.DefaultUserServer: true, types.HiddenUserServer: true, types.GroupServer: true}

var _ gateway.Poster = (*Accounts)(nil)
