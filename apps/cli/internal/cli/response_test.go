package cli

import (
	"net/http"
	"strings"
	"testing"
)

// A 401 is the one failure with an action attached. Everything else the user
// can only report; this one they can fix, so the message has to say how.
func TestUnauthenticatedNamesTheCommandThatFixesIt(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusUnauthorized, []byte("unauthorized"))

	if err == nil {
		t.Fatal("a 401 produced no error")
	}
	if !strings.Contains(err.Error(), "oa login") {
		t.Errorf("the message does not name the fix: %v", err)
	}
}

// A 403 means the token is fine and the caller is not allowed. Telling them
// to log in again sends them round a loop that cannot help — they need
// someone to grant them a role.
func TestForbiddenDoesNotAskForAnotherLogin(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusForbidden, []byte("forbidden"))

	if err == nil {
		t.Fatal("a 403 produced no error")
	}
	if strings.Contains(err.Error(), "oa login") {
		t.Errorf("a 403 told the user to log in again: %v", err)
	}
}

// The brain answers 400 and 409 with a sentence naming the problem — "unknown
// role", "a team with that name already exists". Dropping it for a generic
// message throws away the only useful part of the response.
func TestTheServersSentenceIsKept(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusBadRequest, "id must be a uuid"},
		{http.StatusConflict, "a team with that name already exists"},
	} {
		err := APIError(tc.status, []byte(tc.body))
		if err == nil {
			t.Fatalf("%d produced no error", tc.status)
		}
		if !strings.Contains(err.Error(), tc.body) {
			t.Errorf("%d dropped the server's sentence %q: %v", tc.status, tc.body, err)
		}
	}
}

// An unexpected status has no sentence worth trusting, so the status itself
// is what identifies it in a bug report.
func TestAnUnexpectedStatusIsNamed(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusInternalServerError, []byte("internal server error"))

	if err == nil {
		t.Fatal("a 500 produced no error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the message does not carry the status: %v", err)
	}
}

// Point the CLI at the wrong port and the body is a whole HTML page. Printing
// it fills the terminal and buries the one line that matters.
func TestALongBodyIsTruncated(t *testing.T) {
	t.Parallel()

	page := "<!doctype html><html><head><title>nginx</title></head><body>" +
		strings.Repeat("x", 4000) + "</body></html>"

	err := APIError(http.StatusInternalServerError, []byte(page))

	if err == nil {
		t.Fatal("no error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("the message is %d bytes — a whole page reached the terminal", len(err.Error()))
	}
}

// A multi-line body must not become a multi-line error. `oa: ` is printed as
// a prefix, and the second line would lose it.
func TestNewlinesAreFlattened(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusBadRequest, []byte("first line\nsecond line\n"))

	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("the message spans lines: %q", err.Error())
	}
}

// An empty body is normal for a 404 — the brain answers some failures with a
// status and nothing else. The message still has to say something.
func TestAnEmptyBodyStillProducesAMessage(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusNotFound, nil)

	if err == nil {
		t.Fatal("no error")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("the message is blank")
	}
}

// Nothing calls this with a 2xx, and if something starts to, an error that
// says "success" is worse than a loud one.
func TestASuccessfulStatusIsReportedAsAProgrammingError(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusOK, []byte("ok"))

	if err == nil {
		t.Fatal("apiError returned nil for a 200 — the caller would report success on an unparsed body")
	}
}
