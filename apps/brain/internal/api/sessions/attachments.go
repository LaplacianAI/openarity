package sessions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Objects is where a conversation's files are read, streamed: a large one is
// sent as it is opened, never held.
type Objects interface {
	Open(ctx context.Context, teamID uuid.UUID, key string) (io.ReadCloser, error)
}

var renderable = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"text/plain": true,
}

func (h *handler) listAttachments(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	session, ok := h.visible(w, r, teamID)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListAttachmentsBySessionParams{SessionID: session.ID, PeopleOnly: true, PageSize: limit + 1}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c attachmentCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor = true
		params.AfterCreatedAt = c.CreatedAt
		params.AfterID = c.ID
	}

	rows, err := h.store.ListAttachmentsBySession(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list attachments", err)
		return
	}

	for _, row := range rows {
		// people_only promises a message on every row; one without would
		// panic in the view below, so it is a failure instead.
		if row.MessageID == nil {
			api.Fail(w, h.logger, u, "failed to list attachments",
				fmt.Errorf("attachment %s has no message", row.ID))
			return
		}
	}

	page, err := api.MapPage(rows, limit,
		func(row db.Attachment) any {
			return attachmentCursor{CreatedAt: row.CreatedAt, ID: row.ID}
		},
		func(row db.Attachment) attachment {
			return attachment{
				// people_only: every row listed has its message.
				ID: row.ID, MessageID: *row.MessageID, Filename: row.Filename,
				MediaType: row.MediaType, SizeBytes: row.SizeBytes,
				CreatedAt: row.CreatedAt,
			}
		},
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page attachments", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) getAttachment(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	session, ok := h.visible(w, r, teamID)
	if !ok {
		return
	}

	id, err := uuid.Parse(r.PathValue("attachmentID"))
	if err != nil {
		http.Error(w, "attachmentID must be a uuid", http.StatusBadRequest)
		return
	}

	row, err := h.store.GetAttachmentInSession(r.Context(),
		db.GetAttachmentInSessionParams{ID: id, SessionID: session.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read attachment", err)
		return
	}

	body, err := h.objects.Open(r.Context(), teamID, row.ObjectKey)
	switch {
	case errors.Is(err, objects.ErrNotFound):
		api.Fail(w, h.logger, u, "attachment object is missing", err)
		return
	case err != nil:
		api.Fail(w, h.logger, u, "failed to read attachment object", err)
		return
	}
	defer func() { _ = body.Close() }()

	writeAttachment(w, row)
	// The status is sent: a chunk that fails now cuts the body short of its
	// length, which a client takes as a failed download, never a whole one.
	if n, err := io.Copy(w, body); err != nil && r.Context().Err() == nil {
		h.logger.ErrorContext(r.Context(), "an attachment was cut short",
			"attachment", row.ID, "sent", n, "size", row.SizeBytes, "error", err)
	}
}

func writeAttachment(w http.ResponseWriter, row db.Attachment) {
	w.Header().Set("Content-Type", row.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disposition(row.MediaType, row.Filename))
	w.Header().Set("Content-Length", strconv.FormatInt(row.SizeBytes, 10))
	w.Header().Set("Cache-Control", "private, no-store")

	w.WriteHeader(http.StatusOK)
}

func disposition(mediaType, filename string) string {
	kind := "attachment"
	if bare, _, err := mime.ParseMediaType(mediaType); err == nil && renderable[bare] {
		kind = "inline"
	}
	if filename == "" {
		return kind
	}
	return mime.FormatMediaType(kind, map[string]string{"filename": filename})
}
