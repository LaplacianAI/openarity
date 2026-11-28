package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
)

// recordingClient is a model that keeps what it was sent and answers err. Its
// streams open and report their answer at the first read, as openaicompat's
// do, unless atOpen says the answer comes when one is opened.
type recordingClient struct {
	mu   sync.Mutex
	sent []agent.Request
	err  error
	// streamErr is what a stream that opened reports at its end.
	streamErr error
	// filesErr is what a request carrying a file is answered, as a provider
	// that cannot take one does, while one without is answered err.
	filesErr error
	atOpen   bool
	// midErr is what a stream answered reports after its first event; silent,
	// a stream answered that ends with none.
	midErr  error
	silent  bool
	streams []*endedStream
}

func (c *recordingClient) answer(req agent.Request) error {
	c.sent = append(c.sent, req)
	if c.filesErr != nil && blobsIn(req) > 0 {
		return c.filesErr
	}
	return c.err
}

func (c *recordingClient) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return agent.Response{}, c.answer(req)
}

func (c *recordingClient) Stream(_ context.Context, req agent.Request) (agent.Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.answer(req)
	if err != nil && c.atOpen {
		return nil, err
	}
	s := &endedStream{err: err}
	switch {
	case err != nil:
	case c.midErr != nil:
		s.events, s.err = 1, c.midErr
	case !c.silent:
		s.events, s.err = 1, c.streamErr
	default:
		s.err = c.streamErr
	}
	c.streams = append(c.streams, s)
	return s, nil
}

func blobsIn(req agent.Request) int {
	n := 0
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Blob != nil {
				n++
			}
		}
	}
	return n
}

// endedStream gives its events, then ends with err.
type endedStream struct {
	events, closed int
	err            error
}

func (s *endedStream) Next() bool {
	if s.events == 0 {
		return false
	}
	s.events--
	return true
}
func (s *endedStream) Event() agent.StreamEvent { return agent.StreamEvent{Text: "an answer"} }
func (s *endedStream) Err() error {
	if s.events > 0 {
		return nil
	}
	return s.err
}
func (s *endedStream) Close() error { s.closed++; return nil }

// drain reads a stream to its end as a run does: its events, then its error.
func drain(s agent.Stream) (int, error) {
	n := 0
	for s.Next() {
		n++
	}
	return n, s.Err()
}

type resolveFixture struct {
	session uuid.UUID
	store   *fakeStore
	objects *fakeObjects
	inner   *recordingClient
	client  agent.ModelClient
}

func newResolveFixture(t *testing.T) *resolveFixture {
	t.Helper()
	f := &resolveFixture{
		session: uuid.New(),
		store:   &fakeStore{},
		objects: &fakeObjects{data: map[string]string{}},
		inner:   &recordingClient{},
	}
	factory := Resolving(func(agent.Endpoint) (agent.ModelClient, error) { return f.inner, nil }, f.store, f.objects)
	client, err := factory(agent.Endpoint{})
	if err != nil {
		t.Fatal(err)
	}
	f.client = client
	return f
}

// file is an attachment in the fixture's conversation holding data.
func (f *resolveFixture) file(name, mediaType, data string) db.Attachment {
	a := attachedFile(f.session, name, mediaType, int64(len(data)))
	f.store.attached = append(f.store.attached, a)
	f.objects.data[a.ObjectKey] = data
	return a
}

func (f *resolveFixture) ctx(t *testing.T) context.Context {
	return withSession(t.Context(), f.session)
}

func asking(parts ...agent.Content) agent.Request {
	return agent.Request{
		Model:    agent.ModelRef{Name: "test/model"},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: append([]agent.Content{textPart("look")}, parts...)}},
	}
}

func refTo(a db.Attachment) agent.Content {
	part, err := readable(a)
	if err != nil {
		panic(err)
	}
	return part
}

// The request that goes out carries the bytes; the caller's messages, which
// are the transcript the run saves, keep the reference alone.
func TestARequestCarriesTheBytesAndTheTranscriptKeepsTheReference(t *testing.T) {
	f := newResolveFixture(t)
	shot := f.file("screen.png", "image/png", "\x89PNG-bytes")
	req := asking(refTo(shot))

	if _, err := f.client.Complete(f.ctx(t), req); err != nil {
		t.Fatal(err)
	}

	sent := f.inner.sent[0].Messages[0].Content[1]
	if sent.Type != agent.ContentImage || string(sent.Blob.Data) != "\x89PNG-bytes" || sent.Blob.Ref != "attachment:"+shot.ID.String() {
		t.Fatalf("sent %+v", sent.Blob)
	}
	if kept := req.Messages[0].Content[1].Blob; len(kept.Data) != 0 {
		t.Fatalf("the caller's message was given the bytes: %q", kept.Data)
	}
	if got := f.objects.asked(); len(got) != 1 || got[0] != shot.TeamID.String()+"/"+shot.ObjectKey {
		t.Errorf("read %v, want the file under its own team's key", got)
	}
}

// Fetched for each request that names it, and kept by none.
func TestAFileIsFetchedForEveryRequestThatNamesIt(t *testing.T) {
	f := newResolveFixture(t)
	req := asking(refTo(f.file("screen.png", "image/png", "img")))

	for range 3 {
		if _, err := f.client.Complete(f.ctx(t), req); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(f.objects.asked()); got != 3 {
		t.Errorf("fetched %d times for 3 requests", got)
	}
}

// Without a conversation to read from, a request naming a file is refused
// rather than answered from anywhere else.
func TestAFileIsNeverReadWithoutTheRunsConversation(t *testing.T) {
	f := newResolveFixture(t)
	req := asking(refTo(f.file("screen.png", "image/png", "img")))

	_, err := f.client.Complete(t.Context(), req)
	if err == nil || !strings.Contains(err.Error(), "no conversation was given") {
		t.Fatalf("Complete = %v", err)
	}
	if len(f.inner.sent) != 0 || len(f.objects.asked()) != 0 {
		t.Errorf("sent %d requests and read %v", len(f.inner.sent), f.objects.asked())
	}
}

// A reference to another conversation's file finds nothing: it is read as
// gone, and its bytes are never fetched.
func TestAnotherConversationsFileIsNeverSent(t *testing.T) {
	f := newResolveFixture(t)
	theirs := attachedFile(uuid.New(), "theirs.png", "image/png", 3)
	f.store.attached = append(f.store.attached, theirs)
	f.objects.data[theirs.ObjectKey] = "secret"

	if _, err := f.client.Complete(f.ctx(t), asking(refTo(theirs))); err != nil {
		t.Fatal(err)
	}
	sent := f.inner.sent[0].Messages[0].Content[1]
	if sent.Blob != nil || sent.Text != "[theirs.png is no longer available]" {
		t.Errorf("sent %+v", sent)
	}
	if len(f.objects.asked()) != 0 {
		t.Errorf("read %v", f.objects.asked())
	}
}

// Nine files in one request: the newest eight go, and the oldest is said to
// be one shown before.
func TestARequestSendsTheNewestEightFiles(t *testing.T) {
	f := newResolveFixture(t)
	var parts []agent.Content
	for i := range filesPerRequest + 1 {
		parts = append(parts, refTo(f.file(fmt.Sprintf("shot-%d.png", i), "image/png", "img")))
	}

	if _, err := f.client.Complete(f.ctx(t), asking(parts...)); err != nil {
		t.Fatal(err)
	}
	sent := f.inner.sent[0].Messages[0].Content
	if sent[1].Text != "[shot-0.png: an earlier file, not sent with this request]" {
		t.Errorf("the oldest went as %+v", sent[1])
	}
	for _, p := range sent[2:] {
		if p.Blob == nil || len(p.Blob.Data) == 0 {
			t.Errorf("a newer file went as %+v", p)
		}
	}
	if got := len(f.objects.asked()); got != filesPerRequest {
		t.Errorf("fetched %d files, want %d", got, filesPerRequest)
	}
}

// A file a tool attached inline counts with the conversation's: the newest
// eight go, so an older reference is not read at all.
func TestAToolsInlineFilesCountTowardTheNewestEight(t *testing.T) {
	f := newResolveFixture(t)
	parts := []agent.Content{refTo(f.file("old.png", "image/png", "img"))}
	for i := range filesPerRequest {
		parts = append(parts, agent.Content{Type: agent.ContentImage, Blob: &agent.Blob{
			MediaType: "image/png", Name: fmt.Sprintf("image-%d.png", i+1), Data: []byte("inline"),
		}})
	}

	if _, err := f.client.Complete(f.ctx(t), asking(parts...)); err != nil {
		t.Fatal(err)
	}
	sent := f.inner.sent[0].Messages[0].Content
	if sent[1].Text != "[old.png: an earlier file, not sent with this request]" || len(f.objects.asked()) != 0 {
		t.Errorf("the older reference went as %+v, and %d files were read; want it left out, unread", sent[1], len(f.objects.asked()))
	}
	for _, p := range sent[2:] {
		if p.Blob == nil || string(p.Blob.Data) != "inline" {
			t.Errorf("a newer inline file went as %+v", p)
		}
	}
}

// A request carries 20 MiB of files at most, the newest first; the caller's
// messages, the transcript, keep every file.
func TestARequestsFilesAreHeldToItsBytes(t *testing.T) {
	f := newResolveFixture(t)
	big := func(name string) agent.Content {
		return agent.Content{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png", Name: name, Data: make([]byte, 8<<20)}}
	}
	req := asking(big("a.png"), big("b.png"), big("c.png"))

	if _, err := f.client.Complete(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	sent := f.inner.sent[0].Messages[0].Content
	if sent[1].Text != "[a.png: an earlier file, not sent with this request]" {
		t.Errorf("the oldest went as %+v, want it left out past 20 MiB", sent[1])
	}
	if sent[2].Blob == nil || sent[3].Blob == nil {
		t.Errorf("the newer two went as %+v and %+v, want both", sent[2], sent[3])
	}
	if kept := req.Messages[0].Content[1]; kept.Blob == nil || len(kept.Blob.Data) != 8<<20 {
		t.Errorf("the caller's message lost its file: %+v", kept)
	}
}

// A text file reaches the model as its text, cut short of a long one, and a
// cut never splits a character.
// A text file is a file of the eight as an image is: one older than the
// newest eight is named, and never read.
func TestTextFilesCountTowardTheNewestEight(t *testing.T) {
	f := newResolveFixture(t)
	var parts []agent.Content
	for i := range filesPerRequest + 2 {
		parts = append(parts, refTo(f.file(fmt.Sprintf("notes-%d.txt", i), "text/plain", "words")))
	}

	if _, err := f.client.Complete(f.ctx(t), asking(parts...)); err != nil {
		t.Fatal(err)
	}
	sent := f.inner.sent[0].Messages[0].Content
	for i, want := range []string{
		"[notes-0.txt: an earlier file, not sent with this request]",
		"[notes-1.txt: an earlier file, not sent with this request]",
	} {
		if sent[1+i].Text != want {
			t.Errorf("an older text file went as %q, want %q", sent[1+i].Text, want)
		}
	}
	for _, p := range sent[3:] {
		if !strings.Contains(p.Text, "words") {
			t.Errorf("a newer text file went as %+v, want its text", p)
		}
	}
	if got := len(f.objects.asked()); got != filesPerRequest {
		t.Errorf("read %d files, want %d", got, filesPerRequest)
	}
}

// A file larger than what is left of a request's bytes is named without
// being read from the object store.
func TestAFilePastARequestsBytesIsNeverRead(t *testing.T) {
	f := newResolveFixture(t)
	old := refTo(f.file("old.png", "image/png", strings.Repeat("x", 5<<20)))
	inline := func(name string) agent.Content {
		return agent.Content{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png", Name: name, Data: make([]byte, 8<<20)}}
	}

	if _, err := f.client.Complete(f.ctx(t), asking(old, inline("a.png"), inline("b.png"))); err != nil {
		t.Fatal(err)
	}
	if got := f.inner.sent[0].Messages[0].Content[1].Text; got != "[old.png: an earlier file, not sent with this request]" {
		t.Errorf("the older file went as %q, want it named", got)
	}
	if n := len(f.objects.asked()); n != 0 {
		t.Errorf("read %d files from the object store, want none", n)
	}
}

func TestATextFileIsSentAsItsText(t *testing.T) {
	f := newResolveFixture(t)
	short := f.file("crash.log", "text/plain; charset=utf-8", "panic: nil map")
	// One byte ahead, so the cut falls inside a two-byte character.
	long := f.file("big.log", "text/plain", "x"+strings.Repeat("é", maxTextBytes))

	if _, err := f.client.Complete(f.ctx(t), asking(refTo(short), refTo(long))); err != nil {
		t.Fatal(err)
	}
	sent := f.inner.sent[0].Messages[0].Content
	if sent[1].Blob != nil || sent[1].Text != "The contents of crash.log:\n\npanic: nil map" {
		t.Errorf("the short file went as %+v", sent[1])
	}
	if !strings.HasSuffix(sent[2].Text, "[cut at 100 KB; the file is longer]") || strings.ContainsRune(sent[2].Text, '�') {
		t.Errorf("the long file went as %q…", sent[2].Text[len(sent[2].Text)-60:])
	}
}

// A blob the brain did not name is the caller's, and passes as it came.
func TestABlobTheBrainDidNotNameIsLeftAlone(t *testing.T) {
	f := newResolveFixture(t)
	own := agent.Content{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png", Data: []byte("inline")}}
	other := agent.Content{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png", Ref: "s3://elsewhere"}}

	if _, err := f.client.Complete(t.Context(), asking(own, other)); err != nil {
		t.Fatal(err)
	}
	sent := f.inner.sent[0].Messages[0].Content
	if string(sent[1].Blob.Data) != "inline" || sent[2].Blob.Ref != "s3://elsewhere" || len(f.objects.asked()) != 0 {
		t.Errorf("sent %+v and %+v", sent[1].Blob, sent[2].Blob)
	}
}

func TestAFileThatCannotBeReadFailsTheRequest(t *testing.T) {
	f := newResolveFixture(t)
	req := asking(refTo(f.file("screen.png", "image/png", "img")))
	f.objects.err = errors.New("the object store is unavailable")

	if _, err := f.client.Complete(f.ctx(t), req); err == nil || !strings.Contains(err.Error(), "screen.png") {
		t.Fatalf("Complete = %v", err)
	}
	if len(f.inner.sent) != 0 {
		t.Error("a request went without its file")
	}

	f.objects.err = nil
	f.store.attachedErr = errors.New("connection refused")
	if _, err := f.client.Complete(f.ctx(t), req); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Complete = %v", err)
	}
}

// A provider refusing a request that carried files is most often a model that
// reads none, and says so; the provider's answer stays underneath.
func TestARefusedRequestWithFilesSaysTheModelMayNotReadThem(t *testing.T) {
	f := newResolveFixture(t)
	req := asking(refTo(f.file("screen.png", "image/png", "img")))
	f.inner.err = &agent.ModelError{Status: http.StatusBadRequest, Code: "invalid_request_error"}

	_, err := f.client.Complete(f.ctx(t), req)
	var refusedFiles *filesRefused
	var answered *agent.ModelError
	if !errors.As(err, &refusedFiles) || !errors.As(err, &answered) {
		t.Fatalf("Complete = %v", err)
	}
	if !strings.Contains(err.Error(), "the model test/model refused a request carrying files; it may not read images or PDFs") ||
		!strings.Contains(err.Error(), "400") {
		t.Errorf("err = %v", err)
	}
	if status, failure := outcome(err, nil); status != "failed" || failure != err.Error() {
		t.Errorf("outcome = %s, %q", status, failure)
	}

	// Streamed, whether it is refused at its first read, at its end, or when
	// it is opened.
	s, err := f.client.Stream(f.ctx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(s); !errors.As(err, &refusedFiles) {
		t.Errorf("the stream ended with %v", err)
	}
	f.inner.err, f.inner.streamErr = nil, &agent.ModelError{Status: http.StatusUnprocessableEntity}
	if s, err = f.client.Stream(f.ctx(t), req); err != nil {
		t.Fatal(err)
	}
	if _, err := drain(s); !errors.As(err, &refusedFiles) {
		t.Errorf("the stream ended with %v", err)
	}
	f.inner.err, f.inner.streamErr, f.inner.atOpen = &agent.ModelError{Status: http.StatusBadRequest}, nil, true
	if _, err := f.client.Stream(f.ctx(t), req); !errors.As(err, &refusedFiles) {
		t.Errorf("Stream = %v", err)
	}
}

// Anything else is what it always was: a request with no files, a request
// too long, and a provider that failed.
func TestOtherRefusalsAreLeftAsTheyCame(t *testing.T) {
	f := newResolveFixture(t)
	withFile := asking(refTo(f.file("screen.png", "image/png", "img")))

	for _, tc := range []struct {
		name string
		req  agent.Request
		err  error
	}{
		{"no files", asking(), &agent.ModelError{Status: http.StatusBadRequest}},
		{"too long", withFile, &agent.ModelError{Status: http.StatusBadRequest, Code: "context_length_exceeded"}},
		{"provider down", withFile, &agent.ModelError{Status: http.StatusBadGateway}},
		{"unreachable", withFile, errors.New("dial tcp: connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.inner.err = tc.err
			_, err := f.client.Complete(f.ctx(t), tc.req)
			var refusedFiles *filesRefused
			if errors.As(err, &refusedFiles) || !errors.Is(err, tc.err) {
				t.Errorf("Complete = %v, want %v as it came", err, tc.err)
			}
		})
	}
}

func TestAFactoryThatFailsSaysSo(t *testing.T) {
	t.Parallel()

	down := errors.New("no endpoint")
	_, err := Resolving(func(agent.Endpoint) (agent.ModelClient, error) { return nil, down }, &fakeStore{}, &fakeObjects{})(agent.Endpoint{})
	if !errors.Is(err, down) {
		t.Fatalf("factory = %v", err)
	}
}

func TestAReferenceThatNamesNoFileIsRefused(t *testing.T) {
	f := newResolveFixture(t)
	bad := agent.Content{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png", Name: "x.png", Ref: "attachment:not-an-id"}}

	if _, err := f.client.Complete(f.ctx(t), asking(bad)); err == nil || !strings.Contains(err.Error(), "names no file") {
		t.Fatalf("Complete = %v", err)
	}
}

// asker is how a run asks: Complete, or a stream read to its end.
type asker func(t *testing.T, f *resolveFixture, req agent.Request) error

var asks = map[string]asker{
	"complete": func(t *testing.T, f *resolveFixture, req agent.Request) error {
		_, err := f.client.Complete(f.ctx(t), req)
		return err
	},
	"stream": func(t *testing.T, f *resolveFixture, req agent.Request) error {
		s, err := f.client.Stream(f.ctx(t), req)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		if n, err := drain(s); err != nil || n == 0 {
			return fmt.Errorf("%d events, then %w", n, err)
		}
		return nil
	},
	"stream refused when opened": func(t *testing.T, f *resolveFixture, req agent.Request) error {
		f.inner.atOpen = true
		s, err := f.client.Stream(f.ctx(t), req)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		_, err = drain(s)
		return err
	},
}

// A provider that cannot take a request's files, refusing it or failing on its
// side as a router that breaks on a large PDF does, is asked again without
// them: the model is told which files were not sent, and the run goes on.
func TestARequestWhoseFilesTheProviderCannotTakeGoesWithoutThem(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"refused", &agent.ModelError{Status: http.StatusBadRequest, Code: "invalid_request_error"}},
		{"unprocessable", &agent.ModelError{Status: http.StatusUnprocessableEntity}},
		{"router broke", &agent.ModelError{Status: http.StatusInternalServerError, Code: "internal_server_error"}},
		{"gateway broke", &agent.ModelError{Status: http.StatusBadGateway}},
	} {
		for how, ask := range asks {
			t.Run(tc.name+", "+how, func(t *testing.T) {
				f := newResolveFixture(t)
				req := asking(refTo(f.file("paper.pdf", "application/pdf", "%PDF")), refTo(f.file("notes.txt", "text/plain", "the notes")))
				f.inner.filesErr = tc.err

				if err := ask(t, f, req); err != nil {
					t.Fatalf("asked = %v", err)
				}
				if len(f.inner.sent) != 2 {
					t.Fatalf("%d requests, want the one refused and one without its files", len(f.inner.sent))
				}
				bare := f.inner.sent[1]
				if n := blobsIn(bare); n != 0 {
					t.Errorf("the second request carried %d files", n)
				}
				got := textOf(bare.Messages)
				if !strings.Contains(got, "[paper.pdf: not sent; the model's provider failed on the request carrying it]") {
					t.Errorf("the model was not told the PDF was not sent: %q", got)
				}
				if !strings.Contains(got, "the notes") {
					t.Errorf("a text file is still sent as its text: %q", got)
				}
				if req.Messages[0].Content[1].Blob == nil {
					t.Error("the transcript lost its reference")
				}
				for i, s := range f.inner.streams {
					if s.closed != 1 {
						t.Errorf("stream %d closed %d times", i, s.closed)
					}
				}
			})
		}
	}
}

// Once a run's files were sent without, the rest of its steps send none, and
// never read them: one request each, not a refusal paid for every step. A
// gateway's failure is the exception: the next step may well get through.
func TestAfterARefusalTheRunsLaterRequestsSendNoFiles(t *testing.T) {
	for _, tc := range []struct {
		status int
		sticks bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusUnprocessableEntity, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, false},
		{http.StatusServiceUnavailable, false},
		{http.StatusGatewayTimeout, false},
	} {
		for how, ask := range asks {
			t.Run(fmt.Sprintf("%d, %s", tc.status, how), func(t *testing.T) {
				f := newResolveFixture(t)
				pdf := f.file("paper.pdf", "application/pdf", "%PDF")
				req := asking(refTo(pdf))
				f.inner.filesErr = &agent.ModelError{Status: tc.status}

				if err := ask(t, f, req); err != nil {
					t.Fatal(err)
				}
				f.inner.filesErr = nil
				if tc.sticks {
					delete(f.objects.data, pdf.ObjectKey)
				}
				if err := ask(t, f, req); err != nil {
					t.Fatalf("the next step = %v", err)
				}
				if len(f.inner.sent) != 3 {
					t.Fatalf("%d requests, want 2 then one", len(f.inner.sent))
				}
				if carried := blobsIn(f.inner.sent[2]) == 1; carried == tc.sticks {
					t.Errorf("the next step carried the file: %v", carried)
				}
			})
		}
	}
}

// A stream that gave an event is never asked again, whatever it ends with:
// what it said was shown. The run's files stay on.
func TestAStreamThatAnsweredIsNeverAskedAgain(t *testing.T) {
	f := newResolveFixture(t)
	req := asking(refTo(f.file("screen.png", "image/png", "img")))
	broke := &agent.ModelError{Status: http.StatusInternalServerError}
	f.inner.midErr = broke

	s, err := f.client.Stream(f.ctx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := drain(s); n != 1 || !errors.Is(err, broke) {
		t.Fatalf("%d events, then %v", n, err)
	}
	if s.Next() || len(f.inner.sent) != 1 {
		t.Errorf("%d requests after the stream ended", len(f.inner.sent))
	}

	f.inner.midErr = nil
	if err := asks["stream"](t, f, req); err != nil {
		t.Fatal(err)
	}
	if blobsIn(f.inner.sent[1]) != 1 {
		t.Error("a stream that broke after answering turned the run's files off")
	}
}

// A stream without its files that ends with nothing to say still got past
// the refusal, so the run's files are off.
func TestAStreamWithoutItsFilesThatEndsQuietlyTurnsThemOff(t *testing.T) {
	f := newResolveFixture(t)
	req := asking(refTo(f.file("paper.pdf", "application/pdf", "%PDF")))
	f.inner.filesErr, f.inner.silent = &agent.ModelError{Status: http.StatusInternalServerError}, true

	s, err := f.client.Stream(f.ctx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := drain(s); n != 0 || err != nil {
		t.Fatalf("%d events, then %v", n, err)
	}
	f.inner.filesErr = nil
	if _, err := f.client.Complete(f.ctx(t), req); err != nil {
		t.Fatalf("the next step = %v", err)
	}
	if blobsIn(f.inner.sent[2]) != 0 {
		t.Error("the next step carried the file")
	}
}

// A stream without its files that answers, then breaks, ends with its own
// error: the refusal before it is not what stopped it.
func TestAStreamWithoutItsFilesThatBreaksAfterAnsweringSaysWhy(t *testing.T) {
	f := newResolveFixture(t)
	req := asking(refTo(f.file("paper.pdf", "application/pdf", "%PDF")))
	refusal, broke := &agent.ModelError{Status: http.StatusInternalServerError}, &agent.ModelError{Status: http.StatusBadGateway}
	f.inner.filesErr, f.inner.midErr = refusal, broke

	s, err := f.client.Stream(f.ctx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := drain(s); n != 1 || !errors.Is(err, broke) || errors.Is(err, refusal) {
		t.Errorf("%d events, then %v; want the break", n, err)
	}
}

// A request that fails without its files too is not the files' doing: the
// first answer is the one reported, a refusal still saying the model may not
// read them, it is asked again only once, and the next step tries its files.
func TestARequestThatFailsWithoutItsFilesTooKeepsTheFirstAnswer(t *testing.T) {
	for how, ask := range asks {
		t.Run(how, func(t *testing.T) {
			f := newResolveFixture(t)
			req := asking(refTo(f.file("screen.png", "image/png", "img")))
			first := &agent.ModelError{Status: http.StatusBadRequest, Code: "invalid_request_error"}
			f.inner.filesErr, f.inner.err = first, &agent.ModelError{Status: http.StatusServiceUnavailable}

			err := ask(t, f, req)
			var refusedFiles *filesRefused
			if !errors.As(err, &refusedFiles) || !errors.Is(err, first) {
				t.Fatalf("asked = %v, want the first refusal", err)
			}
			if len(f.inner.sent) != 2 {
				t.Errorf("%d requests, want 2", len(f.inner.sent))
			}

			f.inner.filesErr, f.inner.err = nil, nil
			f.inner.sent = nil
			if err := ask(t, f, req); err != nil {
				t.Fatal(err)
			}
			if blobsIn(f.inner.sent[0]) != 1 {
				t.Error("a failed retry turned the run's files off")
			}
		})
	}
}

// A request too long, or one with no files, is never tried again without
// files: they are not what the provider could not take.
func TestOnlyARequestsFilesAreGoneWithout(t *testing.T) {
	for _, tc := range []struct {
		name string
		file bool
		err  error
	}{
		{"too long", true, &agent.ModelError{Status: http.StatusBadRequest, Code: "context_length_exceeded"}},
		{"no files", false, &agent.ModelError{Status: http.StatusInternalServerError}},
		{"rate limited", true, &agent.ModelError{Status: http.StatusTooManyRequests}},
		{"unreachable", true, errors.New("dial tcp: connection refused")},
	} {
		for how, ask := range asks {
			t.Run(tc.name+", "+how, func(t *testing.T) {
				f := newResolveFixture(t)
				req := asking()
				if tc.file {
					req = asking(refTo(f.file("paper.pdf", "application/pdf", "%PDF")))
				}
				f.inner.err = tc.err

				if err := ask(t, f, req); !errors.Is(err, tc.err) {
					t.Errorf("asked = %v", err)
				}
				if len(f.inner.sent) != 1 {
					t.Errorf("%d requests, want one", len(f.inner.sent))
				}
			})
		}
	}
}

// Through the client the brain runs, whose stream reports a 500 at its first
// read: a router that breaks on the request carrying a PDF is asked again
// without it, and the run's stream answers.
func TestTheBrainsClientStreamsPastARouterThatBreaksOnAFile(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		if strings.Contains(string(body), "file_data") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"Maximum call stack size exceeded","type":"internal_server_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"test/model","choices":[{"index":0,"delta":{"content":"I could not open paper.pdf"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"test/model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	f := newResolveFixture(t)
	client, err := Resolving(openaicompat.Factory(), f.store, f.objects)(agent.Endpoint{BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	req := asking(refTo(f.file("paper.pdf", "application/pdf", "%PDF-1.7")))

	s, err := client.Stream(f.ctx(t), req)
	if err != nil {
		t.Fatalf("Stream = %v", err)
	}
	defer func() { _ = s.Close() }()
	var said strings.Builder
	for s.Next() {
		said.WriteString(s.Event().Text)
	}
	if err := s.Err(); err != nil || said.String() != "I could not open paper.pdf" {
		t.Fatalf("said %q, then %v", said.String(), err)
	}
	mu.Lock()
	defer mu.Unlock()
	withFile := 0
	for _, b := range bodies {
		if strings.Contains(b, "file_data") {
			withFile++
		}
	}
	if withFile != 3 || len(bodies) != 4 {
		t.Errorf("%d requests, %d with the file; want openai-go's three tries, then one without", len(bodies), withFile)
	}
	last := bodies[len(bodies)-1]
	if strings.Contains(last, "file_data") || !strings.Contains(last, "paper.pdf: not sent") {
		t.Errorf("the request that answered: %s", last)
	}
}
