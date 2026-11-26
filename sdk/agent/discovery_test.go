package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

const issueSchema = `{"type":"object","properties":{"repo":{"type":"string"},"n":{"type":"integer"}},` +
	`"required":["repo"],"additionalProperties":false}`

func search() Tool {
	return Tool{Name: "graph", Invoke: func(context.Context, json.RawMessage) (string, error) { return "[]", nil }}
}

// recorder is a deferred tool that records what it was called with.
type recorder struct {
	mu   sync.Mutex
	args []string
}

func (c *recorder) tool(name, description, schema string) Tool {
	return Tool{
		Name: name, Description: description, Schema: json.RawMessage(schema),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.args = append(c.args, string(args))
			return "closed " + string(args), nil
		},
	}
}

func (c *recorder) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.args...)
}

// large is a catalogue past DiscoverAbove: one tool whose description alone
// is over it.
func large(c *recorder) *Discovery {
	return &Discovery{
		Search: "graph",
		Tools: []Tool{
			c.tool("github__close_issue", strings.Repeat("x", 4*DiscoverAbove), issueSchema),
			c.tool("github__list_issues", "List issues", `{"type":"object"}`),
		},
		Groups: []Group{{Name: "github", About: "Issues and pull requests", Tools: []string{"github__close_issue", "github__list_issues"}}},
	}
}

func names(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func toolNamed(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool named %s among %v", name, names(tools))
	return Tool{}
}

func found(t *testing.T, spec Spec) Spec {
	t.Helper()
	out, search, err := withDiscovery(t.Context(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if search != spec.Discovery.Search {
		t.Fatalf("the run does not find by search: search = %q", search)
	}
	return out
}

func call(t *testing.T, tool Tool, args string) (ToolResult, error) {
	t.Helper()
	r, ok, err := tool.Call(t.Context(), json.RawMessage(args))
	if !ok {
		t.Fatalf("%s cannot be called", tool.Name)
	}
	return r, err
}

func TestASmallCatalogueIsSentWithTheRest(t *testing.T) {
	t.Parallel()

	c := &recorder{}
	spec, search, err := withDiscovery(t.Context(), Spec{
		Tools:     []Tool{search()},
		Discovery: &Discovery{Search: "graph", Tools: []Tool{c.tool("github__list_issues", "List issues", `{"type":"object"}`)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(spec.Tools), ","); got != "graph,github__list_issues" || search != "" {
		t.Fatalf("sent %s, search %q; a small catalogue goes as it is", got, search)
	}
	if len(spec.System) != 0 {
		t.Fatalf("a run that searches for nothing was told to search: %+v", spec.System)
	}
}

// The threshold is inclusive: a catalogue of exactly DiscoverAbove tokens is
// still sent.
func TestACatalogueExactlyAtTheThresholdIsStillSent(t *testing.T) {
	t.Parallel()

	c := &recorder{}
	at := c.tool("t", strings.Repeat("x", 4*DiscoverAbove-len("t")-len(`{}`)), `{}`)
	if got := catalogueSize([]Tool{at}, nil); got != DiscoverAbove {
		t.Fatalf("the fixture is %d tokens, not %d", got, DiscoverAbove)
	}
	spec, _, err := withDiscovery(t.Context(), Spec{Tools: []Tool{search()}, Discovery: &Discovery{Search: "graph", Tools: []Tool{at}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(spec.Tools), ","); got != "graph,t" {
		t.Fatalf("at the threshold the run sent %s", got)
	}

	over := c.tool("t", strings.Repeat("x", 4*DiscoverAbove-len("t")-len(`{}`)+4), `{}`)
	spec, _, err = withDiscovery(t.Context(), Spec{Tools: []Tool{search()}, Discovery: &Discovery{Search: "graph", Tools: []Tool{over}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(spec.Tools), ","); got != "graph,describe_tools,call_tool" {
		t.Fatalf("one token over, the run sent %s", got)
	}
}

func TestALargeCatalogueIsFoundBySearch(t *testing.T) {
	t.Parallel()

	spec := found(t, Spec{Tools: []Tool{search()}, Discovery: large(&recorder{})})

	if got := strings.Join(names(spec.Tools), ","); got != "graph,describe_tools,call_tool" {
		t.Fatalf("sent %s; the deferred tools must stay out of the list", got)
	}
	if len(spec.System) != 1 || !spec.System[0].Cacheable {
		t.Fatalf("the listing is %+v; it must be one cacheable block", spec.System)
	}
	listing := spec.System[0].Text
	for _, want := range []string{"search with the graph tool", "describe_tools", "call_tool", "- github: Issues and pull requests (2 tools)"} {
		if !strings.Contains(listing, want) {
			t.Errorf("the listing lacks %q:\n%s", want, listing)
		}
	}
	if strings.Contains(listing, "github__close_issue") {
		t.Errorf("the listing names a deferred tool, which is what deferring saves:\n%s", listing)
	}
}

// Skills count towards the threshold, and past it they are found by search:
// no listing in the prompt, no enumeration in Skill's schema.
func TestManySkillsAreFoundBySearchToo(t *testing.T) {
	t.Parallel()

	var skills []Skill
	for i := range 20 {
		skills = append(skills, Skill{
			Name: "skill-" + string(rune('a'+i)), Description: strings.Repeat("d", descriptionLimit), Body: body("b"),
		})
	}
	spec := found(t, Spec{Tools: []Tool{search()}, Skills: skills, Discovery: &Discovery{Search: "graph"}})
	if got := strings.Join(names(spec.Tools), ","); got != "graph" {
		t.Fatalf("sent %s; with nothing deferred there is nothing to describe or call", got)
	}
	if !strings.Contains(spec.System[0].Text, "Skills are found the same way") {
		t.Errorf("the listing does not say how skills are found:\n%s", spec.System[0].Text)
	}

	spec, err := withSkills(spec, "graph")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.System) != 1 {
		t.Fatalf("the skills were listed as well: %d blocks", len(spec.System))
	}
	skill := toolNamed(t, spec.Tools, SkillToolName)
	if strings.Contains(string(skill.Schema), "enum") || strings.Contains(string(skill.Schema), "skill-a") {
		t.Errorf("Skill's schema names the skills: %s", skill.Schema)
	}
	_, err = skill.Invoke(t.Context(), json.RawMessage(`{"name":"nope"}`))
	if err == nil || !strings.Contains(err.Error(), "find one with graph") || strings.Contains(err.Error(), "skill-a") {
		t.Errorf("an unknown skill says %v; it must point to the search, not list every skill", err)
	}
	if out, err := skill.Invoke(t.Context(), json.RawMessage(`{"name":"skill-c"}`)); err != nil || out != "b" {
		t.Errorf("a skill found by search opened as %q, %v", out, err)
	}
}

// ReWOO plans every call before making one, so it is sent everything.
func TestReWOOIsSentEverything(t *testing.T) {
	t.Parallel()

	spec, search, err := withDiscovery(t.Context(), Spec{Pattern: PatternReWOO, Tools: []Tool{search()}, Discovery: large(&recorder{})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(spec.Tools), ","); got != "graph,github__close_issue,github__list_issues" || search != "" {
		t.Fatalf("ReWOO was sent %s, search %q", got, search)
	}
}

// many is a catalogue past DiscoverAbove with n small tools and Select set.
func many(n int, sel func(ctx context.Context, task string, limit int) ([]string, error)) *Discovery {
	c := &recorder{}
	d := &Discovery{Search: "graph", Select: sel}
	for i := range n {
		d.Tools = append(d.Tools, c.tool(fmt.Sprintf("srv__t%02d", i), strings.Repeat("x", 8*DiscoverAbove/n), `{}`))
	}
	return d
}

func asked(text ...string) []Message {
	var msgs []Message
	for i, s := range text {
		role := RoleUser
		if i%2 == 1 {
			role = RoleAssistant
		}
		msgs = append(msgs, Message{Role: role, Content: []Content{{Type: ContentText, Text: s}}})
	}
	return msgs
}

// A ReWOO run with too many tools to send plans with the ones picked for
// the last thing the user said, best first, never more than SelectLimit.
func TestReWOOPlansWithTheToolsPickedForItsTask(t *testing.T) {
	t.Parallel()

	var gotTask string
	var gotLimit int
	d := many(40, func(_ context.Context, task string, limit int) ([]string, error) {
		gotTask, gotLimit = task, limit
		picks := []string{"srv__t07", "not-a-tool", "srv__t03", "srv__t07"}
		for i := range 30 {
			picks = append(picks, fmt.Sprintf("srv__t%02d", 10+i))
		}
		return picks, nil
	})
	spec, search, err := withDiscovery(t.Context(), Spec{Pattern: PatternReWOO, Tools: []Tool{search()}, Discovery: d},
		asked("hello", "hi", "  label the open issues  "))
	if err != nil {
		t.Fatal(err)
	}

	got := names(spec.Tools)
	if gotTask != "label the open issues\nhi\nhello" || gotLimit != SelectLimit {
		t.Errorf("Select was asked for %q, %d", gotTask, gotLimit)
	}
	if search != "" || got[0] != "graph" || got[1] != "srv__t07" || got[2] != "srv__t03" {
		t.Errorf("sent %v, search %q; want graph, then the picks best first", got, search)
	}
	if len(got) != 1+SelectLimit {
		t.Errorf("sent %d tools, want the run's own and %d picked", len(got), SelectLimit)
	}
	if slices.Contains(got, "not-a-tool") {
		t.Error("a name that is not one of the run's tools was sent")
	}
}

// A follow-up names nothing; the turns before it say what it is about, so
// tools are picked for those too, the person's latest message first.
func TestAFollowUpIsPickedForWithTheTurnsBeforeIt(t *testing.T) {
	t.Parallel()

	var gotTask string
	d := many(40, func(_ context.Context, task string, _ int) ([]string, error) {
		gotTask = task
		return []string{"srv__t01"}, nil
	})
	msgs := asked(
		"what is the weather",
		"sunny",
		"list today's unread mail",
		"It failed: the response was too large. Retry with headers only?",
		"yes do it",
	)
	if _, _, err := withDiscovery(t.Context(), Spec{Pattern: PatternReWOO, Tools: []Tool{search()}, Discovery: d}, msgs); err != nil {
		t.Fatal(err)
	}
	want := "yes do it\nIt failed: the response was too large. Retry with headers only?\nlist today's unread mail\nsunny"
	if gotTask != want {
		t.Errorf("Select was asked for %q, want %q", gotTask, want)
	}
}

// One long turn cannot crowd out the rest: each is cut to its share, on a
// rune boundary.
func TestEachTurnIsClipped(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", turnChars)
	got := recentText(asked("first", long, "last"))
	parts := strings.Split(got, "\n")
	if len(parts) != 3 || parts[0] != "last" || len(parts[1]) > turnChars || !utf8.ValidString(parts[1]) {
		t.Errorf("recentText = %d parts, middle %d bytes, valid %v", len(parts), len(parts[1]), utf8.ValidString(parts[1]))
	}
}

// A compaction's marks are the run's bookkeeping, not the person's words, so
// tools are never picked for them.
func TestToolsAreNotPickedForACompactionsMarks(t *testing.T) {
	t.Parallel()

	msgs := []Message{say(RoleUser, "list today's unread mail"), clearingMessage("abc")}
	if got := recentText(msgs); got != "list today's unread mail" {
		t.Errorf("recentText = %q, want only what the person said", got)
	}
}

// Whatever goes wrong, a ReWOO run is sent every tool, as before Select.
func TestReWOOIsSentEverythingWhenNothingIsPicked(t *testing.T) {
	t.Parallel()

	fails := func(context.Context, string, int) ([]string, error) { return nil, errors.New("graph down") }
	strangers := func(context.Context, string, int) ([]string, error) { return []string{"elsewhere__x"}, nil }
	called := false
	never := func(context.Context, string, int) ([]string, error) { called = true; return []string{"srv__t01"}, nil }

	for _, tc := range []struct {
		name string
		d    *Discovery
		msgs []Message
	}{
		{"Select fails", many(40, fails), asked("label them")},
		{"Select picks none of the run's tools", many(40, strangers), asked("label them")},
		{"no Select", many(40, nil), asked("label them")},
		{"no user text", many(40, never), nil},
		{"only blank user text", many(40, never), asked("   ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, _, err := withDiscovery(t.Context(), Spec{Pattern: PatternReWOO, Tools: []Tool{search()}, Discovery: tc.d}, tc.msgs)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(spec.Tools); n != 41 {
				t.Errorf("sent %d tools, want all 40 and graph", n)
			}
		})
	}
	if called {
		t.Error("Select was asked with no task to pick for")
	}
}

// Select is only for a pattern that cannot search: a small catalogue is sent
// whole, and every other pattern defers as it did.
func TestSelectIsOnlyAskedByReWOOWithTooManyTools(t *testing.T) {
	t.Parallel()

	calls := 0
	sel := func(context.Context, string, int) ([]string, error) { calls++; return []string{"srv__t01"}, nil }

	small := &Discovery{Search: "graph", Select: sel, Tools: []Tool{(&recorder{}).tool("srv__a", "A", `{}`)}}
	if _, _, err := withDiscovery(t.Context(), Spec{Pattern: PatternReWOO, Tools: []Tool{search()}, Discovery: small}, asked("x")); err != nil {
		t.Fatal(err)
	}
	spec, search, err := withDiscovery(t.Context(), Spec{Pattern: PatternReAct, Tools: []Tool{search()}, Discovery: many(40, sel)}, asked("x"))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("Select was asked %d times", calls)
	}
	if search != "graph" || !slices.Contains(names(spec.Tools), CallToolName) {
		t.Errorf("ReAct was sent %v, search %q; want its tools deferred", names(spec.Tools), search)
	}
}

func TestADiscoveryThatCannotWorkIsRefused(t *testing.T) {
	t.Parallel()

	ok := func() Tool { return (&recorder{}).tool("github__list_issues", "", `{}`) }
	for _, tc := range []struct {
		name  string
		tools []Tool
		d     Discovery
		want  string
	}{
		{"no search", []Tool{search()}, Discovery{Tools: []Tool{ok()}}, "Search is empty"},
		{"a search that is not a tool", []Tool{search()}, Discovery{Search: "find", Tools: []Tool{ok()}}, `"find"`},
		{"a deferred call_tool", []Tool{search()}, Discovery{Search: "graph", Tools: []Tool{(&recorder{}).tool(CallToolName, "", `{}`)}}, CallToolName},
		{"a deferred Skill", []Tool{search()}, Discovery{Search: "graph", Tools: []Tool{(&recorder{}).tool(SkillToolName, "", `{}`)}}, SkillToolName},
		{"a listed describe_tools", []Tool{search(), {Name: DescribeToolsName}}, Discovery{Search: "graph"}, DescribeToolsName},
		{"twice", []Tool{search()}, Discovery{Search: "graph", Tools: []Tool{ok(), ok()}}, "two Discovery tools"},
		{"listed and deferred", []Tool{search(), ok()}, Discovery{Search: "graph", Tools: []Tool{ok()}}, "both"},
		{"no name", []Tool{search()}, Discovery{Search: "graph", Tools: []Tool{{Invoke: ok().Invoke}}}, "no name"},
		{"nothing to run", []Tool{search()}, Discovery{Search: "graph", Tools: []Tool{{Name: "x"}}}, "neither Invoke nor Result"},
		{"a schema that is not JSON", []Tool{search()}, Discovery{Search: "graph", Tools: []Tool{(&recorder{}).tool("x", "", `{`)}}, "not valid JSON"},
		{"a group with no name", []Tool{search()}, Discovery{Search: "graph", Tools: []Tool{ok()}, Groups: []Group{{Tools: []string{"github__list_issues"}}}}, "group has no name"},
		{"a group naming a stranger", []Tool{search()}, Discovery{Search: "graph", Tools: []Tool{ok()}, Groups: []Group{{Name: "g", Tools: []string{"slack__post"}}}}, `"slack__post"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := tc.d
			_, _, err := withDiscovery(t.Context(), Spec{Tools: tc.tools, Discovery: &d}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %s", err, tc.want)
			}
		})
	}
}

// A refused Discovery stops the run before the model is asked anything.
func TestARunWithABrokenDiscoveryNeverReachesTheModel(t *testing.T) {
	t.Parallel()

	model := &scripted{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return model, nil }, &callingPattern{})
	_, err := runner.Run(t.Context(), Spec{
		Pattern: "calling", MaxSteps: 1, Tools: []Tool{search()},
		Discovery: &Discovery{Search: "nowhere"},
	}, nil, Endpoint{}, nil)
	if err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("err = %v", err)
	}
	if len(model.sent) != 0 {
		t.Fatalf("the model was asked %d times", len(model.sent))
	}
}

func TestCallToolRunsAToolWithArgumentsItsSchemaAccepts(t *testing.T) {
	t.Parallel()

	c := &recorder{}
	run := toolNamed(t, found(t, Spec{Tools: []Tool{search()}, Discovery: large(c)}).Tools, CallToolName)

	r, err := call(t, run, `{"name":"github__close_issue","arguments":{"repo":"openarity","n":42}}`)
	if err != nil || r.Output != `closed {"repo":"openarity","n":42}` {
		t.Fatalf("call_tool returned %+v, %v", r, err)
	}
	// Some models quote the arguments; they are the same arguments.
	if _, err := call(t, run, `{"name":"github__close_issue","arguments":"{\"repo\":\"a\"}"}`); err != nil {
		t.Fatalf("quoted arguments were refused: %v", err)
	}
	// Left out, they are an empty object, which this schema accepts.
	if _, err := call(t, run, `{"name":"github__list_issues"}`); err != nil {
		t.Fatalf("no arguments were refused: %v", err)
	}
	if got := c.calls(); len(got) != 3 || got[2] != `{}` {
		t.Fatalf("the tools were called with %q", got)
	}
}

// Every refusal leaves the tool uncalled.
func TestCallToolRefusesWithoutRunningTheTool(t *testing.T) {
	t.Parallel()

	c := &recorder{}
	spec := found(t, Spec{Tools: []Tool{search()}, Discovery: large(c)})
	run := toolNamed(t, spec.Tools, CallToolName)

	for _, tc := range []struct{ args, want string }{
		{`{"name":"slack__post","arguments":{}}`, `there is no tool named "slack__post" in this run; find one with graph`},
		{`{"name":"graph","arguments":{}}`, "graph is already among your tools; call it directly"},
		{`{"name":"github__close_issue","arguments":{"n":42}}`, `missing properties: ["repo"]`},
		{`{"name":"github__close_issue","arguments":{"repo":1}}`, `want "string"`},
		{`{"name":"github__close_issue","arguments":{"repo":"a","x":1}}`, "additional properties"},
		{`{"name":"github__close_issue","arguments":"{not json"}`, "a string that is not JSON"},
		{`{"arguments":{}}`, "name is required"},
		{`not json`, "not valid JSON"},
	} {
		_, err := call(t, run, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	_, err := call(t, run, `{"name":"github__close_issue","arguments":{"n":42}}`)
	if !strings.Contains(err.Error(), "describe_tools shows it") {
		t.Errorf("a schema refusal does not say where the schema is: %v", err)
	}
	if got := c.calls(); len(got) != 0 {
		t.Fatalf("a refused call ran the tool with %q", got)
	}
}

// A schema the validator cannot compile is not checked here, and its remote
// $ref is never fetched.
func TestASchemaThatDoesNotCompileIsLeftToTheServer(t *testing.T) {
	t.Parallel()

	var fetched atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)
		_, _ = w.Write([]byte(`{"type":"string"}`))
	}))
	defer srv.Close()

	c := &recorder{}
	d := large(c)
	d.Tools[1] = c.tool("github__list_issues", "List issues", `{"$ref":"`+srv.URL+`/schema.json"}`)
	run := toolNamed(t, found(t, Spec{Tools: []Tool{search()}, Discovery: d}).Tools, CallToolName)

	if _, err := call(t, run, `{"name":"github__list_issues","arguments":{"any":"thing"}}`); err != nil {
		t.Fatalf("a call to a tool with an uncompilable schema was refused: %v", err)
	}
	if fetched.Load() != 0 {
		t.Fatalf("the remote schema was fetched %d times", fetched.Load())
	}

	// JSON, but not a schema at all.
	d.Tools[0] = c.tool("github__close_issue", strings.Repeat("x", 4*DiscoverAbove), `[]`)
	run = toolNamed(t, found(t, Spec{Tools: []Tool{search()}, Discovery: d}).Tools, CallToolName)
	if _, err := call(t, run, `{"name":"github__close_issue","arguments":{"any":"thing"}}`); err != nil {
		t.Fatalf("a call to a tool whose schema is not an object was refused: %v", err)
	}
	if len(c.calls()) != 2 {
		t.Fatalf("the tools ran %d times", len(c.calls()))
	}
}

// What a deferred tool attaches reaches the model as a listed tool's would.
func TestCallToolPassesOnWhatTheToolAttached(t *testing.T) {
	t.Parallel()

	d := large(&recorder{})
	d.Tools[1] = Tool{Name: "github__list_issues", Result: func(context.Context, json.RawMessage) (ToolResult, error) {
		return ToolResult{Output: "one image", Attach: []Content{{Type: ContentImage, Blob: &Blob{MediaType: "image/png", Ref: "a1"}}}}, nil
	}}
	run := toolNamed(t, found(t, Spec{Tools: []Tool{search()}, Discovery: d}).Tools, CallToolName)

	r, err := call(t, run, `{"name":"github__list_issues"}`)
	if err != nil || r.Output != "one image" || len(r.Attach) != 1 || r.Attach[0].Blob.Ref != "a1" {
		t.Fatalf("call_tool returned %+v, %v", r, err)
	}

	msg, ok := Attached(map[string][]Content{"c1": r.Attach},
		[]ToolCall{{ID: "c1", Name: CallToolName, Arguments: json.RawMessage(`{"name":"github__list_issues"}`)}})
	if !ok || msg.Content[0].Text != "Attached by github__list_issues for call c1, as its output, not the person's words:" {
		t.Fatalf("the files are introduced as %+v", msg.Content)
	}
}

func TestDescribeToolsShowsWhatEachToolTakes(t *testing.T) {
	t.Parallel()

	d := large(&recorder{})
	d.Tools[1].Schema = nil
	describe := toolNamed(t, found(t, Spec{Tools: []Tool{search()}, Discovery: d}).Tools, DescribeToolsName)

	out, err := describe.Invoke(t.Context(), json.RawMessage(`{"names":["github__close_issue","github__list_issues","graph","slack__post"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var got []described
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("describe_tools wrote %s: %v", out, err)
	}
	if len(got) != 4 {
		t.Fatalf("described %d tools: %s", len(got), out)
	}
	if string(got[0].InputSchema) != issueSchema || got[0].Error != "" {
		t.Errorf("the first is %+v", got[0])
	}
	if string(got[1].InputSchema) != `{"type":"object"}` {
		t.Errorf("a tool with no schema is shown as %s", got[1].InputSchema)
	}
	if got[2].Error != "already among your tools; call it directly" || got[2].InputSchema != nil {
		t.Errorf("a listed tool is described as %+v", got[2])
	}
	if !strings.Contains(got[3].Error, "find one with graph") {
		t.Errorf("an unknown tool is described as %+v", got[3])
	}

	for _, args := range []string{`{"names":[]}`, `{"names":["a","b","c","d","e","f"]}`, `nope`} {
		if _, err := describe.Invoke(t.Context(), json.RawMessage(args)); err == nil {
			t.Errorf("describe_tools accepted %s", args)
		}
	}
}

// call_tool is one of the run's tools, so a wrap-up stops it as it stops the
// rest, and the tool behind it never runs.
func TestCallToolRefusesOnceAWrapUpIsAsked(t *testing.T) {
	t.Parallel()

	c := &recorder{}
	pattern := &callingPattern{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return &scripted{}, nil }, pattern)
	run := runner.Start(t.Context(), Spec{
		Pattern: "calling", MaxSteps: 1, Tools: []Tool{search()}, Discovery: large(c),
	}, nil, Endpoint{}, nil)
	if _, err := run.Wait(); err != nil {
		t.Fatal(err)
	}
	run.box.finished = false
	if err := run.box.wrap(wrapText); err != nil {
		t.Fatal(err)
	}

	_, err := call(t, toolNamed(t, pattern.tools, CallToolName), `{"name":"github__list_issues"}`)
	if err == nil || err.Error() != wrapText {
		t.Fatalf("after a wrap-up call_tool returned %v", err)
	}
	if len(c.calls()) != 0 {
		t.Fatalf("the tool behind call_tool ran after a wrap-up")
	}
}

func TestInnerNamesTheToolACallStandsFor(t *testing.T) {
	t.Parallel()

	got := Inner(ToolCall{ID: "c1", Name: CallToolName, Arguments: json.RawMessage(`{"name":"github__close_issue","arguments":{"n":1}}`)})
	if got.ID != "c1" || got.Name != "github__close_issue" || string(got.Arguments) != `{"n":1}` {
		t.Errorf("Inner = %+v", got)
	}
	for _, c := range []ToolCall{
		{ID: "c2", Name: "graph", Arguments: json.RawMessage(`{"name":"x"}`)},
		{ID: "c3", Name: CallToolName, Arguments: json.RawMessage(`{`)},
		{ID: "c4", Name: CallToolName, Arguments: json.RawMessage(`{"arguments":{}}`)},
	} {
		if got := Inner(c); got.Name != c.Name || string(got.Arguments) != string(c.Arguments) {
			t.Errorf("Inner(%+v) = %+v; it must come back as it was", c, got)
		}
	}
}

// Calls run in parallel when a turn makes several; each schema compiles once,
// shared, and the race detector watches the sharing.
func TestCallToolIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	c := &recorder{}
	run := toolNamed(t, found(t, Spec{Tools: []Tool{search()}, Discovery: large(c)}).Tools, CallToolName)

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, _, err := run.Call(context.Background(), json.RawMessage(`{"name":"github__close_issue","arguments":{"repo":"a"}}`)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(c.calls()) != 16 {
		t.Fatalf("%d of 16 calls ran", len(c.calls()))
	}
}

// FoundBySearch says what withDiscovery does: found by search past the
// threshold, sent at or under it, sent to ReWOO, and nothing with no
// Discovery.
func TestFoundBySearchSaysWhatARunIsSent(t *testing.T) {
	t.Parallel()

	c := &recorder{}
	small := &Discovery{Search: "graph", Tools: []Tool{c.tool("t", "a tool", `{}`)}}
	for name, tc := range map[string]struct {
		spec Spec
		want bool
	}{
		"large":        {Spec{Discovery: large(c)}, true},
		"small":        {Spec{Discovery: small}, false},
		"large, ReWOO": {Spec{Discovery: large(c), Pattern: PatternReWOO}, false},
		"no discovery": {Spec{}, false},
	} {
		if got := tc.spec.FoundBySearch(); got != tc.want {
			t.Errorf("%s: FoundBySearch = %v, want %v", name, got, tc.want)
		}
		spec := tc.spec
		spec.Tools = []Tool{search()}
		sent, found, err := withDiscovery(t.Context(), spec, nil)
		if err != nil {
			t.Fatal(err)
		}
		if (found != "") != tc.want || slices.Contains(names(sent.Tools), DescribeToolsName) != tc.want {
			t.Errorf("%s: withDiscovery searched by %q, sent %v; FoundBySearch says %v", name, found, names(sent.Tools), tc.want)
		}
	}
}
