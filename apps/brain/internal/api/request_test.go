package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type createRequest struct {
	Name string `json:"name"`
}

// decode runs one body through DecodeJSON and returns what the handler would
// see: the decoded value, whether to continue, and the response so far.
func decode(t *testing.T, body string) (createRequest, bool, *httptest.ResponseRecorder) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/teams", strings.NewReader(body))
	rec := httptest.NewRecorder()

	var got createRequest
	ok := DecodeJSON(rec, req, &got)
	return got, ok, rec
}

func TestDecodeJSONReadsAValidBody(t *testing.T) {
	t.Parallel()

	got, ok, rec := decode(t, `{"name":"platform"}`)
	if !ok {
		t.Fatalf("a valid body was rejected: %s", rec.Body)
	}
	if got.Name != "platform" {
		t.Errorf("Name = %q, want platform", got.Name)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the handler to still own the response", rec.Code)
	}
}

// A client sending a field the server does not know has a bug — usually a
// misspelling. Ignoring it silently means they see a team created with an empty
// name and no idea why.
func TestDecodeJSONRejectsAnUnknownField(t *testing.T) {
	t.Parallel()

	_, ok, rec := decode(t, `{"name":"platform","extra":"typo"}`)
	if ok {
		t.Fatal("an unknown field was accepted")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDecodeJSONRejectsMalformedBodies(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"empty":          "",
		"not json":       "hello",
		"truncated":      `{"name":`,
		"array":          `["platform"]`,
		"wrong type":     `{"name":42}`,
		"null":           `null`,
		"just a string":  `"platform"`,
		"unclosed array": `[`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, ok, rec := decode(t, body)
			if ok && name != "null" {
				t.Errorf("accepted %q", body)
			}
			if !ok && rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// A body can carry a password. The reply names the problem and never echoes
// the payload back.
func TestDecodeJSONDoesNotEchoTheBody(t *testing.T) {
	t.Parallel()

	_, ok, rec := decode(t, `{"password":"hunter2"}`)
	if ok {
		t.Fatal("an unknown field was accepted")
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("the reply echoed the request body: %s", rec.Body)
	}
}

// The trailing-value branch has the decoder error in scope too, and the second
// Decode runs with DisallowUnknownFields still set — so echoing it would leak
// the field names of whatever followed the first object.
func TestDecodeJSONDoesNotEchoATrailingValue(t *testing.T) {
	t.Parallel()

	_, ok, rec := decode(t, `{"name":"platform"}{"password":"hunter2"}`)
	if ok {
		t.Fatal("a trailing object was accepted")
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("the reply echoed the trailing value: %s", rec.Body)
	}
}

// Without a limit a client can stream indefinitely into Decode and hold a
// connection and a goroutine for as long as it likes.
func TestDecodeJSONRefusesAnOversizedBody(t *testing.T) {
	t.Parallel()

	huge := `{"name":"` + strings.Repeat("a", maxBodyBytes+1) + `"}`

	_, ok, rec := decode(t, huge)
	if ok {
		t.Fatal("a body over the limit was accepted")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// A body just under the limit is legal and must still decode.
func TestDecodeJSONAcceptsABodyUnderTheLimit(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("a", 1000)
	got, ok, rec := decode(t, `{"name":"`+name+`"}`)

	if !ok {
		t.Fatalf("a body well under the limit was rejected: %s", rec.Body)
	}
	if got.Name != name {
		t.Errorf("Name was truncated: %d bytes, want %d", len(got.Name), len(name))
	}
}

// The limit has to be a real bound, not a number that happens to be large.
func TestMaxBodyBytesIsBounded(t *testing.T) {
	t.Parallel()

	if maxBodyBytes <= 0 {
		t.Fatal("maxBodyBytes is not set, so a body is unbounded")
	}
	if maxBodyBytes > 10<<20 {
		t.Errorf("maxBodyBytes is %d — large enough that a few concurrent requests are a memory problem", maxBodyBytes)
	}
}

// DecodeJSON answers the client itself, so a handler that continues would send
// a second, conflicting reply. The bool is the whole contract.
func TestDecodeJSONAnswersTheClientOnFailure(t *testing.T) {
	t.Parallel()

	_, ok, rec := decode(t, "garbage")
	if ok {
		t.Fatal("garbage was accepted")
	}
	if rec.Body.Len() == 0 {
		t.Error("DecodeJSON returned false without answering the client")
	}
}

// Decode reads only the first value, so a body with two objects would apply
// the first and discard the second in silence. A client with a serialisation
// bug would get a 200 and half of what they sent.
func TestDecodeJSONRejectsMoreThanOneObject(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"two objects":         `{"name":"first"}{"name":"second"}`,
		"object then garbage": `{"name":"first"}garbage`,
		"object then array":   `{"name":"first"}[]`,
		// This one decodes cleanly, so the guard sees a nil error rather than a
		// failure. It is the case that catches a check written as err != nil.
		"object then empty object": `{"name":"first"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, ok, rec := decode(t, body)
			if ok {
				t.Fatalf("accepted %q", body)
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// Trailing whitespace is not a second value. A client that appends a newline
// is not making a mistake.
func TestDecodeJSONAcceptsTrailingWhitespace(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{"name":"platform"}` + "\n",
		`{"name":"platform"}  `,
		`{"name":"platform"}` + "\r\n",
	} {
		got, ok, rec := decode(t, body)
		if !ok {
			t.Errorf("rejected %q: %s", body, rec.Body)
		}
		if got.Name != "platform" {
			t.Errorf("Name = %q", got.Name)
		}
	}
}

// The two failures are different client bugs, so they say different things.
func TestDecodeJSONDistinguishesItsTwoFailures(t *testing.T) {
	t.Parallel()

	_, _, malformed := decode(t, "garbage")
	_, _, trailing := decode(t, `{"name":"a"}{"name":"b"}`)

	if malformed.Body.String() == trailing.Body.String() {
		t.Errorf("both failures report %q", malformed.Body)
	}
}
