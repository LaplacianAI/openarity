---
title: Structured output
weight: 7
---

Getting an answer your program can read, instead of a paragraph it has to parse.

## Asking for a shape

`Spec.OutputSchema` is a JSON Schema and a name for it:

```go
spec := agent.Spec{
    // ... model, pattern, tools as usual
    OutputSchema: &agent.OutputSchema{
        Name:        "finding",
        Description: "Where the failure is and how bad it is.",
        JSON: json.RawMessage(`{
            "type": "object",
            "properties": {
                "file": {"type": "string"},
                "line": {"type": "integer"},
                "severity": {"type": "string", "enum": ["low", "high"]}
            },
            "required": ["file", "line", "severity"],
            "additionalProperties": false
        }`),
    },
}

result, err := runner.Run(ctx, spec, msgs, endpoint, nil)
if err != nil {
    return err
}

var found finding
if result.Structured == nil {
    return fmt.Errorf("no finding; the model said: %s", result.Plain)
}
if err := json.Unmarshal(result.Structured, &found); err != nil {
    return err
}
```

It becomes `response_format: {"type":"json_schema", …}` on every request the run
makes. That is a feature of the OpenAI-compatible API rather than anything this
library invents, so it reaches whatever your gateway reaches.

No pattern implements it. Patterns talk to the model through `ModelClient`, so
the runner wraps that — the same seam steering uses. ReAct, plan-then-act,
ReWOO, reflection and any pattern you write are covered without a line of
change.

`Description` is not decoration: the gateway passes it to the model as what the
format is *for*. `Strict` is off by default, because strict mode only accepts a
subset of JSON Schema — every property in `required`, `additionalProperties`
false — and a schema that does not satisfy it is refused by the gateway rather
than relaxed.

## Three fields, and which one you want

```go
type Result struct {
    Output     string          // the answer, in the form you asked for
    Plain      string          // what the model actually said
    Structured json.RawMessage // the answer parsed, or nil
    // ...
}
```

`Output` is `Plain` when there is no structured answer, and the structured one
when there is. So a program that does not care which mode produced the answer
reads `Output` and is right either way.

| | no schema | schema, met | schema, unmet |
| ----------- | --------- | ----------- | ------------- |
| `Plain`      | the prose | the JSON, or the prose the JSON was found in | the prose |
| `Structured` | `nil`     | the JSON    | `nil`          |
| `Output`     | the prose | the JSON    | the prose      |

`Plain` is always set, including on a run that asked for nothing. `Structured`
is the only one that is ever nil, and checking it is the only check you need.

## A parser model

`Parser: true` changes when the schema is applied. The run answers in prose,
and then one further call turns that into the schema:

```go
spec := agent.Spec{
    Model:        agent.ModelRef{Name: "a-capable-model"},
    Parser:       true,
    ParserModel:  agent.ModelRef{Name: "a-cheap-one"},
    OutputSchema: schema,
}
```

`ParserModel` is optional; left empty, the run's own model does the parsing.
Either way it is one ordinary model call — no tools, no loop, no pattern — and
its tokens are counted in `Result.Usage` like everything else.

| | `Parser: false` | `Parser: true` |
| ------------------- | ------------------------------ | -------------------------------- |
| requests carrying the schema | every one in the loop  | only the extra call              |
| what the run answers | the JSON                      | prose, kept in `Plain`           |
| model calls          | as many as the pattern needs   | one more                         |
| which model          | the run's                      | `ParserModel`, or the run's       |

Reach for the parser when the answer is worth reading as prose as well —
`Plain` keeps it — or when the run's model is expensive and extraction is not.
Reach for the inline form when you only want the data, since it costs nothing
extra.

The parser is shown the run's whole transcript, not only its final answer. That
is deliberate and was learned the hard way: a run that ends on *"let me look
deeper into that function"* has a final answer containing none of the fields,
while the tool output three messages earlier contains all of them.

Three combinations are refused by name rather than quietly doing nothing:
`Parser` with no `OutputSchema`, `ParserModel` with no `OutputSchema`, and
`ParserModel` with `Parser: false` — where the parser model would never be
called.

## `Structured` can be nil, and that is not a bug

`response_format` is not enforced by every gateway. A request can be accepted
with the schema on it and still come back as prose, and the same model will do
it on one run and not the next. Against OmniRoute with Claude, roughly one run
in three answered in prose instead of the schema.

So an unmet schema is not an error. `Structured` is nil, `Plain` holds what the
model said, and the run is otherwise a success — a caller who cannot proceed
without the data decides that for itself:

```go
if result.Structured == nil {
    return fmt.Errorf("no %s; the model said: %s", schema.Name, result.Plain)
}
```

Making it an error would mean a library deciding that your best-effort
extraction is a failed run.

What *is* handled is a model that answered correctly in an awkward wrapper.
Three shapes all yield the same `Structured`, and all three came from real
gateway runs rather than from imagination.

Claude Opus, plainly:

```json
{"file":"vault.go","line":88,"severity":"high"}
```

Claude Haiku, fenced — a markdown code block where a value was asked for:

~~~text
```json
{"file":"vault.go","line":88,"severity":"high"}
```
~~~

Claude Opus again, a sentence and then the fence — a right answer wearing a hat:

~~~text
Based on my investigation, the failure is at line 88.

```json
{"file":"vault.go","line":88,"severity":"high"}
```
~~~

The whole answer is tried first, then each fenced block in it. That order
matters: a JSON answer that happens to quote a fenced block inside one of its
own strings must not be mistaken for a preamble wrapping that block.

## Seeing it work

```sh
go run ./examples/structured
```

Both modes, one after the other. It prints how many requests carried the
schema, which is the whole difference between them — every request in the
inline section, exactly one in the parser section — and then unmarshals the
result into a Go struct so the value is visibly usable and not just
well-formed.

Set `OPENARITY_PARSER_MODEL` to watch the parse go to a different model than
the run.
