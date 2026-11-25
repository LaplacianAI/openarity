package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/roomfile"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/workroom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/tools/mcp"
)

const (
	// KeepFileToolName keeps a file a tool wrote in its room as an
	// attachment of the conversation.
	KeepFileToolName = "keep_file"
	// keptPerRun is how many files one run keeps from its rooms.
	keptPerRun = 20
	// maxPath is the longest path taken: any real one is far shorter.
	maxPath = 4096
	// keepTimeout bounds reading one file from its room: 25 MiB at a slow
	// megabyte a second, and then some.
	keepTimeout = 2 * time.Minute
)

// RoomFileStore is what keeping a room's file reads beyond F2's keeper: how
// many the run has kept before.
type RoomFileStore interface {
	CountRunKeptFiles(ctx context.Context, runID uuid.UUID) (int64, error)
}

// ServerRooms is where a command MCP server's room hands over its files: the
// room as it runs now, or sandbox.ErrNoRoom.
type ServerRooms interface {
	Files(ctx context.Context, team uuid.UUID, s db.McpServer) (sandbox.Endpoint, error)
}

// RoomFiles lets a run keep a file a tool wrote in a room. Store is required.
// Servers and Client are a command server's rooms and the client that calls
// them, with the policy a command server is called under; without them, only
// the workspace's files are kept.
type RoomFiles struct {
	Store   RoomFileStore
	Servers ServerRooms
	Client  *http.Client
}

// WithRoomFiles gives runs keep_file, wherever F2's keeper keeps tool files.
func (b *Builder) WithRoomFiles(r RoomFiles) *Builder {
	b.roomFiles = &r
	return b
}

// keepFile is the tool: a file read from the run's workspace or one of its
// command servers' rooms, then kept as F2 keeps a tool's file.
type keepFile struct {
	keep    mcp.Keeper
	ws      *runWorkspace // nil when the run has no workspace
	servers map[string]db.McpServer
	rooms   ServerRooms
	client  *http.Client
	team    uuid.UUID
	kept    atomic.Int64
}

func (k *keepFile) tool() agent.Tool {
	var from []string
	if k.ws != nil || k.keep == nil {
		from = append(from, "the workspace (leave server out; a path is from "+workroom.Dir+")")
	}
	if len(k.servers) > 0 || k.keep == nil {
		from = append(from, "a command MCP server's room (server is its name; a path is from where it runs)")
	}
	return agent.Tool{
		Name: KeepFileToolName,
		Description: fmt.Sprintf("Keep a file a tool wrote, in %s, as a file of this conversation: "+
			"it returns the file's id, which send_file sends to the person and read_attachment reads. "+
			"Use it when a tool saved a file and said where rather than returning it. "+
			"A file is kept up to 25 MiB, and %d a run.", strings.Join(from, " or "), keptPerRun),
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"the file's path in the room"},` +
			`"server":{"type":"string","description":"the MCP server whose room it is in; leave out for the workspace"}},` +
			`"required":["path"],"additionalProperties":false}`),
		Invoke: k.invoke,
	}
}

func (k *keepFile) invoke(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Path   string `json:"path"`
		Server string `json:"server"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("the arguments are not what %s takes, a path and a server, both strings: %w", KeepFileToolName, err)
	}
	p := strings.TrimSpace(a.Path)
	switch {
	case p == "":
		return "", errors.New("path is empty: name the file the tool said it wrote")
	case len(p) > maxPath:
		return "", fmt.Errorf("the path is %d bytes; no file's is", len(p))
	}
	read, err := k.reader(strings.TrimSpace(a.Server))
	if err != nil {
		return "", err
	}
	// Counted once the room is known, so a server named wrong costs nothing;
	// a read that then fails still does, so a room that is down is not asked
	// a twenty-first time.
	if k.kept.Add(1) > keptPerRun {
		return "", fmt.Errorf("this run has tried to keep %d files, the most one run keeps; say where the rest are instead", keptPerRun)
	}
	reading, cancel := context.WithTimeout(ctx, keepTimeout)
	data, err := read(reading, p)
	cancel()
	if err != nil {
		return "", err
	}
	name := path.Base(p)
	kept, err := k.keep.Keep(ctx, mcp.File{Tool: KeepFileToolName, Name: name, Data: data})
	if err != nil {
		return "", fmt.Errorf("%s was read but could not be kept", p)
	}
	return fmt.Sprintf("Kept %s (%s, %s) as file %s.", name, objects.Sniff(data), sizeOf(int64(len(data))), kept.ID), nil
}

// reader is how the file is read: from the workspace with no server, or from
// the named server's room, when it is one of the run's command servers.
func (k *keepFile) reader(server string) (func(context.Context, string) ([]byte, error), error) {
	if server == "" {
		if k.ws == nil {
			return nil, errors.New("this run has no workspace; name the MCP server whose room the file is in")
		}
		return k.fromWorkspace, nil
	}
	s, ok := k.servers[server]
	if !ok {
		return nil, fmt.Errorf("this run has no command MCP server named %q whose room could hold the file", server)
	}
	return func(ctx context.Context, p string) ([]byte, error) { return k.fromServer(ctx, s, p) }, nil
}

func (k *keepFile) fromWorkspace(ctx context.Context, p string) ([]byte, error) {
	var data []byte
	_, err := k.ws.call(ctx, func(c *workroom.Client) (string, error) {
		var err error
		data, err = c.File(ctx, p)
		return "", err
	})
	if errors.Is(err, sandbox.ErrReplaced) {
		return nil, fmt.Errorf("%s is gone: the workspace was replaced by a fresh one, without its files", p)
	}
	return data, err
}

func (k *keepFile) fromServer(ctx context.Context, s db.McpServer, p string) ([]byte, error) {
	ep, err := k.rooms.Files(ctx, k.team, s)
	if errors.Is(err, sandbox.ErrNoRoom) {
		return nil, fmt.Errorf("%s is gone: %s's room is not running, and a room's files go with it. "+
			"Call the tool that saved it again, then keep it", p, s.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("%s's room could not be reached: %w", s.Name, err)
	}
	data, err := roomfile.Get(ctx, k.client, ep, p)
	var prob *roomfile.Problem
	switch {
	case errors.As(err, &prob):
		return nil, errors.New(prob.Msg)
	case errors.Is(err, roomfile.ErrGone):
		return nil, fmt.Errorf("%s is gone: %s's room went away, and a room's files go with it", p, s.Name)
	case err != nil:
		return nil, fmt.Errorf("%s could not be read from %s's room: %w", p, s.Name, err)
	}
	return data, nil
}

// keepFileFor is the tool for a run that can keep a file from a room: one
// with a workspace or a command server, where F2's keeper keeps tool files.
// A replay holds a stand-in wherever a run might, as its rooms have moved on.
func (b *Builder) keepFileFor(
	ctx context.Context, run db.Run, ws *runWorkspace, work bool, commands map[string]db.McpServer,
) (agent.Tool, bool, error) {
	if !work && len(commands) == 0 {
		return agent.Tool{}, false, nil
	}
	if b.replay {
		return (&keepFile{}).tool(), true, nil
	}
	keep := b.keeperFor(run)
	if keep == nil || b.roomFiles == nil {
		return agent.Tool{}, false, nil
	}
	k := &keepFile{keep: keep, ws: ws, team: run.TeamID, rooms: b.roomFiles.Servers, client: b.roomFiles.Client}
	if k.rooms != nil && k.client != nil {
		k.servers = commands
	}
	if k.ws == nil && len(k.servers) == 0 {
		return agent.Tool{}, false, nil
	}
	n, err := b.roomFiles.Store.CountRunKeptFiles(ctx, run.ID)
	if err != nil {
		return agent.Tool{}, false, fmt.Errorf("reading how many files the run has kept: %w", err)
	}
	k.kept.Store(n)
	return k.tool(), true, nil
}
