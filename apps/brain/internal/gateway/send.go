package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	replyBatch = 20
	// eventAttempts is how often an event or a file is tried: a broken one
	// must not hold its conversation's answer back through the answer's six.
	eventAttempts = 2
	// replyLease is longer than one post's deadline, and than an upload's
	// (uploadDeadline), so neither outlives the claim it is sent under.
	replyLease     = 2 * time.Minute
	replyAttempts  = 6
	replyReadLimit = 64 << 10

	// A post's deadline is 10 s, and a second more for each 512 KiB it
	// carries, at most a minute: a 25 MiB file is about 34 MB of base64.
	// HTTPPoster sets it per post, so its client should have none.
	postBase    = 10 * time.Second
	postPerByte = 512 << 10
	maxPost     = time.Minute

	// An upload is several posts under one claim, so it has a deadline of
	// its own, short of the lease: past it another sender could claim the
	// row and upload the file again. Each post keeps its own inside it.
	uploadDeadline = replyLease - 20*time.Second
	// maxUploadSteps stops a provider that never says its upload is done;
	// how many steps an upload has is the provider's (Slack's is three).
	maxUploadSteps = 8

	// streamRate is the slowest a file is sent at, in bytes a second: a
	// file's post, its upload and its lease are given its size at this rate.
	// 100 MiB is given 400 s.
	streamRate = 256 << 10
)

// StreamTimeout is how long a post of n bytes that streams a file may take:
// past PostTimeout's minute, as a file is up to 100 MiB.
func StreamTimeout(n int64) time.Duration {
	return postBase + time.Duration(n/streamRate)*time.Second
}

// Timeout is how long posting r may take: PostTimeout for a body held
// whole, StreamTimeout for one that streams a file. A poster that is not
// HTTP, as WhatsApp's, keeps to it too.
func (r OutboundRequest) Timeout() time.Duration {
	if r.Stream != nil {
		return StreamTimeout(r.Length())
	}
	return PostTimeout(len(r.Body))
}

// fileBudget is how long a file reply of size bytes holds its lease: the
// claim's, and the file at streamRate.
func fileBudget(size int64) time.Duration {
	return replyLease + time.Duration(size/streamRate)*time.Second
}

// PostTimeout is how long a post of n bytes may take: a poster that is not
// HTTP, as WhatsApp's, keeps to it too.
func PostTimeout(n int) time.Duration {
	return min(postBase+time.Duration(n/postPerByte)*time.Second, maxPost)
}

// retryAfter is the wait after each failed attempt, the first at index 0.
var retryAfter = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, time.Hour, time.Hour}

type ReplyStore interface {
	ClaimDueReplies(ctx context.Context, arg db.ClaimDueRepliesParams) ([]db.ClaimDueRepliesRow, error)
	ReplyPartSent(ctx context.Context, arg db.ReplyPartSentParams) (int64, error)
	ReplySent(ctx context.Context, arg db.ReplySentParams) (int64, error)
	RetryReply(ctx context.Context, arg db.RetryReplyParams) (int64, error)
	GiveUpReply(ctx context.Context, arg db.GiveUpReplyParams) (int64, error)

	ClaimDueNotices(ctx context.Context, arg db.ClaimDueNoticesParams) ([]db.ClaimDueNoticesRow, error)
	NoticeSent(ctx context.Context, arg db.NoticeSentParams) (int64, error)
	RetryNotice(ctx context.Context, arg db.RetryNoticeParams) (int64, error)
	GiveUpNotice(ctx context.Context, arg db.GiveUpNoticeParams) (int64, error)

	GetReplyFile(ctx context.Context, id int64) (db.GetReplyFileRow, error)
	ExtendReplyLease(ctx context.Context, arg db.ExtendReplyLeaseParams) (int64, error)
}

// FileReader reads a file as it is sent: the attachment store, which
// decrypts it a chunk at a time.
type FileReader interface {
	Open(ctx context.Context, teamID uuid.UUID, key string) (io.ReadCloser, error)
}

// Sender posts the outbox's replies, one conversation's in order, and the
// notices to senders nobody has let in.
type Sender struct {
	store   ReplyStore
	secrets Secrets
	reg     Registry
	poster  Poster
	scope   Scope
	logger  *slog.Logger
	objects FileReader
}

// Poster sends one part a provider built, for a channel. An error is retried
// unless it wraps ErrUnsendable. On a 2xx it calls the request's Accepted,
// when set, with the answer's body: a FileUploader's next step is built from
// it, so a poster for one must.
type Poster interface {
	Post(ctx context.Context, channel uuid.UUID, req OutboundRequest) error
}

// Scope is which channels' replies and notices a sender claims. The zero
// Scope is the worker's: every channel but a socket's.
type Scope struct {
	// Held, when set, is the channels whose socket this process holds; it
	// claims theirs and nothing else. Read at each claim, so a channel taken
	// over or let go changes what the next claim takes.
	Held func() []uuid.UUID
}

func NewSender(s ReplyStore, sec Secrets, reg Registry, poster Poster, scope Scope, logger *slog.Logger) *Sender {
	return &Sender{store: s, secrets: sec, reg: reg, poster: poster, scope: scope, logger: logger}
}

// WithFiles lets the sender send the files runs hand their people. Without
// it a file reply is given up, as nothing could read its bytes.
func (s *Sender) WithFiles(o FileReader) *Sender {
	s.objects = o
	return s
}

// claimScope is the scope as a claim's arguments.
func (s *Sender) claimScope() (heldOnly bool, held []uuid.UUID, sockets []string) {
	if s.scope.Held != nil {
		return true, s.scope.Held(), nil
	}
	return false, nil, s.reg.Sockets()
}

// Drain sends every reply and notice due now and returns how many went. Each
// claim is leased, so a batch never comes back to the same row and the loops
// end.
func (s *Sender) Drain(ctx context.Context) (int, error) {
	replies, err := s.drainReplies(ctx)
	if err != nil {
		return replies, err
	}
	notices, err := s.drainNotices(ctx)
	return replies + notices, err
}

func (s *Sender) drainReplies(ctx context.Context) (int, error) {
	sent := 0
	for {
		heldOnly, held, sockets := s.claimScope()
		rows, err := s.store.ClaimDueReplies(ctx, db.ClaimDueRepliesParams{
			LeaseSeconds: replyLease.Seconds(), BatchSize: replyBatch,
			HeldOnly: heldOnly, Held: held, Sockets: sockets,
		})
		if err != nil {
			return sent, fmt.Errorf("claiming replies: %w", err)
		}
		for _, row := range rows {
			ok, err := s.send(ctx, s.reply(row))
			if err != nil {
				return sent, err
			}
			if ok {
				sent++
			}
		}
		if len(rows) < replyBatch {
			return sent, nil
		}
	}
}

func (s *Sender) drainNotices(ctx context.Context) (int, error) {
	sent := 0
	for {
		heldOnly, held, sockets := s.claimScope()
		rows, err := s.store.ClaimDueNotices(ctx, db.ClaimDueNoticesParams{
			LeaseSeconds: replyLease.Seconds(), BatchSize: replyBatch,
			HeldOnly: heldOnly, Held: held, Sockets: sockets,
		})
		if err != nil {
			return sent, fmt.Errorf("claiming notices: %w", err)
		}
		for _, row := range rows {
			ok, err := s.send(ctx, s.notice(row))
			if err != nil {
				return sent, err
			}
			if ok {
				sent++
			}
		}
		if len(rows) < replyBatch {
			return sent, nil
		}
	}
}

// ErrUnsendable is a reply no retry can send: the answer would be the same.
// A Poster wraps it for a part it can never send.
var ErrUnsendable = errors.New("unsendable")

// errLeaseLost is a reply another sender now holds: it marks the row, not us.
var errLeaseLost = errors.New("lease lost")

// outgoing is one claimed row, a reply or a notice: how to build its
// requests, and how to mark how its attempt went. Every mark matches on the
// claim's attempts and reports the rows it changed; 0 is a lost lease.
type outgoing struct {
	what      string // "reply" or "notice", for the log and the errors
	id        int64
	attempts  int32
	tries     int32 // how many attempts it is given
	partsSent int32
	teamID    uuid.UUID
	channelID uuid.UUID
	provider  string
	// load reads what the requests carry beyond the row, a file reply's
	// file, before they are built: an error is retried unless it wraps
	// ErrUnsendable. Nil for everything else.
	load  func(ctx context.Context) (*OutFile, error)
	build func(creds Credentials, file *OutFile) ([]OutboundRequest, error)
	// step builds a file reply's requests one at a time, from the answers
	// before it, for a provider that is a FileUploader. When set, build is
	// not called.
	step     func(creds Credentials, file *OutFile, answers [][]byte) (OutboundRequest, bool, error)
	sent     func(ctx context.Context) (int64, error)
	partSent func(ctx context.Context, parts int32) (int64, error)
	extend   func(ctx context.Context, lease time.Duration) (int64, error)
	retry    func(ctx context.Context, msg string, after time.Duration) (int64, error)
	giveUp   func(ctx context.Context, msg string) (int64, error)
}

// replyID is the id a reply is sent under, for a receiver to drop a repeat,
// and how many times it is tried. A run's final reply, its answer or the
// apology in its place, carries the run's id: replies_one_per_run allows one
// of the two. A run event's reply carries its event. Any other reply, such as
// the notice that an answer in the chat was taken, carries its own row: under
// the run's id it would be taken for the answer, and the answer after it
// dropped as its repeat.
func replyID(row db.ClaimDueRepliesRow) (string, int32) {
	run := row.RunID.String()
	switch {
	case row.EventSeq != nil:
		return run + ":" + strconv.Itoa(int(*row.EventSeq)), eventAttempts
	case row.Kind == "answer" || row.Kind == "apology":
		return run, replyAttempts
	case row.Kind == "file":
		// Twice, like a step: a file that cannot go must not hold the answer
		// behind it for hours.
		return run + ":file:" + strconv.FormatInt(row.ID, 10), eventAttempts
	default:
		return run + ":" + row.Kind + ":" + strconv.FormatInt(row.ID, 10), replyAttempts
	}
}

func (s *Sender) reply(row db.ClaimDueRepliesRow) outgoing {
	at := db.ReplySentParams{ID: row.ID, Attempts: row.Attempts}
	id, tries := replyID(row)
	ch := Channel{ID: row.ChannelID, TeamID: row.TeamID, ReplyURL: deref(row.ReplyUrl)}
	outbound := func(file *OutFile) (Outbound, error) {
		out := Outbound{
			ID:        id,
			Session:   Session{Ref: deref(row.SessionRef), Kind: SessionKind(row.SessionKind)},
			InReplyTo: deref(row.InReplyTo),
			Kind:      row.Kind,
			Text:      row.Text,
			File:      file,
		}
		return out, withDetail(&out, row.Detail)
	}
	o := outgoing{
		what: "reply", id: row.ID, attempts: row.Attempts, tries: tries, partsSent: row.PartsSent,
		teamID: row.TeamID, channelID: row.ChannelID, provider: row.Provider,
		load: s.fileOf(row),
		build: func(creds Credentials, file *OutFile) ([]OutboundRequest, error) {
			rp, ok := s.reg.Replier(row.Provider)
			if !ok {
				return nil, fmt.Errorf("the %s provider sends no replies", row.Provider)
			}
			out, err := outbound(file)
			if err != nil {
				return nil, err
			}
			return rp.Reply(ch, out, creds, time.Now())
		},
		sent: func(ctx context.Context) (int64, error) { return s.store.ReplySent(ctx, at) },
		partSent: func(ctx context.Context, parts int32) (int64, error) {
			return s.store.ReplyPartSent(ctx, db.ReplyPartSentParams{ID: row.ID, Attempts: row.Attempts, PartsSent: parts})
		},
		extend: func(ctx context.Context, lease time.Duration) (int64, error) {
			return s.store.ExtendReplyLease(ctx, db.ExtendReplyLeaseParams{ID: row.ID, Attempts: row.Attempts, LeaseSeconds: lease.Seconds()})
		},
		retry: func(ctx context.Context, msg string, after time.Duration) (int64, error) {
			return s.store.RetryReply(ctx, db.RetryReplyParams{
				ID: row.ID, Attempts: row.Attempts, Error: msg, RetrySeconds: after.Seconds(),
			})
		},
		giveUp: func(ctx context.Context, msg string) (int64, error) {
			return s.store.GiveUpReply(ctx, db.GiveUpReplyParams{ID: row.ID, Attempts: row.Attempts, Error: msg})
		},
	}
	if u, ok := s.reg.Uploader(row.Provider); ok && row.Kind == "file" {
		o.step = func(creds Credentials, file *OutFile, answers [][]byte) (OutboundRequest, bool, error) {
			out, err := outbound(file)
			if err != nil {
				return OutboundRequest{}, false, err
			}
			return u.FileRequest(ch, out, creds, answers)
		}
	}
	return o
}

// withDetail reads what a prompt or a notice carries beyond its text. One
// that cannot be read is final: the same bytes fail the same way.
func withDetail(out *Outbound, detail []byte) error {
	if len(detail) == 0 {
		return nil
	}
	switch out.Kind {
	case "approval":
		var p approval.Prompt
		if err := json.Unmarshal(detail, &p); err != nil {
			return fmt.Errorf("reading the prompt's requests: %w", err)
		}
		out.Prompt = &p
	case "notice":
		var n ReplyNotice
		if err := json.Unmarshal(detail, &n); err != nil {
			return fmt.Errorf("reading the notice: %w", err)
		}
		out.Notice = &n
	}
	return nil
}

// fileOf is a file reply's file, just before it is sent: its size and how
// to read it, never its bytes, but for a WholeFiles provider's. A file gone
// since, one over what the channel takes, or a provider that sends none, is
// given up; a store that fails is tried again.
func (s *Sender) fileOf(row db.ClaimDueRepliesRow) func(ctx context.Context) (*OutFile, error) {
	if row.Kind != "file" {
		return nil
	}
	return func(ctx context.Context) (*OutFile, error) {
		switch {
		case !s.reg.SendsFiles(row.Provider):
			return nil, fmt.Errorf("%w: the %s provider sends no files", ErrUnsendable, row.Provider)
		case s.objects == nil:
			return nil, fmt.Errorf("%w: this sender reads no files", ErrUnsendable)
		}
		f, err := s.store.GetReplyFile(ctx, row.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: the file is no longer in the conversation", ErrUnsendable)
		}
		if err != nil {
			return nil, fmt.Errorf("reading the reply's file: %w", err)
		}
		// send_file refuses a file over what the channel takes before it is
		// queued; this is for one queued before the limit was the channel's.
		if most := s.reg.MaxFile(row.Provider); f.SizeBytes > most {
			return nil, fmt.Errorf("%w: the file %s is %d bytes, over the %d this channel takes",
				ErrUnsendable, f.Filename, f.SizeBytes, most)
		}
		if f.SizeBytes <= 0 {
			return nil, fmt.Errorf("%w: the file %s is empty", ErrUnsendable, f.Filename)
		}
		var detail struct {
			Caption string `json:"caption"`
		}
		if len(row.Detail) > 0 {
			if err := json.Unmarshal(row.Detail, &detail); err != nil {
				return nil, fmt.Errorf("%w: reading the file's caption: %w", ErrUnsendable, err)
			}
		}
		out := &OutFile{Name: f.Filename, MediaType: f.MediaType, Caption: detail.Caption, Size: f.SizeBytes}
		out.open = func(ctx context.Context) (io.ReadCloser, error) {
			r, err := s.objects.Open(ctx, f.TeamID, f.ObjectKey)
			if err != nil {
				return nil, readFailed(f.Filename, err)
			}
			return &fileBody{r: r, name: f.Filename}, nil
		}
		if s.reg.WholeFiles(row.Provider) {
			if out.Data, err = whole(ctx, out); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
}

// whole is a file's bytes, for a provider that builds its message from them:
// exactly its recorded size, which MaxFile bounds.
func whole(ctx context.Context, f *OutFile) ([]byte, error) {
	r, err := f.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, f.Size+1))
	switch {
	case err != nil:
		return nil, err
	case int64(len(data)) != f.Size:
		return nil, fmt.Errorf("%w: the file %s is %d bytes, not the %d recorded", ErrUnsendable, f.Name, len(data), f.Size)
	}
	return data, nil
}

// readFailed is a file that could not be read: given up when the same read
// would fail again, a file gone or damaged, and tried again otherwise.
func readFailed(name string, err error) error {
	switch {
	case errors.Is(err, objects.ErrNotFound):
		return fmt.Errorf("%w: the file %s is no longer stored", ErrUnsendable, name)
	case errors.Is(err, objects.ErrCorrupt), errors.Is(err, objects.ErrWrongTeam):
		return fmt.Errorf("%w: the file %s cannot be read: %w", ErrUnsendable, name, err)
	}
	return fmt.Errorf("reading the file %s: %w", name, err)
}

// fileBody is a file as it is sent, its failures said as readFailed says
// them: the client's error holds the body's, over HTTP/1 and HTTP/2 alike,
// so a damaged file is final however it is posted.
type fileBody struct {
	r    io.ReadCloser
	name string
	err  error
}

func (b *fileBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		if b.err == nil {
			b.err = readFailed(b.name, err)
		}
		return n, b.err
	}
	return n, err
}

func (b *fileBody) Close() error { return b.r.Close() }

// notice is one request: a notice is a sentence, never split, so it has no
// parts to record and a retry sends it whole.
func (s *Sender) notice(row db.ClaimDueNoticesRow) outgoing {
	at := db.NoticeSentParams{ID: row.ID, Attempts: row.Attempts}
	return outgoing{
		what: "notice", id: row.ID, attempts: row.Attempts, tries: replyAttempts,
		teamID: row.TeamID, channelID: row.ChannelID, provider: row.Provider,
		build: func(creds Credentials, _ *OutFile) ([]OutboundRequest, error) {
			n, ok := s.reg.Notifier(row.Provider)
			if !ok {
				return nil, fmt.Errorf("the %s provider sends no notices", row.Provider)
			}
			kind := NoticeKind(row.Kind)
			req, err := n.Notice(Channel{ID: row.ChannelID, TeamID: row.TeamID}, Notice{
				ID:        "notice:" + strconv.FormatInt(row.ID, 10),
				Kind:      kind,
				Session:   Session{Ref: row.SessionRef, Kind: SessionKind(row.SessionKind)},
				InReplyTo: row.InReplyTo,
				Sender:    Author{Ref: row.SenderRef, DisplayName: row.SenderName},
				Text:      NoticeText(kind),
			}, creds)
			if err != nil {
				return nil, err
			}
			return []OutboundRequest{req}, nil
		},
		sent:     func(ctx context.Context) (int64, error) { return s.store.NoticeSent(ctx, at) },
		partSent: func(context.Context, int32) (int64, error) { return 1, nil },
		retry: func(ctx context.Context, msg string, after time.Duration) (int64, error) {
			return s.store.RetryNotice(ctx, db.RetryNoticeParams{
				ID: row.ID, Attempts: row.Attempts, Error: msg, RetrySeconds: after.Seconds(),
			})
		},
		giveUp: func(ctx context.Context, msg string) (int64, error) {
			return s.store.GiveUpNotice(ctx, db.GiveUpNoticeParams{ID: row.ID, Attempts: row.Attempts, Error: msg})
		},
	}
}

// send makes one attempt and marks how it went. Only a failed mark is an
// error; a failed send is the row's to record. A mark matching no row lost
// its lease to another sender, and that sender's mark is the one that counts.
func (s *Sender) send(ctx context.Context, o outgoing) (bool, error) {
	err := s.attempt(ctx, o)
	switch {
	case errors.Is(err, errLeaseLost):
		return false, nil
	case err == nil:
		if _, markErr := o.sent(ctx); markErr != nil {
			return false, fmt.Errorf("marking %s %d sent: %w", o.what, o.id, markErr)
		}
		return true, nil
	case errors.Is(err, ErrUnsendable) || o.attempts >= o.tries:
		s.logger.WarnContext(ctx, o.what+" not sent; gave up",
			o.what, o.id, "channel", o.channelID, "attempts", o.attempts, "err", err)
		if _, markErr := o.giveUp(ctx, err.Error()); markErr != nil {
			return false, fmt.Errorf("giving up on %s %d: %w", o.what, o.id, markErr)
		}
		return false, nil
	default:
		after := retryAfter[min(int(o.attempts), len(retryAfter))-1]
		if _, markErr := o.retry(ctx, err.Error(), after); markErr != nil {
			return false, fmt.Errorf("retrying %s %d: %w", o.what, o.id, markErr)
		}
		return false, nil
	}
}

// attempt builds the requests through the provider and posts them.
func (s *Sender) attempt(ctx context.Context, o outgoing) error {
	p, ok := s.reg.Get(o.provider)
	if !ok {
		return fmt.Errorf("%w: no %s provider is registered", ErrUnsendable, o.provider)
	}

	creds, err := s.credentials(ctx, o, p.Keys())
	if err != nil {
		return err
	}

	if o.step != nil && o.partsSent > 0 {
		// Uploaded, and only its sent mark was lost: nothing to read again,
		// and a file deleted since must not give up what was delivered.
		return nil
	}
	var file *OutFile
	if o.load != nil {
		if file, err = o.load(ctx); err != nil {
			return err
		}
	}
	if file != nil {
		// A large file outlasts the claim's lease: held now for what it may
		// take, so no other sender takes the reply while it is sent.
		if err := o.hold(ctx, fileBudget(file.Size)); err != nil {
			return err
		}
	}
	if o.step != nil {
		return s.upload(ctx, o, creds, file)
	}
	parts, err := o.build(creds, file)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsendable, err)
	}
	if len(parts) == 0 {
		return fmt.Errorf("%w: the %s provider built no request", ErrUnsendable, o.provider)
	}
	// Each part is recorded as it goes, so a retry starts at the first one
	// not sent rather than sending the earlier ones twice.
	for i := int(o.partsSent); i < len(parts); i++ {
		if err := s.poster.Post(ctx, o.channelID, withFile(parts[i], file)); err != nil {
			return err
		}
		if err := o.recordPart(ctx, int32(i+1)); err != nil { //nolint:gosec // G115: parts are few
			return err
		}
	}
	return nil
}

// withFile gives a request's Stream the file it streams.
func withFile(req OutboundRequest, file *OutFile) OutboundRequest {
	if req.Stream == nil || file == nil {
		return req
	}
	st := *req.Stream
	st.Open = file.open
	req.Stream = &st
	return req
}

// hold pushes o's lease out to lease; none matched is a lease another sender
// now holds.
func (o outgoing) hold(ctx context.Context, lease time.Duration) error {
	n, err := o.extend(ctx, lease)
	switch {
	case err != nil:
		return fmt.Errorf("holding %s %d for its file: %w", o.what, o.id, err)
	case n == 0:
		return errLeaseLost
	}
	return nil
}

// recordPart marks the first parts of o sent; none matched is a lease
// another sender now holds.
func (o outgoing) recordPart(ctx context.Context, parts int32) error {
	n, err := o.partSent(ctx, parts)
	switch {
	case err != nil:
		return fmt.Errorf("recording part %d of %s %d: %w", parts, o.what, o.id, err)
	case n == 0:
		return errLeaseLost
	}
	return nil
}

// upload sends a file in the provider's steps, each built from the answers
// before it. The whole upload is one part: a failure at any step starts again
// from the first on the retry, which a platform that shares a file only once
// its upload completes shows once; one whose last answer was lost, twice.
func (s *Sender) upload(ctx context.Context, o outgoing, creds Credentials, file *OutFile) error {
	// The posts keep to the deadline; the mark after them does not, so one
	// landing at its edge is still recorded rather than shared again.
	parent := ctx
	deadline := uploadDeadline
	if file != nil {
		deadline = max(deadline, fileBudget(file.Size)-(replyLease-uploadDeadline))
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var answers [][]byte
	for range maxUploadSteps {
		req, final, err := o.step(creds, file, answers)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnsendable, err)
		}
		// The provider's own reading still decides; this keeps the answer
		// for the step after.
		var answer []byte
		accept := req.Accepted
		req.Accepted = func(status int, body []byte) error {
			answer = bytes.Clone(body)
			if accept != nil {
				return accept(status, body)
			}
			return nil
		}
		if err := s.poster.Post(ctx, o.channelID, withFile(req, file)); err != nil {
			return err
		}
		if final {
			return o.recordPart(parent, 1)
		}
		answers = append(answers, answer)
	}
	return fmt.Errorf("%w: the %s provider's upload took more than %d requests", ErrUnsendable, o.provider, maxUploadSteps)
}

func (s *Sender) credentials(ctx context.Context, o outgoing, keys []string) (Credentials, error) {
	path := secrets.Path(o.teamID, secrets.KindChannel, o.channelID)
	creds := make(Credentials, len(keys))
	for _, key := range keys {
		v, err := s.secrets.Get(ctx, path, key)
		if err != nil {
			return nil, fmt.Errorf("reading the channel's %s: %w", key, err)
		}
		creds[key] = v
	}
	return creds, nil
}

// HTTPPoster posts each part with client, which must refuse what a team may
// not reach at dial time and follow no redirect: a 302 would turn the POST
// into a GET somewhere else. Each post has a deadline of its own, by its
// size (PostTimeout), so the poster drops the client's Timeout from its own
// copy: it would cut a file short however long its deadline.
func HTTPPoster(client *http.Client) Poster {
	c := *client
	c.Timeout = 0
	return httpPoster{client: &c}
}

type httpPoster struct{ client *http.Client }

func (p httpPoster) Post(ctx context.Context, _ uuid.UUID, out OutboundRequest) error {
	// Held until the answer is read: the deadline covers the whole exchange.
	ctx, cancel := context.WithTimeout(ctx, out.Timeout())
	defer cancel()
	body, err := bodyOf(ctx, out)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, out.URL, body)
	if err != nil {
		return fmt.Errorf("%w: the reply URL: %w", ErrUnsendable, err)
	}
	req.Header = out.Header.Clone()
	// Exact, so a file the store hands back short fails the request rather
	// than reaching the platform as a whole one.
	req.ContentLength = out.Length()

	resp, err := p.client.Do(req) //nolint:gosec // G704: the client's dialer refuses what the team may not reach
	if err != nil {
		// Not the *url.Error itself, which quotes the URL into last_error.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("posting the reply: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, replyReadLimit))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the reply URL answered %d", resp.StatusCode)
	}
	if out.Accepted != nil {
		return out.Accepted(resp.StatusCode, answer)
	}
	return nil
}

// requestBody is a request's body: Body, then the file and Tail when it
// streams one.
type requestBody struct {
	io.Reader
	file io.ReadCloser
}

func bodyOf(ctx context.Context, out OutboundRequest) (*requestBody, error) {
	if out.Stream == nil {
		return &requestBody{Reader: bytes.NewReader(out.Body)}, nil
	}
	if out.Stream.Open == nil {
		return nil, fmt.Errorf("%w: a request streams a file it was not given", ErrUnsendable)
	}
	file, err := out.Stream.Open(ctx)
	if err != nil {
		return nil, err
	}
	return &requestBody{
		Reader: io.MultiReader(bytes.NewReader(out.Body), file, bytes.NewReader(out.Stream.Tail)),
		file:   file,
	}, nil
}

func (b *requestBody) Close() error {
	if b.file == nil {
		return nil
	}
	return b.file.Close()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
