---
name: write-tests
description: Write a Go test for the CLI — any package, any layer. Covers the execute/isolate/seed helpers, when t.Parallel() is allowed, what the test writer cannot see, and the mutation step that proves the test would actually fail. Use for every test.
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
