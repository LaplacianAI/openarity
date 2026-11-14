package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// fakeUploads is the object store and its reservations, as the real Ingest
// writes them.
type fakeUploads struct {
	mu       sync.Mutex
	put      map[string][]byte
	reserved []string
	putErr   error
}

func (f *fakeUploads) Put(_ context.Context, _ uuid.UUID, key string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.put[key] = body
	return nil
}

func (f *fakeUploads) ReserveObjects(_ context.Context, arg db.ReserveObjectsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserved = append(f.reserved, arg.ObjectKeys...)
	return nil
}

var png = []byte("\x89PNG\r\n\x1a\n-the-screenshot")

type upload struct {
	name string
	body []byte
}

// form is a multipart body: its fields in order, then its files.
func form(t *testing.T, fields [][2]string, files ...upload) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		w, err := mw.CreateFormFile("files", f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), buf.Bytes()
}

func (f *fixture) upload(t *testing.T, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New(discardLogger(), f.store, f.authz, f.objects, f.inbox, gateway.NewIngest(f.uploads, f.uploads, nil, discardLogger())).
		Register(mux, api.NewGuard(routes(t), f.authz, liveTeams{}, discardLogger()))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if f.caller != nil {
		req = req.WithContext(auth.WithUser(req.Context(), f.caller))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) startPath() string { return "/teams/" + f.teamID.String() + "/sessions" }

func (f *fixture) messagesPath() string {
	return "/teams/" + f.teamID.String() + "/sessions/" + f.sessionID.String() + "/messages"
}

// A message sent with files is delivered with them, each stored under a key
// reserved before its bytes were written, its type read from its bytes.
func TestAMessageWithFilesIsDeliveredWithThem(t *testing.T) {
	f := newFixture(t)
	f.inbox.out = gateway.Delivered{Session: uuid.New(), Message: uuid.New()}
	kind, body := form(t, [][2]string{{"text", "why does it crash?"}, {"key", "k1"}},
		upload{"../screen.png", png}, upload{"crash.log", []byte("panic: nil map")})

	rec := f.upload(t, f.startPath(), kind, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := f.inbox.got[0]
	if got.Text != "why does it crash?" || len(got.Files) != 2 {
		t.Fatalf("delivered %+v", got)
	}
	if s := got.Files[0]; s.Filename != "screen.png" || s.MediaType != "image/png" || s.SizeBytes != int64(len(png)) {
		t.Errorf("the image was stored as %+v", s)
	}
	if got.Files[1].MediaType != "text/plain; charset=utf-8" {
		t.Errorf("the log was stored as %+v", got.Files[1])
	}
	for _, s := range got.Files {
		if !strings.HasPrefix(s.ObjectKey, objects.TeamPrefix(f.teamID)) || f.uploads.put[s.ObjectKey] == nil {
			t.Errorf("%s was not written under the team", s.ObjectKey)
		}
	}
	if len(f.uploads.reserved) != 2 {
		t.Errorf("reserved %v", f.uploads.reserved)
	}
}

// A message of files alone is a message; one of nothing is not.
func TestAMessageOfFilesAloneIsAccepted(t *testing.T) {
	f := newFixture(t)
	f.inbox.out = gateway.Delivered{Session: uuid.New(), Message: uuid.New()}

	kind, body := form(t, [][2]string{{"key", "k1"}}, upload{"screen.png", png})
	if rec := f.upload(t, f.startPath(), kind, body); rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	kind, body = form(t, [][2]string{{"key", "k2"}})
	rec := f.upload(t, f.startPath(), kind, body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "or a file be sent") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}

// A refusal stores no message. Files written before it were reserved, so the
// reaper takes them, and nothing is delivered.
func TestAnUploadThatIsRefusedDeliversNothing(t *testing.T) {
	nine := make([]upload, maxFiles+1)
	for i := range nine {
		nine[i] = upload{fmt.Sprintf("shot-%d.png", i), png}
	}
	for _, tc := range []struct {
		name   string
		fields [][2]string
		files  []upload
		status int
		want   string
	}{
		{"a ninth file", [][2]string{{"text", "hi"}, {"key", "k"}}, nine, http.StatusBadRequest, "at most 8 files"},
		{"a file too large", [][2]string{{"text", "hi"}, {"key", "k"}}, []upload{{"big.bin", make([]byte, objects.MaxAttachment+1)}}, http.StatusBadRequest, "big.bin is over the 25 MB"},
		{"a page", [][2]string{{"text", "hi"}, {"key", "k"}}, []upload{{"page.html", []byte("<html><script>x</script></html>")}}, http.StatusBadRequest, "page.html is text/html"},
		{"an empty file", [][2]string{{"text", "hi"}, {"key", "k"}}, []upload{{"empty.txt", nil}}, http.StatusBadRequest, "empty.txt is empty"},
		{"a part it does not take", [][2]string{{"text", "hi"}, {"key", "k"}, {"author", "someone else"}}, nil, http.StatusBadRequest, `a part named "author"`},
		{"a bad key", [][2]string{{"text", "hi"}, {"key", "no spaces"}}, []upload{{"a.png", png}}, http.StatusBadRequest, "key must be"},
		{"text too long", [][2]string{{"text", strings.Repeat("x", maxSaid+1)}, {"key", "k"}}, nil, http.StatusBadRequest, "text must be at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			kind, body := form(t, tc.fields, tc.files...)
			rec := f.upload(t, f.startPath(), kind, body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("status %d: %s; want %d %q", rec.Code, rec.Body, tc.status, tc.want)
			}
			if len(f.inbox.got) != 0 {
				t.Errorf("delivered %+v", f.inbox.got)
			}
			// Whatever was written was reserved first.
			for key := range f.uploads.put {
				if !contains(f.uploads.reserved, key) {
					t.Errorf("%s was written without a reservation", key)
				}
			}
		})
	}
}

// A body past its cap is refused as too large, before it is all read.
func TestAnUploadPastItsCapIsTooLarge(t *testing.T) {
	f := newFixture(t)
	kind := "multipart/form-data; boundary=x"
	body := bytes.Repeat([]byte("a"), maxUpload+1)
	body = append([]byte("--x\r\nContent-Disposition: form-data; name=\"files\"; filename=\"a.bin\"\r\n\r\n"), body...)

	rec := f.upload(t, f.startPath(), kind, body)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(f.inbox.got) != 0 {
		t.Errorf("delivered %+v", f.inbox.got)
	}
}

func TestABodyThatIsNotMultipartIsRefused(t *testing.T) {
	f := newFixture(t)
	rec := f.upload(t, f.startPath(), "multipart/form-data", []byte("not a form"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not multipart/form-data") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// A store that fails is our failure, not the sender's.
func TestAStoreThatFailsIsAServerError(t *testing.T) {
	f := newFixture(t)
	f.uploads.putErr = errors.New("the bucket is unavailable")
	kind, body := form(t, [][2]string{{"text", "hi"}, {"key", "k"}}, upload{"a.png", png})

	if rec := f.upload(t, f.startPath(), kind, body); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(f.inbox.got) != 0 {
		t.Errorf("delivered %+v", f.inbox.got)
	}
}

// Nobody uploads into another person's conversation, which they cannot even
// see: refused before a single part is read, so nothing is stored.
func TestNobodyUploadsIntoAnotherPersonsConversation(t *testing.T) {
	f := newFixture(t)
	f.dashboardSession()
	other := uuid.New()
	f.store.sessions[0].UserID = &other
	kind, body := form(t, [][2]string{{"text", "hi"}, {"key", "k"}}, upload{"a.png", png})

	rec := f.upload(t, f.messagesPath(), kind, body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(f.uploads.put) != 0 || len(f.uploads.reserved) != 0 {
		t.Errorf("stored %d and reserved %d", len(f.uploads.put), len(f.uploads.reserved))
	}
}

// Writing in one's own conversation takes files as starting one does.
func TestAMessageInAConversationCarriesFiles(t *testing.T) {
	f := newFixture(t)
	f.dashboardSession()
	f.store.sessions[0].UserID = &f.caller.ID
	f.inbox.out = gateway.Delivered{Session: f.sessionID, Message: uuid.New()}
	kind, body := form(t, [][2]string{{"text", "and this"}, {"key", "k"}}, upload{"a.png", png})

	rec := f.upload(t, f.messagesPath(), kind, body)
	if rec.Code != http.StatusAccepted || len(f.inbox.got) != 1 || len(f.inbox.got[0].Files) != 1 {
		t.Fatalf("status %d: %s; delivered %+v", rec.Code, rec.Body, f.inbox.got)
	}
}

func contains(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// A field is read no further than a byte past its limit, so a client cannot
// make the brain hold a megabyte of text to refuse it.
func TestAFieldIsReadNoFurtherThanItsLimit(t *testing.T) {
	t.Parallel()

	kind, body := form(t, [][2]string{{"text", strings.Repeat("x", 1<<20)}})
	_, params, err := mime.ParseMediaType(kind)
	if err != nil {
		t.Fatal(err)
	}
	part, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	got, err := field(part, 10)
	if err != nil || len(got) != 11 {
		t.Fatalf("read %d bytes, %v; want 11", len(got), err)
	}
}

// A form says builder as a part of its own, as true or false.
func TestAFormStartsAConversationWithTheBuilder(t *testing.T) {
	f := newFixture(t)
	f.inbox.out = gateway.Delivered{Session: uuid.New(), Message: uuid.New()}
	kind, body := form(t, [][2]string{{"text", "read this"}, {"key", "k1"}, {"builder", "true"}}, upload{"spec.txt", []byte("an agent")})
	if rec := f.upload(t, f.startPath(), kind, body); rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := f.inbox.got[0]; !got.Session.Builder || len(got.Files) != 1 {
		t.Errorf("delivered %+v, want the builder's, with its file", got)
	}

	kind, body = form(t, [][2]string{{"text", "read this"}, {"key", "k2"}, {"builder", "yes please"}})
	rec := f.upload(t, f.startPath(), kind, body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "builder must be true or false") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
	if len(f.inbox.got) != 1 {
		t.Errorf("delivered %d messages, want only the first", len(f.inbox.got))
	}
}
