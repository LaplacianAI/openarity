package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// recordingClient is a model that keeps what it was sent and answers err.
type recordingClient struct {
	mu   sync.Mutex
	sent []agent.Request
	err  error
	// streamErr is what a stream that opened reports at its end.
	streamErr error
}

func (c *recordingClient) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, req)
	return agent.Response{}, c.err
}

func (c *recordingClient) Stream(_ context.Context, req agent.Request) (agent.Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, req)
	if c.err != nil {
		return nil, c.err
	}
	return &endedStream{err: c.streamErr}, nil
}

type endedStream struct{ err error }

func (s *endedStream) Next() bool               { return false }
func (s *endedStream) Event() agent.StreamEvent { return agent.StreamEvent{} }
func (s *endedStream) Err() error               { return s.err }
func (s *endedStream) Close() error             { return nil }

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
	if sent[1].Text != "[shot-0.png: a file shown earlier, not sent again]" {
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

// A text file reaches the model as its text, cut short of a long one, and a
// cut never splits a character.
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

	// Streamed, whether it is refused at once or at the end.
	if _, err := f.client.Stream(f.ctx(t), req); !errors.As(err, &refusedFiles) {
		t.Errorf("Stream = %v", err)
	}
	f.inner.err, f.inner.streamErr = nil, &agent.ModelError{Status: http.StatusUnprocessableEntity}
	s, err := f.client.Stream(f.ctx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Err(); !errors.As(err, &refusedFiles) {
		t.Errorf("the stream ended with %v", err)
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
