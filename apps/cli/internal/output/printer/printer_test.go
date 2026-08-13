package printer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
)

// What every list command returns. The envelope is the contract — items and
// whether there are more — so a client can page without guessing.
type page struct {
	Items   []row `json:"items" yaml:"items"`
	HasMore bool  `json:"has_more" yaml:"has_more"`
}

type row struct {
	Name   string `json:"name" yaml:"name"`
	Server string `json:"server" yaml:"server"`
}

func onePage() page {
	return page{
		Items:   []row{{Name: "local", Server: "http://127.0.0.1:21120"}},
		HasMore: false,
	}
}

// The guarantee the whole type exists for. A Rows callback renders with colour
// and column padding; running it under json puts escape sequences inside a
// string and the consumer fails to parse.
func TestMachineFormatsNeverRunTheTableCallback(t *testing.T) {
	t.Parallel()

	for _, format := range []output.Format{output.JSON, output.YAML} {
		var buf bytes.Buffer

		err := New(&buf, format).Print(onePage(), Options{Table: func(*Table) {
			t.Errorf("%s ran the table callback", format)
		}})
		if err != nil {
			t.Errorf("%s: %v", format, err)
		}
	}
}

func TestTableRunsTheCallback(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	ran := false

	err := New(&buf, output.Table).Print(onePage(), Options{Table: func(table *Table) {
		ran = true
		table.Row("local", "http://127.0.0.1:21120")
	}})
	if err != nil {
		t.Fatalf("Print: %v", err)
	}
	if !ran {
		t.Fatal("the table callback never ran")
	}
	if !strings.Contains(buf.String(), "local") {
		t.Errorf("the row never reached the writer: %q", buf.String())
	}
}

// The table is drawn by the callback, so the value must not also be dumped.
// Printing both would put a Go struct literal above every table.
func TestTableDoesNotAlsoRenderTheValue(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	if err := New(&buf, output.Table).Print(onePage(), Options{Table: func(*Table) {}}); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if strings.ContainsAny(buf.String(), "{}") {
		t.Errorf("the value was serialised into the table output: %q", buf.String())
	}
}

// A command with nothing to draw must not panic. `oa context list -o table`
// with no contexts passes a Note and no rows.
func TestTableToleratesNoCallback(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	if err := New(&buf, output.Table).Print(onePage(), Options{}); err != nil {
		t.Fatalf("Print with no rows: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a nil callback still wrote something: %q", buf.String())
	}
}

// The envelope is the point. Flattening it to a bare array would drop has_more
// and every consumer would silently believe it had the last page.
func TestJSONKeepsThePageEnvelope(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	if err := New(&buf, output.JSON).Print(page{Items: []row{{Name: "local"}}, HasMore: true}, Options{}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("the output is not json: %v\n%s", err, buf.String())
	}

	if _, ok := got["items"].([]any); !ok {
		t.Errorf("items is not an array: %#v", got["items"])
	}
	if got["has_more"] != true {
		t.Errorf("has_more = %#v, want true", got["has_more"])
	}
}

func TestYAMLKeepsThePageEnvelope(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	if err := New(&buf, output.YAML).Print(page{Items: []row{{Name: "local"}}, HasMore: true}, Options{}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var got page
	if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("the output is not yaml: %v\n%s", err, buf.String())
	}
	if len(got.Items) != 1 || !got.HasMore {
		t.Errorf("the envelope did not survive the round trip: %+v", got)
	}
}

// yaml.Encoder buffers, and a small document flushes on Encode whether or not
// Close is called — so the check has to be big enough to still be in the
// buffer. Without Close the tail is lost and the command reports success.
func TestYAMLIsFlushed(t *testing.T) {
	t.Parallel()

	big := page{}
	for i := range 500 {
		big.Items = append(big.Items, row{
			Name:   fmt.Sprintf("context-%03d", i),
			Server: fmt.Sprintf("https://brain-%03d.example.com", i),
		})
	}

	var buf bytes.Buffer
	if err := New(&buf, output.YAML).Print(big, Options{}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var got page
	if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("the document is truncated: %v", err)
	}
	if len(got.Items) != len(big.Items) {
		t.Errorf("got %d items, want %d — the tail never reached the writer",
			len(got.Items), len(big.Items))
	}
}

// JSON is a subset of YAML, so yaml.Unmarshal accepts a JSON document happily.
// Every round-trip assertion here would pass if the factory handed back the
// wrong printer, which is exactly the mistake worth catching.
func TestYAMLIsNotJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := New(&buf, output.YAML).Print(onePage(), Options{}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	got := buf.String()
	if strings.ContainsAny(got, "{}[]") {
		t.Errorf("the yaml carries json punctuation: %q", got)
	}
	if !strings.Contains(got, "items:") {
		t.Errorf("the yaml has no block mapping: %q", got)
	}
}

// And the other direction: yaml is not json, so a consumer running `jq` on it
// fails rather than getting a wrong answer.
func TestJSONIsNotYAML(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := New(&buf, output.JSON).Print(onePage(), Options{}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	got := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(got, "{") {
		t.Errorf("the json is not a json object: %q", got)
	}
}

// Output gets read by people during a bug report as often as by jq.
func TestJSONIsIndented(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	if err := New(&buf, output.JSON).Print(onePage(), Options{}); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if !strings.Contains(buf.String(), "\n  ") {
		t.Errorf("the json is not indented: %q", buf.String())
	}
}

// Anything that is not the data corrupts a parse. A hint, a count, an
// empty-set message — all of it has to vanish under json and yaml.
func TestNoteIsSilentForMachines(t *testing.T) {
	t.Parallel()

	for _, format := range []output.Format{output.JSON, output.YAML} {
		var buf bytes.Buffer

		New(&buf, format).Note("no contexts — `oa context create <name> --server <url>`")

		if buf.Len() != 0 {
			t.Errorf("%s printed a note: %q", format, buf.String())
		}
	}
}

func TestNoteReachesAPerson(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	New(&buf, output.Table).Note("no contexts")

	if !strings.Contains(buf.String(), "no contexts") {
		t.Errorf("the note never reached the table writer: %q", buf.String())
	}
}

// A Note before a Print must not end up inside the document. This is the
// ordering that breaks a consumer: the hint lands first, and `jq` sees a
// leading line of prose.
func TestANoteNeverPrecedesMachineOutput(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printer := New(&buf, output.JSON)

	printer.Note("showing the first page")
	if err := printer.Print(onePage(), Options{}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var got page
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("a note corrupted the document: %v\n%s", err, buf.String())
	}
}

type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// `oa teams list -o json | head` closes the pipe early. Print is what the
// command returns, so a failed write has to arrive as an error rather than as
// an exit code of zero and no output.
func TestPrintReportsAWriteFailure(t *testing.T) {
	t.Parallel()

	for _, format := range output.All() {
		err := New(brokenPipe{}, format).Print(onePage(), Options{Table: func(table *Table) {
			table.Row("local")
		}})
		if err == nil {
			t.Errorf("%s reported success writing to a closed pipe", format)
		}
	}
}

// A output.Format that reached here unparsed means something skipped Parse. Falling
// back to the table keeps the command usable rather than printing nothing.
func TestAnUnknownFormatFallsBackToTheTable(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	ran := false

	err := New(&buf, output.Format("wide")).Print(onePage(), Options{Table: func(*Table) { ran = true }})
	if err != nil {
		t.Fatalf("Print: %v", err)
	}
	if !ran {
		t.Error("an unknown format printed nothing at all")
	}
}

// Every printer writes to the writer it was given. One reaching for os.Stdout
// would be invisible in tests and would corrupt a redirect in production.
func TestEveryFormatWritesToItsOwnWriter(t *testing.T) {
	t.Parallel()

	for _, format := range output.All() {
		var buf bytes.Buffer

		err := New(&buf, format).Print(onePage(), Options{Table: func(table *Table) {
			table.Row("local")
		}})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if buf.Len() == 0 {
			t.Errorf("%s wrote nothing to the writer it was given", format)
		}
	}
}

// The table pads columns; the machine formats must not, or a value gains
// trailing spaces that were never in the data.
func TestMachineFormatsDoNotPad(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	if err := New(&buf, output.JSON).Print(row{Name: "local", Server: "s"}, Options{}); err != nil {
		t.Fatalf("Print: %v", err)
	}

	var got row
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if got.Name != "local" {
		t.Errorf("name = %q, want it unpadded", got.Name)
	}
}
