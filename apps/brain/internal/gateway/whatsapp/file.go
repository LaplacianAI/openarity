package whatsapp

import (
	"errors"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// RepliesWithFiles: a file a run sends is a document in the chat, uploaded
// and sent by the socket like any part.
func (provider) RepliesWithFiles() {}

// document is a file reply as one part, quoting nothing, as a step does.
func document(to string, out gateway.Outbound) ([]gateway.OutboundRequest, error) {
	f := out.File
	if f == nil || len(f.Data) == 0 {
		return nil, errors.New("whatsapp: a file reply with no file")
	}
	req, err := request(Part{
		ID: out.ID + ":0", To: to,
		File: &File{Name: f.Name, MediaType: f.MediaType, Caption: f.Caption, Data: f.Data},
	})
	if err != nil {
		return nil, err
	}
	return []gateway.OutboundRequest{req}, nil
}
