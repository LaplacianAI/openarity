package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

const (
	AttachmentsToolName    = "attachments"
	ReadAttachmentToolName = "read_attachment"

	// What a model is sent of a file: images up to 5 MB, a PDF or a text file
	// up to 10 MB, and no more than eight files in one request.
	maxImageBytes   = 5 << 20
	maxDocBytes     = 10 << 20
	filesPerRequest = 8
	listedFiles     = 50

	// attachmentRef prefixes a Blob's Ref naming an attachment, so a saved
	// transcript holds the name and never the bytes.
	attachmentRef = "attachment:"
)

// AttachmentStore is what reading a conversation's files takes: what it
// holds, and one of them, looked up within it so a file from another
// conversation is a missing row.
type AttachmentStore interface {
	ListAttachmentsBySession(ctx context.Context, arg db.ListAttachmentsBySessionParams) ([]db.Attachment, error)
	GetAttachmentInSession(ctx context.Context, arg db.GetAttachmentInSessionParams) (db.Attachment, error)
}

func attachmentTools(s AttachmentStore, session uuid.UUID) []agent.Tool {
	return []agent.Tool{listAttachments(s, session), readAttachment(s, session)}
}

type listedFile struct {
	ID        uuid.UUID `json:"id"`
	Filename  string    `json:"filename"`
	MediaType string    `json:"media_type"`
	Size      string    `json:"size"`
	Sent      string    `json:"sent"`
}

func listAttachments(s AttachmentStore, session uuid.UUID) agent.Tool {
	return agent.Tool{
		Name: AttachmentsToolName,
		Description: "List the files sent in this conversation, newest first: each one's id, name, type and size. " +
			"Read one with " + ReadAttachmentToolName + ".",
		Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Invoke: func(ctx context.Context, _ json.RawMessage) (string, error) {
			rows, err := s.ListAttachmentsBySession(ctx, db.ListAttachmentsBySessionParams{
				SessionID: session, PageSize: listedFiles + 1,
			})
			if err != nil {
				return "", fmt.Errorf("listing the conversation's files: %w", err)
			}
			out := struct {
				Files []listedFile `json:"files"`
				More  bool         `json:"more,omitempty"`
			}{Files: make([]listedFile, 0, min(len(rows), listedFiles))}
			for i, r := range rows {
				if i == listedFiles {
					out.More = true
					break
				}
				out.Files = append(out.Files, listedFile{
					ID: r.ID, Filename: r.Filename, MediaType: r.MediaType,
					Size: sizeOf(r.SizeBytes), Sent: r.CreatedAt.UTC().Format(time.RFC3339),
				})
			}
			b, err := json.Marshal(out)
			return string(b), err
		},
	}
}

func readAttachment(s AttachmentStore, session uuid.UUID) agent.Tool {
	return agent.Tool{
		Name: ReadAttachmentToolName,
		Description: "Look at a file sent in this conversation, by the id " + AttachmentsToolName +
			" or the message gave: an image or a PDF is shown to you, a text file is given as text.",
		Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"the file's id"}},` +
			`"required":["id"],"additionalProperties":false}`),
		Result: func(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
			var a struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return agent.ToolResult{}, fmt.Errorf("the arguments are not valid JSON: %w", err)
			}
			id, err := uuid.Parse(strings.TrimSpace(a.ID))
			if err != nil {
				return agent.ToolResult{}, fmt.Errorf("%q is not a file's id; %s lists them", a.ID, AttachmentsToolName)
			}

			row, err := s.GetAttachmentInSession(ctx, db.GetAttachmentInSessionParams{ID: id, SessionID: session})
			if errors.Is(err, pgx.ErrNoRows) {
				return agent.ToolResult{}, fmt.Errorf("there is no file %s in this conversation; %s lists them", id, AttachmentsToolName)
			}
			if err != nil {
				return agent.ToolResult{}, fmt.Errorf("reading the file %s: %w", id, err)
			}

			part, err := readable(row)
			if err != nil {
				return agent.ToolResult{}, err
			}
			return agent.ToolResult{Output: describe(row), Attach: []agent.Content{part}}, nil
		},
	}
}

// readable is the part a file reaches a model as, by reference, or why it
// cannot. Its bytes are not read here: the model client fetches them for
// each request.
func readable(row db.Attachment) (agent.Content, error) {
	blob := &agent.Blob{MediaType: row.MediaType, Name: row.Filename, Ref: attachmentRef + row.ID.String()}
	switch kind := bareType(row.MediaType); {
	case strings.HasPrefix(kind, "image/") && kind != "image/svg+xml":
		if row.SizeBytes > maxImageBytes {
			return agent.Content{}, fmt.Errorf("%s is %s, more than the %s an image is read up to", row.Filename, sizeOf(row.SizeBytes), sizeOf(maxImageBytes))
		}
		return agent.Content{Type: agent.ContentImage, Blob: blob}, nil
	case kind == "application/pdf" || kind == "text/plain":
		if row.SizeBytes > maxDocBytes {
			return agent.Content{}, fmt.Errorf("%s is %s, more than the %s a PDF or a text file is read up to", row.Filename, sizeOf(row.SizeBytes), sizeOf(maxDocBytes))
		}
		return agent.Content{Type: agent.ContentFile, Blob: blob}, nil
	default:
		return agent.Content{}, fmt.Errorf("%s is %s, which a model cannot read", row.Filename, kind)
	}
}

func bareType(mediaType string) string {
	kind, _, _ := strings.Cut(mediaType, ";")
	return strings.ToLower(strings.TrimSpace(kind))
}

func describe(row db.Attachment) string {
	return fmt.Sprintf("%s, %s, %s", row.Filename, bareType(row.MediaType), sizeOf(row.SizeBytes))
}

func sizeOf(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// filesOf is what a run's first message says of the files sent with it: every
// one named with its id, and the images among them shown, by reference.
func filesOf(files []db.Attachment) []agent.Content {
	if len(files) == 0 {
		return nil
	}
	names := make([]string, 0, len(files))
	var shown []agent.Content
	for _, f := range files {
		names = append(names, fmt.Sprintf("%s (%s, %s, id %s)", f.Filename, bareType(f.MediaType), sizeOf(f.SizeBytes), f.ID))
		if part, err := readable(f); err == nil && part.Type == agent.ContentImage {
			shown = append(shown, part)
		}
	}
	note := "Sent with this message: " + strings.Join(names, ", ") + ". Read one with " + ReadAttachmentToolName + "."
	return append([]agent.Content{{Type: agent.ContentText, Text: note}}, shown...)
}
