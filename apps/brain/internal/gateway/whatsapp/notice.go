package whatsapp

import (
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// Notice tells a sender nobody has let in where they stand, quoting the
// message it answers. In a group it mentions them, so nobody else takes it
// as theirs; in a direct chat their name is enough.
func (provider) Notice(_ gateway.Channel, n gateway.Notice, _ gateway.Credentials) (gateway.OutboundRequest, error) {
	if n.Text == "" {
		return gateway.OutboundRequest{}, fmt.Errorf("whatsapp: no words for a %q notice", n.Kind)
	}
	to, err := conversation(n.Session.Ref)
	if err != nil {
		return gateway.OutboundRequest{}, err
	}
	p := Part{ID: n.ID, To: to, Quote: n.InReplyTo}
	who, err := types.ParseJID(n.Sender.Ref)
	switch {
	case n.Session.Kind != gateway.SessionDirect && err == nil && who.User != "":
		// WhatsApp draws "@<number>" as the person when the address is named.
		p.Text = "@" + who.User + " " + n.Text
		p.Mentions = []string{who.ToNonAD().String()}
	case strings.TrimSpace(n.Sender.DisplayName) != "":
		p.Text = n.Sender.DisplayName + ", " + n.Text
	default:
		p.Text = "Hello, " + n.Text
	}
	if p.Text == "" {
		return gateway.OutboundRequest{}, errors.New("whatsapp: an empty notice")
	}
	return request(p)
}
