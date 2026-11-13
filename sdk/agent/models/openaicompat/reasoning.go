package openaicompat

import (
	"encoding/json"
	"strings"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// The fields providers put a model's reasoning in. The chat completions API
// declares none of them, so openai-go keeps each as an extra field.
const (
	fieldContent = "reasoning_content" // DeepSeek, Qwen, vLLM
	fieldPlain   = "reasoning"         // OpenRouter, Ollama
	fieldDetails = "reasoning_details" // OpenRouter, in parts, possibly signed or encrypted
)

// sentBack is every field a turn's reasoning may return under. Nothing else
// is written onto a request, whatever a stored transcript holds.
var sentBack = []string{fieldContent, fieldPlain, fieldDetails}

// maxDetails bounds the parts of reasoning_details one turn may hold, so an
// index from a gateway cannot size an allocation.
const maxDetails = 256

// joined are the parts of a reasoning_details entry that arrive in pieces;
// shown are those a person can read.
var (
	joined = map[string]bool{"text": true, "summary": true, "data": true}
	shown  = []string{"text", "summary"}
)

// extra is an undeclared field's JSON, or "" when it did not come. Valid
// reports false for every extra field, as openai-go has no schema to check one
// against, so only Raw says whether it arrived. A null reads as nothing
// wherever it goes.
func extra[F interface{ Raw() string }](fields map[string]F, name string) string {
	if f, ok := fields[name]; ok {
		return f.Raw()
	}
	return ""
}

// thoughts gathers a turn's reasoning as it arrives, in whichever fields its
// provider uses.
type thoughts struct {
	content, plain strings.Builder
	details        []map[string]any
}

// add takes one chunk's reasoning fields and returns what of it can be read.
// A provider sending several says the same thing in each, so what is shown
// comes from one: reasoning_content, then reasoning, then the details' text.
func (t *thoughts) add(content, plain, details string) string {
	c := unquote(content)
	t.content.WriteString(c)
	p := unquote(plain)
	t.plain.WriteString(p)
	d := t.merge(details)
	switch {
	case c != "":
		return c
	case p != "":
		return p
	}
	return d
}

// merge folds a reasoning_details delta into what came before. Entries are
// matched by index; their text arrives in pieces and is joined, while a
// type, an id or a signature is simply the latest given.
func (t *thoughts) merge(raw string) string {
	if raw == "" {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var parts []map[string]any
	if dec.Decode(&parts) != nil {
		return ""
	}
	var read strings.Builder
	for i, part := range parts {
		at, ok := indexOf(part, i)
		if !ok {
			continue
		}
		for len(t.details) <= at {
			t.details = append(t.details, map[string]any{})
		}
		into := t.details[at]
		for k, v := range part {
			s, isString := v.(string)
			switch {
			case isString && joined[k]:
				before, _ := into[k].(string)
				into[k] = before + s
				if k != "data" {
					read.WriteString(s)
				}
			case v != nil && (!isString || s != ""):
				into[k] = v
			}
		}
	}
	return read.String()
}

func indexOf(part map[string]any, position int) (int, bool) {
	n, ok := part["index"].(json.Number)
	if !ok {
		return position, position < maxDetails
	}
	at, err := n.Int64()
	if err != nil || at < 0 || at >= maxDetails {
		return 0, false
	}
	return int(at), true
}

// reasoning is the turn's reasoning as a message carries it, or nil when the
// provider sent none.
func (t *thoughts) reasoning(model string) *agent.Reasoning {
	fields := map[string]json.RawMessage{}
	if t.content.Len() > 0 {
		fields[fieldContent] = quote(t.content.String())
	}
	if details := t.entries(); len(details) > 0 {
		if b, err := json.Marshal(details); err == nil {
			fields[fieldDetails] = b
		}
	}
	// A provider sending both has reasoning as the plain text of
	// reasoning_details; returning both would hand the model its thinking twice.
	if t.plain.Len() > 0 && fields[fieldDetails] == nil {
		fields[fieldPlain] = quote(t.plain.String())
	}
	if len(fields) == 0 {
		return nil
	}
	return &agent.Reasoning{Text: t.text(), Model: model, Fields: fields}
}

// entries are the details that arrived, without the gaps an index skipping
// ahead leaves.
func (t *thoughts) entries() []map[string]any {
	out := make([]map[string]any, 0, len(t.details))
	for _, d := range t.details {
		if len(d) > 0 {
			out = append(out, d)
		}
	}
	return out
}

func (t *thoughts) text() string {
	switch {
	case t.content.Len() > 0:
		return t.content.String()
	case t.plain.Len() > 0:
		return t.plain.String()
	}
	var b strings.Builder
	for _, d := range t.details {
		for _, k := range shown {
			if s, ok := d[k].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

// toExtras is the reasoning a turn goes back to its model with: the provider's
// own fields, only to the model that wrote them.
func toExtras(r *agent.Reasoning, model string) map[string]any {
	if r == nil || r.Model == "" || r.Model != model {
		return nil
	}
	out := map[string]any{}
	for _, name := range sentBack {
		if f, ok := r.Fields[name]; ok && json.Valid(f) {
			out[name] = f
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func unquote(raw string) string {
	var s string
	if raw == "" || json.Unmarshal([]byte(raw), &s) != nil {
		return ""
	}
	return s
}

func quote(s string) json.RawMessage {
	b, _ := json.Marshal(s) //nolint:errchkjson // a string always encodes
	return b
}
