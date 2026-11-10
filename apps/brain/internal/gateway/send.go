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
	"time"

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
}

// Sender posts the outbox's replies, one conversation's in order.
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

// Drain sends every reply due now and returns how many went. Each claim is
// leased, so a batch never comes back to the same row and the loop ends.
func (s *Sender) Drain(ctx context.Context) (int, error) {
	sent := 0
	for {
		rows, err := s.store.ClaimDueReplies(ctx, db.ClaimDueRepliesParams{
			LeaseSeconds: replyLease.Seconds(), BatchSize: replyBatch,
		})
		if err != nil {
			return sent, fmt.Errorf("claiming replies: %w", err)
		}
		for _, row := range rows {
			ok, err := s.send(ctx, row)
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

// send makes one attempt and marks how it went. Only a failed mark is an
// error; a failed send is the row's to record. A mark matching no row lost
// its lease to another sender, and that sender's mark is the one that counts.
func (s *Sender) send(ctx context.Context, row db.ClaimDueRepliesRow) (bool, error) {
	err := s.attempt(ctx, row)
	switch {
	case errors.Is(err, errLeaseLost):
		return false, nil
	case err == nil:
		if _, markErr := s.store.ReplySent(ctx, db.ReplySentParams{ID: row.ID, Attempts: row.Attempts}); markErr != nil {
			return false, fmt.Errorf("marking reply %d sent: %w", row.ID, markErr)
		}
		return true, nil
	case errors.Is(err, errUnsendable) || row.Attempts >= replyAttempts:
		s.logger.WarnContext(ctx, "reply not sent; gave up",
			"reply", row.ID, "run", row.RunID, "channel", row.ChannelID, "attempts", row.Attempts, "err", err)
		if _, markErr := s.store.GiveUpReply(ctx, db.GiveUpReplyParams{
			ID: row.ID, Attempts: row.Attempts, Error: err.Error(),
		}); markErr != nil {
			return false, fmt.Errorf("giving up on reply %d: %w", row.ID, markErr)
		}
		return false, nil
	default:
		if _, markErr := s.store.RetryReply(ctx, db.RetryReplyParams{
			ID: row.ID, Attempts: row.Attempts, Error: err.Error(),
			RetrySeconds: retryAfter[min(int(row.Attempts), len(retryAfter))-1].Seconds(),
		}); markErr != nil {
			return false, fmt.Errorf("retrying reply %d: %w", row.ID, markErr)
		}
		return false, nil
	}
}

// attempt builds the request through the provider and posts it.
func (s *Sender) attempt(ctx context.Context, row db.ClaimDueRepliesRow) error {
	p, _ := s.reg.Get(row.Provider)
	rp, ok := s.reg.Replier(row.Provider)
	if !ok {
		return fmt.Errorf("%w: the %s provider sends no replies", errUnsendable, row.Provider)
	}

	creds, err := s.credentials(ctx, row, p.Keys())
	if err != nil {
		return err
	}

	ch := Channel{ID: row.ChannelID, TeamID: row.TeamID, ReplyURL: deref(row.ReplyUrl)}
	out := Outbound{
		ID:        row.RunID.String(),
		Session:   Session{Ref: deref(row.SessionRef), Kind: SessionKind(row.SessionKind)},
		InReplyTo: deref(row.InReplyTo),
		Kind:      row.Kind,
		Text:      row.Text,
	}
	parts, err := rp.Reply(ch, out, creds, time.Now())
	if err != nil {
		return fmt.Errorf("%w: %w", errUnsendable, err)
	}
	if len(parts) == 0 {
		return fmt.Errorf("%w: the %s provider built no request", errUnsendable, row.Provider)
	}
	// Each part is recorded as it goes, so a retry starts at the first one
	// not sent rather than sending the earlier ones twice.
	for i := int(row.PartsSent); i < len(parts); i++ {
		if err := s.post(ctx, parts[i]); err != nil {
			return err
		}
		if err := s.partSent(ctx, row, i+1); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sender) partSent(ctx context.Context, row db.ClaimDueRepliesRow, sent int) error {
	n, err := s.store.ReplyPartSent(ctx, db.ReplyPartSentParams{
		ID: row.ID, Attempts: row.Attempts, PartsSent: int32(sent), //nolint:gosec // G115: parts are few
	})
	switch {
	case err != nil:
		return fmt.Errorf("recording part %d of reply %d: %w", sent, row.ID, err)
	case n == 0:
		return errLeaseLost
	}
	return nil
}

func (s *Sender) credentials(ctx context.Context, row db.ClaimDueRepliesRow, keys []string) (Credentials, error) {
	path := secrets.Path(row.TeamID, secrets.KindChannel, row.ChannelID)
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
