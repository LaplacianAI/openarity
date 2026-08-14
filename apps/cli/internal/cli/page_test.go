package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
	"github.com/LaplacianAI/openarity/apps/cli/internal/theme"
	"github.com/LaplacianAI/openarity/apps/cli/internal/ui"
)

// values parses args the way cobra does and reports what would be sent.
func values(t *testing.T, args ...string) (*int32, *string) {
	t.Helper()

	var paging Paging
	cmd := &cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return nil }}
	paging.Flags(cmd)

	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}

	return paging.Values()
}

// The brain answers 400 to a limit of zero, so the flag's own default has to
// mean unspecified. Sending it unconditionally breaks every plain invocation.
func TestNoFlagsSendsNothing(t *testing.T) {
	t.Parallel()

	limit, cursor := values(t)

	if limit != nil {
		t.Errorf("limit = %d, want nothing sent", *limit)
	}
	if cursor != nil {
		t.Errorf("cursor = %q, want nothing sent", *cursor)
	}
}

func TestBothFlagsAreSentVerbatim(t *testing.T) {
	t.Parallel()

	limit, cursor := values(t, "--limit", "25", "--cursor", "eyJjIjoiMjAyNiJ9")

	if limit == nil || *limit != 25 {
		t.Errorf("limit = %v, want 25", limit)
	}
	if cursor == nil || *cursor != "eyJjIjoiMjAyNiJ9" {
		t.Errorf("cursor = %v, want the cursor unchanged", cursor)
	}
}

// A cursor is opaque and a client cannot construct one, so anything but an
// exact copy is a 400. Trimming is the one change that is safe — it undoes a
// paste, it does not edit the value.
func TestACursorIsTrimmedAndNeverEdited(t *testing.T) {
	t.Parallel()

	_, cursor := values(t, "--cursor", "  eyJjIjoiMjAyNiJ9  ")

	if cursor == nil || *cursor != "eyJjIjoiMjAyNiJ9" {
		t.Errorf("cursor = %v, want the surrounding space gone and nothing else", cursor)
	}
}

// An empty --cursor is what a shell produces from an unset variable. Sending
// it is a 400 about a cursor the caller never typed.
func TestAnEmptyCursorIsNotSent(t *testing.T) {
	t.Parallel()

	for _, given := range []string{"", "   ", "\t"} {
		if _, cursor := values(t, "--cursor", given); cursor != nil {
			t.Errorf("--cursor %q sent %q", given, *cursor)
		}
	}
}

// Zero and negative are both refused by the brain, and neither can be what
// somebody meant. Refusing to send them keeps the failure local.
func TestANonPositiveLimitIsNotSent(t *testing.T) {
	t.Parallel()

	for _, given := range []string{"0", "-1"} {
		if limit, _ := values(t, "--limit", given); limit != nil {
			t.Errorf("--limit %s sent %d", given, *limit)
		}
	}
}

// printed renders a page as json, which is the only format where the shape is
// observable — a table shows cells, not keys.
func printed[T any](t *testing.T, page Page[T]) string {
	t.Helper()

	var out bytes.Buffer
	opts := &Options{
		Stdout: &out,
		Styles: ui.New(&out, theme.Auto),
		Out:    printer.New(&out, output.JSON),
	}

	if err := PrintPage(opts, page); err != nil {
		t.Fatalf("PrintPage: %v", err)
	}
	return out.String()
}

// A nil slice marshals to null and `jq length` fails on null. A brain that
// answers with no items at all is exactly what a fresh install produces.
func TestNoItemsIsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()

	out := printed(t, Page[string]{Items: nil, Row: func(*printer.Table, string) {}})

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}

	items, ok := got["items"].([]any)
	if !ok {
		t.Fatalf("items = %#v, want an empty array", got["items"])
	}
	if len(items) != 0 {
		t.Errorf("items has %d rows", len(items))
	}
}

// The key is next_cursor. The generated page types carry no yaml tags and
// yaml.v3 lowercases a field name, so printing those directly spells it
// nextcursor — one name in json and another in yaml for the same field.
func TestTheCursorKeepsItsWireName(t *testing.T) {
	t.Parallel()

	cursor := "eyJjIjoiMjAyNiJ9"
	out := printed(t, Page[string]{
		Items:      []string{"a"},
		NextCursor: &cursor,
		Row:        func(*printer.Table, string) {},
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if got["next_cursor"] != cursor {
		t.Errorf("next_cursor = %#v, want it carried through verbatim", got["next_cursor"])
	}
}

// On the last page the key must be absent rather than null: a consumer that
// branches on the key being there would otherwise page forever.
func TestTheLastPageCarriesNoCursorKey(t *testing.T) {
	t.Parallel()

	out := printed(t, Page[string]{Items: []string{"a"}, Row: func(*printer.Table, string) {}})

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if _, present := got["next_cursor"]; present {
		t.Errorf("next_cursor is present on the last page: %#v", got["next_cursor"])
	}
}

// Both notes go through Note, so neither reaches a document a parser reads.
// A hint printed before the json is a parse error for the caller.
func TestNeitherNoteReachesAMachineFormat(t *testing.T) {
	t.Parallel()

	cursor := "abc"
	out := printed(t, Page[string]{
		Items:      nil,
		NextCursor: &cursor,
		Empty:      "nothing here",
		More:       "oa teams list",
		Row:        func(*printer.Table, string) {},
	})

	for _, prose := range []string{"nothing here", "--cursor", "more"} {
		if strings.Contains(out, prose) {
			t.Errorf("%q corrupted the document:\n%s", prose, out)
		}
	}
	if err := json.Unmarshal([]byte(out), &map[string]any{}); err != nil {
		t.Fatalf("the json does not parse: %v\n%s", err, out)
	}
}

// The table is the only place a hint is useful, and a cursor nobody can see is
// a page nobody can reach.
func TestTheTableCarriesBothNotes(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	opts := &Options{
		Stdout: &out,
		Styles: ui.New(&out, theme.Auto),
		Out:    printer.New(&out, output.Table),
	}

	cursor := "eyJjIjoiMjAyNiJ9"
	err := PrintPage(opts, Page[string]{
		Items:      nil,
		NextCursor: &cursor,
		Empty:      "nothing here",
		More:       "oa teams list",
		Row:        func(*printer.Table, string) {},
	})
	if err != nil {
		t.Fatalf("PrintPage: %v", err)
	}

	for _, want := range []string{"nothing here", "oa teams list --cursor " + cursor} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the table is missing %q:\n%s", want, out.String())
		}
	}
}

// "no teams" above a table of teams is the kind of contradiction that makes a
// caller distrust everything else on the screen.
func TestAPopulatedPageNeverSaysItIsEmpty(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	opts := &Options{
		Stdout: &out,
		Styles: ui.New(&out, theme.Auto),
		Out:    printer.New(&out, output.Table),
	}

	err := PrintPage(opts, Page[string]{
		Items: []string{"one"},
		Empty: "nothing here",
		Row:   func(table *printer.Table, item string) { table.Row(item) },
	})
	if err != nil {
		t.Fatalf("PrintPage: %v", err)
	}

	if strings.Contains(out.String(), "nothing here") {
		t.Errorf("a page with a row said it was empty:\n%s", out.String())
	}
}

// Row draws every item and no others. A loop that stops early is invisible in
// a two-row fixture and obvious here.
func TestEveryItemIsDrawnOnce(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	opts := &Options{
		Stdout: &out,
		Styles: ui.New(&out, theme.Auto),
		Out:    printer.New(&out, output.Table),
	}

	items := []string{"one", "two", "three"}
	drawn := []string{}

	err := PrintPage(opts, Page[string]{
		Items: items,
		Row: func(table *printer.Table, item string) {
			drawn = append(drawn, item)
			table.Row(item)
		},
	})
	if err != nil {
		t.Fatalf("PrintPage: %v", err)
	}

	if strings.Join(drawn, ",") != strings.Join(items, ",") {
		t.Errorf("drew %v, want %v in order", drawn, items)
	}
}
