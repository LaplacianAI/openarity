package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/tools/mcp"
)

// fileRows records what keeping wrote, in order, and fails a row with err.
type fileRows struct {
	mu       sync.Mutex
	steps    []string
	reserved []db.ReserveObjectsParams
	rows     []db.CreateToolAttachmentParams
	err      error
}

func (f *fileRows) ReserveObjects(_ context.Context, arg db.ReserveObjectsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, "reserve")
	f.reserved = append(f.reserved, arg)
	return nil
}

func (f *fileRows) CreateToolAttachment(ctx context.Context, arg db.CreateToolAttachmentParams) (db.Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, "row")
	if ctx.Err() != nil {
		return db.Attachment{}, ctx.Err()
	}
	if f.err != nil {
		return db.Attachment{}, f.err
	}
	f.rows = append(f.rows, arg)
	return db.Attachment{ID: uuid.New()}, nil
}

// putOrder records a Put into rows' steps, so the order is one list.
type putOrder struct {
	*fakeObjects
	rows *fileRows
}

func (p putOrder) Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error {
	p.rows.mu.Lock()
	p.rows.steps = append(p.rows.steps, "put")
	p.rows.mu.Unlock()
	return p.fakeObjects.Put(ctx, teamID, key, body)
}

func keeperFixture() (*keeper, *fileRows, *fakeObjects) {
	rows, objs := &fileRows{}, &fakeObjects{}
	run := db.Run{ID: uuid.New(), TeamID: uuid.New(), SessionID: uuid.New()}
	files := &ToolFiles{Store: rows, Objects: putOrder{objs, rows}, Logger: quietLogger()}
	return files.keeperFor(run), rows, objs
}

// A tool's file is reserved, sealed under its team's key and recorded with
// its run's call, its type read from its bytes rather than the server's word.
func TestAToolsFileIsReservedStoredAndRecorded(t *testing.T) {
	t.Parallel()
	k, rows, objs := keeperFixture()
	ctx := agent.WithToolCallID(t.Context(), "call-7")

	got, err := k.Keep(ctx, mcp.File{Tool: "mail__fetch", Name: "bundle.png", MediaType: "image/png", Data: []byte("PK\x03\x04zip")})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if strings.Join(rows.steps, ",") != "reserve,put,row" {
		t.Errorf("steps = %v, want the key reserved, then the bytes, then the row", rows.steps)
	}
	r := rows.rows[0]
	if r.RunID == nil || *r.RunID != k.run || r.SessionID != k.session || r.TeamID != k.team ||
		*r.Tool != "mail__fetch" || *r.CallID != "call-7" || r.Filename != "bundle.png" || r.SizeBytes != 7 {
		t.Errorf("row = %+v, want the run's call and the file", r)
	}
	if r.MediaType != "application/zip" {
		t.Errorf("stored as %q, want what its bytes are", r.MediaType)
	}
	if res := rows.reserved[0]; len(res.ObjectKeys) != 1 || res.ObjectKeys[0] != r.ObjectKey || res.TeamID != k.team || res.HoldSeconds <= 0 {
		t.Errorf("reserved %+v, want the row's key held for the team", res)
	}
	if !strings.HasPrefix(r.ObjectKey, "teams/"+k.team.String()+"/objects/") || objs.data[r.ObjectKey] != "PK\x03\x04zip" {
		t.Errorf("the bytes went to %q under the team? %v", r.ObjectKey, objs.put)
	}
	if got.Ref != attachmentRef+got.ID || got.ID == "" {
		t.Errorf("kept as %+v, want the row's id and its reference", got)
	}
}

// What cannot be kept is refused before anything is written: no call, no
// bytes, too many bytes.
func TestAFileThatCannotBeKeptWritesNothing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		call string
		data []byte
	}{
		"no call id":                        {"", []byte("hi")},
		"empty":                             {"c", nil},
		"over the cap":                      {"c", make([]byte, mcp.MaxKeptBytes+1)},
		"a call id past what the row takes": {strings.Repeat("c", maxCallID+1), []byte("hi")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			k, rows, _ := keeperFixture()
			ctx := agent.WithToolCallID(t.Context(), tc.call)
			if _, err := k.Keep(ctx, mcp.File{Tool: "t", Name: "f", Data: tc.data}); err == nil {
				t.Fatal("kept it")
			}
			if len(rows.steps) != 0 {
				t.Errorf("wrote %v, want nothing", rows.steps)
			}
		})
	}
}

// A row that fails leaves the key reserved, so the reaper takes the bytes; a
// store that fails writes no row.
func TestAFailedKeepLeavesNothingTheReaperWillNotTake(t *testing.T) {
	t.Parallel()
	k, rows, _ := keeperFixture()
	rows.err = errors.New("the database is gone")
	ctx := agent.WithToolCallID(t.Context(), "c")
	if _, err := k.Keep(ctx, mcp.File{Tool: "t", Name: "f", Data: []byte("x")}); err == nil {
		t.Fatal("a failed row was kept")
	}
	if strings.Join(rows.steps, ",") != "reserve,put,row" || len(rows.reserved) != 1 {
		t.Errorf("steps %v, want the reservation standing for the reaper", rows.steps)
	}

	k, rows, objs := keeperFixture()
	objs.putErr = errors.New("the bucket is gone")
	if _, err := k.Keep(ctx, mcp.File{Tool: "t", Name: "f", Data: []byte("x")}); err == nil {
		t.Fatal("a failed store was kept")
	}
	if strings.Join(rows.steps, ",") != "reserve,put" {
		t.Errorf("steps %v, want no row for bytes not stored", rows.steps)
	}
}

// The row is written even when the call was cut off after the bytes were
// stored, so the file is recorded rather than left to the reaper.
func TestAFileStoredBeforeTheCallEndedIsRecorded(t *testing.T) {
	t.Parallel()
	k, rows, objs := keeperFixture()
	ctx, cancel := context.WithCancel(agent.WithToolCallID(t.Context(), "c"))
	objs.onPut = cancel
	if _, err := k.Keep(ctx, mcp.File{Tool: "t", Name: "f", Data: []byte("x")}); err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if len(rows.rows) != 1 {
		t.Errorf("recorded %d rows, want the stored file's", len(rows.rows))
	}
}

// Without tool files, a run's servers have no keeper: a nil interface, not
// one holding a nil keeper, which the SDK would call.
func TestNoToolFilesIsNoKeeper(t *testing.T) {
	t.Parallel()
	if k := (&Builder{}).keeperFor(db.Run{}); k != nil {
		t.Errorf("keeperFor = %#v, want nil", k)
	}
	if k := (&Builder{}).WithToolFiles(ToolFiles{}).keeperFor(db.Run{ID: uuid.New()}); k == nil {
		t.Error("with tool files, no keeper")
	}
}
