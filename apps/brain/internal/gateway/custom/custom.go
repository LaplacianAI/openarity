package custom

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

const (
	HeaderTimestamp = "X-Openarity-Timestamp"
	HeaderSignature = "X-Openarity-Signature"
)

const freshness = 5 * time.Minute

func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v1:%s:", timestamp)
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

type body struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	SentAt  string `json:"sent_at"`
	ReplyTo string `json:"reply_to"`

	Author struct {
		Ref         string `json:"ref"`
		DisplayName string `json:"display_name"`
		IsBot       bool   `json:"is_bot"`
	} `json:"author"`

	Mentions []struct {
		SenderRef   string `json:"sender_ref"`
		DisplayName string `json:"display_name"`
		IsUs        bool   `json:"is_us"`
	} `json:"mentions"`

	Session struct {
		Ref  string `json:"ref"`
		Kind string `json:"kind"`
	} `json:"session"`

	Attachments []attachment `json:"attachments"`
}

type attachment struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	MediaType     string `json:"media_type"`
	ContentBase64 string `json:"content_base64"`
}

type provider struct{}

func New() gateway.Provider { return provider{} }

func (provider) Name() string { return "custom" }

func (provider) Routes() []gateway.Route {
	return []gateway.Route{{Method: http.MethodPost}}
}

func (provider) Keys() []string { return []string{gateway.KeySigning} }

func (provider) Verify(req gateway.WebhookRequest, creds gateway.Credentials) error {
	secret := creds.Get(gateway.KeySigning)
	if secret == "" {
		return errors.New("custom: no signing secret")
	}

	timestamp := req.Header.Get(HeaderTimestamp)
	if timestamp == "" {
		return errors.New("custom: no " + HeaderTimestamp)
	}
	at, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("custom: unreadable %s", HeaderTimestamp)
	}
	if drift := req.ReceivedAt.Sub(time.Unix(at, 0)); drift > freshness || drift < -freshness {
		return errors.New("custom: stale delivery")
	}

	want := Sign(secret, timestamp, req.Body)
	if !hmac.Equal([]byte(req.Header.Get(HeaderSignature)), []byte(want)) {
		return errors.New("custom: bad signature")
	}
	return nil
}

func (provider) Parse(req gateway.WebhookRequest) (gateway.Result, error) {
	var b body
	if err := json.Unmarshal(req.Body, &b); err != nil {
		return gateway.Result{}, fmt.Errorf("custom: %w", err)
	}

	sentAt := req.ReceivedAt
	if b.SentAt != "" {
		parsed, err := time.Parse(time.RFC3339, b.SentAt)
		if err != nil {
			return gateway.Result{}, fmt.Errorf("custom: sent_at: %w", err)
		}
		sentAt = parsed
	}

	return gateway.Result{Messages: []gateway.Inbound{b.inbound(sentAt)}}, nil
}

func (provider) FetchAttachment(
	_ context.Context, req gateway.WebhookRequest, ref string, _ gateway.Credentials,
) ([]byte, error) {
	var b body
	if err := json.Unmarshal(req.Body, &b); err != nil {
		return nil, fmt.Errorf("custom: %w", err)
	}

	for i, a := range b.Attachments {
		if a.ref(i) != ref {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(a.ContentBase64)
		if err != nil {
			return nil, fmt.Errorf("custom: attachment %s is not base64: %w", ref, err)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("custom: no attachment %s in this delivery", ref)
}

func (b body) inbound(sentAt time.Time) gateway.Inbound {
	var mentions []gateway.Mention
	for _, m := range b.Mentions {
		mentions = append(mentions, gateway.Mention{
			SenderRef:   m.SenderRef,
			DisplayName: m.DisplayName,
			IsUs:        m.IsUs,
		})
	}

	var attachments []gateway.Attachment
	for i, a := range b.Attachments {
		attachments = append(attachments, gateway.Attachment{
			Ref:              a.ref(i),
			ClaimedFilename:  a.Filename,
			ClaimedMediaType: a.MediaType,
			ClaimedSize:      int64(base64.StdEncoding.DecodedLen(len(a.ContentBase64))),
		})
	}

	return gateway.Inbound{
		ExternalID: b.ID,
		Text:       b.Text,
		ReplyTo:    b.ReplyTo,
		Author: gateway.Author{
			Ref:         b.Author.Ref,
			DisplayName: b.Author.DisplayName,
			IsBot:       b.Author.IsBot,
		},
		Session: gateway.Session{
			Ref:  b.Session.Ref,
			Kind: gateway.SessionKind(b.Session.Kind),
		},
		Mentions:    mentions,
		Attachments: attachments,
		SentAt:      sentAt,
	}
}

func (a attachment) ref(i int) string {
	if a.ID != "" {
		return a.ID
	}
	return "#" + strconv.Itoa(i)
}
