---
name: add-output-format
description: Add a fourth output format alongside table, json and yaml — jsonl, csv, a Go template. Covers the Format constant, why there is exactly one Parse, the printer type and where it goes, per-format options, and the two tests that stop a new format being silently wrong. Use whenever --output grows a value.
---

# Add an output format

Formats are a strategy: one interface, one type per format, picked by a factory.
Adding one touches four places and no existing printer.

## Step 1 — the constant

`internal/output/format.go`:

```go
const (
	Table Format = "table"
	JSON  Format = "json"
	YAML  Format = "yaml"
	JSONL Format = "jsonl"
)
```

Then `Parse`, `All`, and `IsMachine` if the new format is for a person.

```go
func Parse(value string) (Format, bool) {
	switch Format(strings.ToLower(strings.TrimSpace(value))) {
	case Table:
		return Table, true
	...
	case JSONL:
		return JSONL, true
	default:
		return Default, false
	}
}
```

- **`All` must carry it.** It is what `Names()` builds, which is what every
  error message and help string shows. A format that can be set but is not
  offered is worse than no list.
- **The `default:` arm returns `Default, false`** — a caller ignoring the
  boolean gets something usable rather than an empty format matching no case.
- **`exhaustive` will not save you here**, because `Parse` switches on a value
  rather than over the type's domain. `TestAllCarriesEveryFormat` is what
  catches a constant added and never taught to `Parse`.
- **One `Parse`, in this package.** `config` stores the string, the printer
  renders it, neither interprets it.

## Step 2 — the printer

A new file, `internal/output/printer/jsonl.go`. One type, embedding `base`:

```go
package printer

type jsonlPrinter struct {
	base
}

func (p jsonlPrinter) Print(value any, _ Options) error {
	...
}
```

`base` gives it the writer and a silent `Note`. Only `tablePrinter` overrides
`Note`, because only a person can read one.

**Never call an overridable method from `base`.** Go embedding has no virtual
dispatch — a method on `base` calling `Note` gets base's own, never the child's.
The current shape is safe because `base` calls nothing.

## Step 3 — the factory

`internal/output/printer/printer.go`:

```go
	case output.JSONL:
		return jsonlPrinter{shared}
```

`exhaustive` **does** fire here, because this switch is over `output.Format`.
The build breaks until the case exists, which is the point.

## Step 4 — per-format options, if any

`Options` is a plain struct. A format that needs configuration adds a field:

```go
type Options struct {
	Table      func(*Table)
	YAMLIndent int
}
```

The constructor, if there is one, lives in the format's own file. Formats ignore
fields they do not understand, so a command asks for everything it wants once
and never branches on which printer it got.

Do not reach for functional options. Named fields on a struct, zero value works,
and `printer.go` stays the one place a contributor can read every option that
exists.

## Step 5 — the tests

`printer_test.go`. Two of these matter more than the rest:

1. **The table callback never runs.** For every machine format, pass a callback
   that calls `t.Errorf` and assert it is not invoked. Running it puts colour
   and column padding into a document a parser will read.
2. **The format is not another format.** JSON is a subset of YAML, so
   `yaml.Unmarshal` accepts a JSON document happily — every round-trip
   assertion in the YAML tests passed while the factory returned a **json**
   printer. Assert something only the real format has: `items:` and no `{}[]`
   for yaml, a leading `{` for json, one document per line for jsonl.

Then:

3. **The envelope survives** — `items` and `next_cursor` are still there.
4. **A write failure is returned** — drive it with a writer that errors, which
   is `oa teams list -o jsonl | head` in real life.
5. **`Note` is silent** for a machine format.
6. **It writes to the writer it was given**, never `os.Stdout`.

Mutate the factory to return the wrong printer and confirm test 2 fails. That
mutation survived until the assertion was tightened.

```sh
make check
```

## Step 6 — the rest of the wiring

Nothing. `--output` takes it because the flag's help text is built from
`output.Names()`, `oa config set output` takes it because it validates through
`output.Parse`, and every existing command prints through the interface.

If something else needed changing, the format knows too much about a command.
