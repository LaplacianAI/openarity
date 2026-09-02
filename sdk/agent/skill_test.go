package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func body(text string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return text, nil }
}

func mustSkillTool(t *testing.T, skills ...Skill) (Tool, Content) {
	t.Helper()
	tool, listing, err := skillTool(skills)
	if err != nil {
		t.Fatalf("skillTool: %v", err)
	}
	return tool, listing
}

// The listing is tier one: everything the model has to decide on before it has
// loaded anything. A skill missing from it can never be asked for.
func TestTheListingNamesEverySkillWithItsDescription(t *testing.T) {
	t.Parallel()

	_, listing := mustSkillTool(t,
		Skill{Name: "pdf-forms", Description: "Fill in the fields of a PDF form", Body: body("a")},
		Skill{Name: "commit-style", Description: "Write a commit message this repo accepts", Body: body("b")},
	)

	for _, want := range []string{
		"pdf-forms", "Fill in the fields of a PDF form",
		"commit-style", "Write a commit message this repo accepts",
	} {
		if !strings.Contains(listing.Text, want) {
			t.Errorf("the listing does not contain %q:\n%s", want, listing.Text)
		}
	}
	if !strings.Contains(listing.Text, SkillToolName) {
		t.Error("the listing never names the tool the model has to call")
	}
}

// The listing is paid for on every turn whether a skill is opened or not, so
// one skill must not be able to spend the whole budget.
func TestALongDescriptionIsTruncated(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", descriptionLimit*2)
	_, listing := mustSkillTool(t, Skill{Name: "verbose", Description: long, Body: body("a")})

	if strings.Contains(listing.Text, long) {
		t.Fatal("the whole description reached the listing")
	}
	if n := utf8.RuneCountInString(listing.Text); n > descriptionLimit+200 {
		t.Errorf("the listing is %d runes, want the description capped near %d", n, descriptionLimit)
	}
	if !strings.Contains(listing.Text, "…") {
		t.Error("the description was cut with no sign it was cut")
	}
}

// Cutting a multi-byte character in half produces invalid UTF-8, which some
// gateways reject outright and others pass on as a replacement character in the
// middle of a sentence.
func TestTruncationCutsAtRunesNotBytes(t *testing.T) {
	t.Parallel()

	_, listing := mustSkillTool(t, Skill{
		Name:        "multibyte",
		Description: "x" + strings.Repeat("日", descriptionLimit*2), // the leading ASCII byte puts the byte cut mid-character
		Body:        body("a"),
	})

	if !utf8.ValidString(listing.Text) {
		t.Error("the listing is not valid UTF-8, so a character was cut in half")
	}
}

// A description that fits is not touched. Appending an ellipsis to every skill
// would tell the model something was withheld when nothing was.
func TestAShortDescriptionIsLeftAlone(t *testing.T) {
	t.Parallel()

	_, listing := mustSkillTool(t, Skill{Name: "brief", Description: "Short and complete", Body: body("a")})
	if strings.Contains(listing.Text, "…") {
		t.Error("a description that fits was marked as truncated")
	}
}

// The listing is per team while the system prompt above it is not. Without its
// own breakpoint the whole prefix caches as one block and every team's listing
// invalidates the shared part.
func TestTheListingIsACacheBreakpoint(t *testing.T) {
	t.Parallel()

	_, listing := mustSkillTool(t, Skill{Name: "pdf-forms", Description: "Fill PDFs", Body: body("a")})
	if listing.Type != ContentText {
		t.Errorf("Type = %q, want text", listing.Type)
	}
	if !listing.Cacheable {
		t.Error("the listing is not a cache breakpoint, so every turn pays for it again")
	}
}

// Per-skill tools got name validation free from the tool list. One dispatcher
// tool has to put the valid names back, or the model invents them.
func TestTheSchemaEnumeratesExactlyTheSkillNames(t *testing.T) {
	t.Parallel()

	tool, _ := mustSkillTool(t,
		Skill{Name: "pdf-forms", Description: "Fill PDFs", Body: body("a")},
		Skill{Name: "commit-style", Description: "Write commits", Body: body("b")},
	)

	var schema struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("the schema is not valid JSON: %v", err)
	}

	got := strings.Join(schema.Properties.Name.Enum, ",")
	if got != "pdf-forms,commit-style" {
		t.Errorf("enum = %q, want the two skill names in order", got)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "name" {
		t.Errorf("required = %v, want [name]", schema.Required)
	}
	if tool.Name != SkillToolName {
		t.Errorf("the tool is named %q, want %q", tool.Name, SkillToolName)
	}
}

// The whole reason for one tool instead of one per skill: the tool list is the
// front of the cached prefix, so it must be byte-identical for every run in the
// deployment whatever skills that run was offered.
func TestTheSchemaIsTheSameBytesForTheSameSkills(t *testing.T) {
	t.Parallel()

	build := func() []byte {
		tool, _ := mustSkillTool(t,
			Skill{Name: "pdf-forms", Description: "Fill PDFs", Body: body("a")},
			Skill{Name: "commit-style", Description: "Write commits", Body: body("b")},
		)
		return tool.Schema
	}
	first, second := string(build()), string(build())
	if first != second {
		t.Errorf("two builds produced different schema bytes, so the prefix will not cache:\n%s\n%s", first, second)
	}
}

// A name with a quote in it would otherwise produce a schema the gateway
// rejects with an error naming neither the skill nor this package.
func TestASkillNameWithAQuoteStillProducesValidJSON(t *testing.T) {
	t.Parallel()

	tool, _ := mustSkillTool(t, Skill{Name: `pdf"forms`, Description: "Fill PDFs", Body: body("a")})
	if !json.Valid(tool.Schema) {
		t.Fatalf("the schema is not valid JSON: %s", tool.Schema)
	}
}

func TestLoadingASkillReturnsItsBody(t *testing.T) {
	t.Parallel()

	const instructions = "# PDF forms\n\nRun `pdftk form.pdf dump_data_fields`."
	tool, _ := mustSkillTool(t, Skill{Name: "pdf-forms", Description: "Fill PDFs", Body: body(instructions)})

	got, err := tool.Invoke(t.Context(), json.RawMessage(`{"name":"pdf-forms"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if got != instructions {
		t.Errorf("Invoke() = %q, want the skill body", got)
	}
}

// The description is tier one and the body is tier two. A body read while the
// Spec is being built has no tiers at all: every offered skill is paid for
// whether the model opens it or not.
func TestABodyIsNotReadUntilTheSkillIsLoaded(t *testing.T) {
	t.Parallel()

	reads := 0
	tool, _ := mustSkillTool(t, Skill{
		Name:        "pdf-forms",
		Description: "Fill PDFs",
		Body: func(context.Context) (string, error) {
			reads++
			return "instructions", nil
		},
	})

	if reads != 0 {
		t.Fatalf("the body was read %d times before anything loaded the skill", reads)
	}
	if _, err := tool.Invoke(t.Context(), json.RawMessage(`{"name":"pdf-forms"}`)); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if reads != 1 {
		t.Errorf("the body was read %d times, want 1", reads)
	}
}

// A loaded body is still in the messages the pattern is accumulating. Sending it
// again pays for it twice and tells the model nothing it does not have.
func TestLoadingASkillTwiceDoesNotSendTheBodyAgain(t *testing.T) {
	t.Parallel()

	reads := 0
	tool, _ := mustSkillTool(t, Skill{
		Name:        "pdf-forms",
		Description: "Fill PDFs",
		Body: func(context.Context) (string, error) {
			reads++
			return "the whole body", nil
		},
	})

	args := json.RawMessage(`{"name":"pdf-forms"}`)
	if _, err := tool.Invoke(t.Context(), args); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	second, err := tool.Invoke(t.Context(), args)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if reads != 1 {
		t.Errorf("the body was read %d times, want 1", reads)
	}
	if strings.Contains(second, "the whole body") {
		t.Error("the body was sent a second time")
	}
	if !strings.Contains(second, "already loaded") {
		t.Errorf("the second load said %q, want it to say the skill is already there", second)
	}
}

// Marking a skill loaded before its body was actually read would turn one
// failed read into a skill the model can never load for the rest of the run.
func TestASkillWhoseBodyFailedCanBeLoadedAgain(t *testing.T) {
	t.Parallel()

	unreachable := errors.New("the object store is unreachable")
	reads := 0
	tool, _ := mustSkillTool(t, Skill{
		Name:        "pdf-forms",
		Description: "Fill PDFs",
		Body: func(context.Context) (string, error) {
			reads++
			if reads == 1 {
				return "", unreachable
			}
			return "instructions", nil
		},
	})

	args := json.RawMessage(`{"name":"pdf-forms"}`)
	if _, err := tool.Invoke(t.Context(), args); !errors.Is(err, unreachable) {
		t.Fatalf("err = %v, want the body's own error", err)
	}

	got, err := tool.Invoke(t.Context(), args)
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if got != "instructions" {
		t.Errorf("the retry returned %q, want the body", got)
	}
}

// The enum is advice to a gateway, not a guarantee. Naming the real skills
// turns a dead step into one the model can recover from.
func TestAnUnknownSkillIsRefusedAndTheRealOnesAreNamed(t *testing.T) {
	t.Parallel()

	tool, _ := mustSkillTool(t,
		Skill{Name: "pdf-forms", Description: "Fill PDFs", Body: body("a")},
		Skill{Name: "commit-style", Description: "Write commits", Body: body("b")},
	)

	_, err := tool.Invoke(t.Context(), json.RawMessage(`{"name":"deploy"}`))
	if err == nil {
		t.Fatal("an unknown skill was accepted")
	}
	for _, want := range []string{"deploy", "pdf-forms", "commit-style"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

func TestArgumentsThatAreNotJSONAreRefused(t *testing.T) {
	t.Parallel()

	tool, _ := mustSkillTool(t, Skill{Name: "pdf-forms", Description: "Fill PDFs", Body: body("a")})
	if _, err := tool.Invoke(t.Context(), json.RawMessage(`{"name":`)); err == nil {
		t.Error("truncated arguments were accepted")
	}
}

// A nil body is a wiring mistake in the brain. One misconfigured skill in a
// list of forty must not panic every run that was offered it.
func TestASkillWithNoBodyIsRefusedRatherThanPanicking(t *testing.T) {
	t.Parallel()

	tool, _ := mustSkillTool(t, Skill{Name: "pdf-forms", Description: "Fill PDFs"})

	got, err := tool.Invoke(t.Context(), json.RawMessage(`{"name":"pdf-forms"}`))
	if err == nil {
		t.Fatal("a skill with no body returned no error")
	}
	if got != "" {
		t.Errorf("Invoke() = %q, want no output", got)
	}
	if !strings.Contains(err.Error(), "pdf-forms") {
		t.Errorf("err = %v, want it to name the skill", err)
	}
}

// Two skills under one name means the model asks for one and silently gets the
// other, and the run reads as the model ignoring instructions it never had.
func TestTwoSkillsUnderOneNameAreRefused(t *testing.T) {
	t.Parallel()

	_, _, err := skillTool([]Skill{
		{Name: "pdf-forms", Description: "Fill PDFs", Body: body("a")},
		{Name: "pdf-forms", Description: "Something else entirely", Body: body("b")},
	})
	if err == nil {
		t.Fatal("two skills under one name were accepted")
	}
	if !strings.Contains(err.Error(), "pdf-forms") {
		t.Errorf("err = %v, want it to name the clash", err)
	}
}

// An unnamed skill reaches the enum as "" and nothing can ever ask for it.
func TestASkillWithNoNameIsRefused(t *testing.T) {
	t.Parallel()

	if _, _, err := skillTool([]Skill{{Description: "Fill PDFs", Body: body("a")}}); err == nil {
		t.Error("a skill with no name was accepted")
	}
}

// A pattern is free to dispatch several tool calls from one turn concurrently.
// Under -race this catches the already-loaded map losing its lock.
func TestTheSkillToolIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	tool, _ := mustSkillTool(t,
		Skill{Name: "pdf-forms", Description: "Fill PDFs", Body: body("a")},
		Skill{Name: "commit-style", Description: "Write commits", Body: body("b")},
	)

	var wg sync.WaitGroup
	for _, name := range []string{"pdf-forms", "commit-style", "pdf-forms", "commit-style"} {
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := tool.Invoke(t.Context(), json.RawMessage(`{"name":"`+name+`"}`)); err != nil {
					t.Errorf("Invoke(%s): %v", name, err)
				}
			}()
		}
	}
	wg.Wait()
}

// A description under the rune cap but over it in bytes is left whole. The
// fast path compares bytes, so without the second check every multi-byte
// description would be cut at a third of the budget every other one gets.
func TestAMultibyteDescriptionUnderTheCapIsLeftWhole(t *testing.T) {
	t.Parallel()

	// 800 runes, 2400 bytes: past the limit in bytes, well under it in runes.
	whole := strings.Repeat("日", 800)
	_, listing := mustSkillTool(t, Skill{Name: "multibyte", Description: whole, Body: body("a")})

	if !strings.Contains(listing.Text, whole) {
		t.Error("a description within the rune cap was cut because its bytes were counted")
	}
}
