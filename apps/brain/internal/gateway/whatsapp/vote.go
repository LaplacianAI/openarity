package whatsapp

import (
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// VoteText is what a vote reads as in the conversation.
const VoteText = "Voted in the poll"

// MapVote turns a poll vote the linked device received into an Inbound that
// carries the vote as an Answer, or false for one that is not kept: a vote
// that chose nothing (a retracted choice), one in a broadcast list or a
// channel, and the owner's own when the device does not know its number.
//
// hashes are the SHA-256 of each option chosen, as the holder decrypted
// them: decrypting needs the client, so it is not done here. The Answer names
// no request. The gateway finds it by hashing the options of those waiting
// in the conversation, and decides whether the voter may answer it; nothing
// here authorises. The vote lands in the session the chat's messages do, by
// the same rules as Map.
func MapVote(m *events.Message, self Self, hashes [][]byte) (gateway.Inbound, bool) {
	if m == nil || len(hashes) == 0 {
		return gateway.Inbound{}, false
	}
	own := m.Info.IsFromMe
	if own && self.PN.IsEmpty() {
		return gateway.Inbound{}, false
	}
	chat := m.Info.Chat
	switch chat.Server {
	case types.BroadcastServer, types.NewsletterServer:
		return gateway.Inbound{}, false
	}
	toSelf := own && !m.Info.IsGroup && self.is(chat)
	author := phoneFirst(m.Info.Sender, m.Info.SenderAlt)
	if own {
		author = self.PN
	}
	return gateway.Inbound{
		ExternalID: m.Info.ID,
		Author:     gateway.Author{Ref: author.String(), DisplayName: m.Info.PushName},
		Text:       VoteText,
		SentAt:     m.Info.Timestamp,
		ToSelf:     toSelf,
		Session:    place(m, author, own, toSelf),
		Answer:     &gateway.Answer{Hashes: hashes},
	}, true
}
