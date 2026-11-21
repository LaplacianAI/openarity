package improve

import (
	"encoding/json"
	"slices"
)

// The answers' schemas are built from the same sets their answers are
// checked against, so the two cannot drift apart.

type obj = map[string]any

// object is a strict object: every property required, no others.
func object(props obj) obj {
	required := make([]string, 0, len(props))
	for k := range props {
		required = append(required, k)
	}
	slices.Sort(required)
	return obj{"type": "object", "additionalProperties": false, "required": required, "properties": props}
}

func text() obj { return obj{"type": "string"} }

func oneOfSet(set []string) obj { return obj{"type": "string", "enum": set} }

func listOf(item obj) obj { return obj{"type": "array", "items": item} }

func schemaJSON(v obj) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("an answer schema does not encode: " + err.Error())
	}
	return b
}
