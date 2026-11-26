package whatsapp

import (
	"errors"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// RepliesWithFiles: a file a run sends is a document in the chat, uploaded
// and sent by the socket like any part.
func (provider) RepliesWithFiles() {}

// MaxFile is what a conversation keeps, 100 MiB: WhatsApp takes documents
// far larger.
func (provider) MaxFile() int64 { return 100 << 20 }

// document is a file reply as one part, quoting nothing, as a step does.
func document(to string, out gateway.Outbound) ([]gateway.OutboundRequest, error) {
	f := out.File
	if f == nil || f.Size <= 0 {
		return nil, errors.New("whatsapp: a file reply with no file")
	}
	req, err := request(Part{
		ID: out.ID + ":0", To: to,
		File: &File{Name: f.Name, MediaType: f.MediaType, Caption: f.Caption, Size: f.Size},
	})
	if err != nil {
		return nil, err
	}
	req.Stream = &gateway.Stream{Size: f.Size}
	return []gateway.OutboundRequest{req}, nil
}
