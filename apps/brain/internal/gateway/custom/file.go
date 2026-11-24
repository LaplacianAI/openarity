package custom

import (
	"encoding/base64"
	"errors"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// RepliesWithFiles: a file a run sends is a reply of kind "file" carrying the
// file inline, signed like every reply. Inline rather than a link: a link
// would be a route on the brain that serves files to whoever holds it.
func (provider) RepliesWithFiles() {}

// replyFile is a file reply's file, its bytes in base64. A file is 25 MiB at
// most, so the body is about 34 MB at most.
type replyFile struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Size      int    `json:"size"`
	Caption   string `json:"caption,omitempty"`
	Data      string `json:"data"`
}

func fileOf(out gateway.Outbound) (*replyFile, error) {
	if out.Kind != "file" {
		return nil, nil //nolint:nilnil // only a file reply has a file
	}
	f := out.File
	if f == nil || len(f.Data) == 0 {
		return nil, errors.New("custom: a file reply with no file")
	}
	return &replyFile{
		Filename: f.Name, MediaType: f.MediaType, Size: len(f.Data), Caption: f.Caption,
		Data: base64.StdEncoding.EncodeToString(f.Data),
	}, nil
}
