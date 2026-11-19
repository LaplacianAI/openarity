// Package whatsapp holds WhatsApp numbers linked as devices: one connection
// per channel, leased so exactly one process holds each, pairing by QR or
// code, messages handed to the gateway as a webhook's are, and replies sent
// through the socket at a person's pace. The mapping of messages and replies
// is the gateway's (gateway/whatsapp), which is pure; everything with a
// client, a key or a clock is here.
package whatsapp

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Client is the part of a whatsmeow client this package uses, so a test can
// stand in for WhatsApp. *whatsmeow.Client is one.
type Client interface {
	Connect() error
	Disconnect()
	IsConnected() bool
	Logout(ctx context.Context) error
	AddEventHandler(handler whatsmeow.EventHandler) uint32
	GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error)
	PairPhone(ctx context.Context, phone string, showPushNotification bool, clientType whatsmeow.PairClientType, clientDisplayName string) (string, error)
	GenerateMessageID() types.MessageID
	SendMessage(ctx context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	DownloadAny(ctx context.Context, msg *waE2E.Message) ([]byte, error)
	BuildPollCreation(name string, optionNames []string, selectableOptionCount int) *waE2E.Message
	DecryptPollVote(ctx context.Context, vote *events.Message) (*waE2E.PollVoteMessage, error)
	ParseWebMessage(chatJID types.JID, webMsg *waWeb.WebMessageInfo) (*events.Message, error)
}

// Devices is where linked devices' keys are kept. *sqlstore.Container is one.
type Devices interface {
	NewDevice() *store.Device
	GetAllDevices(ctx context.Context) ([]*store.Device, error)
	DeleteDevice(ctx context.Context, device *store.Device) error
}

var _ Client = (*whatsmeow.Client)(nil)
