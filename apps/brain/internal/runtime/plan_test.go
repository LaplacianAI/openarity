package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// noted is what a run's recorder was handed, in order.
type noted struct {
	mu    sync.Mutex
	runs  []uuid.UUID
	kinds []string
	plans []planPayload
	err   error
}

func (n *noted) note(_ context.Context, runID uuid.UUID, _, kind string, payload any) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.runs = append(n.runs, runID)
	n.kinds = append(n.kinds, kind)
	if p, ok := payload.(planPayload); ok {
		n.plans = append(n.plans, p)
	}
	return n.err
}

// planTool is the fixture's run made the orchestrator's, built with a
// recorder, and its plan tool.
func planFixture(t *testing.T) (*fixture, *noted, agent.Tool) {
	t.Helper()

	f := newFixture()
	f.asOrchestrator()
	f.teamModel()
	n := &noted{}
	f.builder.note = n.note
	for _, tl := range f.build(t).Spec.Tools {
		if tl.Name == PlanToolName {
			return f, n, tl
		}
	}
	t.Fatal("the hub has no plan tool")
	return nil, nil, agent.Tool{}
}

func items(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"text":"step","status":"pending"}`
	}
	return `{"items":[` + strings.Join(parts, ",") + `]}`
}

func TestThePlanToolIsNamedAndDescribed(t *testing.T) {
	_, _, tl := planFixture(t)

	if tl.Name != "plan" || tl.Description == "" {
		t.Errorf("name %q, description %q", tl.Name, tl.Description)
	}
	var schema struct {
		Required             []string `json:"required"`
		AdditionalProperties *bool    `json:"additionalProperties"`
		Properties           struct {
			Items struct {
				MaxItems int `json:"maxItems"`
				Items    struct {
					Required   []string `json:"required"`
					Properties struct {
						Text struct {
							MaxLength int `json:"maxLength"`
						} `json:"text"`
						Status struct {
							Enum []string `json:"enum"`
						} `json:"status"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tl.Schema, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	item := schema.Properties.Items
	if !slices.Equal(schema.Required, []string{"items"}) || schema.AdditionalProperties == nil || *schema.AdditionalProperties ||
		item.MaxItems != 30 || item.Items.Properties.Text.MaxLength != 200 ||
		!slices.Equal(item.Items.Properties.Status.Enum, []string{"pending", "in_progress", "done"}) ||
		!slices.Equal(item.Items.Required, []string{"text", "status"}) {
		t.Errorf("schema = %s", tl.Schema)
	}
}

func TestAPlanIsSavedAsAnEvent(t *testing.T) {
	f, n, tl := planFixture(t)

	out, err := tl.Invoke(t.Context(), json.RawMessage(`{"items":[
		{"text":"Find who handles invoices","status":"done"},
		{"text":"  Ask billing for this week's  ","status":"in_progress"},
		{"text":"Write the answer","status":"pending"},
		{"text":"Check it reads plainly","status":"pending"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	if out != "Plan saved: 1 done, 1 in progress, 2 pending." {
		t.Errorf("result = %q", out)
	}
	want := planPayload{Items: []planItem{
		{Text: "Find who handles invoices", Status: "done"},
		{Text: "Ask billing for this week's", Status: "in_progress"},
		{Text: "Write the answer", Status: "pending"},
		{Text: "Check it reads plainly", Status: "pending"},
	}}
	if len(n.plans) != 1 || !slices.Equal(n.plans[0].Items, want.Items) {
		t.Errorf("noted %+v, want %+v", n.plans, want)
	}
	if !slices.Equal(n.kinds, []string{"plan"}) || n.runs[0] != f.run.ID {
		t.Errorf("noted %v on %v, want one plan on the run %s", n.kinds, n.runs, f.run.ID)
	}
}

// The plan is the latest call's list; nothing is carried over.
func TestEachPlanReplacesTheLast(t *testing.T) {
	_, n, tl := planFixture(t)

	for _, args := range []string{
		`{"items":[{"text":"a","status":"in_progress"},{"text":"b","status":"pending"}]}`,
		`{"items":[{"text":"c","status":"done"}]}`,
		`{"items":[]}`,
	} {
		if _, err := tl.Invoke(t.Context(), json.RawMessage(args)); err != nil {
			t.Fatal(err)
		}
	}

	if len(n.plans) != 3 || !slices.Equal(n.plans[1].Items, []planItem{{Text: "c", Status: "done"}}) {
		t.Errorf("plans = %+v, want the second to be c alone", n.plans)
	}
	if n.plans[2].Items == nil || len(n.plans[2].Items) != 0 {
		t.Errorf("an emptied plan noted %+v, want an empty list, not null", n.plans[2].Items)
	}
}

func TestAPlanThatCannotBeKeptIsRefused(t *testing.T) {
	_, n, tl := planFixture(t)

	for name, tc := range map[string]struct{ args, says string }{
		"31 items":               {items(31), "a plan has at most 30 items, and this has 31"},
		"empty text":             {`{"items":[{"text":"a","status":"done"},{"text":"   ","status":"pending"}]}`, "item 2 has no text"},
		"201 bytes":              {`{"items":[{"text":"` + strings.Repeat("a", 201) + `","status":"pending"}]}`, "item 1 is 201 bytes; an item's text is at most 200"},
		"202 bytes of 101 runes": {`{"items":[{"text":"` + strings.Repeat("é", 101) + `","status":"pending"}]}`, "item 1 is 202 bytes; an item's text is at most 200"},
		"two in_progress":        {`{"items":[{"text":"a","status":"in_progress"},{"text":"b","status":"in_progress"}]}`, "only one item may be in_progress, and 2 are"},
		"unknown status":         {`{"items":[{"text":"a","status":"blocked"}]}`, `item 1's status is "blocked"; it must be pending, in_progress or done`},
		"a NUL":                  {`{"items":[{"text":"a\u0000b","status":"pending"}]}`, "item 1 contains a NUL character"},
		"no items":               {`{}`, "items is required"},
		"extra field":            {`{"items":[],"title":"x"}`, "the arguments are not the JSON the schema asks for"},
		"extra in item":          {`{"items":[{"text":"a","status":"done","owner":"billing"}]}`, "the arguments are not the JSON the schema asks for"},
		"not JSON":               {`{`, "the arguments are not the JSON the schema asks for"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tl.Invoke(t.Context(), json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("err = %v, want it to say %q", err, tc.says)
			}
		})
	}
	if len(n.kinds) != 0 {
		t.Errorf("a refused plan noted %v", n.kinds)
	}
}

func TestAPlanOfExactlyTheLimitsIsKept(t *testing.T) {
	_, n, tl := planFixture(t)

	long := `{"items":[{"text":"` + strings.Repeat("é", 100) + `","status":"pending"}]}`
	for _, args := range []string{items(30), long} {
		if _, err := tl.Invoke(t.Context(), json.RawMessage(args)); err != nil {
			t.Errorf("%.40s…: %v", args, err)
		}
	}
	if len(n.plans) != 2 {
		t.Errorf("noted %d plans, want 2", len(n.plans))
	}
}

// A plan that was not recorded was not kept, and the model is told so.
func TestAPlanThatCannotBeRecordedIsAToolError(t *testing.T) {
	_, n, tl := planFixture(t)
	n.err = errors.New("connection reset by peer")

	if _, err := tl.Invoke(t.Context(), json.RawMessage(items(1))); err == nil {
		t.Error("a plan the recorder refused was reported saved")
	}
}

func TestOnlyTheHubKeepsAPlan(t *testing.T) {
	f := newFixture()
	f.builder.note = (&noted{}).note

	for _, tl := range f.build(t).Spec.Tools {
		if tl.Name == PlanToolName {
			t.Error("a plain agent was given plan")
		}
	}
}

// --- end to end ---

// The plan goes through the run's one writer, so it lands after the call
// that made it and before that call's result.
func TestThePlanEventLandsBetweenItsCallAndItsResult(t *testing.T) {
	f := newExecFixture(t, newScripted(
		callTool("call_1", "plan", `{"items":[{"text":"Find billing","status":"in_progress"},{"text":"Answer","status":"pending"}]}`),
		answer("Billing has three due.", 1, 1),
	))
	f.hubRun(t, "team/model")

	f.mustExecute(t)

	wantStatus(t, f.row(t), "succeeded", "")
	events := f.events(t)
	at := func(kind string) int {
		return slices.IndexFunc(events, func(e db.RunEvent) bool { return e.Kind == kind })
	}
	if c, p, r := at("tool_call"), at("plan"), at("tool_result"); c < 0 || c >= p || p >= r {
		t.Fatalf("events = %v, want tool_call, plan, tool_result in order", kinds(events))
	}
	plan := payload[planPayload](t, events[at("plan")])
	if len(plan.Items) != 2 || plan.Items[0] != (planItem{Text: "Find billing", Status: "in_progress"}) {
		t.Errorf("plan = %+v", plan)
	}
	if r := payload[toolResultPayload](t, events[at("tool_result")]); r.Output != "Plan saved: 0 done, 1 in progress, 1 pending." {
		t.Errorf("tool_result = %+v", r)
	}
}

// plan is the brain's name, as graph and delegate are: a bare server's tool
// of that name is left out and said so, for every agent, since any agent may
// be chosen as the hub.
func TestBuildLeavesOutAnMCPToolNamedPlan(t *testing.T) {
	f := newFixture()
	f.asChosenHub()
	f.builder.note = (&noted{}).note
	f.store.grants = []db.ListGrantedMCPServersRow{urlServer("web", serve(t, "plan", "search").url, true)}

	built := f.build(t)

	var plans []agent.Tool
	for _, tl := range built.Spec.Tools {
		if tl.Name == PlanToolName {
			plans = append(plans, tl)
		}
	}
	if len(plans) != 1 || plans[0].Description != planDescription {
		t.Errorf("plan tools = %d, want only the brain's", len(plans))
	}
	want := Unavailable{Name: "plan", Reason: `the MCP server "web" offers a tool named plan, which is the brain's own`}
	if !slices.Contains(built.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", built.Unavailable, want)
	}
}
