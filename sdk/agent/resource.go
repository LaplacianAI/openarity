package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const SkillResourceToolName = "SkillResource"

func resourceTool(skills []Skill, loaded *loadedSkills) (Tool, bool) {
	byName := map[string]map[string]Resource{}
	var names []string

	for _, s := range skills {
		if len(s.Resources) == 0 {
			continue
		}
		rs := make(map[string]Resource, len(s.Resources))
		for _, r := range s.Resources {
			rs[r.Name] = r
		}
		byName[s.Name] = rs
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return Tool{}, false
	}

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skill": map[string]any{
				"type":        "string",
				"enum":        names,
				"description": "The skill the resource belongs to, exactly as listed.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "The resource, exactly as the skill's instructions list it.",
			},
		},
		"required":             []string{"skill", "name"},
		"additionalProperties": false,
	})

	return Tool{
		Name: SkillResourceToolName,
		Description: "Read one resource bundled with a skill. Load the skill with the " +
			SkillToolName + " tool first; its instructions list the resources it has.",
		Schema: json.RawMessage(schema),
		Invoke: func(ctx context.Context, args json.RawMessage) (string, error) {
			var call struct {
				Skill string `json:"skill"`
				Name  string `json:"name"`
			}
			if err := json.Unmarshal(args, &call); err != nil {
				return "", fmt.Errorf("the arguments are not valid JSON: %w", err)
			}

			rs, ok := byName[call.Skill]
			if !ok {
				return "", fmt.Errorf("no skill named %q has resources; the ones that do are: %s",
					call.Skill, strings.Join(names, ", "))
			}
			if !loaded.has(call.Skill) {
				return "", fmt.Errorf("load the skill %q with the %s tool first; its instructions say when to use its resources",
					call.Skill, SkillToolName)
			}

			r, ok := rs[call.Name]
			if !ok {
				return "", fmt.Errorf("the skill %q has no resource named %q", call.Skill, call.Name)
			}
			return r.Read(ctx)
		},
	}, true
}
