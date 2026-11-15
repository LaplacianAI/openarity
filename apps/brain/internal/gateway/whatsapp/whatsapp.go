// Package whatsapp is a WhatsApp number linked as a device, as WhatsApp Web
// is. It is pure: it turns a message the linked device received into an
// Inbound, and a reply into the parts to send. The connection itself, which
// has the client, the device's keys and the pacing, is internal/whatsapp's,
// held by the process that runs `brain whatsapp`.
package whatsapp

import (
	"errors"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

const Name = "whatsapp"

type provider struct{}

// New is the WhatsApp provider. It holds a connection (gateway.Socket), so it
// declares no webhook routes and no keys: the linked device's keys are in its
// own store, not the channel's secret.
func New() gateway.Provider { return provider{} }

func (provider) Name() string            { return Name }
func (provider) Routes() []gateway.Route { return nil }
func (provider) Keys() []string          { return nil }
func (provider) HoldsConnection()        {}
func (provider) RepliesWithEvents()      {}

var errNoWebhook = errors.New("whatsapp: messages arrive on the linked device's connection, never by webhook")

// Verify refuses everything: nothing is mounted for WhatsApp, and a request
// that somehow arrived has nothing to prove itself with.
func (provider) Verify(gateway.WebhookRequest, gateway.Credentials) error { return errNoWebhook }

func (provider) Parse(gateway.WebhookRequest, gateway.Credentials) (gateway.Result, error) {
	return gateway.Result{}, errNoWebhook
}
