package improve

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"unicode"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/improvements"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

//go:embed prompts/screen.md
var screenPrompt string

const (
	screenOK     = "ok"
	screenRefuse = "refuse"
)

var screenSchema = &agent.OutputSchema{
	Name:        "screen",
	Description: "Whether a proposed change carries one run's specifics.",
	Strict:      true,
	JSON:        schemaJSON(object(obj{"verdict": oneOfSet([]string{screenOK, screenRefuse}), "reason": text()})),
}

type screenAnswer struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// screen asks the judge model whether a proposal's new text carries one
// run's specifics, reading only that text and the cited runs' summaries,
// and returns what that spent. It is asked twice before giving up: no
// proposal reaches a person unscreened. An answer it could make nothing of
// is final; a model it could not reach is not.
func (im *Improver) screen(ctx context.Context, settings db.GetImprovementSettingsRow, n improvements.Proposed, in proposeArgs) (screenAnswer, agent.Usage, error) {
	var spent agent.Usage
	model, ok := judgeModelOf(settings)
	if !ok {
		return screenAnswer{}, spent, errNoModel
	}
	var b strings.Builder
	writeSection(&b, section{"The change's new text", addedText(n, in)})
	var summaries []string
	for _, id := range n.RunIDs {
		rows, err := im.store.ListRunDigests(ctx, id)
		if err != nil {
			return screenAnswer{}, spent, fmt.Errorf("reading run %s's digests: %w", id, err)
		}
		for _, r := range rows {
			if r.AgentID == n.AgentID {
				summaries = append(summaries, r.Summary)
			}
		}
	}
	writeSection(&b, section{"The runs it rests on", summaries})

	var err error
	for range 2 {
		// A reply of its own each time: one that leaves a field out keeps
		// nothing of the last.
		var answer screenAnswer
		var usage agent.Usage
		usage, err = im.judge.do(ctx, ask{model: model, system: screenPrompt, user: b.String(), schema: screenSchema}, &answer)
		spent.InputTokens += usage.InputTokens
		spent.OutputTokens += usage.OutputTokens
		if err == nil && oneOf(answer.Verdict, []string{screenOK, screenRefuse}) {
			return answer, spent, nil
		}
		if err == nil {
			err = unusable("verdict %q", answer.Verdict)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return screenAnswer{}, spent, err
}

// addedText is what a proposal adds that every later run and member would
// read: its why and hypothesis, and the new side of each text it changes.
func addedText(n improvements.Proposed, in proposeArgs) []string {
	lines := []string{"why: " + in.Why, "hypothesis: " + in.Hypothesis}
	b, a := n.Before, n.After
	if a.Agent != nil && b.Agent != nil {
		if a.Agent.Agent.Instructions != b.Agent.Agent.Instructions {
			lines = append(lines, "instructions: "+added(b.Agent.Agent.Instructions, a.Agent.Agent.Instructions))
		}
		if a.Agent.Agent.Description != b.Agent.Agent.Description {
			lines = append(lines, "description: "+a.Agent.Agent.Description)
		}
		for _, note := range a.Agent.Notes {
			if !hasNote(b.Agent.Notes, note) {
				lines = append(lines, "tool note: "+note.Note)
			}
		}
	}
	if s := a.Skill; s != nil {
		var old improvements.SkillState
		if b.Skill != nil {
			old = *b.Skill
		}
		if s.Description != old.Description {
			lines = append(lines, "skill description: "+s.Description)
		}
		if s.Body != old.Body {
			lines = append(lines, "skill body: "+added(old.Body, s.Body))
		}
		for _, f := range s.Files {
			if f.Text != nil {
				lines = append(lines, "file "+f.Path+": "+*f.Text)
			}
		}
	}
	return lines
}

func hasNote(notes []improvements.ToolNote, n improvements.ToolNote) bool {
	for _, x := range notes {
		if x == n {
			return true
		}
	}
	return false
}

// added is the part of after that is not before's: what lies between their
// shared head and tail, widened to whole words so the screen never reads
// half of one.
func added(before, after string) string {
	b, a := []rune(before), []rune(after)
	i := 0
	for i < len(b) && i < len(a) && b[i] == a[i] {
		i++
	}
	j := 0
	for j < len(b)-i && j < len(a)-i && b[len(b)-1-j] == a[len(a)-1-j] {
		j++
	}
	inWord := func(k int) bool { // whether a cut before a[k] splits a word
		return k > 0 && k < len(a) && !unicode.IsSpace(a[k-1]) && !unicode.IsSpace(a[k])
	}
	for inWord(i) {
		i--
	}
	for j > 0 && inWord(len(a)-j) {
		j--
	}
	return strings.TrimSpace(string(a[i : len(a)-j]))
}
