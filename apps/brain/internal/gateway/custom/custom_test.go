package custom_test

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/providertest"
)

const secret = "s3cr3t"

var signedAt = time.Unix(1755412345, 0).UTC()

func request(t *testing.T, body string) gateway.WebhookRequest {
	t.Helper()

	ts := strconv.FormatInt(signedAt.Unix(), 10)
	h := http.Header{}
	h.Set(custom.HeaderTimestamp, ts)
	h.Set(custom.HeaderSignature, custom.Sign(secret, ts, []byte(body)))

	return gateway.WebhookRequest{
		Method:     http.MethodPost,
		Header:     h,
		Body:       []byte(body),
		ReceivedAt: signedAt,
	}
}

const message = `{
  "id": "order-4821-note",
  "text": "customer asked to change the delivery address",
  "sent_at": "2026-08-21T12:00:00Z",
  "author": { "ref": "agent-17", "display_name": "Asha", "is_bot": false },
  "session": { "ref": "ticket-4821", "kind": "direct" }
}`

// A delivery carrying two files, one named and one relying on its index.
var withAttachments = `{
  "id": "order-4821-photo",
  "text": "here is the label",
  "sent_at": "2026-08-21T12:00:00Z",
  "author": { "ref": "agent-17", "display_name": "Asha", "is_bot": false },
  "session": { "ref": "ticket-4821", "kind": "direct" },
  "attachments": [
    {
      "id": "label",
      "filename": "label.png",
      "media_type": "image/png",
      "content_base64": "iVBORw0KGgpib2R5IGJ5dGVz"
    },
    {
      "filename": "notes.txt",
      "media_type": "text/plain",
      "content_base64": "anVzdCBzb21lIHdvcmRz"
    }
  ]
}`

// The whole point of shipping this adapter rather than a test double: it runs
// the same suite every other adapter will, so the suite is proven against
// something real before Slack or Telegram depends on it.
func TestCustomPassesTheConformanceSuite(t *testing.T) {
	providertest.Run(t, custom.New(), providertest.Fixtures{
		Creds:             gateway.Credentials{gateway.KeySigning: secret},
		Request:           request(t, message),
		WantExternalID:    "order-4821-note",
		EnforcesFreshness: true,

		AttachmentRequest: request(t, withAttachments),
	})
}

func TestParseReadsEveryField(t *testing.T) {
	t.Parallel()

	body := `{
	  "id": "m-9",
	  "text": "@bot can you check this?",
	  "sent_at": "2026-08-21T12:00:00Z",
	  "reply_to": "m-8",
	  "author": { "ref": "u-1", "display_name": "Asha", "is_bot": false },
	  "session": { "ref": "t-1", "kind": "thread" },
	  "mentions": [
	    { "sender_ref": "bot-0", "display_name": "openarity", "is_us": true },
	    { "sender_ref": "u-2", "display_name": "Shri" }
	  ]
	}`

	res, err := custom.New().Parse(request(t, body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(res.Messages))
	}

	m := res.Messages[0]
	if m.ExternalID != "m-9" {
		t.Errorf("ExternalID = %q, want m-9", m.ExternalID)
	}
	if m.Text != "@bot can you check this?" {
		t.Errorf("Text = %q", m.Text)
	}
	if m.ReplyTo != "m-8" {
		t.Errorf("ReplyTo = %q, want m-8", m.ReplyTo)
	}
	if m.Author != (gateway.Author{Ref: "u-1", DisplayName: "Asha"}) {
		t.Errorf("Author = %+v", m.Author)
	}
	if m.Session != (gateway.Session{Ref: "t-1", Kind: gateway.SessionThread}) {
		t.Errorf("Session = %+v", m.Session)
	}
	if want := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC); !m.SentAt.Equal(want) {
		t.Errorf("SentAt = %v, want %v", m.SentAt, want)
	}

	if len(m.Mentions) != 2 {
		t.Fatalf("got %d mentions, want 2", len(m.Mentions))
	}
	if !m.Mentions[0].IsUs {
		t.Error("the first mention is of us and was not marked so")
	}
	if m.Mentions[1].IsUs {
		t.Error("the second mention is of another person and was marked as us")
	}
	if m.Mentions[1].DisplayName != "Shri" {
		t.Errorf("Mentions[1].DisplayName = %q, want Shri", m.Mentions[1].DisplayName)
	}
}

// An omitted timestamp is the common case for a hand-written integration, and
// the handler needs one. ReceivedAt is on the request, so defaulting to it
// keeps Parse pure.
func TestAnOmittedSentAtBecomesWhenItArrived(t *testing.T) {
	t.Parallel()

	body := `{"id":"m-1","author":{"ref":"u-1"},"session":{"ref":"c-1","kind":"direct"}}`

	res, err := custom.New().Parse(request(t, body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !res.Messages[0].SentAt.Equal(signedAt) {
		t.Errorf("SentAt = %v, want the arrival time %v", res.Messages[0].SentAt, signedAt)
	}
}

// The adapter translates and does not judge — the required-field rules belong
// to Inbound and are tested there. What this pins is that a body missing them
// still produces a message the handler will reject, rather than one that slips
// through looking complete.
func TestAMessageMissingARequiredFieldDoesNotValidate(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"no id":          `{"author":{"ref":"u-1"},"session":{"ref":"c-1","kind":"direct"}}`,
		"no author ref":  `{"id":"m-1","author":{},"session":{"ref":"c-1","kind":"direct"}}`,
		"no session ref": `{"id":"m-1","author":{"ref":"u-1"},"session":{"kind":"direct"}}`,
		"no kind":        `{"id":"m-1","author":{"ref":"u-1"},"session":{"ref":"c-1"}}`,
		"unknown kind":   `{"id":"m-1","author":{"ref":"u-1"},"session":{"ref":"c-1","kind":"channel"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, err := custom.New().Parse(request(t, body))
			if err != nil {
				return // refusing outright is also a correct answer
			}
			if len(res.Messages) == 0 {
				t.Fatal("Parse returned neither an error nor a message")
			}
			if err := res.Messages[0].Validate(); err == nil {
				t.Errorf("a body with %s produced a message the handler would accept", name)
			}
		})
	}
}

// sent_at is this format's rule rather than every format's, so the adapter
// still owns it.
func TestAnUnparseableSentAtIsRefused(t *testing.T) {
	t.Parallel()

	body := `{"id":"m-1","sent_at":"yesterday","author":{"ref":"u-1"},"session":{"ref":"c-1","kind":"direct"}}`

	if _, err := custom.New().Parse(request(t, body)); err == nil {
		t.Error("Parse accepted a sent_at that is not a timestamp")
	}
}

func TestVerifyRejectsAStaleDelivery(t *testing.T) {
	t.Parallel()

	creds := gateway.Credentials{gateway.KeySigning: secret}

	fresh := request(t, message)
	fresh.ReceivedAt = signedAt.Add(4 * time.Minute)
	if err := custom.New().Verify(fresh, creds); err != nil {
		t.Errorf("Verify rejected a delivery four minutes old: %v", err)
	}

	stale := request(t, message)
	stale.ReceivedAt = signedAt.Add(6 * time.Minute)
	if err := custom.New().Verify(stale, creds); err == nil {
		t.Error("Verify accepted a delivery six minutes old")
	}

	// Clocks drift both ways, and a delivery signed in the future is as much
	// a replay signal as one signed in the past.
	early := request(t, message)
	early.ReceivedAt = signedAt.Add(-6 * time.Minute)
	if err := custom.New().Verify(early, creds); err == nil {
		t.Error("Verify accepted a delivery signed six minutes in the future")
	}
}

func TestVerifyRejectsAnUnreadableTimestamp(t *testing.T) {
	t.Parallel()

	req := request(t, message)
	req.Header.Set(custom.HeaderTimestamp, "yesterday")

	if err := custom.New().Verify(req, gateway.Credentials{gateway.KeySigning: secret}); err == nil {
		t.Error("Verify accepted a timestamp that is not a number")
	}
}

// The version prefix is in the wire format from the first commit, because a
// shipped signature scheme cannot grow one later.
func TestTheSignatureIsVersioned(t *testing.T) {
	t.Parallel()

	got := custom.Sign(secret, "1755412345", []byte(message))
	if !strings.HasPrefix(got, "v1=") {
		t.Errorf("Sign() = %q, want a v1= prefix", got)
	}
}

// Anyone integrating writes this themselves, in whatever language, so the
// scheme is pinned to a value rather than to our own implementation of it.
func TestSignIsStable(t *testing.T) {
	t.Parallel()

	// Produced independently of our implementation:
	//   printf 'v1:1755412345:{"id":"m-1"}' | openssl dgst -sha256 -hmac s3cr3t
	// A change here means the scheme moved and every existing integration
	// stopped working, which is not something a refactor may do quietly.
	const want = "v1=7cc382f3a6ab4b83d7ebae8adaa3300bb8fa26cf7bad072bd38b19d61471aa5b"

	if got := custom.Sign("s3cr3t", "1755412345", []byte(`{"id":"m-1"}`)); got != want {
		t.Errorf("Sign() = %q, want %q", got, want)
	}
}

func TestTheAdapterDeclaresOneRouteAndOneKey(t *testing.T) {
	t.Parallel()

	p := custom.New()

	if p.Name() != "custom" {
		t.Errorf("Name() = %q, want custom", p.Name())
	}
	if routes := p.Routes(); len(routes) != 1 || routes[0].Method != http.MethodPost || routes[0].Suffix != "" {
		t.Errorf("Routes() = %+v, want one POST at the hook path", routes)
	}
	if keys := p.Keys(); len(keys) != 1 || keys[0] != gateway.KeySigning {
		t.Errorf("Keys() = %v, want just the signing secret", keys)
	}
}

// Parse names attachments; FetchAttachment resolves them. The two halves are
// tested together because a ref that Parse produces and FetchAttachment
// cannot find is the failure mode that compiles.
func TestAttachmentsRoundTripThroughTheirRefs(t *testing.T) {
	t.Parallel()

	req := request(t, withAttachments)

	res, err := custom.New().Parse(req)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := res.Messages[0].Attachments
	if len(got) != 2 {
		t.Fatalf("parsed %d attachments, want 2", len(got))
	}

	fetcher, ok := custom.New().(gateway.Fetcher)
	if !ok {
		t.Fatal("custom no longer implements gateway.Fetcher")
	}

	for name, tc := range map[string]struct {
		index int
		ref   string
		want  []byte
	}{
		"named by its id":           {0, "label", []byte("\x89PNG\r\n\x1a\nbody bytes")},
		"falling back to its index": {1, "#1", []byte("just some words")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got[tc.index].Ref != tc.ref {
				t.Fatalf("Ref = %q, want %q", got[tc.index].Ref, tc.ref)
			}

			body, err := fetcher.FetchAttachment(t.Context(), req, tc.ref, nil)
			if err != nil {
				t.Fatalf("FetchAttachment(%q): %v", tc.ref, err)
			}
			if !bytes.Equal(body, tc.want) {
				t.Errorf("FetchAttachment(%q) = %q, want %q", tc.ref, body, tc.want)
			}
		})
	}
}

// ClaimedSize is arithmetic on the encoded length, so it never requires
// decoding — a message with four large files must cost nothing to look at.
func TestParseDoesNotDecodeToReportASize(t *testing.T) {
	t.Parallel()

	res, err := custom.New().Parse(request(t, withAttachments))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for i, a := range res.Messages[0].Attachments {
		if a.ClaimedSize <= 0 {
			t.Errorf("attachment %d claims %d bytes", i, a.ClaimedSize)
		}
		// DecodedLen rounds up to the padded length, so it is an upper bound
		// rather than the exact figure. Close enough to refuse a fetch with,
		// which is all it is for.
		if a.ClaimedSize > 64 {
			t.Errorf("attachment %d claims %d bytes for a small fixture", i, a.ClaimedSize)
		}
	}
}

// The two ways a ref can fail to resolve, both of which reach the handler as
// an error rather than as empty bytes it would happily store.
func TestFetchAttachmentRefusesWhatItCannotResolve(t *testing.T) {
	t.Parallel()

	fetcher, ok := custom.New().(gateway.Fetcher)
	if !ok {
		t.Fatal("custom no longer implements gateway.Fetcher")
	}

	badBase64 := `{
	  "id": "m-1",
	  "author": { "ref": "a" },
	  "session": { "ref": "s", "kind": "direct" },
	  "attachments": [{ "id": "broken", "content_base64": "not base64 !!" }]
	}`

	for name, tc := range map[string]struct {
		body, ref, wants string
	}{
		"a ref that is not in the delivery": {withAttachments, "nope", "no attachment"},
		"content that is not base64":        {badBase64, "broken", "not base64"},
		"an empty ref":                      {withAttachments, "", "no attachment"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body, err := fetcher.FetchAttachment(t.Context(), request(t, tc.body), tc.ref, nil)
			if err == nil {
				t.Fatalf("FetchAttachment(%q) returned %d bytes and no error", tc.ref, len(body))
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("err = %v, want it to mention %q", err, tc.wants)
			}
			if body != nil {
				t.Errorf("FetchAttachment returned %d bytes alongside the error", len(body))
			}
		})
	}
}

// A message with no attachments produces none, rather than an empty non-nil
// slice the handler would iterate over for nothing.
func TestAMessageWithoutAttachmentsHasNone(t *testing.T) {
	t.Parallel()

	res, err := custom.New().Parse(request(t, message))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := res.Messages[0].Attachments; got != nil {
		t.Errorf("Attachments = %v, want nil", got)
	}
}
