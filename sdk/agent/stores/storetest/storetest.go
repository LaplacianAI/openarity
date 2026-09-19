// Package storetest is the conformance suite every agent.Store runs. It
// asserts what a run assumes and cannot check for itself: that a steer is
// handed out once, that a transcript comes back in the order it went in, that
// two sessions are separate, and that neither side shares a slice with the
// other.
//
// A store author's whole test file is one call to Run.
package storetest

import (
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Run drives one store through the whole contract. open is called once per
// subtest and must return a store with nothing in it — a store that carries
// state between subtests will pass this suite and fail in production, because
// the subtests deliberately reuse the same session ids.
func Run(t *testing.T, open func(t *testing.T) agent.Store) {
	t.Helper()

	t.Run("a transcript comes back in the order it went in", func(t *testing.T) {
		store := open(t)
		saved := []agent.Message{
			{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "one"}}},
			{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "two"}}},
			{
				Role: agent.RoleTool, ToolCallID: "call_1",
				Content: []agent.Content{{Type: agent.ContentText, Text: "three"}},
			},
		}
		if err := store.Save(t.Context(), "s1", saved); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		back, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if len(back) != 3 {
			t.Fatalf("Messages() returned %d of 3 messages", len(back))
		}
		for i, want := range []string{"one", "two", "three"} {
			if back[i].Text() != want {
				t.Errorf("message %d is %q, want %q — a transcript out of order is a\n"+
					"conversation that did not happen", i, back[i].Text(), want)
			}
		}
		if back[2].Role != agent.RoleTool || back[2].ToolCallID != "call_1" {
			t.Errorf("a tool result came back as %+v; the fields a run dispatches on must\n"+
				"survive the round trip", back[2])
		}
	})

	t.Run("saving replaces rather than appends", func(t *testing.T) {
		store := open(t)
		one := []agent.Message{{Role: agent.RoleUser}}
		two := []agent.Message{{Role: agent.RoleUser}, {Role: agent.RoleAssistant}}

		if err := store.Save(t.Context(), "s1", one); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		if err := store.Save(t.Context(), "s1", two); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		back, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if len(back) != 2 {
			t.Errorf("the session holds %d messages after saving 1 then 2, want 2 — Save is\n"+
				"the transcript as it now stands, and a store that appends doubles every turn",
				len(back))
		}
	})

	t.Run("saving nothing empties the session", func(t *testing.T) {
		store := open(t)
		if err := store.Save(t.Context(), "s1", []agent.Message{{Role: agent.RoleUser}}); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		if err := store.Save(t.Context(), "s1", nil); err != nil {
			t.Fatalf("Save() with nothing = %v", err)
		}

		back, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if len(back) != 0 {
			t.Errorf("the session still holds %d messages after being saved empty; a store\n"+
				"that treats nil as \"leave it alone\" cannot represent a cleared session",
				len(back))
		}
	})

	t.Run("a session nobody has used is empty rather than an error", func(t *testing.T) {
		store := open(t)

		msgs, err := store.Messages(t.Context(), "never-used")
		if err != nil {
			t.Fatalf("Messages() on an unknown session = %v, want no error — a run starts\n"+
				"before its session has anything in it", err)
		}
		if len(msgs) != 0 {
			t.Errorf("Messages() = %+v, want nothing", msgs)
		}

		steers, err := store.TakeSteers(t.Context(), "never-used")
		if err != nil {
			t.Fatalf("TakeSteers() on an unknown session = %v, want no error — this runs\n"+
				"before every model call, including the first", err)
		}
		if len(steers) != 0 {
			t.Errorf("TakeSteers() = %v, want nothing", steers)
		}
	})

	t.Run("a steer is handed out exactly once", func(t *testing.T) {
		store := open(t)
		if err := store.AddSteer(t.Context(), "s1", "look in vault.go"); err != nil {
			t.Fatalf("AddSteer() = %v", err)
		}

		first, err := store.TakeSteers(t.Context(), "s1")
		if err != nil {
			t.Fatalf("TakeSteers() = %v", err)
		}
		if len(first) != 1 || first[0] != "look in vault.go" {
			t.Fatalf("TakeSteers() = %v, want the one steer that was left", first)
		}

		again, err := store.TakeSteers(t.Context(), "s1")
		if err != nil {
			t.Fatalf("TakeSteers() = %v", err)
		}
		if len(again) != 0 {
			t.Errorf("TakeSteers() returned %v a second time. A store that reads without\n"+
				"removing hands the same steer to every turn, and the model re-reads it as a\n"+
				"fresh instruction and never concludes", again)
		}
	})

	t.Run("steers keep the order they were left in", func(t *testing.T) {
		store := open(t)
		for _, text := range []string{"one", "two", "three"} {
			if err := store.AddSteer(t.Context(), "s1", text); err != nil {
				t.Fatalf("AddSteer(%q) = %v", text, err)
			}
		}

		got, err := store.TakeSteers(t.Context(), "s1")
		if err != nil {
			t.Fatalf("TakeSteers() = %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("TakeSteers() = %v, want three", got)
		}
		for i, want := range []string{"one", "two", "three"} {
			if got[i] != want {
				t.Errorf("steer %d is %q, want %q — two steers arriving out of order read as\n"+
					"a correction of the wrong thing", i, got[i], want)
			}
		}
	})

	t.Run("two sessions do not see each other", func(t *testing.T) {
		store := open(t)
		if err := store.Save(t.Context(), "s1", []agent.Message{{Role: agent.RoleUser}}); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		if err := store.AddSteer(t.Context(), "s1", "for s1 only"); err != nil {
			t.Fatalf("AddSteer() = %v", err)
		}

		msgs, err := store.Messages(t.Context(), "s2")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		steers, err := store.TakeSteers(t.Context(), "s2")
		if err != nil {
			t.Fatalf("TakeSteers() = %v", err)
		}
		if len(msgs) != 0 || len(steers) != 0 {
			t.Errorf("s2 sees %d messages and %v steers that belong to s1 — one conversation\n"+
				"leaking into another is the worst failure this store has", len(msgs), steers)
		}
	})

	t.Run("two replicas taking at once do not both get the steer", func(t *testing.T) {
		store := open(t)
		if err := store.AddSteer(t.Context(), "s1", "once"); err != nil {
			t.Fatalf("AddSteer() = %v", err)
		}

		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			total int
		)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()

				got, err := store.TakeSteers(t.Context(), "s1")
				if err != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				total += len(got)
			}()
		}
		wg.Wait()

		if total != 1 {
			t.Errorf("two replicas between them took %d copies of one steer, want 1. This is\n"+
				"the case the whole store exists for: more than one process polling the same\n"+
				"session", total)
		}
	})

	// Appending past a slice's length is invisible to whoever holds the shorter
	// slice, so these assert a write *within* the shared length. That is the
	// aliasing that actually corrupts a transcript, and the only one a test can
	// see.
	t.Run("a saved transcript is not the caller's slice", func(t *testing.T) {
		store := open(t)
		msgs := []agent.Message{{
			Role:    agent.RoleUser,
			Content: []agent.Content{{Type: agent.ContentText, Text: "first"}},
		}}

		if err := store.Save(t.Context(), "s1", msgs); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		msgs[0].Content[0].Text = "rewritten by the caller"
		msgs[0].Role = agent.RoleAssistant

		back, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if len(back) != 1 {
			t.Fatalf("Messages() = %+v, want one message", back)
		}
		if back[0].Role != agent.RoleUser {
			t.Errorf("the stored role changed to %q when the caller edited their own slice",
				back[0].Role)
		}
		if back[0].Text() != "first" {
			t.Errorf("the stored transcript says %q after the caller edited theirs; a run\n"+
				"holds its messages across turns and edits them in place",
				back[0].Text())
		}
	})

	// Content and ToolCalls are slices inside a Message, and Blob.Data and
	// Arguments are slices inside those. A store that copies one level down
	// protects a sentence and silently shares an attachment.
	t.Run("an attachment is not shared with the caller", func(t *testing.T) {
		store := open(t)
		data := []byte{0x25, 0x50, 0x44, 0x46}
		msgs := []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{{
				Type: agent.ContentFile,
				Blob: &agent.Blob{MediaType: "application/pdf", Name: "lease.pdf", Data: data},
			}},
		}}

		if err := store.Save(t.Context(), "s1", msgs); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		data[0] = 0x00
		msgs[0].Content[0].Blob.Name = "rewritten.pdf"

		back, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		blob := back[0].Content[0].Blob
		if blob == nil {
			t.Fatal("the attachment is gone")
		}
		if blob.Name != "lease.pdf" {
			t.Errorf("the stored attachment is named %q after the caller renamed theirs", blob.Name)
		}
		if string(blob.Data) != "%PDF" {
			t.Errorf("the stored bytes are %v after the caller edited theirs; a store that\n"+
				"copies one level down protects a sentence and shares an attachment", blob.Data)
		}
	})

	t.Run("a tool call's arguments are not shared with the caller", func(t *testing.T) {
		store := open(t)
		args := []byte(`{"query":"vault"}`)
		msgs := []agent.Message{{
			Role:      agent.RoleAssistant,
			ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "search", Arguments: args}},
		}}

		if err := store.Save(t.Context(), "s1", msgs); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		args[2] = 'X'
		msgs[0].ToolCalls[0].Name = "rm"

		back, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if len(back[0].ToolCalls) != 1 {
			t.Fatalf("the tool call is gone: %+v", back[0])
		}
		if got := back[0].ToolCalls[0].Name; got != "search" {
			t.Errorf("the stored call is now %q; a resumed run would dispatch it", got)
		}
		if got := string(back[0].ToolCalls[0].Arguments); got != `{"query":"vault"}` {
			t.Errorf("the stored arguments are %s after the caller edited theirs", got)
		}
	})

	t.Run("a read transcript is not the store's slice", func(t *testing.T) {
		store := open(t)
		saved := []agent.Message{{
			Role:    agent.RoleUser,
			Content: []agent.Content{{Type: agent.ContentText, Text: "first"}},
		}}
		if err := store.Save(t.Context(), "s1", saved); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		first, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if len(first) != 1 {
			t.Fatalf("Messages() = %+v, want one message", first)
		}
		first[0].Role = agent.RoleAssistant

		second, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if second[0].Role != agent.RoleUser {
			t.Errorf("the store now says %q because a caller edited what it handed back;\n"+
				"resuming a session is exactly read it, then work on it", second[0].Role)
		}
	})
}
