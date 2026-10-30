package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

const (
	PlanToolName = "plan"
	maxPlanItems = 30
	maxPlanText  = 200
)

const planDescription = "Keep a plan for work of more than two steps. Send the whole list each time: " +
	"it replaces the last one. Mark one item in_progress while you work on it, and done when it is."

var planSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "items": {
      "type": "array",
      "maxItems": 30,
      "items": {
        "type": "object",
        "properties": {
          "text": {"type": "string", "maxLength": 200, "description": "one step, in a few words"},
          "status": {"type": "string", "enum": ["pending", "in_progress", "done"]}
        },
        "required": ["text", "status"],
        "additionalProperties": false
      }
    }
  },
  "required": ["items"],
  "additionalProperties": false
}`)

type planItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

type planPayload struct {
	Items []planItem `json:"items"`
}

type Note func(ctx context.Context, kind string, payload any) error

func PlanTool(note Note) agent.Tool {
	return agent.Tool{
		Name:        PlanToolName,
		Description: planDescription,
		Schema:      planSchema,
		Invoke: func(ctx context.Context, raw json.RawMessage) (string, error) {
			p, err := parsePlan(raw)
			if err != nil {
				return "", err
			}
			if err := note(ctx, "plan", p); err != nil {
				return "", fmt.Errorf("the plan could not be saved: %w", err)
			}
			count := map[string]int{}
			for _, it := range p.Items {
				count[it.Status]++
			}
			return fmt.Sprintf("Plan saved: %d done, %d in progress, %d pending.",
				count["done"], count["in_progress"], count["pending"]), nil
		},
	}
}

func parsePlan(raw json.RawMessage) (planPayload, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req struct {
		Items *[]planItem `json:"items"`
	}
	if err := dec.Decode(&req); err != nil {
		return planPayload{}, fmt.Errorf("the arguments are not the JSON the schema asks for: %w", err)
	}
	if req.Items == nil {
		return planPayload{}, errors.New("items is required")
	}
	if n := len(*req.Items); n > maxPlanItems {
		return planPayload{}, fmt.Errorf("a plan has at most %d items, and this has %d", maxPlanItems, n)
	}

	out := make([]planItem, 0, len(*req.Items))
	working := 0
	for i, it := range *req.Items {
		n, text := i+1, strings.TrimSpace(it.Text)
		switch {
		case text == "":
			return planPayload{}, fmt.Errorf("item %d has no text", n)
		case len(text) > maxPlanText:
			return planPayload{}, fmt.Errorf("item %d is %d bytes; an item's text is at most %d", n, len(text), maxPlanText)
		case strings.ContainsRune(text, 0):
			return planPayload{}, fmt.Errorf("item %d contains a NUL character", n)
		}
		switch it.Status {
		case "pending", "done":
		case "in_progress":
			working++
		default:
			return planPayload{}, fmt.Errorf("item %d's status is %q; it must be pending, in_progress or done", n, it.Status)
		}
		out = append(out, planItem{Text: text, Status: it.Status})
	}
	if working > 1 {
		return planPayload{}, fmt.Errorf("only one item may be in_progress, and %d are", working)
	}
	return planPayload{Items: out}, nil
}
