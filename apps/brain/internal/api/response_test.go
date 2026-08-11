package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func recordingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

type payload struct {
	Name string `json:"name"`
}

func TestWriteJSONSendsTheBodyAndStatus(t *testing.T) {
	t.Parallel()

	logger, _ := recordingLogger()
	rec := httptest.NewRecorder()

	WriteJSON(rec, logger, http.StatusCreated, payload{Name: "thing"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}

	var got payload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON (%v): %s", err, rec.Body)
	}
	if got.Name != "thing" {
		t.Errorf("body = %+v", got)
	}
}

// The header must be set before the status. Afterwards the headers are already
// on the wire and the Set silently does nothing — and Result() is the only
// place that shows it, because Header() returns the live map the handler wrote
// to rather than what was sent.
func TestWriteJSONSetsContentTypeBeforeTheStatus(t *testing.T) {
	t.Parallel()

	logger, _ := recordingLogger()
	rec := httptest.NewRecorder()

	WriteJSON(rec, logger, http.StatusOK, payload{Name: "thing"})

	got := rec.Result().Header.Get("Content-Type") //nolint:bodyclose // ResponseRecorder needs no close
	if !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// failingWriter accepts headers and the status, then fails the body write —
// the shape of a client that disconnects mid-response.
type failingWriter struct {
	header http.Header
	status int
}

func (f *failingWriter) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}
func (f *failingWriter) WriteHeader(status int)    { f.status = status }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("connection reset") }

// By the time the body fails the status is already sent, so there is nothing
// to tell the client. It must be logged rather than swallowed, and it must not
// panic — a panic here drops the connection with no reply at all.
func TestWriteJSONLogsAFailedBodyWrite(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	w := &failingWriter{}

	WriteJSON(w, logger, http.StatusOK, payload{Name: "thing"})

	if w.status != http.StatusOK {
		t.Errorf("status = %d, want 200 — it is written before the body fails", w.status)
	}
	if !strings.Contains(buf.String(), "connection reset") {
		t.Errorf("the write failure was not logged: %s", buf)
	}
}

// A value json cannot encode fails after the status is on the wire, exactly
// like a broken connection. Same requirement: log it, do not panic.
func TestWriteJSONLogsAnUnencodableValue(t *testing.T) {
	t.Parallel()

	logger, buf := recordingLogger()
	rec := httptest.NewRecorder()

	WriteJSON(rec, logger, http.StatusOK, map[string]any{"bad": math.Inf(1)})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if buf.Len() == 0 {
		t.Error("an unencodable value was written with no log line")
	}
}

// An empty slice must serialise as [] rather than null. The two mean different
// things to a client: "none" versus "not answered".
func TestWriteJSONKeepsAnEmptySliceAsAnArray(t *testing.T) {
	t.Parallel()

	logger, _ := recordingLogger()
	rec := httptest.NewRecorder()

	WriteJSON(rec, logger, http.StatusOK, struct {
		Items []string `json:"items"`
	}{Items: []string{}})

	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body = %s, want an empty array", rec.Body)
	}
}
