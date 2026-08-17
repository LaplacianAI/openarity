---
name: write-tests
description: Write a Go test for the CLI — any package, any layer. Covers the execute/isolate/seed helpers, the two stub brains and when the routed one is required, when t.Parallel() is allowed, what the test writer cannot see, and the mutation step that proves the test would actually fail. Use for every test.
---

# Write a test

Every change ships with tests, and a test is not done until it has been shown to
fail against broken code.

## Step 1 — name it after the behaviour

The name is a sentence about the system, not about the function.

```go
func TestDeletingTheActiveContextLeavesNoDanglingPointer(t *testing.T)
func TestMachineFormatsNeverRunTheTableCallback(t *testing.T)
func TestTheTokenValueIsNeverResolved(t *testing.T)
```

Not `TestDelete`, `TestPrint`, `TestResolve2`. A failing name should say what
broke without opening the file.

Above it, one comment saying **why the test exists** — the failure it prevents,
not what it asserts. The assertion is already in the code.

```go
// Deleting the active one must not leave Current naming a context that is
// gone — a dangling pointer resolves to the built-in address, so the next
// command goes somewhere nobody asked for.
```

## Step 2 — pick the level

| Testing                     | Use                                    |
| --------------------------- | -------------------------------------- |
| a pure function             | call it directly, `t.Parallel()`       |
| a command end to end        | `execute(t, "context", "list")`        |
| anything touching the config file | `isolate(t)` first, no `t.Parallel()` |
| a command that calls one endpoint | `clitest.BrainStub(t, status, body)` |
| a command that calls two, or where the order matters | `clitest.Routes(t, ...)` |

`execute` drives the whole binary the way a shell does — it builds the root
command, so a command that is built but never registered fails here rather than
passing every test written about its body.

```go
func TestListMarksTheActiveContext(t *testing.T) {
	isolate(t)

	seed(t, "context", "create", "local", "--server", "http://127.0.0.1:21120")

	out, err := execute(t, "context", "list")
	if err != nil {
		t.Fatalf("context list: %v", err)
	}
	...
}
```

- **`isolate(t)`** gives the test its own config directory by setting
  `XDG_CONFIG_HOME` *and* `HOME` — macOS ignores the former.
- **`seed(t, ...)`** runs a setup command and `t.Fatalf`s if it fails. Use it
  for anything whose failure would make the real assertion meaningless.
- **`execute` returns combined output and the error.** Assert on `out+err.Error()`
  when checking a message, because cobra may print rather than return.

### Two stubs, and when the second one is required

`clitest.BrainStub` serves one canned reply to every path and remembers only
the last request. That is enough for a command that makes one call.

It is not enough the moment a command resolves a name, because the interesting
assertions are about **which endpoints were called and in what order** — and a
single reply cannot be both a page of teams and a 204. `clitest.Routes` takes
`http.ServeMux` patterns and records every request:

```go
script := clitest.Routes(t, map[string]clitest.Reply{
	"GET /teams":               {Status: http.StatusOK, Body: onePage},
	"POST /teams/{id}/members": {Status: http.StatusNoContent, Body: ""},
})

if n := script.Calls(http.MethodGet, "/users"); n != 0 {
	t.Errorf("the directory was read %d times to add somebody by name", n)
}
seen := script.All()   // in order
```

- **Key the `Reply` fields.** `govet`'s `composites` fails an unkeyed literal
  for a struct from another package, and `make lint` reports only three of them
  before truncating.
- **Assert the calls that must *not* happen.** `Calls(...) != 0` on an endpoint
  the command should never touch is what catches a permission regression — the
  request still succeeds, it just needed an authority it should not have.
- **An unrouted request answers 404 and is still recorded**, so a call nobody
  expected shows up as a failed assertion rather than a hang.

## Step 3 — t.Parallel() only where nothing is shared

Anything that reads or writes the environment cannot be parallel, and that
includes every test using `isolate`. `t.Setenv` fails the test outright if
`t.Parallel` was called.

Pure packages — `theme`, `output`, and `config`'s resolution — are parallel
throughout.

## Step 4 — assert the negative too

A test that only checks the happy path passes when a guard is deleted. For every
refusal, assert that **nothing changed**:

```go
	if _, err := execute(t, "context", "create", "prod", "--server", "x"); err == nil {
		t.Fatal("a duplicate name was accepted")
	}

	saved, _ := config.Load()
	if got := saved.Contexts["prod"].Server; got != "https://brain.example.com" {
		t.Errorf("prod server = %q, want the rejected create to have changed nothing", got)
	}
```

**A request that should not have been made is a negative too**, and it is the
one nothing else catches. A command that reaches an endpoint it did not need
still succeeds — it merely required an authority it should not have, and no
assertion about its output will ever notice:

```go
	if n := script.Calls(http.MethodGet, "/users"); n != 0 {
		t.Errorf("the directory was read %d times to add somebody by name", n)
	}
```

Assert it whenever a command could plausibly have taken a shortcut through a
wider-scoped endpoint. `Calls` returning zero is the whole test.

## Step 5 — know what the test writer cannot see

The test writer is a `bytes.Buffer`. lipgloss detects no terminal on it, so
**every style is a no-op in tests**. This means:

- A styled value looks identical to a plain one. A test cannot catch a value
  styled before it reaches `Print`, which is the bug that puts escape sequences
  inside a JSON string.
- Column alignment cannot be tested either, because the escapes that break it
  are never emitted.

Both need a pty. Capture one with `script -q /dev/null ./bin/oa ...` and
`CLICOLOR_FORCE=1`, strip the escapes, and compare column positions. Do it by
hand when touching either; do not fake a test that cannot fail.

## Step 6 — fakes are structs in the test file

No mocking library. A fake is a struct with the method and a field for what it
should return.

```go
type fakeDiscoverer struct {
	response *client.AuthConfigResponse
	err      error
}

func (f fakeDiscoverer) GetAuthConfigWithResponse(context.Context, ...) (...) {
	return f.response, f.err
}
```

If faking is awkward, the dependency is too wide — narrow the interface at the
consumer, not the provider.

## Step 7 — break it, and confirm the test fails

This is the step that makes the rest worth anything.

```sh
# comment out the guard, or invert the condition
go test ./... -count=1     # expect a failure naming your test
# put it back
go test ./... -count=1     # expect ok
```

Mutate **every** guard, not one. This session found four tests that passed
against broken code:

- `TestYAMLKeepsThePageEnvelope` passed when the factory returned a **json**
  printer, because JSON is a subset of YAML and `yaml.Unmarshal` accepts it.
- `TestSaveIsAtomicUnderConcurrentReads` passed when the temp-file-and-rename
  was replaced with `os.WriteFile`, because the readers stopped before the
  writer started.
- A `settingViews` test passed with a setting listed twice, because nothing
  asserted the count.
- A mutation that removed the last use of an import reported "survived",
  because the harness read a build failure as silence. Check that the mutated
  code still **compiles** before believing it passed.

When a branch genuinely cannot be tested — `yaml.Encoder.Close` produces
byte-identical output with and without it — say so in a comment and move on.
Never write an assertion that cannot fail.

```sh
make check
```
