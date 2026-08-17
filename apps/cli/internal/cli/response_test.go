package cli

import (
	"errors"
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

// Two different things arrive as 404 and they need different answers. The
// router answers an unrouted path with exactly "404 page not found", which
// means the endpoint is not there — the shape of `oa` being newer than the
// brain it is talking to, after a CLI upgrade or before a deployment.
//
// Reported as "not found, or not visible to you" it reads as a permission
// problem, and the next hour goes on roles and tokens.
func TestAMissingRouteIsNotReportedAsAMissingRecord(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusNotFound, []byte("404 page not found\n"))
	if err == nil {
		t.Fatal("a 404 produced no error")
	}
	if strings.Contains(err.Error(), "visible to you") {
		t.Errorf("a missing endpoint was reported as a permission problem: %v", err)
	}
	if !strings.Contains(err.Error(), "older") {
		t.Errorf("the message does not say the brain may be behind: %v", err)
	}
}

// The brain's own 404s name what was not found — "no user has that subject"
// is the whole answer to `oa users list somebody`. It was being discarded for
// a fixed sentence that says less.
func TestTheBrainsOwn404SentenceIsKept(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusNotFound, []byte("no user has that subject\n"))
	if err == nil {
		t.Fatal("a 404 produced no error")
	}
	if !strings.Contains(err.Error(), "no user has that subject") {
		t.Errorf("the brain's sentence was dropped: %v", err)
	}
}

// The bare "not found" the brain sends for a team or a membership says
// nothing the generic sentence does not, and the generic one adds the half
// that matters: it may be there and not yours to see.
func TestAGeneric404StillMentionsVisibility(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"not found\n", ""} {
		err := APIError(http.StatusNotFound, []byte(body))
		if err == nil {
			t.Fatalf("a 404 with body %q produced no error", body)
		}
		if !strings.Contains(err.Error(), "visible to you") {
			t.Errorf("body %q lost the visibility half: %v", body, err)
		}
		if strings.Contains(err.Error(), "older") {
			t.Errorf("body %q was mistaken for a missing endpoint: %v", body, err)
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

// fakeResponse stands in for a generated *WithResponse type. The generated
// code fills a different field per status, which is why there are three
// unwrappers rather than one.
type fakeResponse struct {
	status  int
	body    []byte
	json200 *string
	json201 *string
}

func (f *fakeResponse) StatusCode() int     { return f.status }
func (f *fakeResponse) GetBody() []byte     { return f.body }
func (f *fakeResponse) GetJSON200() *string { return f.json200 }
func (f *fakeResponse) GetJSON201() *string { return f.json201 }

// A transport failure is returned untouched. Wrapping it in an API error would
// blame the brain for a DNS failure or a closed VPN.
func TestTheUnwrappersPassATransportErrorThrough(t *testing.T) {
	t.Parallel()

	boom := errors.New("dial tcp: connection refused")

	if _, err := Result[string](&fakeResponse{}, boom); !errors.Is(err, boom) {
		t.Errorf("Result rewrote the transport error: %v", err)
	}
	if _, err := Created[string](&fakeResponse{}, boom); !errors.Is(err, boom) {
		t.Errorf("Created rewrote the transport error: %v", err)
	}
	if err := NoContent(&fakeResponse{}, boom); !errors.Is(err, boom) {
		t.Errorf("NoContent rewrote the transport error: %v", err)
	}
}

// A 201 fills GetJSON201 and leaves GetJSON200 nil. Unwrapping a create with
// Result would therefore report a successful creation as a failure.
func TestCreatedReadsThe201Body(t *testing.T) {
	t.Parallel()

	body := "the-new-team"
	got, err := Created[string](&fakeResponse{status: http.StatusCreated, json201: &body}, nil)
	if err != nil {
		t.Fatalf("Created: %v", err)
	}
	if got == nil || *got != body {
		t.Errorf("got %v, want the body", got)
	}
}

// The generated type for a 201 has no JSON200 field to fall back on, so a
// status that is not 201 has to become an error rather than a nil body the
// caller dereferences.
func TestCreatedTurnsAnythingElseIntoAnError(t *testing.T) {
	t.Parallel()

	_, err := Created[string](&fakeResponse{status: http.StatusConflict, body: []byte("already exists")}, nil)
	if err == nil {
		t.Fatal("a 409 was unwrapped as a created resource")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the brain's reason was dropped: %v", err)
	}
}

// 204 has no body at all, so its status is the only thing that says it worked.
func TestNoContentAcceptsOnly204(t *testing.T) {
	t.Parallel()

	if err := NoContent(&fakeResponse{status: http.StatusNoContent}, nil); err != nil {
		t.Fatalf("a 204 was reported as a failure: %v", err)
	}

	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusCreated} {
		if err := NoContent(&fakeResponse{status: status}, nil); err == nil {
			t.Errorf("status %d was accepted as a completed write", status)
		}
	}
}

// A 500 names the status, because the caller can do nothing about it and the
// number is what they will quote in a bug report.
func TestAServerErrorCarriesTheStatus(t *testing.T) {
	t.Parallel()

	withBody := APIError(http.StatusInternalServerError, []byte("boom"))
	if !strings.Contains(withBody.Error(), "500") || !strings.Contains(withBody.Error(), "boom") {
		t.Errorf("the error drops the status or the detail: %v", withBody)
	}

	bare := APIError(http.StatusBadGateway, nil)
	if !strings.Contains(bare.Error(), "502") {
		t.Errorf("a 500 with no body lost its status: %v", bare)
	}
}

// An unrecognised status with no body still has to say something. An empty
// error message reads as a bug in the CLI.
func TestAnUnknownStatusWithNoBodyStillExplains(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusTeapot, nil)
	if err == nil || err.Error() == "" {
		t.Fatalf("a 418 with no body produced %v", err)
	}
	if !strings.Contains(err.Error(), "418") {
		t.Errorf("the status is not in the message: %v", err)
	}
}

// A body long enough to fill a terminal is truncated, because an error is a
// sentence and a stack trace pasted into one is unreadable.
func TestALongBodyIsSummarised(t *testing.T) {
	t.Parallel()

	err := APIError(http.StatusBadRequest, []byte(strings.Repeat("x", maxBodyInAnError*3)))
	if len(err.Error()) > maxBodyInAnError+10 {
		t.Errorf("the body was not truncated: %d characters", len(err.Error()))
	}
	if !strings.HasSuffix(err.Error(), "…") {
		t.Errorf("truncation is not marked: %q", err.Error())
	}
}
