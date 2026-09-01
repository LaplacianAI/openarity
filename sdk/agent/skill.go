package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type Skill struct {
	Name        string
	Description string
	Body        func(context.Context) (string, error)
}

const (
	SkillToolName    = "Skill"
	descriptionLimit = 1536
)

func skillTool(skills []Skill) (Tool, Content, error) {
	byName := make(map[string]Skill, len(skills))
	names := make([]string, 0, len(skills))

	for _, s := range skills {
		if s.Name == "" {
			return Tool{}, Content{}, fmt.Errorf("a skill has no name")
		}
		if _, clash := byName[s.Name]; clash {
			return Tool{}, Content{}, fmt.Errorf("two skills are named %q", s.Name)
		}
		byName[s.Name] = s
		names = append(names, s.Name)
	}

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"enum":        names,
				"description": "The name of the skill to load, exactly as listed.",
			},
		},
		"required":             []string{"name"},
		"additionalProperties": false,
	})

	var (
		mu     sync.Mutex
		loaded = map[string]bool{}
	)

	tool := Tool{
		Name: SkillToolName,
		Description: "Load the instructions for one of the available skills. " +
			"Call this when a skill's description matches what you are about to do.",
		Schema: json.RawMessage(schema),
		Invoke: func(ctx context.Context, args json.RawMessage) (string, error) {
			var call struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &call); err != nil {
				return "", fmt.Errorf("the arguments are not valid JSON: %w", err)
			}

			s, ok := byName[call.Name]
			if !ok {
				return "", fmt.Errorf("there is no skill named %q; the ones offered are: %s",
					call.Name, strings.Join(names, ", "))
			}
			if s.Body == nil {
				return "", fmt.Errorf("the skill %q has no body", s.Name)
			}

			mu.Lock()
			already := loaded[s.Name]
			mu.Unlock()
			if already {
				return fmt.Sprintf(
					"The %s skill is already loaded. Its instructions are earlier in this conversation.",
					s.Name), nil
			}

			body, err := s.Body(ctx)
			if err != nil {
				return "", err
			}

			mu.Lock()
			loaded[s.Name] = true
			mu.Unlock()

			return body, nil
		},
	}

	return tool, skillListing(skills), nil
}

func skillListing(skills []Skill) Content {
	var b strings.Builder
	b.WriteString("Skills available through the ")
	b.WriteString(SkillToolName)
	b.WriteString(" tool. Load one only when its description matches what you are doing.\n\n")

	for _, s := range skills {
		b.WriteString("- ")
		b.WriteString(s.Name)
		b.WriteString(": ")
		b.WriteString(truncate(s.Description, descriptionLimit))
		b.WriteString("\n")
	}

	return Content{Type: ContentText, Text: b.String(), Cacheable: true}
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return strings.TrimSpace(string(r[:limit])) + "…"
}
