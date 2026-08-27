package providertest_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/providertest"
)

// A conformance suite that passes everything is worse than none, because it
// reads as coverage. These tests break a correct adapter one property at a
// time and require the suite to notice each break.
//
// The suite reports failure through *testing.T, so the only way to assert it
// failed is to run it in a child process and look at the exit status. That is
// what brokenEnv selects.

const brokenEnv = "PROVIDERTEST_BROKEN"

const (
	secret     = "s3cr3t"
	signedAt   = int64(1755412345)
	externalID = "m-1"
)

var body = []byte(`{
  "id": "m-1",
  "text": "what's our deploy status?",
  "author": { "ref": "u-1", "display_name": "Asha" },
  "session": { "ref": "c-1", "kind": "direct" }
}`)

func sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v1:%s:", ts)
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func fixtures() providertest.Fixtures {
	ts := strconv.FormatInt(signedAt, 10)

	h := http.Header{}
	h.Set("X-Timestamp", ts)
	h.Set("X-Signature", sign(secret, ts, body))

	return providertest.Fixtures{
		Creds: gateway.Credentials{gateway.KeySigning: secret},
		Request: gateway.WebhookRequest{
			Method:     http.MethodPost,
			Header:     h,
			Body:       body,
			ReceivedAt: time.Unix(signedAt, 0),
		},
		WantExternalID:    externalID,
		EnforcesFreshness: true,

		// good produces no attachments, so it declares that rather than
		// leaving the case with nothing to check.
		CarriesNoAttachments: true,
	}
}

// good is a correct adapter, and the baseline every broken one is derived
// from by overriding exactly one method.
type good struct{}

func (good) Name() string { return "good" }

func (good) Routes() []gateway.Route {
	return []gateway.Route{{Method: http.MethodPost}}
}

func (good) Keys() []string { return []string{gateway.KeySigning} }

func (good) Verify(req gateway.WebhookRequest, creds gateway.Credentials) error {
	secret := creds.Get(gateway.KeySigning)
	if secret == "" {
		return errors.New("good: no signing secret")
	}

	ts := req.Header.Get("X-Timestamp")
	if ts == "" {
		return errors.New("good: no timestamp")
	}
	at, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("good: unreadable timestamp")
	}
	if drift := req.ReceivedAt.Sub(time.Unix(at, 0)); drift > 5*time.Minute || drift < -5*time.Minute {
		return errors.New("good: stale delivery")
	}

	want := sign(secret, ts, req.Body)
	if !hmac.Equal([]byte(req.Header.Get("X-Signature")), []byte(want)) {
		return errors.New("good: bad signature")
	}
	return nil
}

type payload struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Author struct {
		Ref         string `json:"ref"`
		DisplayName string `json:"display_name"`
	} `json:"author"`
	Session struct {
		Ref  string `json:"ref"`
		Kind string `json:"kind"`
	} `json:"session"`
}

func decode(b []byte) (payload, error) {
	var p payload
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("good: %w", err)
	}
	if p.ID == "" || p.Author.Ref == "" || p.Session.Ref == "" {
		return p, errors.New("good: a required field is missing")
	}
	switch gateway.SessionKind(p.Session.Kind) {
	case gateway.SessionDirect, gateway.SessionGroup, gateway.SessionThread:
	default:
		return p, fmt.Errorf("good: unknown conversation kind %q", p.Session.Kind)
	}
	return p, nil
}

func (good) Parse(req gateway.WebhookRequest) (gateway.Result, error) {
	p, err := decode(req.Body)
	if err != nil {
		return gateway.Result{}, err
	}
	return gateway.Result{Messages: []gateway.Inbound{{
		ExternalID: p.ID,
		Text:       p.Text,
		Author:     gateway.Author{Ref: p.Author.Ref, DisplayName: p.Author.DisplayName},
		Session:    gateway.Session{Ref: p.Session.Ref, Kind: gateway.SessionKind(p.Session.Kind)},
	}}}, nil
}

// blindVerify is the mistake the suite exists for: an adapter whose Verify
// was stubbed during development and never finished.
type blindVerify struct{ good }

func (blindVerify) Verify(gateway.WebhookRequest, gateway.Credentials) error { return nil }

// openWhenBlank compares a shared token instead of an HMAC, and forgets that
// an absent header and an absent secret are both "". Telegram authenticates
// exactly this way, so this is not a hypothetical shape.
type openWhenBlank struct{ good }

func (openWhenBlank) Verify(req gateway.WebhookRequest, creds gateway.Credentials) error {
	if req.Header.Get("X-Token") != creds.Get(gateway.KeySigning) {
		return errors.New("openWhenBlank: bad token")
	}
	return nil
}

// noFreshness checks the signature but not the age, so a captured request
// replays forever.
type noFreshness struct{ good }

func (noFreshness) Verify(req gateway.WebhookRequest, creds gateway.Credentials) error {
	secret := creds.Get(gateway.KeySigning)
	if secret == "" {
		return errors.New("noFreshness: no signing secret")
	}
	ts := req.Header.Get("X-Timestamp")
	if ts == "" {
		return errors.New("noFreshness: no timestamp")
	}
	want := sign(secret, ts, req.Body)
	if !hmac.Equal([]byte(req.Header.Get("X-Signature")), []byte(want)) {
		return errors.New("noFreshness: bad signature")
	}
	return nil
}

// panicky indexes the body before checking that there is one.
type panicky struct{ good }

func (p panicky) Parse(req gateway.WebhookRequest) (gateway.Result, error) {
	if req.Body[0] != '{' {
		return gateway.Result{}, errors.New("panicky: not an object")
	}
	return p.good.Parse(req)
}

// nondeterministic reads state Parse is not allowed to have.
type nondeterministic struct{ good }

var seen int

func (n nondeterministic) Parse(req gateway.WebhookRequest) (gateway.Result, error) {
	res, err := n.good.Parse(req)
	if err != nil {
		return res, err
	}
	seen++
	res.Messages[0].Text = fmt.Sprintf("%s (#%d)", res.Messages[0].Text, seen)
	return res, nil
}

// mutates writes into the body it was lent, which the handler still needs.
type mutates struct{ good }

func (m mutates) Parse(req gateway.WebhookRequest) (gateway.Result, error) {
	res, err := m.good.Parse(req)
	if len(req.Body) > 0 {
		req.Body[0] = 'X'
	}
	return res, err
}

// noAuthor produces a message the handler cannot attribute to anyone.
type noAuthor struct{ good }

func (n noAuthor) Parse(req gateway.WebhookRequest) (gateway.Result, error) {
	res, err := n.good.Parse(req)
	if err != nil {
		return res, err
	}
	res.Messages[0].Author.Ref = ""
	return res, nil
}

// carries is a correct adapter that has attachments: it names them and can
// resolve them. The baseline plus the two methods that go together.
type carries struct{ good }

func (c carries) Parse(req gateway.WebhookRequest) (gateway.Result, error) {
	res, err := c.good.Parse(req)
	if err != nil || len(res.Messages) == 0 {
		return res, err
	}
	res.Messages[0].Attachments = []gateway.Attachment{{
		Ref:              "file-1",
		ClaimedFilename:  "holiday.png",
		ClaimedMediaType: "image/png",
		ClaimedSize:      1024,
	}}
	return res, nil
}

func (carries) FetchAttachment(
	context.Context, gateway.WebhookRequest, string, gateway.Credentials,
) ([]byte, error) {
	return []byte("\x89PNG\r\n\x1a\n"), nil
}

// refsNoFetcher names attachments it has no way to resolve. It compiles, it
// parses, and it fails on the first real message.
type refsNoFetcher struct{ carries }

func (refsNoFetcher) FetchAttachment() {}

// duplicateRefs names two attachments by one ref. FetchAttachment is given a
// ref and nothing else, so the second file resolves to the first one's bytes
// under the second one's filename — an ingest that succeeds and is wrong.
type duplicateRefs struct{ carries }

func (d duplicateRefs) Parse(req gateway.WebhookRequest) (gateway.Result, error) {
	res, err := d.carries.Parse(req)
	if err != nil || len(res.Messages) == 0 {
		return res, err
	}
	second := res.Messages[0].Attachments[0]
	second.ClaimedFilename = "receipt.pdf"
	res.Messages[0].Attachments = append(res.Messages[0].Attachments, second)
	return res, nil
}

// fetcherNoRefs is the opposite: a Fetcher nothing will ever call, which
// reads as a working feature.
type fetcherNoRefs struct{ good }

func (fetcherNoRefs) FetchAttachment(
	context.Context, gateway.WebhookRequest, string, gateway.Credentials,
) ([]byte, error) {
	return nil, nil
}

var brokenAdapters = map[string]gateway.Provider{
	"Verify always passes":          blindVerify{},
	"blank token opens the door":    openWhenBlank{},
	"no replay window":              noFreshness{},
	"Parse panics on an empty body": panicky{},
	"Parse is not deterministic":    nondeterministic{},
	"Parse mutates the body":        mutates{},
	"a message with no author":      noAuthor{},
	"attachments with no Fetcher":   refsNoFetcher{},
	"a Fetcher nothing can call":    fetcherNoRefs{},
	"two attachments share a ref":   duplicateRefs{},
}

// needsAttachmentFixture names the broken adapters whose failure is about
// attachments, and so cannot be reached from the baseline fixture.
var needsAttachmentFixture = map[string]bool{
	"attachments with no Fetcher": true,
	"two attachments share a ref": true,
}

// fixturesFor gives the attachment cases the fixture their failure needs.
// Everything else runs against the baseline, which declares no attachments.
func fixturesFor(name string) providertest.Fixtures {
	f := fixtures()
	if needsAttachmentFixture[name] {
		f.CarriesNoAttachments = false
		f.AttachmentRequest = f.Request
	}
	return f
}

func TestTheSuiteAcceptsACorrectAdapter(t *testing.T) {
	providertest.Run(t, good{}, fixtures())
}

// The other correct shape. An adapter that carries attachments passes only
// when it both names them and can resolve them, so this proves the case is
// not simply refusing everything with an AttachmentRequest.
func TestTheSuiteAcceptsAnAdapterThatCarriesAttachments(t *testing.T) {
	f := fixtures()
	f.CarriesNoAttachments = false
	f.AttachmentRequest = f.Request

	providertest.Run(t, carries{}, f)
}

func TestTheSuiteCatchesABrokenAdapter(t *testing.T) {
	// The child process: run the suite against the named broken adapter and
	// let it fail, which is what the parent is asserting.
	if name := os.Getenv(brokenEnv); name != "" {
		p, ok := brokenAdapters[name]
		if !ok {
			t.Fatalf("no broken adapter named %q", name)
		}
		providertest.Run(t, p, fixturesFor(name))
		return
	}

	for name := range brokenAdapters {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.CommandContext(t.Context(), os.Args[0],
				"-test.run", "^TestTheSuiteCatchesABrokenAdapter$")
			cmd.Env = append(os.Environ(), brokenEnv+"="+name)

			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("the conformance suite passed an adapter where %s:\n%s", name, out)
			}
		})
	}
}
