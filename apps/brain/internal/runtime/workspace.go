package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/workroom"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// The workspace's tools, named as Claude Code names them, so a model that
// knows those knows these.
const (
	BashToolName  = "Bash"
	ReadToolName  = "Read"
	WriteToolName = "Write"
	EditToolName  = "Edit"
	GlobToolName  = "Glob"
	GrepToolName  = "Grep"
)

// WorkspaceToolNames are the workspace's tools, which a run with one
// reserves.
var WorkspaceToolNames = []string{BashToolName, ReadToolName, WriteToolName, EditToolName, GlobToolName, GrepToolName}

// Workspaces is where a session's workspace is: its room, made the first
// time it is needed.
type Workspaces interface {
	Ensure(ctx context.Context, team, session uuid.UUID) (sandbox.Endpoint, error)
	Touch(ctx context.Context, session uuid.UUID) error
}

type WorkspaceDeps struct {
	Rooms Workspaces
	// Client calls the room; its policy decides which addresses a room may
	// be at, as for a command MCP server's.
	Client *http.Client
}

// WithWorkspaces lets runs of agents granted the workspace have one. Without
// it, no run does.
func (b *Builder) WithWorkspaces(d *WorkspaceDeps) *Builder {
	b.workspaces = d
	return b
}

// workspaceClass is a workspace tool's class. Bash can send anything to any
// public host, so it is a write; the rest read and change only the session's
// own room, which nothing outside it sees.
func workspaceClass(name string) approval.Class {
	if name == BashToolName {
		return approval.Write
	}
	return approval.ClassFor(approval.Room, approval.Actor{})
}

// runWorkspace is a run's way into its session's workspace: the room ensured
// by the first call, and only marked used by the calls after it.
type runWorkspace struct {
	deps          WorkspaceDeps
	team, session uuid.UUID

	mu     sync.Mutex
	client *workroom.Client
}

// open is the workspace's client, ensuring its room on the first call.
func (w *runWorkspace) open(ctx context.Context) (*workroom.Client, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.client != nil {
		// The call about to be made finds out whether the room answers; this
		// only keeps the sweep of idle rooms off it. A failed touch costs at
		// most a room, which the next call makes again.
		_ = w.deps.Rooms.Touch(ctx, w.session)
		return w.client, nil
	}
	ep, err := w.deps.Rooms.Ensure(ctx, w.team, w.session)
	if err != nil {
		return nil, fmt.Errorf("the workspace could not be started: %w", err)
	}
	w.client = workroom.NewClient(ep, w.deps.Client)
	return w.client, nil
}

// forget drops the client of a room that has gone, so the next call ensures
// a new one.
func (w *runWorkspace) forget(c *workroom.Client) {
	w.mu.Lock()
	if w.client == c {
		w.client = nil
	}
	w.mu.Unlock()
}

// call runs fn against the workspace. A room found gone is never retried:
// an Edit or a Bash sent twice could do its work twice. The model is told,
// and its next call starts a fresh room.
func (w *runWorkspace) call(ctx context.Context, fn func(*workroom.Client) (string, error)) (string, error) {
	c, err := w.open(ctx)
	if err != nil {
		return "", err
	}
	out, err := fn(c)
	var p *workroom.Problem
	switch {
	case err == nil:
		return out, nil
	case errors.As(err, &p):
		return "", errors.New(p.Msg)
	case errors.Is(err, workroom.ErrGone):
		w.forget(c)
		return "", errors.New("the workspace's room was gone, so the call did not reach it; it was " +
			"cleared, as an idle workspace is. Call again to start a fresh one: what was in it before is not there")
	default:
		return "", err
	}
}

// workspaceTools are the six tools over w.
func (w *runWorkspace) tools() []agent.Tool {
	return []agent.Tool{
		{
			Name: BashToolName,
			Description: "Run a command with bash in the session's workspace, " + workroom.Dir + ": a Linux sandbox " +
				"with python3, node, npm, uv, git and curl, which reaches the public internet and nothing private. " +
				"Every agent working on this conversation shares it, and its files stay while it is in use; an idle " +
				"one is cleared. Each call starts in " + workroom.Dir + " in a fresh shell: cd and exported variables " +
				"do not carry over, and anything left running in the background is stopped when the command ends. " +
				"stdout and stderr are each cut to their first and last 128 KB.",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"command":{"type":"string","description":"The command, run with bash -c"},` +
				`"timeout_seconds":{"type":"integer","minimum":1,"maximum":270,"description":"Stop it after this long; 120 if left out"}},` +
				`"required":["command"]}`),
			Invoke: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct {
					Command        string `json:"command"`
					TimeoutSeconds int    `json:"timeout_seconds"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return "", fmt.Errorf("the arguments are not the JSON Bash takes: %w", err)
				}
				return w.call(ctx, func(c *workroom.Client) (string, error) {
					out, err := c.Exec(ctx, in.Command, in.TimeoutSeconds)
					return execText(out), err
				})
			},
		},
		{
			Name: ReadToolName,
			Description: "Read a text file in the workspace, its lines numbered as cat -n numbers them, 2,000 at a " +
				"time from offset. A path is relative to " + workroom.Dir + " unless it starts with /.",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"path":{"type":"string"},` +
				`"offset":{"type":"integer","minimum":1,"description":"The first line to read, from 1"},` +
				`"limit":{"type":"integer","minimum":1,"description":"How many lines; 2000 if left out"}},` +
				`"required":["path"]}`),
			Invoke: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct {
					Path   string `json:"path"`
					Offset int    `json:"offset"`
					Limit  int    `json:"limit"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return "", fmt.Errorf("the arguments are not the JSON Read takes: %w", err)
				}
				return w.call(ctx, func(c *workroom.Client) (string, error) {
					out, err := c.Read(ctx, in.Path, in.Offset, in.Limit)
					return readText(out, in.Offset, in.Limit), err
				})
			},
		},
		{
			Name:        WriteToolName,
			Description: "Write a file in the workspace whole, making its directories; a file already there is replaced.",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
			Invoke: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return "", fmt.Errorf("the arguments are not the JSON Write takes: %w", err)
				}
				return w.call(ctx, func(c *workroom.Client) (string, error) {
					out, err := c.Write(ctx, in.Path, in.Content)
					return fmt.Sprintf("Wrote %d bytes to %s.", out.Bytes, out.Path), err
				})
			},
		},
		{
			Name: EditToolName,
			Description: "Replace text in a file in the workspace. old_string must be there exactly once, or set " +
				"replace_all to replace every one; Read the file first and copy the text exactly.",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},` +
				`"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"]}`),
			Invoke: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct {
					Path       string `json:"path"`
					Old        string `json:"old_string"`
					New        string `json:"new_string"`
					ReplaceAll bool   `json:"replace_all"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return "", fmt.Errorf("the arguments are not the JSON Edit takes: %w", err)
				}
				return w.call(ctx, func(c *workroom.Client) (string, error) {
					out, err := c.Edit(ctx, in.Path, in.Old, in.New, in.ReplaceAll)
					return fmt.Sprintf("Replaced %d in %s.", out.Replaced, out.Path), err
				})
			},
		},
		{
			Name: GlobToolName,
			Description: "List the files in the workspace whose paths match a glob, newest first: ** is any number " +
				"of directories, as in **/*.py. .git and node_modules are left out.",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"pattern":{"type":"string"},"path":{"type":"string","description":"Where to look; ` + workroom.Dir + ` if left out"}},` +
				`"required":["pattern"]}`),
			Invoke: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct {
					Pattern string `json:"pattern"`
					Path    string `json:"path"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return "", fmt.Errorf("the arguments are not the JSON Glob takes: %w", err)
				}
				return w.call(ctx, func(c *workroom.Client) (string, error) {
					out, err := c.Glob(ctx, in.Pattern, in.Path)
					return globText(out), err
				})
			},
		},
		{
			Name: GrepToolName,
			Description: "Search the text files in the workspace for a regular expression (RE2). output is files " +
				"(the default: the files that match), content (the matching lines, with context lines around them) " +
				"or count. glob keeps it to files whose name matches, as *.go.",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"},` +
				`"output":{"type":"string","enum":["files","content","count"]},` +
				`"context":{"type":"integer","minimum":0,"maximum":20},"ignore_case":{"type":"boolean"}},` +
				`"required":["pattern"]}`),
			Invoke: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in workroom.GrepQuery
				if err := json.Unmarshal(raw, &in); err != nil {
					return "", fmt.Errorf("the arguments are not the JSON Grep takes: %w", err)
				}
				return w.call(ctx, func(c *workroom.Client) (string, error) {
					out, err := c.Grep(ctx, in)
					return grepText(out), err
				})
			},
		},
	}
}

func execText(e workroom.Exec) string {
	var b strings.Builder
	if e.TimedOut {
		b.WriteString("The command ran out of time and was stopped, with everything it started.\n")
	}
	b.WriteString(e.Stdout)
	if e.Stderr != "" {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("stderr:\n" + e.Stderr)
	}
	if e.ExitCode != 0 {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "exit code %d", e.ExitCode)
	}
	if b.Len() == 0 {
		return "(no output)"
	}
	return b.String()
}

func readText(r workroom.Read, offset, limit int) string {
	if r.Lines == 0 {
		return "(the file is empty)"
	}
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = 2000
	}
	if r.Content == "" {
		return fmt.Sprintf("(the file has %d lines, so nothing is at line %d)", r.Lines, offset)
	}
	if last := offset + limit - 1; last < r.Lines {
		return r.Content + fmt.Sprintf("(lines %d to %d of %d; read on from offset %d)", offset, last, r.Lines, last+1)
	}
	return r.Content
}

func globText(g workroom.Glob) string {
	if len(g.Paths) == 0 {
		return "No files match."
	}
	out := strings.Join(g.Paths, "\n")
	if g.Truncated {
		out += "\n(more match: these are the newest 1,000)"
	}
	return out
}

func grepText(g workroom.Grep) string {
	if g.Files == 0 {
		return "No matches."
	}
	if g.Truncated {
		return g.Output + "(more match than are shown: narrow the pattern, the path or the glob)"
	}
	return g.Output
}
