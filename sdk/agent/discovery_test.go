package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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
	out, search, err := withDiscovery(spec)
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
	spec, search, err := withDiscovery(Spec{
		Tools:     []Tool{search()},
		Discovery: &Discovery{Search: "graph", Tools: []Tool{c.tool("github__list_issues", "List issues", `{"type":"object"}`)}},
	})
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
	spec, _, err := withDiscovery(Spec{Tools: []Tool{search()}, Discovery: &Discovery{Search: "graph", Tools: []Tool{at}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(spec.Tools), ","); got != "graph,t" {
		t.Fatalf("at the threshold the run sent %s", got)
	}

	over := c.tool("t", strings.Repeat("x", 4*DiscoverAbove-len("t")-len(`{}`)+4), `{}`)
	spec, _, err = withDiscovery(Spec{Tools: []Tool{search()}, Discovery: &Discovery{Search: "graph", Tools: []Tool{over}}})
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

	spec, search, err := withDiscovery(Spec{Pattern: PatternReWOO, Tools: []Tool{search()}, Discovery: large(&recorder{})})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(spec.Tools), ","); got != "graph,github__close_issue,github__list_issues" || search != "" {
		t.Fatalf("ReWOO was sent %s, search %q", got, search)
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
			_, _, err := withDiscovery(Spec{Tools: tc.tools, Discovery: &d})
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
	if !ok || msg.Content[0].Text != "Attached by github__list_issues for call c1:" {
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
