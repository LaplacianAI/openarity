package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
)

// Discovery holds tools a run may use without sending their schemas up
// front. The model finds them with Search, reads what one takes with
// describe_tools and runs it with call_tool, so the tools list a provider
// caches never changes during the run. A run whose tools and skills fit under
// DiscoverAbove sends everything as usual.
type Discovery struct {
	Tools []Tool
	// Groups are listed in the prompt, one line each, so the model knows what
	// there is to search for: one per MCP server, say.
	Groups []Group
	// Search names the tool in Spec.Tools the model finds tools and skills
	// with. Required, so a run never defers what it has no way to find.
	Search string
	// Select picks, best first, the names of the Tools a task most likely
	// needs. A pattern that plans every call before making one cannot search
	// midway, so with too many tools to send it plans with what Select picks.
	// Optional: without it, or when it fails or picks none of the Tools,
	// such a pattern is sent every one.
	Select func(ctx context.Context, task string, limit int) ([]string, error)
}

// A Group is a set of Discovery tools named in the prompt by what they are
// for.
type Group struct {
	Name  string
	About string
	Tools []string
}

const (
	// DiscoverAbove is the estimated size, in tokens, of the deferred tools'
	// schemas and the skills' descriptions past which a run finds them by
	// search: about 2% of a 200k-token window. A size rather than a share of
	// the window, which on a 1M-token model would defer nothing.
	DiscoverAbove = 4000

	DescribeToolsName = "describe_tools"
	CallToolName      = "call_tool"

	describeLimit = 5
	aboutLimit    = 200

	// SelectLimit is how many tools Select is asked for: enough for a plan of
	// several steps across a server or two, few enough to stay well under
	// DiscoverAbove.
	SelectLimit = 15
)

// withDiscovery sends Spec.Discovery's tools with the rest when they are
// small, and offers describe_tools and call_tool in their place when they are
// not. It returns the search tool's name when the run finds by search, so the
// skills are found the same way.
func withDiscovery(ctx context.Context, spec Spec, msgs []Message) (Spec, string, error) {
	d := spec.Discovery
	if d == nil {
		return spec, "", nil
	}
	if err := checkDiscovery(spec.Tools, d); err != nil {
		return Spec{}, "", err
	}

	small := catalogueSize(d.Tools, spec.Skills) <= DiscoverAbove
	// ReWOO plans every call before making one, so it cannot search midway:
	// it plans with the tools picked for its task, or with every one.
	if spec.Pattern == PatternReWOO || small {
		tools := d.Tools
		if !small && spec.Pattern == PatternReWOO {
			if picked := selected(ctx, d, msgs); len(picked) > 0 {
				tools = picked
			}
		}
		spec.Tools = append(slices.Clone(spec.Tools), tools...)
		return spec, "", nil
	}

	if len(d.Tools) > 0 {
		listed := make(map[string]bool, len(spec.Tools))
		for _, t := range spec.Tools {
			listed[t.Name] = true
		}
		cat := catalogue{search: d.Search, listed: listed, tools: make(map[string]*deferred, len(d.Tools))}
		for _, t := range d.Tools {
			cat.tools[t.Name] = &deferred{tool: t}
		}
		spec.Tools = append(slices.Clone(spec.Tools), cat.describeTool(), cat.callTool())
	}
	spec.System = append(slices.Clone(spec.System), discoveryListing(*d, len(spec.Skills) > 0))
	return spec, d.Search, nil
}

// selected is the Discovery tools Select picks for the conversation's latest
// turns, best first. None when there is no Select, no text to pick for, or
// Select fails: the caller then sends every tool rather than plan with less.
func selected(ctx context.Context, d *Discovery, msgs []Message) []Tool {
	if d.Select == nil {
		return nil
	}
	task := recentText(msgs)
	if task == "" {
		return nil
	}
	names, err := d.Select(ctx, task, SelectLimit)
	if err != nil {
		return nil
	}

	byName := make(map[string]Tool, len(d.Tools))
	for _, t := range d.Tools {
		byName[t.Name] = t
	}
	var picked []Tool
	for _, name := range names {
		t, ok := byName[name]
		if !ok {
			continue // not one of this run's tools
		}
		delete(byName, name)
		picked = append(picked, t)
		if len(picked) == SelectLimit {
			break
		}
	}
	return picked
}

const (
	// recentTurns is how many of the latest spoken turns tools are picked
	// for: the person's last message and what led up to it.
	recentTurns = 4
	// turnChars bounds each turn's share, so one long answer does not drown
	// the message that asked for something.
	turnChars = 500
)

// recentText is what tools are picked for: the person's last message first,
// then the turns before it, newest first. A follow-up such as "yes, do it"
// names nothing, and the turn before it says what "it" is. Empty when the
// person has said nothing yet, so nothing is picked for the run's own words.
func recentText(msgs []Message) string {
	var turns []string
	heard := false
	for i := len(msgs) - 1; i >= 0 && len(turns) < recentTurns; i-- {
		m := msgs[i]
		if (m.Role != RoleUser && m.Role != RoleAssistant) || m.Compaction != nil {
			continue
		}
		text := strings.TrimSpace(m.Text())
		if text == "" {
			continue
		}
		if m.Role == RoleUser && !heard {
			// The person's latest message leads, whatever came after it.
			heard = true
			turns = append([]string{clip(text, turnChars)}, turns...)
			continue
		}
		turns = append(turns, clip(text, turnChars))
	}
	if !heard {
		return ""
	}
	return strings.Join(turns, "\n")
}

// clip is s cut to at most n bytes, at a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func checkDiscovery(tools []Tool, d *Discovery) error {
	if d.Search == "" {
		return errors.New("Discovery.Search is empty; a run cannot defer tools it has no way to find")
	}

	listed := map[string]bool{}
	for _, t := range tools {
		listed[t.Name] = true
	}
	if !listed[d.Search] {
		return fmt.Errorf("Discovery.Search names %q, which is not one of Spec.Tools", d.Search)
	}
	for _, name := range []string{DescribeToolsName, CallToolName} {
		if listed[name] {
			return fmt.Errorf("a tool is already named %q, which is a name deferred tools arrive under", name)
		}
	}

	seen := map[string]bool{}
	for _, t := range d.Tools {
		switch {
		case t.Name == "":
			return errors.New("a Discovery tool has no name")
		case reservedName(t.Name):
			return fmt.Errorf("a Discovery tool is named %q, which is a name the run gives its own tools", t.Name)
		case listed[t.Name]:
			return fmt.Errorf("%q is both in Spec.Tools and in Discovery.Tools", t.Name)
		case seen[t.Name]:
			return fmt.Errorf("two Discovery tools are named %q", t.Name)
		case t.Invoke == nil && t.Result == nil:
			return fmt.Errorf("the Discovery tool %q has neither Invoke nor Result", t.Name)
		case len(t.Schema) > 0 && !json.Valid(t.Schema):
			return fmt.Errorf("the schema of the Discovery tool %q is not valid JSON", t.Name)
		}
		seen[t.Name] = true
	}

	for _, g := range d.Groups {
		if g.Name == "" {
			return errors.New("a Discovery group has no name")
		}
		for _, name := range g.Tools {
			if !seen[name] {
				return fmt.Errorf("the Discovery group %q names %q, which is not one of Discovery.Tools", g.Name, name)
			}
		}
	}
	return nil
}

func reservedName(name string) bool {
	switch name {
	case DescribeToolsName, CallToolName, SkillToolName, SkillResourceToolName, ReadOutputToolName:
		return true
	}
	return false
}

// catalogueSize estimates the tokens sending every deferred tool and listing
// every skill would cost, as compaction estimates a request's tools.
func catalogueSize(tools []Tool, skills []Skill) int {
	n := 0
	for _, t := range tools {
		n += (len(t.Name) + len(t.Description) + len(t.Schema)) / charsPerToken
	}
	for _, s := range skills {
		n += (len(s.Name) + len(truncate(s.Description, descriptionLimit))) / charsPerToken
	}
	return n
}

func discoveryListing(d Discovery, skills bool) Content {
	var b strings.Builder
	b.WriteString("You start with few tools. Before saying something cannot be done, search with the ")
	b.WriteString(d.Search)
	b.WriteString(" tool for what the task needs.")
	if len(d.Tools) > 0 {
		b.WriteString(" " + DescribeToolsName + " shows what a tool it found takes, and " +
			CallToolName + " runs it.")
	}
	if skills {
		b.WriteString(" Skills are found the same way, and opened with " + SkillToolName +
			" by the name the search returned.")
	}

	if len(d.Groups) > 0 {
		b.WriteString("\n\nWhat there is to search:\n")
		for _, g := range d.Groups {
			fmt.Fprintf(&b, "- %s", g.Name)
			if g.About != "" {
				b.WriteString(": " + truncate(g.About, aboutLimit))
			}
			fmt.Fprintf(&b, " (%d tools)\n", len(g.Tools))
		}
	}
	return Content{Type: ContentText, Text: b.String(), Cacheable: true}
}

type catalogue struct {
	search string
	listed map[string]bool
	tools  map[string]*deferred
}

// A deferred tool compiles its schema the first time it is called, once.
type deferred struct {
	tool   Tool
	once   sync.Once
	schema *jsonschema.Resolved
}

// check validates arguments against the tool's schema. A schema that does not
// compile, such as one with a remote $ref, which is never fetched, is not
// checked here: the tool's server still checks what it is sent.
func (d *deferred) check(args json.RawMessage) error {
	d.once.Do(func() {
		if len(d.tool.Schema) == 0 {
			return
		}
		var s jsonschema.Schema
		if json.Unmarshal(d.tool.Schema, &s) != nil {
			return
		}
		if r, err := s.Resolve(nil); err == nil {
			d.schema = r
		}
	})
	if d.schema == nil {
		return nil
	}

	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return err
	}
	return d.schema.Validate(v)
}

func (d *deferred) inputSchema() json.RawMessage {
	if len(d.tool.Schema) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return d.tool.Schema
}

var describeSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "names": {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 5,
              "description": "the tools to describe, as the search returned them"}
  },
  "required": ["names"],
  "additionalProperties": false
}`)

type described struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func (c catalogue) describeTool() Tool {
	return Tool{
		Name: DescribeToolsName,
		Description: "Show what tools found with " + c.search + " take: each one's description and the " +
			"JSON schema of its arguments. At most 5 at a time.",
		Schema: describeSchema,
		Invoke: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Names []string `json:"names"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("the arguments are not valid JSON: %w", err)
			}
			switch {
			case len(a.Names) == 0:
				return "", errors.New("names is required: the tools to describe")
			case len(a.Names) > describeLimit:
				return "", fmt.Errorf("at most %d tools at a time, and %d were asked for", describeLimit, len(a.Names))
			}

			out := make([]described, 0, len(a.Names))
			for _, name := range a.Names {
				d, ok := c.tools[name]
				switch {
				case ok:
					out = append(out, described{Name: name, Description: d.tool.Description, InputSchema: d.inputSchema()})
				case c.listed[name]:
					out = append(out, described{Name: name, Error: "already among your tools; call it directly"})
				default:
					out = append(out, described{Name: name, Error: "not one of this run's tools; find one with " + c.search})
				}
			}
			b, err := json.Marshal(out)
			return string(b), err
		},
	}
}

var callSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "the tool to run, as the search returned it"},
    "arguments": {"type": "object", "description": "its arguments, as describe_tools shows them"}
  },
  "required": ["name"],
  "additionalProperties": false
}`)

func (c catalogue) callTool() Tool {
	return Tool{
		Name: CallToolName,
		Description: "Run a tool found with " + c.search + ", by its name and arguments. " +
			DescribeToolsName + " shows the arguments a tool takes.",
		Schema: callSchema,
		Result: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			name, args, err := parseCall(raw)
			if err != nil {
				return ToolResult{}, err
			}

			d, ok := c.tools[name]
			if !ok {
				if c.listed[name] {
					return ToolResult{}, fmt.Errorf("%s is already among your tools; call it directly", name)
				}
				return ToolResult{}, fmt.Errorf("there is no tool named %q in this run; find one with %s", name, c.search)
			}
			if err := d.check(args); err != nil {
				return ToolResult{}, fmt.Errorf("the arguments for %s do not match its schema: %w; %s shows it",
					name, err, DescribeToolsName)
			}

			r, _, err := d.tool.Call(ctx, args)
			return r, err
		},
	}
}

// parseCall reads call_tool's arguments. Some models send the inner arguments
// as a JSON string rather than an object; both are taken.
func parseCall(raw json.RawMessage) (string, json.RawMessage, error) {
	var c struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", nil, fmt.Errorf("the arguments are not valid JSON: %w", err)
	}
	if c.Name == "" {
		return "", nil, errors.New("name is required: the tool to run")
	}

	args := c.Arguments
	var quoted string
	if json.Unmarshal(args, &quoted) == nil {
		args = json.RawMessage(quoted)
		if !json.Valid(args) {
			return "", nil, fmt.Errorf("the arguments for %s are a string that is not JSON", c.Name)
		}
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	return c.Name, args, nil
}

// Inner is the call a call_tool call stands for, so a run's events name the
// tool the model meant. Any other call, or one whose arguments do not parse,
// comes back as it is.
func Inner(c ToolCall) ToolCall {
	if c.Name != CallToolName {
		return c
	}
	name, args, err := parseCall(c.Arguments)
	if err != nil {
		return c
	}
	return ToolCall{ID: c.ID, Name: name, Arguments: args}
}
