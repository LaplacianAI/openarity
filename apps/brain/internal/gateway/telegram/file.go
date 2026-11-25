package telegram

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"unicode"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// RepliesWithFiles: a file a run sends is a document in the chat.
func (provider) RepliesWithFiles() {}

// document is a file as sendDocument, multipart as the Bot API takes an
// upload. Always a document, never a photo: sendPhoto recompresses, and a
// person who asked for the file wants its bytes. The boundary is a hash of
// the reply's id and the file's bytes: the same reply builds the same body on
// every attempt, and no file can hold its own boundary to add a field.
func document(token string, chatID, thread int64, out gateway.Outbound) ([]gateway.OutboundRequest, error) {
	f := out.File
	if f == nil || len(f.Data) == 0 {
		return nil, errors.New("telegram: a file reply with no file")
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := sha256.New()
	h.Write([]byte(out.ID))
	h.Write([]byte{0})
	h.Write(f.Data)
	if err := w.SetBoundary(hex.EncodeToString(h.Sum(nil)[:16])); err != nil {
		return nil, fmt.Errorf("telegram: the document's boundary: %w", err)
	}
	fields := [][2]string{{"chat_id", strconv.FormatInt(chatID, 10)}}
	if thread != 0 {
		fields = append(fields, [2]string{"message_thread_id", strconv.FormatInt(thread, 10)})
	}
	if f.Caption != "" {
		fields = append(fields, [2]string{"caption", f.Caption})
	}
	for _, kv := range fields {
		if err := w.WriteField(kv[0], kv[1]); err != nil {
			return nil, fmt.Errorf("telegram: the document's %s: %w", kv[0], err)
		}
	}
	ph := textproto.MIMEHeader{}
	ph.Set("Content-Disposition", `form-data; name="document"; filename="`+quoteSafe(f.Name)+`"`)
	ph.Set("Content-Type", f.MediaType)
	part, err := w.CreatePart(ph)
	if err != nil {
		return nil, fmt.Errorf("telegram: the document's part: %w", err)
	}
	if _, err := part.Write(f.Data); err != nil {
		return nil, fmt.Errorf("telegram: the document's bytes: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("telegram: closing the document: %w", err)
	}
	header := http.Header{}
	header.Set("Content-Type", w.FormDataContentType())
	return []gateway.OutboundRequest{{URL: apiBase + token + "/sendDocument", Header: header, Body: body.Bytes()}}, nil
}

// quoteSafe is a name a quoted Content-Disposition filename can carry,
// escaped as mime/multipart escapes one: a quote or a backslash escaped, and
// no control character, as a line break would end the header.
func quoteSafe(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name)
}
