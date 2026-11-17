package whatsapp

import (
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// Self is the linked device's own addresses, by phone number and by LID. A
// mention of either is a mention of us.
type Self struct {
	PN  types.JID
	LID types.JID
}

func (s Self) is(j types.JID) bool {
	return (!s.PN.IsEmpty() && j.User == s.PN.User && j.Server == s.PN.Server) ||
		(!s.LID.IsEmpty() && j.User == s.LID.User && j.Server == s.LID.Server)
}

// Map turns a message the linked device received into an Inbound, or false
// for one that is not kept:
//
//   - the owner's own, typed on the phone or another of its devices, except
//     one that holds the channel's trigger word, which is the owner calling
//     the bot in that chat, and, on a channel with no word, one in "Message
//     yourself", the chat with the number itself: anything else is the owner talking to someone, and
//     nobody asked the bot. One the holder sent itself is the caller's to
//     drop, since only it knows its own;
//   - a status, a broadcast list or a channel (newsletter), which are not
//     conversations;
//   - an edit, which would answer the same message twice;
//   - a reaction, a deletion and the other protocol messages, which say
//     nothing to answer;
//   - one with neither words nor a file.
//
// historical marks one WhatsApp sent when the number was linked, from
// before: stored, never answered. The owner's own from before are not kept
// at all: "Message yourself" holds the bot's old answers too, and the holder
// no longer knows which were its own.
//
// trigger is the channel's trigger word; empty is none. shared is a number
// other channels link too: every message reaches each of them, so the word
// is what everyone's message must hold, not only the owner's, or each
// channel would answer each stranger.
func Map(m *events.Message, self Self, trigger string, historical, shared bool) (gateway.Inbound, bool) {
	if m == nil || m.Message == nil || m.IsEdit {
		return gateway.Inbound{}, false
	}
	chat := m.Info.Chat
	own := m.Info.IsFromMe
	if own && (historical || self.PN.IsEmpty()) {
		return gateway.Inbound{}, false
	}
	toSelf := own && !m.Info.IsGroup && self.is(chat)
	switch chat.Server {
	case types.BroadcastServer, types.NewsletterServer:
		return gateway.Inbound{}, false
	}
	// A reaction, a deletion and the other protocol messages carry neither
	// words nor a file here, so the check below drops them too.
	text, ctx := body(m.Message)
	files := filesOf(m.Info.ID, m.Message)
	if strings.TrimSpace(text) == "" && len(files) == 0 {
		return gateway.Inbound{}, false
	}
	// The owner's own asks only by the word, "Message yourself" included once
	// a word is set; without one, only "Message yourself" does.
	if own && (trigger != "" || !toSelf) && !gateway.HasWord(text, trigger) {
		return gateway.Inbound{}, false
	}
	// History too: a team has no business keeping what was never sent to it.
	if shared && !gateway.HasWord(text, trigger) {
		return gateway.Inbound{}, false
	}

	author := phoneFirst(m.Info.Sender, m.Info.SenderAlt)
	if own {
		// The owner by their number, whichever of their devices typed it, so
		// they are one sender to approve wherever they call the bot.
		author = self.PN
	}
	in := gateway.Inbound{
		ExternalID:  m.Info.ID,
		Author:      gateway.Author{Ref: author.String(), DisplayName: m.Info.PushName},
		Text:        text,
		Attachments: files,
		ReplyTo:     ctx.GetStanzaID(),
		SentAt:      m.Info.Timestamp,
		Historical:  historical,
		ToSelf:      toSelf,
	}
	if m.Info.IsGroup {
		in.Session = gateway.Session{Ref: chat.ToNonAD().String(), Kind: gateway.SessionGroup}
	} else {
		// A direct chat is the other person, named as their sender is, so a
		// chat that arrives by LID and one by number are one conversation.
		// The owner's own is to the person the chat is with.
		other := author
		if own && !toSelf {
			other = phoneFirst(chat, m.Info.RecipientAlt)
		}
		in.Session = gateway.Session{Ref: other.String(), Kind: gateway.SessionDirect}
	}
	for _, raw := range ctx.GetMentionedJID() {
		// A mention is of a person; anything else in the list is not one,
		// and must not cost the message its place.
		j, err := types.ParseJID(raw)
		if err != nil || j.User == "" || (j.Server != types.DefaultUserServer && j.Server != types.HiddenUserServer) {
			continue
		}
		in.Mentions = append(in.Mentions, gateway.Mention{SenderRef: j.ToNonAD().String(), IsUs: self.is(j)})
	}
	return in, true
}

// phoneFirst is a person by their number when WhatsApp gave one, since a LID
// is a second name for the same person: one sender, not two.
func phoneFirst(j, alt types.JID) types.JID {
	if j.Server == types.HiddenUserServer && alt.Server == types.DefaultUserServer {
		j = alt
	}
	return j.ToNonAD()
}

// body is a message's words and the context its mentions and quote are in,
// whichever kind of message carries them.
func body(m *waE2E.Message) (string, *waE2E.ContextInfo) {
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText(), m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetCaption(), m.GetImageMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetCaption(), m.GetDocumentMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetCaption(), m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return "", m.GetAudioMessage().GetContextInfo()
	default:
		return m.GetConversation(), nil
	}
}

// FileRef names a message's file for the process holding the connection,
// which downloads it from the message: one file per message.
func FileRef(id types.MessageID) string { return id + ":0" }

// filesOf is the file a message carries, named as Telegram's are when
// WhatsApp gives no name. A sticker is not a file anyone sent to be read.
func filesOf(id types.MessageID, m *waE2E.Message) []gateway.Attachment {
	file := func(name, mediaType string, size uint64) []gateway.Attachment {
		return []gateway.Attachment{{
			Ref: FileRef(id), ClaimedFilename: name, ClaimedMediaType: mediaType,
			ClaimedSize: int64(min(size, 1<<62)), //nolint:gosec // G115: clamped
		}}
	}
	switch {
	case m.GetImageMessage() != nil:
		x := m.GetImageMessage()
		return file("photo-"+id+".jpg", x.GetMimetype(), x.GetFileLength())
	case m.GetDocumentMessage() != nil:
		x := m.GetDocumentMessage()
		return file(x.GetFileName(), x.GetMimetype(), x.GetFileLength())
	case m.GetVideoMessage() != nil:
		x := m.GetVideoMessage()
		return file("video-"+id+".mp4", x.GetMimetype(), x.GetFileLength())
	case m.GetAudioMessage() != nil:
		x := m.GetAudioMessage()
		return file("audio-"+id+".ogg", x.GetMimetype(), x.GetFileLength())
	default:
		return nil
	}
}
