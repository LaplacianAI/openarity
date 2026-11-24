package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/tools/mcp"
)

const (
	// keptHold is how long a tool file's key is held back from the reaper
	// between reserving it and its row committing, as the gateway holds a
	// person's.
	keptHold = time.Hour
	// rowTimeout bounds the row, which no longer follows the call.
	rowTimeout = 10 * time.Second
	// maxCallID is the most of a call's id the row takes, as the database
	// bounds it.
	maxCallID = 255
)

// ToolFileStore is what keeping a tool's file writes: its key reserved, and
// its row.
type ToolFileStore interface {
	ReserveObjects(ctx context.Context, arg db.ReserveObjectsParams) error
	CreateToolAttachment(ctx context.Context, arg db.CreateToolAttachmentParams) (db.Attachment, error)
}

// ObjectWriter seals and stores a file under its team's key.
type ObjectWriter interface {
	Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error
}

// ToolFiles keeps the files a run's MCP tools return as its conversation's
// attachments. Every field is required: a nil Logger panics, as the brain's
// loggers do.
type ToolFiles struct {
	Store   ToolFileStore
	Objects ObjectWriter
	Logger  *slog.Logger
}

// keeper keeps one run's tool files: each a conversation attachment, its
// source the run's call, its type read from its bytes.
type keeper struct {
	files              ToolFiles
	team, session, run uuid.UUID
}

func (f *ToolFiles) keeperFor(run db.Run) *keeper {
	return &keeper{files: *f, team: run.TeamID, session: run.SessionID, run: run.ID}
}

func (k *keeper) Keep(ctx context.Context, f mcp.File) (mcp.Kept, error) {
	kept, err := k.keep(ctx, f)
	if err != nil {
		// The model is told only that the file was not kept: the cause can
		// name the store.
		k.files.Logger.WarnContext(ctx, "a tool's file was not kept",
			"run", k.run, "tool", f.Tool, "bytes", len(f.Data), "err", err.Error())
	}
	return kept, err
}

func (k *keeper) keep(ctx context.Context, f mcp.File) (mcp.Kept, error) {
	switch {
	case len(f.Data) == 0:
		return mcp.Kept{}, errors.New("the file is empty")
	case len(f.Data) > mcp.MaxKeptBytes:
		return mcp.Kept{}, fmt.Errorf("the file is %d bytes, more than %d", len(f.Data), mcp.MaxKeptBytes)
	}
	call := agent.ToolCallID(ctx)
	switch {
	case call == "":
		return mcp.Kept{}, errors.New("the call that returned the file has no id")
	case len(call) > maxCallID:
		// Refused before anything is written, not after the bytes are.
		return mcp.Kept{}, fmt.Errorf("the call's id is %d bytes, more than %d", len(call), maxCallID)
	}

	// Kept whatever it is: the type stored is what its bytes are, never what
	// the server said, so the bytes route serves it as exactly that.
	mediaType := objects.Sniff(f.Data)
	key := objects.TeamPrefix(k.team) + "objects/" + uuid.New().String()
	// Reserved before the bytes are written, so an object whose row never
	// commits is the reaper's once the hold ends.
	if err := k.files.Store.ReserveObjects(ctx, db.ReserveObjectsParams{
		ObjectKeys: []string{key}, TeamID: k.team, HoldSeconds: keptHold.Seconds(),
	}); err != nil {
		return mcp.Kept{}, fmt.Errorf("reserving its key: %w", err)
	}
	if err := k.files.Objects.Put(ctx, k.team, key, f.Data); err != nil {
		return mcp.Kept{}, fmt.Errorf("storing it: %w", err)
	}
	// The bytes are stored: a call cut off now still records them, within
	// a deadline of its own.
	rowCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowTimeout)
	defer cancel()
	row, err := k.files.Store.CreateToolAttachment(rowCtx, db.CreateToolAttachmentParams{
		SessionID: k.session, TeamID: k.team, RunID: &k.run, Tool: &f.Tool, CallID: &call,
		ObjectKey: key, MediaType: mediaType, SizeBytes: int64(len(f.Data)), Filename: f.Name,
	})
	if err != nil {
		return mcp.Kept{}, fmt.Errorf("recording it: %w", err)
	}
	return mcp.Kept{ID: row.ID.String(), Ref: attachmentRef + row.ID.String()}, nil
}
