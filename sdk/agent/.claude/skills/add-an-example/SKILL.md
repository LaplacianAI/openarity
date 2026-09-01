---
name: add-an-example
description: Add a runnable example under sdk/agent/examples. Use when demonstrating a loop, a skill, a model client, or any SDK feature that only proves itself by running.
---

# Add an example to `sdk/agent/examples`

Examples in this module are not illustrations. They are the only thing that
runs the loop, the SSE accumulator, the model client and the tool dispatch
together, and CI runs every one of them on every push.

## Step 1 — the layout

One directory per example, `package main`, one file:

```text
examples/
├── README.md
├── gateway/    the stub and the printing every example shares — not an example
├── tools/      one turn, one tool
└── skills/     two skills offered, one opened
```

`gateway` is a library package that happens to live here. `make example`
discovers `./examples/...` and excludes it by name; anything else you add is
run by CI the moment it exists.

## Step 2 — it must run with nothing installed

```go
endpoint, shutdown := gateway.Resolve(
	gateway.ToolCall("count_issues", `{"repo":"openarity"}`),
	gateway.Answer("openarity has 3 open issues."),
)
defer shutdown()
```

`Resolve` uses a real gateway when `OPENARITY_MODELS_BASE_URL` is set and an
in-process stub otherwise. The stub speaks real SSE, so the run is real: only
the model's judgement is scripted.

Script one `Turn` per model response, in order. Past the end the stub answers
"the script ended" rather than repeating itself — a repeating stub loops to
`MaxSteps` and reads as a bug in the loop.

If the stub cannot express what you need, extend `gateway`, do not fork it. A
second stub is a second thing to keep speaking the protocol correctly.

## Step 3 — the shape every example has

```go
func main() {
	// os.Exit skips deferred calls, so the signal handler is released in
	// attempt rather than in a defer here that would never run.
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

`gocritic`'s `exitAfterDefer` catches this, and the reason it matters is real:
a streaming run holds an open connection, so `signal.NotifyContext` has to be
released.

Events are consumed on their own goroutine, closed after `Run` returns, and
waited for:

```go
events := make(chan agent.Event, 64)
done := make(chan struct{})
go func() { defer close(done); gateway.Report(events) }()

result, err := runner.Run(ctx, spec, msgs, endpoint, events)
close(events)
<-done
```

`Emit` never blocks on a slow reader, but a reader that never starts still sees
nothing.

## Step 4 — show the thing, and prove it

An example earns its place by printing something the reader could not have
assumed. `skills` counts how many times each body was read:

```text
bodies   commit-style read 1 time(s), pdf-forms read 0 time(s)
```

That line is the example. Without it a reader has to take progressive
disclosure on trust; with it, the unopened skill is visibly never read.

Find the equivalent for whatever you are demonstrating and print it.

## Step 5 — the traps

- **Usage on every SSE chunk.** The accumulator sums them, and the run reports
  five times the tokens it spent. Usage rides the final chunk only.
- **Arguments in one chunk.** Real gateways split them mid-JSON. `gateway`
  splits them into three on purpose; an example that sends them whole never
  exercises the accumulator it appears to be testing.
- **Building JSON by hand.** Scripted text contains quotes and braces — a
  skill's arguments *are* JSON. Use `gateway`'s `quote`, or the stub emits a
  chunk the client cannot parse.
- **A default model that only suits one gateway.** `gateway.Model()` reads
  `OPENARITY_MODEL`. Do not hard-code one in the example.

## Step 6 — no credentials, ever

An example is the most-copied code in a repository. No key, no real base URL,
no team identifier — the environment supplies all three. A real OmniRoute key
has already been pasted into a terminal in this project once.

## Step 7 — document and verify

Add a row to `examples/README.md`, then:

```sh
cd sdk/agent
make example      # runs every example against the stub
make check        # fmt, vet, lint, boundary, build, cover, vuln
```

`COVER_PKGS` excludes anything under `examples/`, so an example does not move
the coverage number in either direction. Do not add tests for one; add them to
the package it demonstrates.

Then run it against a real gateway before committing:

```sh
OPENARITY_MODELS_BASE_URL=http://127.0.0.1:20128/v1 \
OPENARITY_MODELS_API_KEY=sk-… \
OPENARITY_MODEL=cc/claude-opus-4-8 \
go run ./examples/yours
```

The stub is scripted. Only a real model tells you whether the prompt, the tool
descriptions and the skill descriptions actually work.
