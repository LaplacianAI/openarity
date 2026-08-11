package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type row struct {
	ID   int
	Name string
}

type wire struct {
	Name string `json:"name"`
}

func toWire(r row) wire { return wire{Name: r.Name} }

func cursorOf(r row) any { return r.ID }

func rows(n int) []row {
	out := make([]row, n)
	for i := range out {
		out[i] = row{ID: i, Name: "row-" + strconv.Itoa(i)}
	}
	return out
}

// limitOf drives Limit through a real request, which is the only way it is
// ever called.
func limitOf(t *testing.T, query string) (int32, *httptest.ResponseRecorder) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/things"+query, nil)
	rec := httptest.NewRecorder()

	n, ok := Limit(rec, req)
	if !ok {
		return 0, rec
	}
	return n, rec
}

// --- Limit ---------------------------------------------------------------

func TestLimitDefaultsWhenAbsent(t *testing.T) {
	t.Parallel()

	for _, query := range []string{"", "?", "?limit=", "?other=3"} {
		n, rec := limitOf(t, query)
		if rec.Code != http.StatusOK {
			t.Errorf("%q was rejected: %s", query, rec.Body)
		}
		if n != DefaultLimit {
			t.Errorf("%q gave %d, want the default %d", query, n, DefaultLimit)
		}
	}
}

func TestLimitReadsAValidValue(t *testing.T) {
	t.Parallel()

	// "+5" is a valid signed integer and ParseInt takes it. Refusing it would
	// be code written to reject something harmless.
	for _, query := range []string{"?limit=7", "?limit=%2B7", "?limit=07"} {
		n, rec := limitOf(t, query)
		if rec.Code != http.StatusOK {
			t.Errorf("%q was rejected: %s", query, rec.Body)
		}
		if n != 7 {
			t.Errorf("%q gave %d, want 7", query, n)
		}
	}
}

// A client guessing high gets the maximum rather than an error. AIP-158 makes
// this explicit, and it is what Stripe and Kubernetes do.
func TestLimitClampsRatherThanRejects(t *testing.T) {
	t.Parallel()

	for _, query := range []string{"?limit=101", "?limit=1000", "?limit=2147483647"} {
		n, rec := limitOf(t, query)
		if rec.Code != http.StatusOK {
			t.Errorf("%q was rejected, want a clamp: %s", query, rec.Body)
		}
		if n != MaxLimit {
			t.Errorf("%q gave %d, want %d", query, n, MaxLimit)
		}
	}
}

// A limit that cannot mean anything is a client bug. Zero and negatives are
// included because "give me nothing" is almost certainly a computed value gone
// wrong, not an intent.
func TestLimitRejectsAValueItCannotUse(t *testing.T) {
	t.Parallel()

	for _, query := range []string{
		"?limit=0",
		"?limit=-1",
		"?limit=abc",
		"?limit=1.5",
		"?limit=%205", // a leading space, as a real client would encode it
		"?limit=5x",
		"?limit=9999999999",           // past int32
		"?limit=99999999999999999999", // past int64 as well
	} {
		n, rec := limitOf(t, query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q gave %d, want 400", query, rec.Code)
		}
		if n != 0 {
			t.Errorf("%q returned %d alongside a refusal", query, n)
		}
	}
}

// The bound is a real one. A maximum above int32 would overflow the Postgres
// LIMIT parameter it becomes.
func TestLimitsAreSane(t *testing.T) {
	t.Parallel()

	if DefaultLimit < 1 {
		t.Error("DefaultLimit is not positive, so a caller that says nothing gets nothing")
	}
	if MaxLimit < DefaultLimit {
		t.Errorf("MaxLimit %d is below DefaultLimit %d, so the default is unreachable", MaxLimit, DefaultLimit)
	}
}

// --- cursors -------------------------------------------------------------

func TestCursorRoundTrips(t *testing.T) {
	t.Parallel()

	type position struct {
		Subject string `json:"s"`
		N       int    `json:"n"`
	}
	want := position{Subject: "alice", N: 42}

	encoded, err := EncodeCursor(want)
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}

	var got position
	if !DecodeCursor(httptest.NewRecorder(), encoded, &got) {
		t.Fatal("DecodeCursor rejected a cursor this package produced")
	}
	if got != want {
		t.Errorf("round trip gave %+v, want %+v", got, want)
	}
}

// A cursor travels in a query string, so it must survive one without escaping.
// Standard base64 uses + and /, both of which change meaning in a URL.
func TestCursorIsURLSafe(t *testing.T) {
	t.Parallel()

	// Bytes chosen to produce + and / under standard base64.
	encoded, err := EncodeCursor(map[string]string{"k": "\xff\xfe\xfd\xfc\xfb\xfa"})
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}

	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("cursor %q carries a character that a query string changes", encoded)
	}
}

// A value that cannot be marshalled is a programming error, and it has to
// surface as an error rather than an empty cursor that silently restarts
// paging from the top.
func TestEncodeCursorReportsAnUnmarshallableValue(t *testing.T) {
	t.Parallel()

	encoded, err := EncodeCursor(make(chan int))
	if err == nil {
		t.Fatal("EncodeCursor accepted a channel")
	}
	if encoded != "" {
		t.Errorf("EncodeCursor returned %q alongside an error", encoded)
	}
}

func TestDecodeCursorRejectsWhatItDidNotIssue(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"not base64":         "!!!not-base64!!!",
		"standard padding":   "eyJhIjoxfQ==",
		"base64 but garbage": "aGVsbG8gd29ybGQ",
		"a json array":       "WzEsMiwzXQ",
		"a bare number":      "NDI",
		"empty":              "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			var into struct {
				A int `json:"a"`
			}

			if DecodeCursor(rec, raw, &into) {
				t.Fatalf("accepted %q", raw)
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// base64 decodes in four-character groups and returns the bytes it managed
// before the bad character. Appending garbage on a group boundary therefore
// yields complete, valid JSON — so the base64 error cannot be ignored on the
// grounds that the JSON step will catch it. It does not.
func TestDecodeCursorRejectsGarbageOnAGroupBoundary(t *testing.T) {
	t.Parallel()

	// Nine bytes is twelve base64 characters, a whole number of groups.
	valid, err := EncodeCursor(map[string]int{"a": 123})
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}
	if len(valid)%4 != 0 {
		t.Fatalf("cursor %q is not a whole number of base64 groups, so this test proves nothing", valid)
	}

	rec := httptest.NewRecorder()
	var into map[string]int

	if DecodeCursor(rec, valid+"!!!!", &into) {
		t.Fatalf("accepted a cursor with garbage appended, decoded to %v", into)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// A cursor is opaque, not secret. This records that on purpose: it carries a
// position, never authority, and every request re-checks access.
func TestCursorCarriesOnlyAPosition(t *testing.T) {
	t.Parallel()

	encoded, err := EncodeCursor(map[string]string{"s": "alice"})
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}

	var decoded map[string]string
	if !DecodeCursor(httptest.NewRecorder(), encoded, &decoded) {
		t.Fatal("could not decode")
	}
	if len(decoded) != 1 || decoded["s"] != "alice" {
		t.Errorf("cursor decoded to %v", decoded)
	}
}

// --- MapPage -------------------------------------------------------------

// Fewer rows than the limit is the last page, and the last page carries no
// cursor — its absence is the only end-of-collection signal.
func TestMapPageOmitsTheCursorOnAShortPage(t *testing.T) {
	t.Parallel()

	page, err := MapPage(rows(3), 5, cursorOf, toWire)
	if err != nil {
		t.Fatalf("MapPage: %v", err)
	}
	if len(page.Items) != 3 {
		t.Errorf("got %d items, want 3", len(page.Items))
	}
	if page.NextCursor != nil {
		t.Errorf("a short page carries cursor %q", *page.NextCursor)
	}
}

// Exactly limit rows is also the last page. Querying limit+1 is what makes
// this distinguishable, and an off-by-one here emits a cursor to nowhere.
func TestMapPageOmitsTheCursorOnAnExactlyFullPage(t *testing.T) {
	t.Parallel()

	page, err := MapPage(rows(5), 5, cursorOf, toWire)
	if err != nil {
		t.Fatalf("MapPage: %v", err)
	}
	if len(page.Items) != 5 {
		t.Errorf("got %d items, want 5", len(page.Items))
	}
	if page.NextCursor != nil {
		t.Errorf("an exactly full page carries cursor %q", *page.NextCursor)
	}
}

// The sentinel row is dropped, not served, and the cursor points at the last
// row the client actually received.
func TestMapPageDropsTheSentinelAndPointsAtTheLastKeptRow(t *testing.T) {
	t.Parallel()

	page, err := MapPage(rows(6), 5, cursorOf, toWire)
	if err != nil {
		t.Fatalf("MapPage: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("got %d items, want 5 — the sentinel was served", len(page.Items))
	}
	if page.Items[4].Name != "row-4" {
		t.Errorf("last item is %q, want row-4", page.Items[4].Name)
	}
	if page.NextCursor == nil {
		t.Fatal("a full page with more behind it carries no cursor")
	}

	var got int
	if !DecodeCursor(httptest.NewRecorder(), *page.NextCursor, &got) {
		t.Fatal("the cursor it produced cannot be decoded")
	}
	if got != 4 {
		t.Errorf("cursor points at row %d, want 4 — the last row the client saw", got)
	}
}

// An empty result is an answer. Serialising it as null makes a client that
// iterates the items crash on something that is not an error.
func TestMapPageSerialisesEmptyAsAnArray(t *testing.T) {
	t.Parallel()

	for name, in := range map[string][]row{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			page, err := MapPage(in, 5, cursorOf, toWire)
			if err != nil {
				t.Fatalf("MapPage: %v", err)
			}

			body, err := json.Marshal(page)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(body) != `{"items":[]}` {
				t.Errorf("body = %s, want {\"items\":[]}", body)
			}
		})
	}
}

// The conversion runs on every row it keeps and none it drops.
func TestMapPageConvertsEveryKeptRow(t *testing.T) {
	t.Parallel()

	var converted int
	page, err := MapPage(rows(6), 5, cursorOf, func(r row) wire {
		converted++
		return toWire(r)
	})
	if err != nil {
		t.Fatalf("MapPage: %v", err)
	}

	if converted != 5 {
		t.Errorf("converted %d rows, want 5", converted)
	}
	for i, item := range page.Items {
		if want := "row-" + strconv.Itoa(i); item.Name != want {
			t.Errorf("item %d is %q, want %q", i, item.Name, want)
		}
	}
}

// A cursor that cannot be built must fail the request rather than silently
// return a page that claims to be the last one.
func TestMapPageReportsACursorFailure(t *testing.T) {
	t.Parallel()

	page, err := MapPage(rows(6), 5, func(row) any { return make(chan int) }, toWire)
	if err == nil {
		t.Fatal("MapPage succeeded with a cursor it could not encode")
	}
	if page.Items != nil {
		t.Errorf("MapPage returned %d items alongside an error", len(page.Items))
	}
}

// A limit of zero trims every row, which leaves no last row to build a cursor
// from. Limit refuses zero so no handler reaches this, but MapPage is exported
// and it used to index rows[-1] and panic — in a handler that takes the
// connection down.
func TestMapPageWithAZeroLimitKeepsNothing(t *testing.T) {
	t.Parallel()

	page, err := MapPage(rows(3), 0, cursorOf, toWire)
	if err != nil {
		t.Fatalf("MapPage: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("got %d items for a zero limit, want none", len(page.Items))
	}
	if page.NextCursor != nil {
		t.Errorf("cursor = %q, but no row was kept to point at", *page.NextCursor)
	}
}
