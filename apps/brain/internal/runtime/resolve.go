package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// maxTextBytes is how much of a text file a model is sent.
const maxTextBytes = 100 << 10

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

type resolving struct {
	inner   agent.ModelClient
	store   AttachmentReader
	objects Objects
}

func (r *resolving) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	sent, files, err := r.resolve(ctx, req)
	if err != nil {
		return agent.Response{}, err
	}
	resp, err := r.inner.Complete(ctx, sent)
	return resp, refused(err, files, req.Model.Name)
}

func (r *resolving) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	sent, files, err := r.resolve(ctx, req)
	if err != nil {
		return nil, err
	}
	s, err := r.inner.Stream(ctx, sent)
	if err != nil {
		return nil, refused(err, files, req.Model.Name)
	}
	return &refusingStream{Stream: s, files: files, model: req.Model.Name}, nil
}

type refusingStream struct {
	agent.Stream
	files int
	model string
}

func (s *refusingStream) Err() error { return refused(s.Stream.Err(), s.files, s.model) }

type place struct{ msg, part int }

// resolve fills the bytes of every attachment the request names, newest
// eight first; older ones are said to be earlier files rather than sent
// again. It copies what it changes, so the caller's messages, which are the
// transcript, keep only the references.
func (r *resolving) resolve(ctx context.Context, req agent.Request) (agent.Request, int, error) {
	var named []place
	for i, m := range req.Messages {
		for j, c := range m.Content {
			if c.Blob != nil && len(c.Blob.Data) == 0 && strings.HasPrefix(c.Blob.Ref, attachmentRef) {
				named = append(named, place{i, j})
			}
		}
	}
	if len(named) == 0 {
		return req, 0, nil
	}
	session, ok := sessionOf(ctx)
	if !ok {
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

	sent := 0
	for k, p := range named {
		blob := msgs[p.msg].Content[p.part].Blob
		if k < len(named)-filesPerRequest {
			set(p, textPart(fmt.Sprintf("[%s: a file shown earlier, not sent again]", blob.Name)))
			continue
		}
		part, err := r.fetch(ctx, session, blob)
		if err != nil {
			return req, 0, err
		}
		set(p, part)
		if part.Blob != nil {
			sent++
		}
	}
	req.Messages = msgs
	return req, sent, nil
}

func (r *resolving) fetch(ctx context.Context, session uuid.UUID, blob *agent.Blob) (agent.Content, error) {
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
	data, err := r.objects.Get(ctx, row.TeamID, row.ObjectKey)
	if err != nil {
		return agent.Content{}, fmt.Errorf("reading the file %s: %w", blob.Name, err)
	}

	if bareType(row.MediaType) == "text/plain" {
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
