package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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

// ObjectWriter seals and stores a file under its team's key: whole, or as a
// stream it never holds.
type ObjectWriter interface {
	Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error
	PutStream(ctx context.Context, teamID uuid.UUID, key string, r io.Reader) error
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
	call, err := callOf(ctx)
	if err != nil {
		return mcp.Kept{}, err
	}

	// Kept whatever it is: the type stored is what its bytes are, never what
	// the server said, so the bytes route serves it as exactly that.
	mediaType := objects.Sniff(f.Data)
	key, err := k.reserve(ctx)
	if err != nil {
		return mcp.Kept{}, err
	}
	if err := k.files.Objects.Put(ctx, k.team, key, f.Data); err != nil {
		return mcp.Kept{}, fmt.Errorf("storing it: %w", err)
	}
	return k.record(ctx, call, f.Tool, f.Name, key, mediaType, int64(len(f.Data)))
}

// A streamed is a file kept from a stream: what it was kept as, and what was
// read of it.
type streamed struct {
	mcp.Kept
	MediaType string
	Size      int64
}

// KeepStream keeps a file read from r as it is read, never held whole: a
// room's file, of up to the size r stops at. Its type is read from its first
// bytes, as Keep reads it from all of them. A stream that fails leaves its
// key reserved, for the reaper.
func (k *keeper) KeepStream(ctx context.Context, tool, name string, r io.Reader) (streamed, error) {
	kept, err := k.keepStream(ctx, tool, name, r)
	if err != nil {
		k.files.Logger.WarnContext(ctx, "a tool's file was not kept",
			"run", k.run, "tool", tool, "bytes", kept.Size, "err", err.Error())
	}
	return kept, err
}

func (k *keeper) keepStream(ctx context.Context, tool, name string, r io.Reader) (streamed, error) {
	call, err := callOf(ctx)
	if err != nil {
		return streamed{}, err
	}
	br := bufio.NewReaderSize(r, sniffLen)
	head, err := br.Peek(sniffLen)
	switch {
	case len(head) == 0 && (err == nil || errors.Is(err, io.EOF)):
		return streamed{}, errEmpty
	case err != nil && !errors.Is(err, io.EOF):
		return streamed{}, err
	}
	mediaType := objects.Sniff(head)
	key, err := k.reserve(ctx)
	if err != nil {
		return streamed{}, err
	}
	counted := &countedReader{r: br}
	if err := k.files.Objects.PutStream(ctx, k.team, key, counted); err != nil {
		return streamed{Size: counted.n}, fmt.Errorf("storing it: %w", err)
	}
	kept, err := k.record(ctx, call, tool, name, key, mediaType, counted.n)
	return streamed{Kept: kept, MediaType: mediaType, Size: counted.n}, err
}

// errEmpty is a stream with nothing in it.
var errEmpty = errors.New("the file is empty")

// sniffLen is all of a file Sniff reads.
const sniffLen = 512

// countedReader counts what is read through it.
type countedReader struct {
	r io.Reader
	n int64
}

func (c *countedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// callOf is the id of the call the file came from, which its row records.
func callOf(ctx context.Context) (string, error) {
	call := agent.ToolCallID(ctx)
	switch {
	case call == "":
		return "", errors.New("the call that returned the file has no id")
	case len(call) > maxCallID:
		// Refused before anything is written, not after the bytes are.
		return "", fmt.Errorf("the call's id is %d bytes, more than %d", len(call), maxCallID)
	}
	return call, nil
}

// reserve is a fresh key for the file, reserved before the bytes are written,
// so an object whose row never commits is the reaper's once the hold ends.
func (k *keeper) reserve(ctx context.Context) (string, error) {
	key := objects.TeamPrefix(k.team) + "objects/" + uuid.New().String()
	if err := k.files.Store.ReserveObjects(ctx, db.ReserveObjectsParams{
		ObjectKeys: []string{key}, TeamID: k.team, HoldSeconds: keptHold.Seconds(),
	}); err != nil {
		return "", fmt.Errorf("reserving its key: %w", err)
	}
	return key, nil
}

// record is the file's row, once its bytes are stored: a call cut off now
// still records them, within a deadline of its own.
func (k *keeper) record(ctx context.Context, call, tool, name, key, mediaType string, size int64) (mcp.Kept, error) {
	rowCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowTimeout)
	defer cancel()
	row, err := k.files.Store.CreateToolAttachment(rowCtx, db.CreateToolAttachmentParams{
		SessionID: k.session, TeamID: k.team, RunID: &k.run, Tool: &tool, CallID: &call,
		ObjectKey: key, MediaType: mediaType, SizeBytes: size, Filename: name,
	})
	if err != nil {
		return mcp.Kept{}, fmt.Errorf("recording it: %w", err)
	}
	return mcp.Kept{ID: row.ID.String(), Ref: attachmentRef + row.ID.String()}, nil
}
