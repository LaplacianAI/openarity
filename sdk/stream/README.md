# `sdk/stream`

One conversation as a stream of events: what people said, and what each run
did in answer, in the order it happened. It is served as server-sent events,
and every event carries a sequence number, so a client that loses its
connection asks for what came after the last one it saw and misses nothing.

It is the stream the brain serves for a session, as a library: any Go server
can serve its own conversations the same way, and any Go client can read them.

## Install

```sh
go get github.com/LaplacianAI/openarity/sdk/stream
```

Needs Go 1.26.11. The `stream` package links only the standard library, and a
`make check` step fails if it ever links anything else. `agentstream`, the
bridge from [`sdk/agent`](../agent/README.md), is the one package that links the
agent SDK.

It is a `v0`: nothing about the API is promised yet. Pin the version.

## Serve a conversation

```go
var log stream.Memory // or a Log of your own over a database

http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
	after, err := stream.Resume(r) // Last-Event-ID, else ?after=, else the start
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = stream.Follower{Log: &log}.Follow(w, r, after)
})

log.Append("", stream.Message{ID: "m1", Parts: []stream.Part{{Type: stream.PartText, Text: "hi"}}})
```

A run of `sdk/agent` streams into the same log with `agentstream.Pipe`: hand
`Runner.Run` an events channel, pipe it into the log under the run's id, and
close it when `Run` returns.

## Read one

```go
r := stream.NewReader(res.Body)
for {
	env, err := r.Next() // io.EOF when the stream ends
	if err != nil {
		break
	}
	fmt.Println(env.Seq, env.Event.Kind())
}
last, _ := r.Last() // send as Last-Event-ID to pick up after it
```

## The events

Each frame is `id: <seq>`, `event: <kind>`, and `data:` one flat JSON object
with the envelope's `at` and `run_id` beside the event's own fields.
[`schema.json`](schema.json) is the catalog as JSON Schema.

| Kind | What it is |
| --- | --- |
| `message` | something a person said; `pending` while it waits for a run |
| `message_consumed` | a run taking messages: the one it started from, or ones sent while it worked |
| `run_started` | a run beginning, with its parent when another run handed it the work |
| `step_started` | a run making its next model call |
| `text_message_chunk` | a piece of what a run is writing |
| `tool_call_chunk` | a run calling a tool, with all of its arguments |
| `tool_call_result` | what the call returned, or why it failed |
| `usage` | what one model call spent |
| `compacted` | a run shrinking its context to carry on |
| `run_finished` | a run ending, with its whole answer |
| `custom` | a server's own event, named |

The names follow [AG-UI](https://docs.ag-ui.com/concepts/events)'s, so an
AG-UI client needs a rename rather than a rewrite. Text and tool calls are
chunks, AG-UI's stateless form: a client that joins mid-answer needs no start
event it never saw, and `run_finished` carries the whole answer. A kind this
version does not know decodes as `Unknown` and is written back unchanged, so an
old client reads a newer stream.

Sequence numbers may be negative: a server that numbers history it had before
it kept a stream counts it down to 0, and `stream.Start` is the cursor before
all of it.

## Examples

```sh
make example   # or: go run ./examples/resume
```

`resume` serves a conversation, drops the connection halfway through a run's
answer, reconnects with the last sequence number, and prints that nothing was
missed and nothing came twice.
