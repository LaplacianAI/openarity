package contracts

import "time"

// Message is the one normalised shape every channel produces. Nothing
// downstream ever learns which channel a Message came from except by reading
// Channel.
//
// Field ownership is split and pinned by tests: an adapter's Parse fills the
// payload-derived fields (ConversationID, ProviderMessageID, ProviderUserID,
// Text, SentAt) and leaves the rest zero; the gateway handler fills the
// wiring-derived ones (Channel, ChannelID, TeamID) from the channel
// registration.
//
// Session and thread mapping are deliberately absent until a channel that
// supplies thread ids exists (Slack) — see HLD §Conversation vs Session.
type Message struct {
	Channel           string
	ChannelID         string
	TeamID            string
	ConversationID    string
	ProviderMessageID string
	ProviderUserID    string
	Text              string
	SentAt            time.Time
}
