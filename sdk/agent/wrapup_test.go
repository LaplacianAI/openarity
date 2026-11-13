package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const wrapText = "Your token budget is nearly spent. Stop calling tools and answer now with what you have."

func TestAWrapUpReachesTheModelAsTheCallersWords(t *testing.T) {
	box := &steerBox{}
	var announced []Event
	client, inner := steering(box, func(e Event) { announced = append(announced, e) })

	if err := box.wrap(wrapText); err != nil {
		t.Fatalf("wrap() = %v", err)
	}
	msgs := []Message{say(RoleUser, "review it")}
	if _, err := client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatal(err)
	}

	sent := inner.requests()[0].Messages
	if len(sent) != 2 || text(sent[1]) != wrapText {
		t.Fatalf("sent %d messages ending %q; the wrap-up must arrive word for word", len(sent), text(sent[len(sent)-1]))
	}
	if strings.Contains(text(sent[1]), "The user sent this") {
		t.Fatalf("the wrap-up is phrased as the user's")
	}
	if len(announced) != 0 {
		t.Fatalf("the wrap-up was announced as a steer: %+v", announced)
	}

	// It stays where it arrived on every later request, as a steer does.
	msgs = append(msgs, say(RoleAssistant, "here is what I found"))
	if _, err := client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	if got := inner.requests()[1].Messages; len(got) != 3 || text(got[1]) != wrapText {
		t.Fatalf("the wrap-up moved or vanished on the next request")
	}
}

func TestEveryToolRefusesOnceAWrapUpIsAsked(t *testing.T) {
	box := &steerBox{}
	ran := 0
	tools := refusingAfterWrapUp([]Tool{{Name: "get_diff", Invoke: func(context.Context, json.RawMessage) (string, error) {
		ran++
		return "the diff", nil
	}}}, box)

	if out, err := tools[0].Invoke(t.Context(), nil); err != nil || out != "the diff" {
		t.Fatalf("before a wrap-up the tool returned %q, %v", out, err)
	}
	_ = box.wrap(wrapText)
	_, err := tools[0].Invoke(t.Context(), nil)
	if err == nil || err.Error() != wrapText {
		t.Fatalf("after a wrap-up the tool returned %v, want the wrap-up's words", err)
	}
	if ran != 1 {
		t.Fatalf("the tool ran %d times; after a wrap-up it must not run", ran)
	}
}

func TestSkillToolsRefuseAfterAWrapUpToo(t *testing.T) {
	pattern := &callingPattern{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return &scripted{}, nil }, pattern)
	run := runner.Start(t.Context(), Spec{
		Pattern: "calling", MaxSteps: 1,
		Skills: []Skill{{
			Name: "rubric", Description: "how to rate",
			Body: func(context.Context) (string, error) { return "P0 when data is lost", nil },
		}},
	}, nil, Endpoint{}, nil)
	if _, err := run.Wait(); err != nil {
		t.Fatal(err)
	}
	// The tools outlive the run; reopen its box to ask for a wrap-up on them.
	run.box.finished = false
	if err := run.box.wrap(wrapText); err != nil {
		t.Fatal(err)
	}

	for _, tool := range pattern.tools {
		if tool.Name != SkillToolName {
			continue
		}
		if _, err := tool.Invoke(t.Context(), json.RawMessage(`{"name":"rubric"}`)); err == nil {
			t.Fatalf("the skill tool loaded a skill after a wrap-up")
		}
		return
	}
	t.Fatalf("no %s tool reached the pattern", SkillToolName)
}

func TestAWrapUpAskedTwiceKeepsTheFirst(t *testing.T) {
	box := &steerBox{}
	_ = box.wrap("first")
	_ = box.wrap("second")

	if got, _ := box.refusal(); got != "first" {
		t.Fatalf("refusal = %q, want the first", got)
	}
	_, carried := box.take(1)
	if len(carried) != 1 || carried[0].text != "first" {
		t.Fatalf("carried %+v, want only the first", carried)
	}
}

func TestWrappingUpAFinishedRunSaysSo(t *testing.T) {
	runner, _ := New(func(Endpoint) (ModelClient, error) { return &scripted{}, nil }, &callingPattern{})
	run := runner.Start(t.Context(), Spec{Pattern: "calling", MaxSteps: 1}, nil, Endpoint{}, nil)
	if _, err := run.Wait(); err != nil {
		t.Fatal(err)
	}

	if err := run.WrapUp(wrapText); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("WrapUp() = %v, want ErrRunFinished", err)
	}
}

// A tool that hands back files is stopped as surely as one that returns text.
func TestAToolWithFilesRefusesOnceAWrapUpIsAsked(t *testing.T) {
	box := &steerBox{}
	ran := 0
	tools := refusingAfterWrapUp([]Tool{{Name: "read_attachment", Result: func(context.Context, json.RawMessage) (ToolResult, error) {
		ran++
		return ToolResult{Output: "screen.png"}, nil
	}}}, box)

	if r, _, err := tools[0].Call(t.Context(), nil); err != nil || r.Output != "screen.png" {
		t.Fatalf("before a wrap-up the tool returned %+v, %v", r, err)
	}
	_ = box.wrap(wrapText)
	if _, _, err := tools[0].Call(t.Context(), nil); err == nil || err.Error() != wrapText {
		t.Fatalf("after a wrap-up the tool returned %v, want the wrap-up's words", err)
	}
	if ran != 1 {
		t.Fatalf("the tool ran %d times; after a wrap-up it must not run", ran)
	}
}
