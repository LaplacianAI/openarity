package improve

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// passTools are a pass's tools and what they have done.
type passTools struct {
	im       *Improver
	pass     db.ImproverPass
	material material
	// stop ends the pass's run with a cause: the screen could not be
	// reached, or the pass has spent its tokens.
	stop context.CancelCauseFunc
	// tokens is what the try may spend, the screen's included.
	tokens int

	mu sync.Mutex
	// proposed starts at what earlier attempts stored.
	proposed    int32
	refused     int32
	screenedOut int32
	// What the screen spent, and what the run has: the run's from its
	// events, so a pass is stopped as it crosses passTokens.
	screen agent.Usage
	run    agent.Usage
}

// halt stops the run, when it is one: a tool called outside a run has
// nothing to stop.
func (t *passTools) halt(cause error) {
	if t.stop != nil {
		t.stop(cause)
	}
}

// watch counts what the run spends as it spends it, and stops it past
// its tokens.
func (t *passTools) watch(events <-chan agent.Event) {
	for e := range events {
		u, ok := e.(agent.UsageEvent)
		if !ok {
			continue
		}
		t.mu.Lock()
		t.run.InputTokens += u.Usage.InputTokens
		t.run.OutputTokens += u.Usage.OutputTokens
		over := t.overLocked()
		t.mu.Unlock()
		if over {
			t.halt(errPassTokens)
		}
	}
}

func (t *passTools) overLocked() bool {
	spent := t.run.InputTokens + t.run.OutputTokens + t.screen.InputTokens + t.screen.OutputTokens
	return t.tokens > 0 && spent > t.tokens
}

// spent is what the attempt cost: the run's own count, which sees every
// call, and the screen's.
func (t *passTools) spent(run agent.Usage) (in, out int64, screened int32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return int64(run.InputTokens + t.screen.InputTokens), int64(run.OutputTokens + t.screen.OutputTokens), t.screenedOut
}

func (t *passTools) tools() []agent.Tool {
	return []agent.Tool{
		{
			Name:        "read_run",
			Description: "Read one of the runs listed under Runs to read, in full: the conversation before it, the request, what the agent did, its answer and what came next.",
			Schema:      schemaJSON(object(obj{"run": text()})),
			Invoke:      t.readRun,
		},
		{
			Name:        "read_skill",
			Description: "Read a skill's SKILL.md, or with path one of its text files under references/ or scripts/. Only the team's own skills.",
			Schema:      schemaJSON(obj{"type": "object", "properties": obj{"skill": text(), "path": text()}, "required": []string{"skill"}}),
			Invoke:      t.readSkill,
		},
		{
			Name:        "list_team",
			Description: "List the team's skills and MCP servers with their tools: what a grant may name.",
			Schema:      schemaJSON(obj{"type": "object", "properties": obj{}}),
			Invoke:      t.listTeam,
		},
		{
			Name:        "propose_change",
			Description: proposeDescription,
			Schema:      proposeSchema,
			Invoke:      t.propose,
		},
	}
}

func (t *passTools) readRun(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Run string `json:"run"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	id, parsed := uuid.Parse(strings.TrimSpace(in.Run))
	if parsed != nil || !t.material.runs[id] {
		return "That run is not one of the Runs to read.", nil //nolint:nilerr // the model is told, and may read another
	}
	ev, err := gather(ctx, t.im.store, id, true)
	if err != nil {
		return "", fmt.Errorf("reading run %s: %w", id, err)
	}
	return ev.render(checkBudget), nil
}

func (t *passTools) readSkill(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Skill string `json:"skill"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	s, ok := t.material.skills[strings.TrimSpace(in.Skill)]
	if !ok || s.Source != "upload" {
		return "That is not one of the team's own skills: an imported skill is never changed, and is not read here.", nil
	}
	files, err := t.im.store.ListSkillFiles(ctx, s.ID)
	if err != nil {
		return "", fmt.Errorf("reading skill %s's files: %w", s.Name, err)
	}
	if in.Path == "" {
		var b strings.Builder
		writeSection(&b, section{"SKILL.md of " + s.Name, []string{"description: " + s.Description, s.Body}})
		var paths []string
		for _, f := range files {
			paths = append(paths, fmt.Sprintf("%s (%d bytes)", f.Path, f.Size))
		}
		writeSection(&b, section{"Its files", paths})
		return b.String(), nil
	}
	i := slices.IndexFunc(files, func(f db.SkillFile) bool { return f.Path == in.Path })
	if i < 0 {
		return "The skill has no file " + in.Path + ".", nil
	}
	data, err := t.im.objects.Get(ctx, s.TeamID, files[i].ObjectKey)
	if err != nil {
		return "", fmt.Errorf("reading %s/%s: %w", s.Name, in.Path, err)
	}
	var b strings.Builder
	writeSection(&b, section{s.Name + "/" + in.Path, []string{clip(string(data), checkBudget)}})
	return b.String(), nil
}

func (t *passTools) listTeam(context.Context, json.RawMessage) (string, error) {
	var skills, servers []string
	for _, s := range t.material.skills {
		skills = append(skills, fmt.Sprintf("%s (%s): %s", s.Name, s.Source, s.Description))
	}
	for _, s := range t.material.servers {
		servers = append(servers, fmt.Sprintf("%s: %s", s.Name, strings.Join(s.Tools, ", ")))
	}
	slices.Sort(skills)
	slices.Sort(servers)
	var b strings.Builder
	writeSection(&b, section{"The team's skills", skills})
	writeSection(&b, section{"The team's MCP servers", servers})
	return b.String(), nil
}
