package improve

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

//go:embed prompts/digest.md
var digestPrompt string

var (
	parts  = []string{"instructions", "description", "skill", "tool", "server", "limits", "delegation", "pattern", "none"}
	causes = []string{
		"missing_knowledge", "skipped_step", "wrong_tool", "misused_tool", "misread_request",
		"gave_up", "ran_out", "wrong_delegate", "external", "other",
	}
	wents = []string{"wrong", "well"}
)

type signature struct {
	Part      string `json:"part"`
	Target    string `json:"target"`
	Cause     string `json:"cause"`
	Behaviour string `json:"behaviour"`
}

type agentDigest struct {
	Agent     string    `json:"agent"`
	Summary   string    `json:"summary"`
	Went      string    `json:"went"`
	Signature signature `json:"signature"`
}

type digestAnswer struct {
	Agents []agentDigest `json:"agents"`
}

var digestSchema = &agent.OutputSchema{
	Name:        "digest",
	Description: "What each agent in the run did, and the part of its setup involved.",
	Strict:      true,
	JSON: json.RawMessage(`{
  "type": "object", "additionalProperties": false, "required": ["agents"],
  "properties": {"agents": {"type": "array", "items": {
    "type": "object", "additionalProperties": false,
    "required": ["agent", "summary", "went", "signature"],
    "properties": {
      "agent": {"type": "string"},
      "summary": {"type": "string"},
      "went": {"type": "string", "enum": ["wrong", "well"]},
      "signature": {"type": "object", "additionalProperties": false,
        "required": ["part", "target", "cause", "behaviour"],
        "properties": {
          "part": {"type": "string", "enum": ["instructions", "description", "skill", "tool", "server", "limits", "delegation", "pattern", "none"]},
          "target": {"type": "string"},
          "cause": {"type": "string", "enum": ["missing_knowledge", "skipped_step", "wrong_tool", "misused_tool", "misread_request", "gave_up", "ran_out", "wrong_delegate", "external", "other"]},
          "behaviour": {"type": "string"}
        }}
    }}}}
}`),
}

// subject is a custom agent of the tree, with what its runs held.
type subject struct {
	id   uuid.UUID
	held held
}

// subjects are the tree's agents a digest covers: those the team defined,
// by name. The hub and other built-in agents are code.
func (ev evidence) subjects() map[string]subject {
	out := map[string]subject{}
	for _, r := range ev.runs {
		if !r.Custom || r.AgentID == nil {
			continue
		}
		s := out[r.AgentName]
		s.id = *r.AgentID
		h := ev.held[r.ID]
		s.held.skills = append(s.held.skills, h.skills...)
		s.held.tools = append(s.held.tools, h.tools...)
		out[r.AgentName] = s
	}
	return out
}

// rows turns an answer into the rows to store: one per agent the tree
// holds, each signature from the closed sets, a target only when the agent
// holds it.
func (a digestAnswer) rows(o db.RunOutcome, subjects map[string]subject, outsider bool, model string, usage agent.Usage) []db.AddRunDigestParams {
	var out []db.AddRunDigestParams
	seen := map[uuid.UUID]bool{}
	for _, d := range a.Agents {
		s, ok := subjects[strings.TrimSpace(d.Agent)]
		sig := d.Signature
		if !ok || seen[s.id] || !oneOf(d.Went, wents) || !oneOf(sig.Part, parts) || !oneOf(sig.Cause, causes) {
			continue
		}
		summary, behaviour := clip(d.Summary, 1200), clip(sig.Behaviour, 300)
		if summary == "" || behaviour == "" {
			continue
		}
		seen[s.id] = true
		out = append(out, db.AddRunDigestParams{
			RunID: o.RunID, AgentID: s.id, TeamID: o.TeamID, Went: d.Went, Summary: summary,
			Part: sig.Part, Target: targetOf(sig, s.held), Cause: sig.Cause, Behaviour: behaviour,
			Outsider: outsider, Model: model,
		})
	}
	// The call's tokens are counted once, on the first row.
	if len(out) > 0 {
		out[0].InputTokens, out[0].OutputTokens = int64(usage.InputTokens), int64(usage.OutputTokens)
	}
	return out
}

// targetOf is the signature's target when the agent held it, else empty: a
// name the model made up must not become a pattern of its own.
func targetOf(sig signature, h held) string {
	t := strings.TrimSpace(sig.Target)
	switch sig.Part {
	case "skill":
		if slices.Contains(h.skills, t) {
			return t
		}
	case "tool", "server":
		if slices.Contains(h.tools, t) || slices.ContainsFunc(h.tools, func(tool string) bool {
			return strings.HasPrefix(tool, t+"__") || strings.HasPrefix(tool, t+".")
		}) {
			return t
		}
	}
	return ""
}

// Digest studies one judged run, once. The model is asked outside any
// transaction; the rows are written with the outcome held and still
// pending, so two digests of one run cannot both write. A model failure
// leaves the run skipped (a later 👎 reopens it); only the brain's own
// failures are returned.
func (c *Checker) Digest(ctx context.Context, runID uuid.UUID) error {
	o, err := c.store.GetRunOutcome(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading run %s's outcome: %w", runID, err)
	}
	if o.Digest != digestPending {
		return nil
	}
	settings, err := c.store.GetImprovementSettings(ctx, o.TeamID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !settings.Improvement) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading team %s's settings: %w", o.TeamID, err)
	}

	rows, err := c.study(ctx, o, settings)
	state := digestDone
	if err != nil {
		c.logger.WarnContext(ctx, "A run could not be digested", "run", runID, "err", err)
		state = digestSkipped
	}
	return c.store.InTx(ctx, func(q *db.Queries) error {
		held, err := q.LockRunOutcome(ctx, runID)
		if err != nil {
			return fmt.Errorf("holding run %s's outcome: %w", runID, err)
		}
		if held.Digest != digestPending || held.DigestRound != o.DigestRound {
			return nil // digested, or reopened, meanwhile
		}
		for _, r := range rows {
			if err := q.AddRunDigest(ctx, r); err != nil {
				return fmt.Errorf("writing run %s's digest: %w", runID, err)
			}
		}
		return q.SetRunDigest(ctx, db.SetRunDigestParams{RunID: runID, Digest: state})
	})
}

// study asks the model about the run's tree and returns the rows to write.
func (c *Checker) study(ctx context.Context, o db.RunOutcome, settings db.GetImprovementSettingsRow) ([]db.AddRunDigestParams, error) {
	model, ok := judgeModelOf(settings)
	if !ok {
		return nil, errors.New("the team has no model to digest with")
	}
	ev, err := gather(ctx, c.store, o.RunID, true)
	if err != nil {
		return nil, err
	}
	subjects := ev.subjects()
	if len(subjects) == 0 {
		return nil, nil // only built-in agents ran
	}
	names := make([]string, 0, len(subjects))
	for n := range subjects {
		names = append(names, n)
	}
	slices.Sort(names)
	user := ev.render(digestBudget) + "## How it was judged\n" + judged(o) + "\n\n## Agents to digest\n" + strings.Join(names, "\n") + "\n"

	var answer digestAnswer
	usage, err := c.judge.doRetrying(ctx, ask{model: model, system: digestPrompt, user: user, schema: digestSchema}, &answer)
	if err != nil {
		return nil, err
	}
	return answer.rows(o, subjects, ev.outsider, model.Name, usage), nil
}

func judged(o db.RunOutcome) string {
	s := "verdict " + o.Verdict
	if o.Missed != "" {
		s += "; missed: " + o.Missed
	}
	if o.Implicit != implicitNone {
		s += "; the next message was a " + o.Implicit
	}
	return s
}
