package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// counted returns a Read that reports how often it ran, so a test can prove a
// resource was not read before the model asked for it.
func counted(s string, n *atomic.Int32) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		n.Add(1)
		return s, nil
	}
}

func reviewSkill(resources ...Resource) Skill {
	return Skill{
		Name:        "review",
		Description: "Review a pull request",
		Body:        body("# Review\nCheck the diff against the checklist."),
		Resources:   resources,
	}
}

// tools builds both skill tools the way withSkills does: one loaded set,
// shared. ok is false when no skill has resources.
func tools(t *testing.T, skills ...Skill) (skill, resource Tool, ok bool) {
	t.Helper()

	loaded := newLoadedSkills()
	skill, _, err := skillTool(skills, loaded)
	if err != nil {
		t.Fatalf("skillTool: %v", err)
	}
	resource, ok = resourceTool(skills, loaded)
	return skill, resource, ok
}

func load(t *testing.T, skill Tool, name string) string {
	t.Helper()

	got, err := skill.Invoke(t.Context(), json.RawMessage(`{"name":"`+name+`"}`))
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return got
}

func read(t *testing.T, resource Tool, skill, name string) (string, error) {
	t.Helper()

	args, _ := json.Marshal(map[string]string{"skill": skill, "name": name})
	return resource.Invoke(t.Context(), args)
}

// --- what a Spec may hold ---

// Each of these is a skill that cannot work, refused at Start rather than
// when the model first reaches for it — after it has already been paid for.
func TestAResourceThatCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		resources []Resource
		want      string
	}{
		"no name":         {[]Resource{{Read: body("a")}}, "no name"},
		"no Read":         {[]Resource{{Name: "checklist"}}, "checklist"},
		"two of one name": {[]Resource{{Name: "checklist", Read: body("a")}, {Name: "checklist", Read: body("b")}}, "two resources"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := withSkills(Spec{Skills: []Skill{reviewSkill(tc.resources...)}})
			if err == nil {
				t.Fatal("the spec was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "review") {
				t.Errorf("err = %v, want it to name the skill and say %q", err, tc.want)
			}
		})
	}
}

// The same resource name in two skills is two resources, not a clash.
func TestTwoSkillsMayEachHaveAResourceOfTheSameName(t *testing.T) {
	t.Parallel()

	other := Skill{
		Name: "deploy", Description: "Deploy", Body: body("deploy"),
		Resources: []Resource{{Name: "checklist", Read: body("deploy checklist")}},
	}
	skill, resource, _ := tools(t, reviewSkill(Resource{Name: "checklist", Read: body("review checklist")}), other)

	load(t, skill, "review")
	load(t, skill, "deploy")
	for s, want := range map[string]string{"review": "review checklist", "deploy": "deploy checklist"} {
		if got, err := read(t, resource, s, "checklist"); err != nil || got != want {
			t.Errorf("%s/checklist = %q, %v; want %q", s, got, err, want)
		}
	}
}

// --- the tool list ---

// A run whose skills have no resources sends the same tools as before
// resources existed, so its cached prefix does not move.
func TestSkillsWithNoResourcesAddNoSecondTool(t *testing.T) {
	t.Parallel()

	spec, err := withSkills(Spec{Skills: []Skill{{Name: "review", Description: "Review", Body: body("a")}}})
	if err != nil {
		t.Fatalf("withSkills: %v", err)
	}
	for _, tl := range spec.Tools {
		if tl.Name == SkillResourceToolName {
			t.Error("the resource tool was offered with no resource to read")
		}
	}
}

// The body tells the model to call a tool by name. That name has to be the
// one the runner registered, or the model calls a tool that does not exist.
func TestTheListingNamesTheToolTheRunnerRegisters(t *testing.T) {
	t.Parallel()

	spec, err := withSkills(Spec{Skills: []Skill{reviewSkill(Resource{Name: "checklist", Read: body("a")})}})
	if err != nil {
		t.Fatalf("withSkills: %v", err)
	}

	var skill Tool
	var registered []string
	for _, tl := range spec.Tools {
		registered = append(registered, tl.Name)
		if tl.Name == SkillToolName {
			skill = tl
		}
	}
	if !slices.Contains(registered, SkillResourceToolName) {
		t.Fatalf("tools = %v, want %s among them", registered, SkillResourceToolName)
	}

	got := load(t, skill, "review")
	if !strings.Contains(got, " "+SkillResourceToolName+" tool") {
		t.Errorf("the body's listing does not name the registered tool %q:\n%s", SkillResourceToolName, got)
	}
}

func TestACallersToolNamedSkillResourceIsRefused(t *testing.T) {
	t.Parallel()

	_, err := withSkills(Spec{
		Tools:  []Tool{{Name: SkillResourceToolName}},
		Skills: []Skill{reviewSkill(Resource{Name: "checklist", Read: body("a")})},
	})
	if err == nil {
		t.Fatal("a caller's tool shadowed the resource tool")
	}
	if !strings.Contains(err.Error(), SkillResourceToolName) {
		t.Errorf("err = %v, want it to name %s", err, SkillResourceToolName)
	}
}

// Only skills that have something to read can be named, so the gateway
// refuses a call for any other before Invoke is reached.
func TestTheResourceSchemaEnumeratesOnlySkillsWithResources(t *testing.T) {
	t.Parallel()

	_, resource, ok := tools(t,
		reviewSkill(Resource{Name: "checklist", Read: body("a")}),
		Skill{Name: "commit-style", Description: "Commits", Body: body("b")},
	)
	if !ok {
		t.Fatal("no resource tool")
	}

	var schema struct {
		Properties struct {
			Skill struct {
				Enum []string `json:"enum"`
			} `json:"skill"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(resource.Schema, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if !slices.Equal(schema.Properties.Skill.Enum, []string{"review"}) {
		t.Errorf("enum = %v, want only the skill that has resources", schema.Properties.Skill.Enum)
	}
}

// --- the three levels ---

// Resource names are level three. In the system listing they would be paid
// for on every request whether the skill is ever opened or not.
func TestTheSystemListingNamesNoResource(t *testing.T) {
	t.Parallel()

	_, listing := mustSkillTool(t, reviewSkill(Resource{Name: "references/checklist.md", Read: body("a")}))

	if strings.Contains(listing.Text, "checklist") {
		t.Errorf("the always-sent listing names a resource:\n%s", listing.Text)
	}
}

func TestLoadingASkillListsEachOfItsResources(t *testing.T) {
	t.Parallel()

	skill, _, _ := tools(t, reviewSkill(
		Resource{Name: "references/checklist.md", Read: body("a")},
		Resource{Name: "assets/template.md", Read: body("b")},
	))

	got := load(t, skill, "review")
	if !strings.HasPrefix(got, "# Review\nCheck the diff against the checklist.") {
		t.Errorf("the body does not come first:\n%s", got)
	}
	for _, name := range []string{"references/checklist.md", "assets/template.md"} {
		if !strings.Contains(got, "- "+name+"\n") {
			t.Errorf("the load does not list %q:\n%s", name, got)
		}
	}
}

// Nothing is read ahead: loading the body reads no resource, and a resource
// is read exactly when it is asked for.
func TestAResourceIsReadOnlyWhenAskedFor(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	skill, resource, _ := tools(t, reviewSkill(Resource{Name: "checklist", Read: counted("- tests for every guard", &reads)}))

	load(t, skill, "review")
	if n := reads.Load(); n != 0 {
		t.Fatalf("loading the body read the resource %d times", n)
	}

	got, err := read(t, resource, "review", "checklist")
	if err != nil || got != "- tests for every guard" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("the resource was read %d times, want 1", n)
	}
}

// Level three waits for level two: a resource read without the instructions
// that say when to use it is refused, and Read never runs.
func TestAResourceIsRefusedUntilItsSkillIsLoaded(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	skill, resource, _ := tools(t, reviewSkill(Resource{Name: "checklist", Read: counted("a", &reads)}))

	_, err := read(t, resource, "review", "checklist")
	if err == nil {
		t.Fatal("a resource was returned before its skill was loaded")
	}
	if !strings.Contains(err.Error(), SkillToolName) || !strings.Contains(err.Error(), "review") {
		t.Errorf("err = %v, want it to send the model to the %s tool for review", err, SkillToolName)
	}
	if reads.Load() != 0 {
		t.Error("the refused resource was read anyway")
	}

	load(t, skill, "review")
	if _, err := read(t, resource, "review", "checklist"); err != nil {
		t.Errorf("after loading the skill: %v", err)
	}
}

func TestLoadingOneSkillDoesNotUnlockAnothersResources(t *testing.T) {
	t.Parallel()

	other := Skill{
		Name: "deploy", Description: "Deploy", Body: body("deploy"),
		Resources: []Resource{{Name: "runbook", Read: body("a")}},
	}
	skill, resource, _ := tools(t, reviewSkill(Resource{Name: "checklist", Read: body("b")}), other)

	load(t, skill, "review")
	if _, err := read(t, resource, "deploy", "runbook"); err == nil {
		t.Error("loading review unlocked deploy's resources")
	}
}

// A body that failed was never read, so the model has not seen the
// instructions — its resources stay locked.
func TestAFailedLoadDoesNotUnlockResources(t *testing.T) {
	t.Parallel()

	failing := reviewSkill(Resource{Name: "checklist", Read: body("a")})
	failing.Body = func(context.Context) (string, error) { return "", errors.New("unreachable") }
	skill, resource, _ := tools(t, failing)

	if _, err := skill.Invoke(t.Context(), json.RawMessage(`{"name":"review"}`)); err == nil {
		t.Fatal("the failing body loaded")
	}
	if _, err := read(t, resource, "review", "checklist"); err == nil {
		t.Error("a resource was unlocked by a load that failed")
	}
}

// Each run starts with nothing loaded. A set shared across runs would let one
// conversation's load unlock another's resources.
func TestEachRunStartsWithNothingLoaded(t *testing.T) {
	t.Parallel()

	spec := Spec{Skills: []Skill{reviewSkill(Resource{Name: "checklist", Read: body("a")})}}

	find := func(s Spec, name string) Tool {
		for _, tl := range s.Tools {
			if tl.Name == name {
				return tl
			}
		}
		t.Fatalf("no tool named %s", name)
		return Tool{}
	}

	first, err := withSkills(spec)
	if err != nil {
		t.Fatalf("withSkills: %v", err)
	}
	second, err := withSkills(spec)
	if err != nil {
		t.Fatalf("withSkills: %v", err)
	}

	load(t, find(first, SkillToolName), "review")
	if _, err := read(t, find(second, SkillResourceToolName), "review", "checklist"); err == nil {
		t.Error("a load in one run unlocked a resource in another")
	}
}

// The path a run takes: both tools as withSkills builds them. The other tests
// build the tools through a helper that shares the loaded set correctly, so
// without this one withSkills could hand each tool its own set — and every
// resource would be refused forever — with the whole suite passing.
func TestALoadThroughTheRunnersToolsUnlocksTheResource(t *testing.T) {
	t.Parallel()

	spec, err := withSkills(Spec{Skills: []Skill{reviewSkill(Resource{Name: "checklist", Read: body("- tests")})}})
	if err != nil {
		t.Fatalf("withSkills: %v", err)
	}

	byName := map[string]Tool{}
	for _, tl := range spec.Tools {
		byName[tl.Name] = tl
	}

	load(t, byName[SkillToolName], "review")
	got, err := read(t, byName[SkillResourceToolName], "review", "checklist")
	if err != nil || got != "- tests" {
		t.Errorf("after loading through the runner's own tools: %q, %v", got, err)
	}
}

// --- refusals the model can act on ---

func TestAResourceOfASkillWithNoneNamesTheOnesThatHaveSome(t *testing.T) {
	t.Parallel()

	_, resource, _ := tools(t,
		reviewSkill(Resource{Name: "checklist", Read: body("a")}),
		Skill{Name: "commit-style", Description: "Commits", Body: body("b")},
	)

	_, err := read(t, resource, "commit-style", "checklist")
	if err == nil || !strings.Contains(err.Error(), "review") {
		t.Errorf("err = %v, want it to name the skills that have resources", err)
	}
}

func TestAnUnknownResourceIsRefused(t *testing.T) {
	t.Parallel()

	skill, resource, _ := tools(t, reviewSkill(Resource{Name: "checklist", Read: body("a")}))
	load(t, skill, "review")

	_, err := read(t, resource, "review", "../../etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "no resource named") {
		t.Errorf("err = %v, want the unknown name refused", err)
	}
}

func TestResourceArgumentsThatAreNotJSONAreRefused(t *testing.T) {
	t.Parallel()

	_, resource, _ := tools(t, reviewSkill(Resource{Name: "checklist", Read: body("a")}))

	if _, err := resource.Invoke(t.Context(), json.RawMessage(`{"skill":`)); err == nil {
		t.Error("malformed arguments were accepted")
	}
}

// A failure of the caller's storage reaches the pattern unchanged, the same
// way a failing body does.
func TestAReadErrorIsReturned(t *testing.T) {
	t.Parallel()

	unreachable := errors.New("the object store is unreachable")
	skill, resource, _ := tools(t, reviewSkill(Resource{
		Name: "checklist",
		Read: func(context.Context) (string, error) { return "", unreachable },
	}))
	load(t, skill, "review")

	if _, err := read(t, resource, "review", "checklist"); !errors.Is(err, unreachable) {
		t.Errorf("err = %v, want the read's own error", err)
	}
}

// A pattern may dispatch a turn's tool calls concurrently, so a load and a
// read of the same skill can race. Under -race this catches the shared set
// losing its lock.
func TestTheSkillToolsAreSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	skill, resource, _ := tools(t, reviewSkill(Resource{Name: "checklist", Read: body("a")}))

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = skill.Invoke(t.Context(), json.RawMessage(`{"name":"review"}`))
		}()
		go func() {
			defer wg.Done()
			_, _ = read(t, resource, "review", "checklist")
		}()
	}
	wg.Wait()
}
