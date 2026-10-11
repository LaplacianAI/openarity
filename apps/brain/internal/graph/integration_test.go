package graph

import (
	"math"
	"os"
	"slices"
	"testing"

	falkordb "github.com/FalkorDB/falkordb-go/v2"
	"github.com/google/uuid"
)

// liveFalkorURL returns the address of a real FalkorDB, or skips. CI sets
// BRAIN_TEST_FALKOR_URL from its service container.
func liveFalkorURL(t *testing.T) string {
	t.Helper()

	url := os.Getenv("BRAIN_TEST_FALKOR_URL")
	if url == "" {
		t.Skip("BRAIN_TEST_FALKOR_URL is not set")
	}
	return url
}

// scratchGraph is a graph of its own for one test, deleted afterwards, so
// tests cannot see each other's nodes and may run in parallel.
func scratchGraph(t *testing.T) *falkordb.Graph {
	t.Helper()

	db, err := falkordb.FromURL(liveFalkorURL(t))
	if err != nil {
		t.Fatalf("connect to FalkorDB: %v", err)
	}
	g := db.SelectGraph("test:" + uuid.NewString())
	t.Cleanup(func() {
		_ = g.Delete()
		_ = db.Conn.Close()
	})
	return g
}

// returned runs RETURN $v with the header this package writes, through the
// client the brain uses, and gives back what the database understood.
func returned(t *testing.T, g *falkordb.Graph, value any) any {
	t.Helper()

	head, err := header(map[string]any{"v": value})
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	res, err := g.Query(head+"RETURN $v", nil, nil)
	if err != nil {
		t.Fatalf("query with %q: %v", head, err)
	}
	if !res.Next() {
		t.Fatalf("query with %q returned no row", head)
	}
	got, err := res.Record().GetByIndex(0)
	if err != nil {
		t.Fatalf("read the column: %v", err)
	}
	return got
}

// Every string is stored as it was written. The library's own parameter
// writing fails eight of these: it quotes with Go's strconv.Quote, and a
// non-breaking space arrives as the six characters \u00a0.
func TestEveryStringSurvivesTheDatabase(t *testing.T) {
	t.Parallel()

	g := scratchGraph(t)
	for _, s := range []string{
		`plain`, `it's`, `say "hi"`, `back\slash`, `trailing\`, `\"`, `a\\" b`,
		"new\nline", "cr\rlf\n", "tab\there", "bell\x07", "ctrl\x01\x1f", "del\x7f",
		"é ü 漢字 🙂", "sep\u2028here", "nbsp\u00a0here", "bom\ufeffhere",
		`" RETURN 1 //`, `\" injection`, `$notparam`, "`backtick`",
		`\n two characters`, `A six characters`, "", " lead and trail ",
		"'; MATCH (n) DETACH DELETE n; //", `"}) DETACH DELETE n //`,
	} {
		if got := returned(t, g, s); got != s {
			t.Errorf("%q came back as %q", s, got)
		}
	}
}

// A string that tries to end its literal and run a second statement is just
// a string: the node it would have deleted is still there.
func TestAStringCannotBecomeAStatement(t *testing.T) {
	t.Parallel()

	g := scratchGraph(t)
	if _, err := g.Query(`CREATE (:Canary {id: "alive"})`, nil, nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, attack := range []string{
		`" MATCH (n) DETACH DELETE n RETURN "`,
		`\" MATCH (n) DETACH DELETE n RETURN \"`,
		"\" \nMATCH (n) DETACH DELETE n //",
		`x" v="y`,
	} {
		if got := returned(t, g, attack); got != attack {
			t.Errorf("%q came back as %q", attack, got)
		}
	}

	res, err := g.Query(`MATCH (n:Canary) RETURN count(n)`, nil, nil)
	if err != nil || !res.Next() {
		t.Fatalf("count: %v", err)
	}
	if n, _ := res.Record().GetByIndex(0); n != int64(1) {
		t.Errorf("the canary count is %v, want 1", n)
	}
}

// A vector is stored exactly: every float32, including the extremes and the
// small values a six-decimal display would round to zero.
func TestAVectorIsStoredExactly(t *testing.T) {
	t.Parallel()

	g := scratchGraph(t)
	want := []float32{0, 1, -1, 0.1, 1e-7, 1e6, 1e20, math.MaxFloat32, math.SmallestNonzeroFloat32, 1.1754944e-38, -0.5, 123456.79}

	head, err := header(map[string]any{"v": want})
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := g.Query(head+`CREATE (:N {id: "a", embedding: vecf32($v)})`, nil, nil); err != nil {
		t.Fatalf("store: %v", err)
	}

	res, err := g.Query(`MATCH (n:N) RETURN n.embedding`, nil, nil)
	if err != nil || !res.Next() {
		t.Fatalf("read: %v", err)
	}
	raw, _ := res.Record().GetByIndex(0)
	got, ok := raw.([]float32)
	if !ok {
		t.Fatalf("the vector came back as a %T", raw)
	}
	if !slices.Equal(got, want) {
		t.Errorf("vector = %v, want %v", got, want)
	}
}

func TestEachTypeArrivesAsItself(t *testing.T) {
	t.Parallel()

	g := scratchGraph(t)
	for name, tc := range map[string]struct{ in, want any }{
		"null":              {nil, nil},
		"true":              {true, true},
		"false":             {false, false},
		"an int":            {42, int64(42)},
		"an int32":          {int32(-7), int64(-7)},
		"the largest int64": {int64(math.MaxInt64), int64(math.MaxInt64)},
		"the least int64":   {int64(math.MinInt64), int64(math.MinInt64)},
		"a float64":         {1.5, 1.5},
		"a small float64":   {1e-9, 1e-9},
	} {
		if got := returned(t, g, tc.in); got != tc.want {
			t.Errorf("%s: %#v came back as %#v", name, tc.in, got)
		}
	}

	list, ok := returned(t, g, []string{"a", `b"c`, ""}).([]any)
	if !ok || len(list) != 3 || list[0] != "a" || list[1] != `b"c` || list[2] != "" {
		t.Errorf("a list of strings came back as %#v", list)
	}
}

// A map is usable as properties, and a list of maps as rows to UNWIND — the
// two shapes the graph client sends.
func TestMapsAndListsOfMapsAreUsableInAQuery(t *testing.T) {
	t.Parallel()

	g := scratchGraph(t)
	head, err := header(map[string]any{
		"id":    "n1",
		"props": map[string]any{"name": "pdf", "description": "Extract \"text\"\u00a0from PDFs", "kind": nil},
		"rows":  []map[string]any{{"to": "a", "allow": []string{"x", "y"}}, {"to": "b"}},
	})
	if err != nil {
		t.Fatalf("header: %v", err)
	}

	res, err := g.Query(head+`MERGE (n:Skill {id: $id}) SET n += $props
		WITH n UNWIND $rows AS row
		RETURN n.name, n.description, n.kind, row.to, size(coalesce(row.allow, []))
		ORDER BY row.to`, nil, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	var rows [][]any
	for res.Next() {
		rows = append(rows, res.Record().Values())
	}
	want := [][]any{
		{"pdf", "Extract \"text\"\u00a0from PDFs", nil, "a", int64(2)},
		{"pdf", "Extract \"text\"\u00a0from PDFs", nil, "b", int64(0)},
	}
	if len(rows) != 2 || !slices.Equal(rows[0], want[0]) || !slices.Equal(rows[1], want[1]) {
		t.Errorf("rows = %#v, want %#v", rows, want)
	}
}
