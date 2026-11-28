package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// maxTextBytes is how much of a text file a model is sent.
const maxTextBytes = 100 << 10

// bytesPerRequest is how much of a conversation's files and its tools' one
// request carries, the newest first: under what a provider takes in one.
const bytesPerRequest = 20 << 20

type AttachmentReader interface {
	GetAttachmentInSession(ctx context.Context, arg db.GetAttachmentInSessionParams) (db.Attachment, error)
}

type sessionKey struct{}

// withSession names the conversation a run's model requests may read files
// from.
func withSession(ctx context.Context, session uuid.UUID) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

func sessionOf(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(sessionKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// Resolving wraps a ClientFactory so each request carries the bytes of the
// files its transcript names, fetched for that request alone: the transcript
// keeps the reference, and the bytes are never saved with it.
func Resolving(f agent.ClientFactory, s AttachmentReader, o Objects) agent.ClientFactory {
	return func(e agent.Endpoint) (agent.ModelClient, error) {
		inner, err := f(e)
		if err != nil {
			return nil, err
		}
		return &resolving{inner: inner, store: s, objects: o}, nil
	}
}

// resolving is one run's model client: Resolving's factory is called once a
// run, so what it learns about the run's files lasts that run alone.
type resolving struct {
	inner   agent.ModelClient
	store   AttachmentReader
	objects Objects
	// filesOff is set once a request was refused with its files and answered
	// without them, so the rest of the run sends none rather than paying for
	// the same refusal on every step.
	filesOff atomic.Bool
}

func (r *resolving) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	sent, files, err := r.resolve(ctx, req, !r.filesOff.Load())
	if err != nil {
		return agent.Response{}, err
	}
	resp, err := r.inner.Complete(ctx, sent)
	if !unsendable(err, files) {
		return resp, refused(err, files, req.Model.Name)
	}
	bare, _, berr := r.resolve(ctx, req, false)
	if berr == nil {
		resp, berr = r.inner.Complete(ctx, bare)
	}
	if berr != nil {
		return agent.Response{}, refused(err, files, req.Model.Name)
	}
	r.settle(err)
	return resp, nil
}

// Stream asks again without files as Complete does. A provider's answer
// reaches a stream at its first read, not when it is opened, so the stream
// asks again there, before any event, never after one.
func (r *resolving) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	sent, files, err := r.resolve(ctx, req, !r.filesOff.Load())
	if err != nil {
		return nil, err
	}
	rs := &refusingStream{r: r, ctx: ctx, req: req, files: files}
	rs.Stream, err = r.inner.Stream(ctx, sent)
	if err == nil {
		return rs, nil
	}
	if !unsendable(err, files) || !rs.reopen(err) {
		return nil, refused(err, files, req.Model.Name)
	}
	return rs, nil
}

// settle turns the run's files off after a refusal that a request without
// them got past: a bad request, or a 500, which is what a router that breaks
// on a file answers. Any other 5xx is a gateway or an overloaded provider,
// which the next request may well not meet.
func (r *resolving) settle(refusal error) {
	var answered *agent.ModelError
	if !errors.As(refusal, &answered) {
		return
	}
	switch answered.Status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusInternalServerError:
		r.filesOff.Store(true)
	}
}

// unsendable is a request whose files the provider would not take: refused as
// a bad request, or failed on its side, as a router that breaks on a large
// file does with a 500. Such a request is tried again without its files, so
// one file never stops a conversation. A request too long is compaction's.
func unsendable(err error, files int) bool {
	var answered *agent.ModelError
	if err == nil || files == 0 || !errors.As(err, &answered) || answered.Code == "context_length_exceeded" {
		return false
	}
	return answered.Status == http.StatusBadRequest || answered.Status == http.StatusUnprocessableEntity ||
		answered.Status >= http.StatusInternalServerError
}

type refusingStream struct {
	agent.Stream
	r     *resolving
	ctx   context.Context
	req   agent.Request
	files int
	// first is the answer to the request with its files, once the stream
	// went without them; seen, that an event was read; tried, that it asked
	// again, whether or not that opened.
	first error
	seen  bool
	tried bool
}

func (s *refusingStream) Next() bool {
	if s.Stream.Next() {
		s.saw()
		return true
	}
	err := s.Stream.Err()
	if s.tried || s.seen || !unsendable(err, s.files) {
		if s.first != nil && err == nil {
			s.saw()
		}
		return false
	}
	return s.reopen(err) && s.Next()
}

// reopen asks again without files, keeping the stream it has when it cannot.
func (s *refusingStream) reopen(first error) bool {
	s.tried = true
	bare, _, err := s.r.resolve(s.ctx, s.req, false)
	if err != nil {
		return false
	}
	next, err := s.r.inner.Stream(s.ctx, bare)
	if err != nil {
		return false
	}
	if s.Stream != nil {
		_ = s.Close()
	}
	s.Stream, s.first = next, first
	return true
}

// saw is an answer: the first one after a refusal settles the run's files.
func (s *refusingStream) saw() {
	if !s.seen && s.first != nil {
		s.r.settle(s.first)
	}
	s.seen = true
}

// Err is the stream's own error, but the first answer when the stream without
// files failed before anything was read: that answer is the files' refusal.
func (s *refusingStream) Err() error {
	err := s.Stream.Err()
	switch {
	case s.first == nil, err == nil:
		return refused(err, s.files, s.req.Model.Name)
	case s.seen:
		return err
	}
	return refused(s.first, s.files, s.req.Model.Name)
}

type place struct{ msg, part int }

// resolve fills the bytes of every attachment the request names and keeps
// the files a tool attached whole, the newest first, up to eight files and
// bytesPerRequest of them, text files among them; older ones are named as
// earlier files rather than sent, and one past the bytes left is never read.
// Without files, each image or PDF is named as not sent, never read, and
// text files are still sent as their text. It copies what it changes, so the
// caller's messages, which are the transcript, keep only the references and
// every file. It reports how many files went as files.
func (r *resolving) resolve(ctx context.Context, req agent.Request, withFiles bool) (agent.Request, int, error) {
	var (
		files []place
		refs  bool
	)
	for i, m := range req.Messages {
		for j, c := range m.Content {
			switch {
			case c.Blob == nil:
			case len(c.Blob.Data) > 0:
				files = append(files, place{i, j})
			case strings.HasPrefix(c.Blob.Ref, attachmentRef):
				files = append(files, place{i, j})
				refs = true
			}
		}
	}
	if len(files) == 0 {
		return req, 0, nil
	}
	session, ok := sessionOf(ctx)
	if refs && !ok {
		return req, 0, errors.New("a request names a conversation's files, but no conversation was given to read them from")
	}

	msgs := slices.Clone(req.Messages)
	copied := map[int]bool{}
	set := func(p place, c agent.Content) {
		if !copied[p.msg] {
			msgs[p.msg].Content = slices.Clone(msgs[p.msg].Content)
			copied[p.msg] = true
		}
		msgs[p.msg].Content[p.part] = c
	}
	leftOut := func(p place, name string) {
		set(p, textPart(fmt.Sprintf("[%s: an earlier file, not sent with this request]", name)))
	}

	// counted is every file that went, text or not; sent, the ones that went
	// as files, which a refusal names.
	counted, sent, bytes := 0, 0, 0
	for k := len(files) - 1; k >= 0; k-- {
		p := files[k]
		part := msgs[p.msg].Content[p.part]
		name := part.Blob.Name
		if counted >= filesPerRequest {
			leftOut(p, name)
			continue
		}
		if !withFiles && bareType(part.Blob.MediaType) != "text/plain" {
			set(p, textPart(fmt.Sprintf("[%s: not sent; the model's provider failed on the request carrying it]", name)))
			continue
		}
		if len(part.Blob.Data) == 0 {
			var err error
			part, err = r.fetch(ctx, session, part.Blob, bytesPerRequest-bytes)
			if errors.Is(err, errPastBytes) {
				leftOut(p, name)
				continue
			}
			if err != nil {
				return req, 0, err
			}
		}
		size := len(part.Text)
		if part.Blob != nil {
			size = len(part.Blob.Data)
		}
		if bytes+size > bytesPerRequest {
			leftOut(p, name)
			continue
		}
		bytes += size
		counted++
		set(p, part)
		if part.Blob != nil {
			sent++
		}
	}
	req.Messages = msgs
	return req, sent, nil
}

// errPastBytes is a file larger than what is left of a request's bytes, so
// not read.
var errPastBytes = errors.New("past the request's bytes")

// fetch is the file blob names, read for one request, as a model is sent it:
// a text file as its text. A file larger than room is errPastBytes, and is
// never read from the object store.
func (r *resolving) fetch(ctx context.Context, session uuid.UUID, blob *agent.Blob, room int) (agent.Content, error) {
	id, err := uuid.Parse(strings.TrimPrefix(blob.Ref, attachmentRef))
	if err != nil {
		return agent.Content{}, fmt.Errorf("the file reference %q names no file", blob.Ref)
	}
	row, err := r.store.GetAttachmentInSession(ctx, db.GetAttachmentInSessionParams{ID: id, SessionID: session})
	if errors.Is(err, pgx.ErrNoRows) {
		return textPart(fmt.Sprintf("[%s is no longer available]", blob.Name)), nil
	}
	if err != nil {
		return agent.Content{}, fmt.Errorf("reading the file %s: %w", blob.Name, err)
	}
	text := bareType(row.MediaType) == "text/plain"
	if size := row.SizeBytes; (text && min(size, maxTextBytes) > int64(room)) || (!text && size > int64(room)) {
		return agent.Content{}, errPastBytes
	}
	data, err := r.objects.Get(ctx, row.TeamID, row.ObjectKey)
	if err != nil {
		return agent.Content{}, fmt.Errorf("reading the file %s: %w", blob.Name, err)
	}

	if text {
		return textPart(fileText(row.Filename, data)), nil
	}
	filled := *blob
	filled.Data = data
	return agent.Content{Type: partType(row.MediaType), Blob: &filled}, nil
}

func partType(mediaType string) agent.ContentType {
	if strings.HasPrefix(bareType(mediaType), "image/") {
		return agent.ContentImage
	}
	return agent.ContentFile
}

func fileText(name string, data []byte) string {
	cut := len(data) > maxTextBytes
	if cut {
		data = data[:maxTextBytes]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	out := "The contents of " + name + ":\n\n" + strings.ToValidUTF8(string(data), "�")
	if cut {
		out += fmt.Sprintf("\n\n[cut at %s; the file is longer]", sizeOf(maxTextBytes))
	}
	return out
}

func textPart(s string) agent.Content { return agent.Content{Type: agent.ContentText, Text: s} }

// filesRefused is a request carrying files that the model's provider refused,
// most often because the model reads no images or PDFs. It keeps the
// provider's answer underneath, so a run's compaction still reads it.
type filesRefused struct {
	model string
	err   error
}

func (f *filesRefused) Error() string {
	return fmt.Sprintf("the model %s refused a request carrying files; it may not read images or PDFs: %v", f.model, f.err)
}

func (f *filesRefused) Unwrap() error { return f.err }

func refused(err error, files int, model string) error {
	var answered *agent.ModelError
	if err == nil || files == 0 || !errors.As(err, &answered) {
		return err
	}
	switch {
	case answered.Code == "context_length_exceeded":
		return err
	case answered.Status == http.StatusBadRequest, answered.Status == http.StatusUnprocessableEntity:
		return &filesRefused{model: model, err: err}
	}
	return err
}
