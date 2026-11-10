package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	replyBatch     = 20
	replyLease     = 2 * time.Minute // longer than the client's 10 s timeout
	replyAttempts  = 6
	replyReadLimit = 64 << 10
)

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
}

// Sender posts the outbox's replies, one conversation's in order, and the
// notices to senders nobody has let in.
type Sender struct {
	store   ReplyStore
	secrets Secrets
	reg     Registry
	client  *http.Client
	logger  *slog.Logger
}

// client must refuse what a team may not reach, at dial time, and follow no
// redirect: a 302 would turn the POST into a GET somewhere else.
func NewSender(s ReplyStore, sec Secrets, reg Registry, client *http.Client, logger *slog.Logger) *Sender {
	return &Sender{store: s, secrets: sec, reg: reg, client: client, logger: logger}
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
		rows, err := s.store.ClaimDueReplies(ctx, db.ClaimDueRepliesParams{
			LeaseSeconds: replyLease.Seconds(), BatchSize: replyBatch,
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
		rows, err := s.store.ClaimDueNotices(ctx, db.ClaimDueNoticesParams{
			LeaseSeconds: replyLease.Seconds(), BatchSize: replyBatch,
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

// errUnsendable is a reply no retry can send: the answer would be the same.
var errUnsendable = errors.New("unsendable")

// errLeaseLost is a reply another sender now holds: it marks the row, not us.
var errLeaseLost = errors.New("lease lost")

// outgoing is one claimed row, a reply or a notice: how to build its
// requests, and how to mark how its attempt went. Every mark matches on the
// claim's attempts and reports the rows it changed; 0 is a lost lease.
type outgoing struct {
	what      string // "reply" or "notice", for the log and the errors
	id        int64
	attempts  int32
	partsSent int32
	teamID    uuid.UUID
	channelID uuid.UUID
	provider  string
	build     func(creds Credentials) ([]OutboundRequest, error)
	sent      func(ctx context.Context) (int64, error)
	partSent  func(ctx context.Context, parts int32) (int64, error)
	retry     func(ctx context.Context, msg string, after time.Duration) (int64, error)
	giveUp    func(ctx context.Context, msg string) (int64, error)
}

func (s *Sender) reply(row db.ClaimDueRepliesRow) outgoing {
	at := db.ReplySentParams{ID: row.ID, Attempts: row.Attempts}
	return outgoing{
		what: "reply", id: row.ID, attempts: row.Attempts, partsSent: row.PartsSent,
		teamID: row.TeamID, channelID: row.ChannelID, provider: row.Provider,
		build: func(creds Credentials) ([]OutboundRequest, error) {
			rp, ok := s.reg.Replier(row.Provider)
			if !ok {
				return nil, fmt.Errorf("the %s provider sends no replies", row.Provider)
			}
			ch := Channel{ID: row.ChannelID, TeamID: row.TeamID, ReplyURL: deref(row.ReplyUrl)}
			return rp.Reply(ch, Outbound{
				ID:        row.RunID.String(),
				Session:   Session{Ref: deref(row.SessionRef), Kind: SessionKind(row.SessionKind)},
				InReplyTo: deref(row.InReplyTo),
				Kind:      row.Kind,
				Text:      row.Text,
			}, creds, time.Now())
		},
		sent: func(ctx context.Context) (int64, error) { return s.store.ReplySent(ctx, at) },
		partSent: func(ctx context.Context, parts int32) (int64, error) {
			return s.store.ReplyPartSent(ctx, db.ReplyPartSentParams{ID: row.ID, Attempts: row.Attempts, PartsSent: parts})
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
}

// notice is one request: a notice is a sentence, never split, so it has no
// parts to record and a retry sends it whole.
func (s *Sender) notice(row db.ClaimDueNoticesRow) outgoing {
	at := db.NoticeSentParams{ID: row.ID, Attempts: row.Attempts}
	return outgoing{
		what: "notice", id: row.ID, attempts: row.Attempts,
		teamID: row.TeamID, channelID: row.ChannelID, provider: row.Provider,
		build: func(creds Credentials) ([]OutboundRequest, error) {
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
	case errors.Is(err, errUnsendable) || o.attempts >= replyAttempts:
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
		return fmt.Errorf("%w: no %s provider is registered", errUnsendable, o.provider)
	}

	creds, err := s.credentials(ctx, o, p.Keys())
	if err != nil {
		return err
	}

	parts, err := o.build(creds)
	if err != nil {
		return fmt.Errorf("%w: %w", errUnsendable, err)
	}
	if len(parts) == 0 {
		return fmt.Errorf("%w: the %s provider built no request", errUnsendable, o.provider)
	}
	// Each part is recorded as it goes, so a retry starts at the first one
	// not sent rather than sending the earlier ones twice.
	for i := int(o.partsSent); i < len(parts); i++ {
		if err := s.post(ctx, parts[i]); err != nil {
			return err
		}
		n, err := o.partSent(ctx, int32(i+1)) //nolint:gosec // G115: parts are few
		switch {
		case err != nil:
			return fmt.Errorf("recording part %d of %s %d: %w", i+1, o.what, o.id, err)
		case n == 0:
			return errLeaseLost
		}
	}
	return nil
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

func (s *Sender) post(ctx context.Context, out OutboundRequest) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, out.URL, bytes.NewReader(out.Body))
	if err != nil {
		return fmt.Errorf("%w: the reply URL: %w", errUnsendable, err)
	}
	req.Header = out.Header.Clone()

	resp, err := s.client.Do(req) //nolint:gosec // G704: the client's dialer refuses what the team may not reach
	if err != nil {
		// Not the *url.Error itself, which quotes the URL into last_error.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("posting the reply: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, replyReadLimit))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the reply URL answered %d", resp.StatusCode)
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
