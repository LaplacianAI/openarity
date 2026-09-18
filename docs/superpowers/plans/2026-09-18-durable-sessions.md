# Durable sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A steer posted to one replica reaches a run executing on another, and a run whose process died resumes on a different one.

**Architecture:** Two interfaces in `sdk/agent` — `Store` (a backend, several implementations) and `Session` (one conversation, already addressed). The loop reaches both through the same `ModelClient` wrapper seam that steering, usage counting and output schemas already use, so no pattern changes and no pattern can tell. Ownership — team, permissions — stays in the brain and never crosses `agent.Open`.

**Tech Stack:** Go 1.26.6, `jackc/pgx/v5`, `modernc.org/sqlite`.

**Spec:** [`docs/superpowers/specs/2026-09-18-durable-sessions-design.md`](../specs/2026-09-18-durable-sessions-design.md)

## Global Constraints

- **Module boundary.** `make boundary` fails if `.` or `./patterns` links a third-party package listed in `FORBIDDEN`. Every new driver goes in a subpackage under `stores/` **and** gets a `FORBIDDEN` line. `MemoryStore` stays in the core because it links nothing.
- **Coverage.** `COVER_MIN := 95`, and every package is at 100 today and stays there. Do not write an unreachable branch — it cannot be covered, and adding a test that exercises it without asserting anything is worse than the line.
- **`Spec.Session == nil` is the default and must cost nothing.** No store call, no allocation, no behaviour change. There is one test whose whole job is to assert this against a store that fails every call.
- **`TakeSteers` is destructive by contract.** A steer is returned exactly once. Any store that returns one twice is broken.
- **Errors fail the run.** A failure from `TakeSteers` or `Save` returns from `Complete`/`Stream`. Never log and continue: a dropped steer looks delivered to whoever sent it.
- **No secret, credential or DSN with a password in any diff.** Read the diff before committing.
- **Commit messages** are Conventional Commits, no model attribution, no `Co-Authored-By` trailer.
- **Test names are sentences**, matching the package: `TestASteerLeftByAnotherReplicaReachesTheNextRequest`, not `TestTakeSteers`.
- Run `make check` in `sdk/agent` before every commit. It runs `tidy-check fmt-check vet lint boundary build cover vuln`.

## Scope

This plan covers **`sdk/agent` only** — Tasks 1 through 7. After Task 7, durable steering and resume work end to end against SQLite and Postgres, provable without the brain existing.

The brain wrapper is a separate spec → plan cycle. It is a different module with its own `make check db=`, its own migration discipline with an advisory lock, and an API surface that needs its own authorisation tests. Folding it in here would produce a plan where half the tasks cannot run without a database.

## File Structure

| File | Responsibility |
| --- | --- |
| `sdk/agent/session.go` | `Store`, `Session`, `Open`, `MemoryStore` — the whole concept, no drivers |
| `sdk/agent/session_test.go` | `Open`'s refusals, `MemoryStore`, the client wrappers |
| `sdk/agent/message.go` | gains JSON tags, because the stored transcript is now a format |
| `sdk/agent/steer.go` | `apply` collects from the session before the box |
| `sdk/agent/runner.go` | builds the chain, saves when the pattern returns |
| `sdk/agent/spec.go` | `Session` field |
| `sdk/agent/stores/storetest/storetest.go` | the contract suite every store is driven through |
| `sdk/agent/stores/sqlite/sqlite.go` | `modernc.org/sqlite`, creates its tables on open |
| `sdk/agent/stores/postgres/postgres.go` | `pgx/v5`, exports DDL and runs none |
| `sdk/agent/Makefile` | two new `FORBIDDEN` entries |

---

### Task 1: The interfaces, and a stable form for a stored message

**Files:**
- Create: `sdk/agent/session.go`
- Create: `sdk/agent/session_test.go`
- Modify: `sdk/agent/message.go`

**Interfaces:**
- Consumes: `Message`, `Content`, `Blob`, `ToolCall` from `message.go`
- Produces: `Store`, `Session`, `Open(id string, store Store) (Session, error)`

- [ ] **Step 1: Write the failing tests**

`sdk/agent/session_test.go`:

```go
package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// Tasks 3 and 4 append to this file and will add "context" and "errors" to
// the block above. Do not add them now — an unused import does not compile.

func TestASessionWithNoIDIsRefused(t *testing.T) {
	_, err := Open("", NewMemoryStore())
	if err == nil {
		t.Fatal("an empty id was accepted; every conversation would land in one bucket")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestASessionWithNoStoreIsRefused(t *testing.T) {
	if _, err := Open("s1", nil); err == nil {
		t.Fatal("a nil Store was accepted, so the first Save would panic mid-run")
	}
}

func TestASessionAddressesTheStoreWithItsOwnID(t *testing.T) {
	store := NewMemoryStore()
	session, err := Open("s1", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	if err := session.Save(t.Context(), []Message{{Role: RoleUser}}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	kept, err := store.Messages(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(kept) != 1 {
		t.Errorf("the store holds %d messages under s1, want 1 — the session did not pass its id",
			len(kept))
	}
	other, _ := store.Messages(t.Context(), "s2")
	if len(other) != 0 {
		t.Errorf("s2 gained %d messages from a save on s1", len(other))
	}
}

// The transcript is written to somebody's database the moment a Store exists,
// so the JSON shape is a compatibility surface rather than an implementation
// detail. Lower-case and omitempty are cheaper per row and stable across a
// field being added.
func TestAStoredMessageHasAStableShape(t *testing.T) {
	raw, err := json.Marshal(Message{
		Role:    RoleAssistant,
		Content: []Content{{Type: ContentText, Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	const want = `{"role":"assistant","content":[{"type":"text","text":"hello"}]}`
	if string(raw) != want {
		t.Errorf("a stored message is\n  %s\nwant\n  %s", raw, want)
	}
}

func TestAMessageSurvivesTheRoundTrip(t *testing.T) {
	original := Message{
		Role:       RoleTool,
		ToolCallID: "call_1",
		Content:    []Content{{Type: ContentText, Text: "done"}},
		ToolCalls:  []ToolCall{{ID: "call_1", Name: "grep", Arguments: json.RawMessage(`{"q":"x"}`)}},
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var back Message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}

	if back.ToolCallID != original.ToolCallID || back.Text() != "done" {
		t.Errorf("round trip lost something: %+v", back)
	}
	if len(back.ToolCalls) != 1 || back.ToolCalls[0].Name != "grep" {
		t.Errorf("the tool call did not survive: %+v", back.ToolCalls)
	}
}
```

- [ ] **Step 2: Run the tests and verify they fail**

```sh
cd sdk/agent && go test -run 'Session|StoredMessage|RoundTrip' .
```

Expected: FAIL — `undefined: Open`, `undefined: NewMemoryStore`.

- [ ] **Step 3: Add the JSON tags**

In `sdk/agent/message.go`, replace the four struct definitions:

```go
type Message struct {
	Role       Role       `json:"role"`
	Content    []Content  `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type Content struct {
	Type      ContentType `json:"type"`
	Text      string      `json:"text,omitempty"`
	Blob      *Blob       `json:"blob,omitempty"`
	Cacheable bool        `json:"cacheable,omitempty"`
}

type Blob struct {
	MediaType string `json:"media_type"`
	Name      string `json:"name,omitempty"`
	Data      []byte `json:"data,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}
```

- [ ] **Step 4: Write the interfaces**

`sdk/agent/session.go`:

```go
package agent

import (
	"context"
	"errors"
	"slices"
	"sync"
)

// Store is a backend. It knows nothing about agents: it holds a transcript and
// a queue of undelivered steers, addressed by a session id it never
// interprets. Each method is one statement against a database.
type Store interface {
	// Save records the transcript as it now stands, replacing what was there.
	// It is not an append because a steer is inserted at a pinned position
	// rather than added to the end, so a delta would have to describe an
	// insertion and the method would stop being one statement.
	Save(ctx context.Context, session string, msgs []Message) error

	Messages(ctx context.Context, session string) ([]Message, error)

	AddSteer(ctx context.Context, session, text string) error

	// TakeSteers is destructive: a steer is returned exactly once, so a store
	// that reads without removing will deliver it on every turn for the rest
	// of the run.
	TakeSteers(ctx context.Context, session string) ([]string, error)
}

// Session is one conversation, already addressed, so nothing in the loop
// threads an id around. A caller whose storage does not fit Store may
// implement this directly.
type Session interface {
	Save(ctx context.Context, msgs []Message) error
	Messages(ctx context.Context) ([]Message, error)
	Steer(ctx context.Context, text string) error
	TakeSteers(ctx context.Context) ([]string, error)
}

func Open(id string, store Store) (Session, error) {
	if id == "" {
		return nil, errors.New(
			"a session needs an id; an empty one would collect every conversation in a single bucket")
	}
	if store == nil {
		return nil, errors.New(
			"a session needs a Store; NewMemoryStore() is one that lives as long as the process")
	}
	return &session{id: id, store: store}, nil
}

type session struct {
	id    string
	store Store
}

func (s *session) Save(ctx context.Context, msgs []Message) error {
	return s.store.Save(ctx, s.id, msgs)
}

func (s *session) Messages(ctx context.Context) ([]Message, error) {
	return s.store.Messages(ctx, s.id)
}

func (s *session) Steer(ctx context.Context, text string) error {
	return s.store.AddSteer(ctx, s.id, text)
}

func (s *session) TakeSteers(ctx context.Context) ([]string, error) {
	return s.store.TakeSteers(ctx, s.id)
}

var _ Session = (*session)(nil)
```

- [ ] **Step 5: Add `MemoryStore`, in the same file**

```go
// NewMemoryStore returns a Store that lives as long as the process. It is what
// tests and examples use, and it links nothing — which is why it may live in
// the core while every other store is a subpackage.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		msgs:   make(map[string][]Message),
		steers: make(map[string][]string),
	}
}

type MemoryStore struct {
	mu     sync.Mutex
	msgs   map[string][]Message
	steers map[string][]string
}

func (m *MemoryStore) Save(_ context.Context, session string, msgs []Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.msgs[session] = slices.Clone(msgs)
	return nil
}

func (m *MemoryStore) Messages(_ context.Context, session string) ([]Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.msgs[session]), nil
}

func (m *MemoryStore) AddSteer(_ context.Context, session, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.steers[session] = append(m.steers[session], text)
	return nil
}

func (m *MemoryStore) TakeSteers(_ context.Context, session string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := m.steers[session]
	delete(m.steers, session)
	return out, nil
}

var _ Store = (*MemoryStore)(nil)
```

`slices.Clone` on both sides is not decoration: without it a caller's slice and
the store's share a backing array, and the next `append` writes into stored
state. This is the same bug `TestTheCallersHistoryIsNotMutated` exists for in
`patterns`.

- [ ] **Step 6: Run the tests and verify they pass**

```sh
cd sdk/agent && go test -race -run 'Session|StoredMessage|RoundTrip' .
```

Expected: PASS.

- [ ] **Step 7: Prove each guard can fail**

Break each, confirm the named test fails, restore:

| Break | Test that must fail |
| --- | --- |
| delete the `id == ""` check in `Open` | `TestASessionWithNoIDIsRefused` |
| delete the `store == nil` check in `Open` | `TestASessionWithNoStoreIsRefused` |
| make `session.Save` pass `""` instead of `s.id` | `TestASessionAddressesTheStoreWithItsOwnID` |
| remove the `json:"role"` tag | `TestAStoredMessageHasAStableShape` |
| drop `slices.Clone` from `MemoryStore.Save` | see Task 2 — add the aliasing test there |

- [ ] **Step 8: Commit**

```sh
cd sdk/agent && make check
git add sdk/agent/session.go sdk/agent/session_test.go sdk/agent/message.go
git commit -m "feat(sdk): a session, and a store to keep one in"
```

---

### Task 2: One contract every store is held to

**Files:**
- Create: `sdk/agent/stores/storetest/storetest.go`
- Create: `sdk/agent/stores/storetest/storetest_test.go`

**Interfaces:**
- Consumes: `agent.Store`, `agent.NewMemoryStore` from Task 1
- Produces: `storetest.Run(t *testing.T, open func(t *testing.T) agent.Store)`

The suite exists so that SQLite and Postgres cannot quietly disagree with
`MemoryStore` about what `TakeSteers` means. `apps/brain/internal/gateway/providertest`
is the same idea for channel adapters.

- [ ] **Step 1: Write the suite**

`sdk/agent/stores/storetest/storetest.go`:

```go
// Package storetest holds the contract every agent.Store is driven through.
// A store that passes it behaves the same as every other, which is the only
// thing that makes the backend a deployment choice rather than a behaviour
// change.
package storetest

import (
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Run drives one store through the whole contract. open is called once per
// subtest and must return a store with nothing in it.
func Run(t *testing.T, open func(t *testing.T) agent.Store) {
	t.Helper()

	t.Run("a transcript comes back in the order it was saved", func(t *testing.T) {
		store := open(t)
		saved := []agent.Message{
			{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "one"}}},
			{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "two"}}},
		}
		if err := store.Save(t.Context(), "s1", saved); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		back, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if len(back) != 2 || back[0].Text() != "one" || back[1].Text() != "two" {
			t.Fatalf("Messages() = %+v, want one then two", back)
		}
		if back[1].Role != agent.RoleAssistant {
			t.Errorf("the role did not survive: %q", back[1].Role)
		}
	})

	t.Run("saving replaces rather than appends", func(t *testing.T) {
		store := open(t)
		first := []agent.Message{{Role: agent.RoleUser}}
		second := []agent.Message{{Role: agent.RoleUser}, {Role: agent.RoleAssistant}}

		if err := store.Save(t.Context(), "s1", first); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		if err := store.Save(t.Context(), "s1", second); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		back, err := store.Messages(t.Context(), "s1")
		if err != nil {
			t.Fatalf("Messages() = %v", err)
		}
		if len(back) != 2 {
			t.Errorf("the session holds %d messages after saving 1 then 2, want 2 — Save is a\n"+
				"snapshot, and a store that appends will double every turn", len(back))
		}
	})

	t.Run("an unknown session is empty rather than an error", func(t *testing.T) {
		store := open(t)

		msgs, err := store.Messages(t.Context(), "never-used")
		if err != nil {
			t.Fatalf("Messages() on an unknown session = %v, want no error", err)
		}
		if len(msgs) != 0 {
			t.Errorf("Messages() = %+v, want nothing", msgs)
		}

		steers, err := store.TakeSteers(t.Context(), "never-used")
		if err != nil {
			t.Fatalf("TakeSteers() on an unknown session = %v, want no error", err)
		}
		if len(steers) != 0 {
			t.Errorf("TakeSteers() = %v, want nothing", steers)
		}
	})

	t.Run("a steer is delivered exactly once", func(t *testing.T) {
		store := open(t)
		if err := store.AddSteer(t.Context(), "s1", "look in vault.go"); err != nil {
			t.Fatalf("AddSteer() = %v", err)
		}

		first, err := store.TakeSteers(t.Context(), "s1")
		if err != nil {
			t.Fatalf("TakeSteers() = %v", err)
		}
		if len(first) != 1 || first[0] != "look in vault.go" {
			t.Fatalf("TakeSteers() = %v, want the one steer", first)
		}

		again, err := store.TakeSteers(t.Context(), "s1")
		if err != nil {
			t.Fatalf("TakeSteers() = %v", err)
		}
		if len(again) != 0 {
			t.Errorf("TakeSteers() returned %v a second time; a store that reads without\n"+
				"removing delivers the same steer on every turn for the rest of the run", again)
		}
	})

	t.Run("steers keep the order they were added in", func(t *testing.T) {
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
		if len(got) != 3 || got[0] != "one" || got[2] != "three" {
			t.Errorf("TakeSteers() = %v, want one two three", got)
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

		msgs, _ := store.Messages(t.Context(), "s2")
		steers, _ := store.TakeSteers(t.Context(), "s2")
		if len(msgs) != 0 || len(steers) != 0 {
			t.Errorf("s2 sees %d messages and %v steers belonging to s1", len(msgs), steers)
		}
	})

	t.Run("two callers taking at once do not both get the steer", func(t *testing.T) {
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
				total += len(got)
				mu.Unlock()
			}()
		}
		wg.Wait()

		if total != 1 {
			t.Errorf("two replicas took %d copies of one steer, want 1 — the model would be\n"+
				"told the same thing twice", total)
		}
	})
}
```

- [ ] **Step 2: Drive `MemoryStore` through it**

`sdk/agent/stores/storetest/storetest_test.go`:

```go
package storetest_test

import (
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

// MemoryStore is the reference: it is the store every other one has to agree
// with, so it is driven through the same suite before any driver exists.
func TestMemoryStoreKeepsTheContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) agent.Store { return agent.NewMemoryStore() })
}
```

An external test package (`storetest_test`) rather than `storetest`, because
`storetest` imports `agent` and a same-package test importing `storetest` from
`agent`'s own tests would be a cycle.

- [ ] **Step 3: Add the aliasing test Task 1 deferred**

Append to `sdk/agent/session_test.go`:

```go
// append on a caller's slice with spare capacity writes into their backing
// array. Without the clone, saving a transcript and then appending one more
// message rewrites what was stored.
func TestSavingDoesNotAliasTheCallersSlice(t *testing.T) {
	store := NewMemoryStore()
	msgs := make([]Message, 1, 4)
	msgs[0] = Message{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "first"}}}

	if err := store.Save(t.Context(), "s1", msgs); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	_ = append(msgs, Message{Role: RoleAssistant}) //nolint:staticcheck // writing into the shared array is the point

	back, _ := store.Messages(t.Context(), "s1")
	if len(back) != 1 || back[0].Text() != "first" {
		t.Errorf("the stored transcript changed under the store: %+v", back)
	}
}
```

- [ ] **Step 4: Run and verify**

```sh
cd sdk/agent && go test -race ./... && make check
```

Expected: PASS, coverage still 100%.

- [ ] **Step 5: Prove the suite bites**

Break `MemoryStore.TakeSteers` so it reads without deleting; confirm
`a steer is delivered exactly once` fails. Drop `slices.Clone` from `Save`;
confirm `TestSavingDoesNotAliasTheCallersSlice` fails. Restore both.

- [ ] **Step 6: Commit**

```sh
git add sdk/agent/stores/storetest sdk/agent/session_test.go
git commit -m "test(sdk): one contract every store has to keep"
```

---

### Task 3: A steer left by another replica reaches the run

**Files:**
- Modify: `sdk/agent/spec.go`
- Modify: `sdk/agent/steer.go`
- Modify: `sdk/agent/runner.go`
- Modify: `sdk/agent/session_test.go`

**Interfaces:**
- Consumes: `Session` from Task 1
- Produces: `Spec.Session Session`; `steeringClient.session`; `steerBox.fill([]string)`

- [ ] **Step 1: Write the failing tests**

Append to `sdk/agent/session_test.go`:

```go
// failingStore fails whichever method is named, so a test can assert that a
// run stops rather than carrying on without the durability it asked for.
type failingStore struct {
	inner  Store
	onTake error
	onSave error
}

func (f *failingStore) Save(ctx context.Context, session string, msgs []Message) error {
	if f.onSave != nil {
		return f.onSave
	}
	return f.inner.Save(ctx, session, msgs)
}

func (f *failingStore) Messages(ctx context.Context, session string) ([]Message, error) {
	return f.inner.Messages(ctx, session)
}

func (f *failingStore) AddSteer(ctx context.Context, session, text string) error {
	return f.inner.AddSteer(ctx, session, text)
}

func (f *failingStore) TakeSteers(ctx context.Context, session string) ([]string, error) {
	if f.onTake != nil {
		return nil, f.onTake
	}
	return f.inner.TakeSteers(ctx, session)
}

func TestASteerLeftByAnotherReplicaReachesTheNextRequest(t *testing.T) {
	store := NewMemoryStore()
	session, err := Open("s1", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	// Nobody holds the *Run: this is what a second replica can do.
	if err := session.Steer(t.Context(), "the bug is in vault.go"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	requests := inner.requests()
	if len(requests) != 1 {
		t.Fatalf("the pattern made %d requests, want 1", len(requests))
	}
	sent := requests[0].Messages
	if got := text(sent[len(sent)-1]); !strings.Contains(got, "the bug is in vault.go") {
		t.Errorf("a steer left in the store never reached the model:\n%s", got)
	}
}

func TestAStoredSteerIsNotDeliveredTwice(t *testing.T) {
	store := NewMemoryStore()
	session, _ := Open("s1", store)
	if err := session.Steer(t.Context(), "only once"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	inner := &recordingClient{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &echoPattern{calls: 3})

	spec := Spec{Pattern: "echo", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	var carrying int
	for _, req := range inner.requests() {
		for _, m := range req.Messages {
			if strings.Contains(text(m), "only once") {
				carrying++
				break
			}
		}
	}
	if carrying != 1 {
		t.Errorf("%d of %d requests carried the steer, want exactly 1 — a steer the store\n"+
			"hands out again is re-read as a fresh instruction every turn",
			carrying, len(inner.requests()))
	}
}

func TestAStoreThatCannotBeReadStopsTheRun(t *testing.T) {
	store := &failingStore{inner: NewMemoryStore(), onTake: errors.New("postgres is down")}
	session, _ := Open("s1", store)

	inner := &recordingClient{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	_, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err == nil {
		t.Fatal("the run succeeded with an unreadable steer store; whoever sent a steer was\n" +
			"told it was delivered")
	}
	if !strings.Contains(err.Error(), "postgres is down") {
		t.Errorf("the cause was swallowed: %v", err)
	}
}

func TestWithoutASessionNoStoreIsEverTouched(t *testing.T) {
	inner := &recordingClient{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})

	// Spec.Session is nil, so nothing below may call a store at all. A store
	// that fails every call proves it rather than asserting a count.
	spec := Spec{Pattern: "oblivious", MaxSteps: 1}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v — a run with no session must behave exactly as it did before", err)
	}
}
```

- [ ] **Step 2: Run the tests and verify they fail**

```sh
cd sdk/agent && go test -run 'AnotherReplica|DeliveredTwice|CannotBeRead|NoStoreIsEverTouched' .
```

Expected: FAIL — `unknown field Session in struct literal`.

- [ ] **Step 3: Add the spec field**

In `sdk/agent/spec.go`, inside `Spec`, after `ParserModel`:

```go
	// Session makes a run durable: steers left by another process are
	// collected before each request, and the transcript is written as it
	// grows. Nil is the default and costs nothing — no call, no allocation,
	// no change in behaviour.
	Session Session
```

- [ ] **Step 4: Let the box be filled from outside**

In `sdk/agent/steer.go`, after `add`:

```go
// fill adds steers that arrived through a Session rather than through
// Run.Steer. It has no finished check because it is only ever called from
// inside a request the run is still making, and a branch that cannot be
// reached is a branch that cannot be covered.
func (b *steerBox) fill(texts []string) {
	if len(texts) == 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.pending = append(b.pending, texts...)
}
```

- [ ] **Step 5: Collect from the session before taking from the box**

Replace `steeringClient` and `apply` in `sdk/agent/steer.go`:

```go
type steeringClient struct {
	inner   ModelClient
	box     *steerBox
	session Session
	emit    func(Event)
}

func (c *steeringClient) Complete(ctx context.Context, req Request) (Response, error) {
	msgs, err := c.apply(ctx, req.Messages)
	if err != nil {
		return Response{}, err
	}
	req.Messages = msgs
	return c.inner.Complete(ctx, req)
}

func (c *steeringClient) Stream(ctx context.Context, req Request) (Stream, error) {
	msgs, err := c.apply(ctx, req.Messages)
	if err != nil {
		return nil, err
	}
	req.Messages = msgs
	return c.inner.Stream(ctx, req)
}

func (c *steeringClient) apply(ctx context.Context, msgs []Message) ([]Message, error) {
	if c.session != nil {
		elsewhere, err := c.session.TakeSteers(ctx)
		if err != nil {
			return nil, fmt.Errorf("collecting the steers left for this session: %w", err)
		}
		c.box.fill(elsewhere)
	}

	fresh, carried := c.box.take(len(msgs))

	for _, text := range fresh {
		if c.emit != nil {
			c.emit(SteerEvent{Text: text})
		}
	}
	return recordSteers(msgs, carried), nil
}
```

Add `"fmt"` to the imports in `steer.go`.

- [ ] **Step 6: Pass the session in**

In `sdk/agent/runner.go`, in `r.run`, replace the `steered` literal:

```go
	steered := &steeringClient{
		inner:   client,
		box:     box,
		session: spec.Session,
		emit:    func(e Event) { emit(ctx, events, e) },
	}
```

- [ ] **Step 7: Run and verify**

```sh
cd sdk/agent && go test -race ./... && make check
```

Expected: PASS. The steer collected from the store lands at the pinned
position, because `fill` puts it in the box and everything after that is the
code that already existed.

- [ ] **Step 8: Prove each guard can fail**

| Break | Test that must fail |
| --- | --- |
| drop the `c.session != nil` block from `apply` | `TestASteerLeftByAnotherReplicaReachesTheNextRequest` |
| make `MemoryStore.TakeSteers` read without deleting | `TestAStoredSteerIsNotDeliveredTwice` |
| return `msgs, nil` instead of the error in `apply` | `TestAStoreThatCannotBeReadStopsTheRun` |

- [ ] **Step 9: Commit**

```sh
git add sdk/agent/spec.go sdk/agent/steer.go sdk/agent/runner.go sdk/agent/session_test.go
git commit -m "feat(sdk): collect the steers another process left before each turn"
```

---

### Task 4: The transcript is written as it grows

**Files:**
- Modify: `sdk/agent/session.go`
- Modify: `sdk/agent/runner.go`
- Modify: `sdk/agent/session_test.go`

**Interfaces:**
- Consumes: `Session`, `Spec.Session`, `steeringClient` from Tasks 1 and 3
- Produces: `savingClient`

- [ ] **Step 1: Write the failing tests**

Append to `sdk/agent/session_test.go`:

```go
// The save has to happen inside the steering client, so the transcript that
// reaches the store already has the steer in it. Saved outside, a steer taken
// from the store and then lost to a crash would be gone from both places.
func TestASteerIsDurableTheMomentItIsTaken(t *testing.T) {
	store := NewMemoryStore()
	session, _ := Open("s1", store)
	if err := session.Steer(t.Context(), "check the lease"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	inner := &recordingClient{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	kept, _ := store.Messages(t.Context(), "s1")
	var found bool
	for _, m := range kept {
		if strings.Contains(m.Text(), "check the lease") {
			found = true
		}
	}
	if !found {
		t.Errorf("the steer is not in the saved transcript:\n%+v", kept)
	}
}

func TestTheAnswerReachesTheStoreAndNotOnlyTheRequests(t *testing.T) {
	store := NewMemoryStore()
	session, _ := Open("s1", store)

	inner := &recordingClient{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	result, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	kept, _ := store.Messages(t.Context(), "s1")
	if len(kept) != len(result.Messages) {
		t.Fatalf("the store holds %d messages and the result has %d — the model's last reply\n"+
			"only ever appears in the next request, so a save per request misses it",
			len(kept), len(result.Messages))
	}
	if kept[len(kept)-1].Text() != "finished" {
		t.Errorf("the saved transcript does not end with the answer: %q",
			kept[len(kept)-1].Text())
	}
}

func TestAStoreThatCannotBeWrittenStopsTheRun(t *testing.T) {
	store := &failingStore{inner: NewMemoryStore(), onSave: errors.New("disk is full")}
	session, _ := Open("s1", store)

	inner := &recordingClient{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})

	spec := Spec{Pattern: "oblivious", MaxSteps: 1, Session: session}
	_, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil)
	if err == nil {
		t.Fatal("the run succeeded without saving anything, so it was not durable and nobody\n" +
			"was told")
	}
	if !strings.Contains(err.Error(), "disk is full") {
		t.Errorf("the cause was swallowed: %v", err)
	}
}

// What a crash costs is a decision, not an accident: saving once per request
// means the step in flight is redone, tool calls included.
func TestTheTranscriptIsSavedOnceBeforeEveryRequest(t *testing.T) {
	var saves int
	counting := &countingSaves{Store: NewMemoryStore(), saves: &saves}
	counted, _ := Open("s1", counting)

	inner := &recordingClient{}
	runner, _ := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &echoPattern{calls: 3})

	spec := Spec{Pattern: "echo", MaxSteps: 1, Session: counted}
	if _, err := runner.Run(t.Context(), spec, nil, Endpoint{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	// one per request, plus one when the pattern returns
	if want := len(inner.requests()) + 1; saves != want {
		t.Errorf("Save was called %d times across %d requests, want %d",
			saves, len(inner.requests()), want)
	}
}

type countingSaves struct {
	Store
	saves *int
}

func (c *countingSaves) Save(ctx context.Context, session string, msgs []Message) error {
	*c.saves++
	return c.Store.Save(ctx, session, msgs)
}
```

- [ ] **Step 2: Run the tests and verify they fail**

```sh
cd sdk/agent && go test -run 'DurableTheMoment|AnswerReachesTheStore|CannotBeWritten|SavedOnceBefore' .
```

Expected: FAIL — nothing is saved, so every assertion about the store is empty.

- [ ] **Step 3: Write the saving client**

Append to `sdk/agent/session.go`:

```go
// savingClient writes the transcript before every request. It is built only
// when a run has a session, so there is no nil branch here for a run that does
// not want one.
//
// It sits inside the steering client rather than outside, so req.Messages
// already carries any steer that was just collected: a steer becomes durable
// in the same instant it is taken, rather than in a window where a crash would
// lose it from the store and from memory at once.
type savingClient struct {
	inner   ModelClient
	session Session
}

func (c *savingClient) Complete(ctx context.Context, req Request) (Response, error) {
	if err := c.session.Save(ctx, req.Messages); err != nil {
		return Response{}, fmt.Errorf("saving the transcript: %w", err)
	}
	return c.inner.Complete(ctx, req)
}

func (c *savingClient) Stream(ctx context.Context, req Request) (Stream, error) {
	if err := c.session.Save(ctx, req.Messages); err != nil {
		return nil, fmt.Errorf("saving the transcript: %w", err)
	}
	return c.inner.Stream(ctx, req)
}

var _ ModelClient = (*savingClient)(nil)
```

Add `"fmt"` to the imports in `session.go`.

- [ ] **Step 4: Put it in the chain, and save the answer**

In `sdk/agent/runner.go`, in `r.run`, replace the client construction so the
saving client is beneath the steering one:

```go
	var beneath ModelClient = client
	if spec.Session != nil {
		beneath = &savingClient{inner: client, session: spec.Session}
	}

	steered := &steeringClient{
		inner:   beneath,
		box:     box,
		session: spec.Session,
		emit:    func(e Event) { emit(ctx, events, e) },
	}
```

Then, in the continuation loop, immediately after
`result.Messages = recordSteers(this.Messages, box.applied())`:

```go
		// The model's last reply only ever appears in the next request, so a
		// save per request never writes the answer. This is that save.
		if spec.Session != nil {
			if err := spec.Session.Save(ctx, result.Messages); err != nil {
				return result, fmt.Errorf("saving the transcript: %w", err)
			}
		}
```

- [ ] **Step 5: Run and verify**

```sh
cd sdk/agent && go test -race ./... && make check
```

Expected: PASS, coverage 100%.

- [ ] **Step 6: Prove each guard can fail**

| Break | Test that must fail |
| --- | --- |
| build `savingClient` outside `steered` instead of inside | `TestASteerIsDurableTheMomentItIsTaken` |
| delete the save after `recordSteers` | `TestTheAnswerReachesTheStoreAndNotOnlyTheRequests` |
| return `nil` instead of the error in `savingClient.Complete` | `TestAStoreThatCannotBeWrittenStopsTheRun` |

- [ ] **Step 7: Commit**

```sh
git add sdk/agent/session.go sdk/agent/runner.go sdk/agent/session_test.go
git commit -m "feat(sdk): write the transcript as the run produces it"
```

---

### Task 5: A store on SQLite

**Files:**
- Create: `sdk/agent/stores/sqlite/sqlite.go`
- Create: `sdk/agent/stores/sqlite/sqlite_test.go`
- Modify: `sdk/agent/Makefile`
- Modify: `sdk/agent/go.mod`, `sdk/agent/go.sum`

**Interfaces:**
- Consumes: `agent.Store`, `storetest.Run` from Tasks 1 and 2
- Produces: `sqlite.Open(path string) (*Store, error)`, `(*Store).Close() error`

- [ ] **Step 1: Add the driver**

```sh
cd sdk/agent && go get modernc.org/sqlite && go mod tidy
```

`modernc.org/sqlite` rather than `mattn/go-sqlite3`: it is pure Go, so it does
not force `CGO_ENABLED=1` or break cross-compilation for anybody who imports
this module.

- [ ] **Step 2: Write the failing test**

`sdk/agent/stores/sqlite/sqlite_test.go`:

```go
package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/sqlite"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

func TestTheSQLiteStoreKeepsTheContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) agent.Store {
		store, err := sqlite.Open(filepath.Join(t.TempDir(), "sessions.db"))
		if err != nil {
			t.Fatalf("Open() = %v", err)
		}
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Errorf("Close() = %v", err)
			}
		})
		return store
	})
}

// A file store is only useful if what it wrote is there when the process that
// wrote it has gone.
func TestATranscriptOutlivesTheStoreThatWroteIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")

	first, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	saved := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "still here"}},
	}}
	if err := first.Save(t.Context(), "s1", saved); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("reopening = %v", err)
	}
	defer second.Close() //nolint:errcheck // the assertion below is the point

	back, err := second.Messages(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(back) != 1 || back[0].Text() != "still here" {
		t.Errorf("reopening the file found %+v, want the message the first store saved", back)
	}
}
```

- [ ] **Step 3: Run it and verify it fails**

```sh
cd sdk/agent && go test ./stores/sqlite/
```

Expected: FAIL — `no required module provides package .../stores/sqlite`.

- [ ] **Step 4: Write the store**

`sdk/agent/stores/sqlite/sqlite.go`:

```go
// Package sqlite keeps sessions in a single file. It is the store for one
// machine — a laptop, a test, a program with no database to point at.
//
// The driver is modernc.org/sqlite, which is pure Go: a cgo driver would force
// CGO_ENABLED=1 and break cross-compilation for anybody importing this module.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/LaplacianAI/openarity/sdk/agent"
	_ "modernc.org/sqlite" // the driver this package exists to use
)

const schema = `
CREATE TABLE IF NOT EXISTS agent_messages (
	session  TEXT    NOT NULL,
	position INTEGER NOT NULL,
	body     TEXT    NOT NULL,
	PRIMARY KEY (session, position)
);
CREATE TABLE IF NOT EXISTS agent_steers (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	session TEXT NOT NULL,
	text    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_steers_session ON agent_steers (session, id);
`

// Open opens or creates the file and makes sure the tables are there. Unlike
// the Postgres store it runs its own DDL: one file and one process means there
// is nothing to coordinate with.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close() //nolint:errcheck // the open failed; this error is the one worth reporting
		return nil, fmt.Errorf("creating the session tables in %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

type Store struct{ db *sql.DB }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Save(ctx context.Context, session string, msgs []agent.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("saving session %s: %w", session, err)
	}
	defer tx.Rollback() //nolint:errcheck // a committed tx rolls back to nothing

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_messages WHERE session = ?`, session); err != nil {
		return fmt.Errorf("clearing session %s: %w", session, err)
	}
	for i, m := range msgs {
		body, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("encoding message %d of session %s: %w", i, session, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agent_messages (session, position, body) VALUES (?, ?, ?)`,
			session, i, string(body)); err != nil {
			return fmt.Errorf("saving message %d of session %s: %w", i, session, err)
		}
	}
	return tx.Commit()
}

func (s *Store) Messages(ctx context.Context, session string) ([]agent.Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT body FROM agent_messages WHERE session = ? ORDER BY position`, session)
	if err != nil {
		return nil, fmt.Errorf("reading session %s: %w", session, err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below is the one that matters

	var msgs []agent.Message
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("reading session %s: %w", session, err)
		}
		var m agent.Message
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			return nil, fmt.Errorf("decoding a message of session %s: %w", session, err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *Store) AddSteer(ctx context.Context, session, text string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_steers (session, text) VALUES (?, ?)`, session, text); err != nil {
		return fmt.Errorf("leaving a steer for session %s: %w", session, err)
	}
	return nil
}

// TakeSteers removes what it returns, inside one transaction, so two callers
// cannot both take the same steer and tell the model the same thing twice.
func (s *Store) TakeSteers(ctx context.Context, session string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	defer tx.Rollback() //nolint:errcheck // a committed tx rolls back to nothing

	rows, err := tx.QueryContext(ctx,
		`DELETE FROM agent_steers WHERE session = ? RETURNING text`, session)
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}

	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close() //nolint:errcheck // the scan error is the one to report
			return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
		}
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		rows.Close() //nolint:errcheck // same
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	return texts, tx.Commit()
}

var _ agent.Store = (*Store)(nil)
```

- [ ] **Step 5: Keep the driver out of the core**

In `sdk/agent/Makefile`, add to `FORBIDDEN`:

```make
	modernc.org/sqlite:a@store@is@a@deployment@choice,@not@something@a@pattern@links
```

- [ ] **Step 6: Run and verify**

```sh
cd sdk/agent && go test -race ./stores/... && make check
```

Expected: PASS, including `the core packages link no provider or protocol SDK`.

- [ ] **Step 7: Prove the boundary check bites**

Add `import _ "modernc.org/sqlite"` to `sdk/agent/session.go`, run
`make boundary`, confirm it fails naming the package, then remove it.

- [ ] **Step 8: Commit**

```sh
git add sdk/agent/stores/sqlite sdk/agent/Makefile sdk/agent/go.mod sdk/agent/go.sum
git commit -m "feat(sdk): keep sessions in a sqlite file"
```

---

### Task 6: A store on Postgres

**Files:**
- Create: `sdk/agent/stores/postgres/postgres.go`
- Create: `sdk/agent/stores/postgres/postgres_test.go`
- Modify: `sdk/agent/Makefile`
- Modify: `sdk/agent/go.mod`, `sdk/agent/go.sum`

**Interfaces:**
- Consumes: `agent.Store`, `storetest.Run` from Tasks 1 and 2
- Produces: `postgres.New(pool *pgxpool.Pool) *Store`, `postgres.Schema` (a `string` of DDL)

- [ ] **Step 1: Add the driver**

```sh
cd sdk/agent && go get github.com/jackc/pgx/v5 && go mod tidy
```

`pgx/v5` because `apps/brain` already uses it — one driver across the
repository rather than two connection pools with different semantics.

- [ ] **Step 2: Write the failing test**

`sdk/agent/stores/postgres/postgres_test.go`:

```go
package postgres_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/storetest"
)

// AGENT_TEST_POSTGRES_DSN, not a hard-coded address: CI and a laptop disagree
// about where Postgres is, and a test that assumes one fails on the other for
// a reason that has nothing to do with the code.
func TestThePostgresStoreKeepsTheContract(t *testing.T) {
	dsn := os.Getenv("AGENT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AGENT_TEST_POSTGRES_DSN to run the Postgres store against a real database")
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(t.Context(), postgres.Schema); err != nil {
		t.Fatalf("applying Schema: %v", err)
	}

	storetest.Run(t, func(t *testing.T) agent.Store {
		// Each subtest gets the tables empty; the ids are shared otherwise.
		if _, err := pool.Exec(t.Context(),
			`TRUNCATE agent_messages, agent_steers`); err != nil {
			t.Fatalf("clearing: %v", err)
		}
		return postgres.New(pool)
	})
}

// The brain applies this through its own migration system, under the advisory
// lock it already takes. If the constant stops being valid DDL, that migration
// fails at deploy time rather than here — so it is checked here.
func TestTheSchemaIsApplicableAndRepeatable(t *testing.T) {
	dsn := os.Getenv("AGENT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AGENT_TEST_POSTGRES_DSN to check the schema against a real database")
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(pool.Close)

	for i := range 2 {
		if _, err := pool.Exec(t.Context(), postgres.Schema); err != nil {
			t.Fatalf("applying Schema pass %d: %v — it has to be safe to run twice, because\n"+
				"a migration that half-applied will be run again", i+1, err)
		}
	}
}
```

- [ ] **Step 3: Run it and verify it fails**

```sh
cd sdk/agent && go test ./stores/postgres/
```

Expected: FAIL — the package does not exist. With no DSN set the tests skip,
so the package must still compile: that is what this step checks.

- [ ] **Step 4: Write the store**

`sdk/agent/stores/postgres/postgres.go`:

```go
// Package postgres keeps sessions in Postgres. It is the store for a
// deployment with more than one replica, which is the case this whole feature
// exists for.
//
// It applies no DDL. A library that silently migrates a shared database is a
// library that will one day do it during an incident — so Schema is exported
// and the caller applies it under whatever lock it already takes.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Schema is the DDL this store needs. It is safe to run more than once,
// because a migration that half-applied will be run again.
const Schema = `
CREATE TABLE IF NOT EXISTS agent_messages (
	session  text   NOT NULL,
	position int    NOT NULL,
	body     jsonb  NOT NULL,
	PRIMARY KEY (session, position)
);

CREATE TABLE IF NOT EXISTS agent_steers (
	id      bigserial PRIMARY KEY,
	session text NOT NULL,
	text    text NOT NULL
);

CREATE INDEX IF NOT EXISTS agent_steers_session ON agent_steers (session, id);
`

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Store struct{ pool *pgxpool.Pool }

func (s *Store) Save(ctx context.Context, session string, msgs []agent.Message) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM agent_messages WHERE session = $1`, session); err != nil {
			return fmt.Errorf("clearing session %s: %w", session, err)
		}
		for i, m := range msgs {
			body, err := json.Marshal(m)
			if err != nil {
				return fmt.Errorf("encoding message %d of session %s: %w", i, session, err)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO agent_messages (session, position, body) VALUES ($1, $2, $3)`,
				session, i, body); err != nil {
				return fmt.Errorf("saving message %d of session %s: %w", i, session, err)
			}
		}
		return nil
	})
}

func (s *Store) Messages(ctx context.Context, session string) ([]agent.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT body FROM agent_messages WHERE session = $1 ORDER BY position`, session)
	if err != nil {
		return nil, fmt.Errorf("reading session %s: %w", session, err)
	}

	msgs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (agent.Message, error) {
		var body []byte
		if err := row.Scan(&body); err != nil {
			return agent.Message{}, err
		}
		var m agent.Message
		err := json.Unmarshal(body, &m)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("reading session %s: %w", session, err)
	}
	return msgs, nil
}

func (s *Store) AddSteer(ctx context.Context, session, text string) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO agent_steers (session, text) VALUES ($1, $2)`, session, text); err != nil {
		return fmt.Errorf("leaving a steer for session %s: %w", session, err)
	}
	return nil
}

// TakeSteers deletes what it returns in one statement. Two replicas polling at
// the same moment therefore split the queue between them rather than both
// taking everything — DELETE takes a row lock, and the loser sees no row.
func (s *Store) TakeSteers(ctx context.Context, session string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`DELETE FROM agent_steers WHERE session = $1 RETURNING text`, session)
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}

	texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	return texts, nil
}

var _ agent.Store = (*Store)(nil)
```

`DELETE … RETURNING` does not preserve insertion order on its own. The
contract suite asserts order, so if it fails here, change the statement to
delete by a subselect that is `ORDER BY id`:

```sql
DELETE FROM agent_steers
WHERE id IN (SELECT id FROM agent_steers WHERE session = $1 ORDER BY id)
RETURNING text
```

Run the suite before deciding — do not add the subselect speculatively.

- [ ] **Step 5: Keep the driver out of the core**

In `sdk/agent/Makefile`, add to `FORBIDDEN`:

```make
	jackc/pgx:a@store@is@a@deployment@choice,@not@something@a@pattern@links
```

- [ ] **Step 6: Run and verify, with a database**

```sh
docker run --rm -d -p 5433:5432 -e POSTGRES_PASSWORD=postgres --name agentpg postgres:18
cd sdk/agent
AGENT_TEST_POSTGRES_DSN='postgres://postgres:postgres@127.0.0.1:5433/postgres?sslmode=disable' \
  go test -race ./stores/postgres/
make check
docker rm -f agentpg
```

Expected: PASS, and `make check` still reports the core links nothing. The DSN
is a throwaway container on a non-default port; it never goes in a file.

- [ ] **Step 7: Prove the contract bites here too**

Change `TakeSteers` to `SELECT text FROM agent_steers WHERE session = $1`
without the delete. Confirm `a steer is delivered exactly once` and
`two callers taking at once do not both get the steer` both fail. Restore.

- [ ] **Step 8: Commit**

```sh
git add sdk/agent/stores/postgres sdk/agent/Makefile sdk/agent/go.mod sdk/agent/go.sum
git commit -m "feat(sdk): keep sessions in postgres, and let the caller migrate"
```

---

### Task 7: An example that proves it, and the docs

**Files:**
- Create: `sdk/agent/examples/sessions/main.go`
- Modify: `sdk/agent/examples/README.md`
- Modify: `sdk/agent/README.md`
- Create: `site/content/docs/agent-sdk/sessions.md`
- Modify: `site/content/docs/agent-sdk/_index.md`
- Modify: `site/content/docs/examples/_index.md`

**Interfaces:**
- Consumes: everything from Tasks 1 through 6

Follow the `add-an-example` skill: `package main`, one file, `gateway.Resolve`
for the endpoint, events on their own goroutine, `attempt()` rather than a
defer under `os.Exit`.

- [ ] **Step 1: Write the example**

`sdk/agent/examples/sessions/main.go` must print something a reader could not
have assumed. The line that earns its place:

```text
replica A   started the run, held no handle to it
replica B   left a steer in the store and never saw the run
transcript  7 messages, and the steer is message 3 of them
resumed     replica C continued from the store with 7 messages it never produced
```

Three separate `Runner`s against **one** SQLite file — not goroutines
pretending to be replicas, which would prove nothing a mutex could not.

```go
func attempt() error {
	ctx, release := signal.NotifyContext(context.Background(), os.Interrupt)
	defer release()

	dir, err := os.MkdirTemp("", "openarity-sessions")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a temp dir on the way out

	store, err := sqlite.Open(filepath.Join(dir, "sessions.db"))
	if err != nil {
		return err
	}
	defer store.Close() //nolint:errcheck // same

	const id = "conversation-1"

	// Replica B: leaves a steer holding no handle to any run, because there
	// is no run yet. This is the case a *Run cannot serve at all.
	left, err := agent.Open(id, store)
	if err != nil {
		return err
	}
	if err := left.Steer(ctx, "check the lease expiry, not the token"); err != nil {
		return err
	}
	fmt.Println("replica B   left a steer, holding no handle to any run")

	// Replica A: runs, and collects the steer it was never told about.
	session, err := agent.Open(id, store)
	if err != nil {
		return err
	}
	result, err := run(ctx, session, nil)
	if err != nil {
		return err
	}
	fmt.Printf("replica A   ran %d steps and never saw replica B\n", result.Steps)

	at := -1
	for i, m := range result.Messages {
		if strings.Contains(m.Text(), "check the lease expiry") {
			at = i
		}
	}
	fmt.Printf("transcript  %d messages, and the steer is message %d of them\n",
		len(result.Messages), at)

	// Replica C: has never run anything. It reads the store and carries on.
	kept, err := store.Messages(ctx, id)
	if err != nil {
		return err
	}
	resumed, err := run(ctx, nil, kept)
	if err != nil {
		return err
	}
	fmt.Printf("replica C   resumed from %d messages it never produced, answered %q\n",
		len(kept), resumed.Output)

	return nil
}
```

`run` builds its own `agent.Runner` each time — a fresh one per replica, so
nothing is shared but the file. Set `Spec.Session` only when `session` is
non-nil, so the resume call demonstrates the nil path in the same program.

- [ ] **Step 2: Run it against the stub**

```sh
cd sdk/agent && go run ./examples/sessions && make example
```

Expected: exit 0, and the four lines above with real numbers.

- [ ] **Step 3: Run it against a real gateway**

```sh
OPENARITY_MODELS_BASE_URL=http://127.0.0.1:20128/v1 \
OPENARITY_MODELS_API_KEY=… \
OPENARITY_MODEL=… \
go run ./examples/sessions
```

The stub is scripted; only a real model says whether a steer collected from a
store reads as an instruction rather than as noise.

- [ ] **Step 4: Write the docs**

- `site/content/docs/agent-sdk/sessions.md`, weight 8, covering: what a session
  is and is not, the two interfaces, the three stores, what a crash costs
  (the step is redone, tools included), and that `nil` is the default.
- A card for it in `site/content/docs/agent-sdk/_index.md`.
- A `sessions` row in `sdk/agent/examples/README.md` **and** in
  `site/content/docs/examples/_index.md`, and bump both example counts from
  ten to eleven.
- A "Sessions" section in `sdk/agent/README.md`.
- In `site/content/docs/agent-sdk/steering.md`, a paragraph the spec requires:
  `Result.UnappliedSteers` still means *steers that reached the in-memory box
  and never made it onto a request*. Once a `Session` is in play it is no
  longer the complete list of what is undelivered — a steer still sitting in
  the store was never taken, so it stays there and the next run collects it.
  That is the right outcome, and it needs saying rather than coding.

- [ ] **Step 5: Verify the site builds**

```sh
cd site && HUGO_ENVIRONMENT=production make check
```

Expected: the snippets compile and Hugo reports one more page than before.

- [ ] **Step 6: Commit**

```sh
git add sdk/agent/examples/sessions sdk/agent/examples/README.md sdk/agent/README.md site/content/docs
git commit -m "docs(sdk): sessions, and an example that steers a run it never held"
```

---

## What is not in this plan

- **The brain wrapper** — its own spec → plan cycle. It needs a migration
  applying `postgres.Schema` with a `Down`, an endpoint that accepts a steer,
  and authorisation tests proving a caller who may not see a session never
  reaches the store. Different module, different `make check db=`.
- **`forkSession` and `resumeSessionAt`** — branching a conversation, and
  resuming at a chosen message. Real features, not this one.
- **Tool-level journalling** — would stop a tool re-running after a crash.
  Addable later without changing the interface, because how often `Save` is
  called is not part of the contract.
- **Run ownership and cancellation across replicas** — steering and resume
  work without knowing which replica holds a run. Cancelling does not.
