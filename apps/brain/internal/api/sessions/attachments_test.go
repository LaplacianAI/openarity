package sessions

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// seedAttachment puts one file in the fixture's session and in its bucket.
func seedAttachment(f *fixture, filename, mediaType string, body []byte) db.Attachment {
	row := db.Attachment{
		ID:         uuid.New(),
		MessageID:  f.store.messages[0].ID,
		SessionID:  f.sessionID,
		ObjectKey:  "teams/" + f.teamID.String() + "/objects/" + uuid.NewString(),
		KeyVersion: 1,
		MediaType:  mediaType,
		SizeBytes:  int64(len(body)),
		Filename:   filename,
		CreatedAt:  time.Unix(1755412345, 0).UTC(),
	}
	f.store.attachments = append(f.store.attachments, row)
	f.objects.bodies[row.ObjectKey] = body
	return row
}

func (f *fixture) attachmentAt(id uuid.UUID) string {
	return f.sessionAttachments() + "/" + id.String()
}

func attachmentPage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[attachment] {
	t.Helper()

	var got api.Page[attachment]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of attachments: %v (%s)", err, rec.Body)
	}
	return got
}

func TestListingASessionsAttachments(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	row := seedAttachment(f, "label.png", "image/png", []byte("PNG"))

	rec := f.get(t, f.sessionAttachments())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	page := attachmentPage(t, rec)
	if len(page.Items) != 1 {
		t.Fatalf("%d items, want 1", len(page.Items))
	}
	got := page.Items[0]
	if got.ID != row.ID || got.MessageID != row.MessageID {
		t.Errorf("item = %+v", got)
	}
	if got.Filename != "label.png" || got.MediaType != "image/png" || got.SizeBytes != 3 {
		t.Errorf("item = %+v", got)
	}

	// The object key is where the bytes live. A caller reaches them through
	// the route below, never by being handed the key.
	if strings.Contains(rec.Body.String(), row.ObjectKey) {
		t.Errorf("the response carries the object key: %s", rec.Body)
	}
}

func TestListingAttachmentsIsScopedToTheSession(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	seedAttachment(f, "label.png", "image/png", []byte("PNG"))

	f.get(t, f.sessionAttachments())

	if len(f.store.attachmentListArgs) != 1 {
		t.Fatalf("%d list calls", len(f.store.attachmentListArgs))
	}
	if got := f.store.attachmentListArgs[0].SessionID; got != f.sessionID {
		t.Errorf("listed session %s, want %s", got, f.sessionID)
	}
}

// The bytes, and everything said about them, come from the row.
func TestGettingAnAttachmentServesWhatWasRecorded(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	body := []byte("\x89PNG\r\n\x1a\nsome bytes")
	row := seedAttachment(f, "label.png", "image/png", body)

	rec := f.get(t, f.attachmentAt(row.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != string(body) {
		t.Errorf("body = %q, want the stored bytes", rec.Body)
	}

	for header, want := range map[string]string{
		"Content-Type":           "image/png",
		"X-Content-Type-Options": "nosniff",
		"Content-Length":         strconv.Itoa(len(body)),
		"Cache-Control":          "private, no-store",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// The recorded type is served exactly, whatever the filename suggests. This is
// the promise the gateway's sniff is only worth anything because of.
func TestTheServedTypeComesFromTheRowAndNotTheFilename(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		filename, mediaType, want string
	}{
		"an svg extension on text":  {"logo.svg", "text/plain; charset=utf-8", "text/plain; charset=utf-8"},
		"an html extension on text": {"page.html", "text/plain; charset=utf-8", "text/plain; charset=utf-8"},
		"a png extension on a pdf":  {"invoice.png", "application/pdf", "application/pdf"},
		"no extension at all":       {"scan", "image/jpeg", "image/jpeg"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			row := seedAttachment(f, tc.filename, tc.mediaType, []byte("bytes"))

			rec := f.get(t, f.attachmentAt(row.ID))
			if got := rec.Header().Get("Content-Type"); got != tc.want {
				t.Errorf("Content-Type = %q, want %q — it followed the filename",
					got, tc.want)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q; without it the browser "+
					"may sniff past the recorded type", got)
			}
		})
	}
}

func TestWhatRendersInPlaceAndWhatDownloads(t *testing.T) {
	t.Parallel()

	for mediaType, want := range map[string]string{
		"image/png":                 "inline",
		"image/jpeg":                "inline",
		"image/gif":                 "inline",
		"image/webp":                "inline",
		"text/plain; charset=utf-8": "inline",

		// A same-origin PDF runs JavaScript in every major viewer, and a zip
		// gains nothing from opening in place.
		"application/pdf": "attachment",
		"application/zip": "attachment",
		"audio/mpeg":      "attachment",

		// Nothing we wrote, so it downloads.
		"application/octet-stream": "attachment",
		"not a media type at all":  "attachment",
	} {
		t.Run(mediaType, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			row := seedAttachment(f, "file.bin", mediaType, []byte("bytes"))

			rec := f.get(t, f.attachmentAt(row.ID))
			got := rec.Header().Get("Content-Disposition")
			if !strings.HasPrefix(got, want) {
				t.Errorf("Content-Disposition = %q, want it to start %q", got, want)
			}
		})
	}
}

func TestTheFilenameSurvivesTheHeader(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ filename, want string }{
		"an ordinary name": {"label.png", `inline; filename=label.png`},
		"a space":          {"my label.png", `inline; filename="my label.png"`},
		"a quote":          {`he said "hi".png`, `inline; filename="he said \"hi\".png"`},
		"a semicolon":      {"a;b.png", `inline; filename="a;b.png"`},
		"non-ascii":        {"报告.png", `inline; filename*=utf-8''%E6%8A%A5%E5%91%8A.png`},

		// Some providers send none, and filename="" is worse than silence.
		"no name at all": {"", "inline"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			row := seedAttachment(f, tc.filename, "image/png", []byte("bytes"))

			rec := f.get(t, f.attachmentAt(row.ID))
			if got := rec.Header().Get("Content-Disposition"); got != tc.want {
				t.Errorf("Content-Disposition = %q, want %q", got, tc.want)
			}
		})
	}
}

// The team selects the decryption key, and it comes from the session the
// caller was allowed to read — never from the object key, which is a string on
// a row somebody could have changed.
func TestTheObjectIsReadUnderTheSessionsTeam(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	row := seedAttachment(f, "label.png", "image/png", []byte("bytes"))

	f.get(t, f.attachmentAt(row.ID))

	if len(f.objects.teams) != 1 || f.objects.teams[0] != f.teamID {
		t.Fatalf("read under teams %v, want [%s]", f.objects.teams, f.teamID)
	}
	if len(f.objects.keys) != 1 || f.objects.keys[0] != row.ObjectKey {
		t.Errorf("read keys %v, want [%s]", f.objects.keys, row.ObjectKey)
	}
}

// An attachment id from another conversation is a missing row, not a file.
func TestAnAttachmentInAnotherSessionIsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	row := seedAttachment(f, "label.png", "image/png", []byte("bytes"))
	f.store.attachments[0].SessionID = uuid.New()

	rec := f.get(t, f.attachmentAt(row.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if len(f.objects.keys) != 0 {
		t.Errorf("the bucket was read anyway: %v", f.objects.keys)
	}
}

// A direct session belongs to its participant. Its files are not a way around
// that, and the answer is the same 404 the session itself gives.
func TestAStrangerCannotReadADirectSessionsAttachment(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	row := seedAttachment(f, "label.png", "image/png", []byte("bytes"))
	f.caller = memberOf(f.teamID, "member")

	for name, path := range map[string]string{
		"the list": f.sessionAttachments(),
		"the file": f.attachmentAt(row.ID),
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.get(t, path)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404: %s", rec.Code, rec.Body)
			}
		})
	}

	// A refusal has to change nothing and do nothing. A handler that carried
	// on past the 404 would still answer 404 — the status is already written —
	// so the only way to see it is to assert the queries never ran.
	if len(f.objects.keys) != 0 {
		t.Errorf("the bucket was read for a stranger: %v", f.objects.keys)
	}
	if len(f.store.attachmentListArgs) != 0 {
		t.Errorf("the list ran for a stranger: %v", f.store.attachmentListArgs)
	}
	if len(f.store.getAttachmentArgs) != 0 {
		t.Errorf("the row was read for a stranger: %v", f.store.getAttachmentArgs)
	}
}

func TestAMalformedAttachmentIDIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	rec := f.get(t, f.sessionAttachments()+"/not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// A row whose object is gone is our failure, not the caller's: they asked for
// something the database says exists.
func TestAMissingObjectIsAServerError(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	row := seedAttachment(f, "label.png", "image/png", []byte("bytes"))
	delete(f.objects.bodies, row.ObjectKey)

	rec := f.get(t, f.attachmentAt(row.ID))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
}

func TestAFailedObjectReadDoesNotLeak(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	row := seedAttachment(f, "label.png", "image/png", []byte("bytes"))
	f.objects.err = errors.New("bucket credentials expired")

	rec := f.get(t, f.attachmentAt(row.ID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "credentials") {
		t.Errorf("the response repeated the internal error: %s", rec.Body)
	}
}
