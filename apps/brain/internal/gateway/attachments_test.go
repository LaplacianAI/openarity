package gateway

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

// A one-pixel PNG. Real bytes, because the whole point of the sniff is that it
// reads them — a constructed header would test the test.
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
	0x1f, 0x15, 0xc4, 0x89,
}

// fetchProvider is an adapter that carries attachments. What it returns for a
// ref, and how long it takes, is what each test varies.
type fetchProvider struct {
	bodies map[string][]byte
	err    error
	delay  time.Duration

	refs []string
}

func (*fetchProvider) Name() string   { return "fake" }
func (*fetchProvider) Keys() []string { return []string{KeySigning} }
func (*fetchProvider) Routes() []Route {
	return []Route{{Method: "POST"}}
}
func (*fetchProvider) Verify(WebhookRequest, Credentials) error { return nil }
func (*fetchProvider) Parse(WebhookRequest) (Result, error)     { return Result{}, nil }

func (f *fetchProvider) FetchAttachment(
	ctx context.Context, _ WebhookRequest, ref string, _ Credentials,
) ([]byte, error) {
	f.refs = append(f.refs, ref)

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.bodies[ref], nil
}

// noFetch is the same adapter without the Fetcher half. Redeclaring the method
// with a different signature is the only way to un-implement an interface that
// embedding gave you.
type noFetch struct{ *fetchProvider }

func (noFetch) FetchAttachment() {}

type fakeObjects struct {
	put map[string][]byte
	err error

	teams []uuid.UUID
}

func newFakeObjects() *fakeObjects {
	return &fakeObjects{put: map[string][]byte{}}
}

func (f *fakeObjects) Put(_ context.Context, teamID uuid.UUID, key string, body []byte) error {
	if f.err != nil {
		return f.err
	}
	f.teams = append(f.teams, teamID)
	f.put[key] = bytes.Clone(body)
	return nil
}

// A file that is refused only for its size: it sniffs as image/png, which is
// on the allow list, so nothing but the size check stands in its way. Padding
// with zero bytes instead would sniff as application/octet-stream and the
// allow list would refuse it — leaving the test passing with the size guard
// removed.
func oversizePNG() []byte {
	body := make([]byte, 0, objects.MaxAttachment+1)
	body = append(body, pngBytes...)
	return append(body, bytes.Repeat([]byte{0}, objects.MaxAttachment+1-len(body))...)
}

func claim(ref, filename string) Attachment {
	return Attachment{Ref: ref, ClaimedFilename: filename}
}

func fetchFixture(t *testing.T) (*Ingest, *fetchProvider, *fakeObjects, uuid.UUID) {
	t.Helper()

	store := newFakeObjects()
	p := &fetchProvider{bodies: map[string][]byte{"a": pngBytes}}
	return NewIngest(store, discardLogger()), p, store, uuid.New()
}

func TestFetchStoresWhatItMeasured(t *testing.T) {
	t.Parallel()

	g, p, store, team := fetchFixture(t)

	got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
		[]Attachment{{
			Ref:              "a",
			ClaimedFilename:  "holiday.png",
			ClaimedMediaType: "application/pdf",
			ClaimedSize:      99999,
		}})

	if len(got) != 1 {
		t.Fatalf("stored %d attachments, want 1", len(got))
	}

	// The claim said pdf and 99999 bytes. Neither survives.
	if got[0].MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png — the claim was believed", got[0].MediaType)
	}
	if got[0].SizeBytes != int64(len(pngBytes)) {
		t.Errorf("SizeBytes = %d, want %d", got[0].SizeBytes, len(pngBytes))
	}
	if got[0].Filename != "holiday.png" {
		t.Errorf("Filename = %q", got[0].Filename)
	}

	if !objects.InTeam(got[0].ObjectKey, team) {
		t.Errorf("ObjectKey %q is not inside team %s", got[0].ObjectKey, team)
	}
	if !bytes.Equal(store.put[got[0].ObjectKey], pngBytes) {
		t.Error("the bytes written are not the bytes fetched")
	}
	if len(store.teams) != 1 || store.teams[0] != team {
		t.Errorf("Put was called with teams %v, want [%s]", store.teams, team)
	}
}

// The key is a fresh id, so the same file twice is two objects. A hash would
// tell whoever can list the bucket that two teams hold the same file.
func TestTwoIdenticalFilesGetDifferentKeys(t *testing.T) {
	t.Parallel()

	g, p, store, team := fetchFixture(t)
	p.bodies["b"] = pngBytes

	got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
		[]Attachment{claim("a", "one.png"), claim("b", "two.png")})

	if len(got) != 2 {
		t.Fatalf("stored %d, want 2", len(got))
	}
	if got[0].ObjectKey == got[1].ObjectKey {
		t.Fatalf("both files went to %s", got[0].ObjectKey)
	}
	if len(store.put) != 2 {
		t.Errorf("%d objects written, want 2", len(store.put))
	}
}

func TestFetchWithNothingToFetchAsksForNothing(t *testing.T) {
	t.Parallel()

	g, p, store, team := fetchFixture(t)

	if got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team, nil); got != nil {
		t.Errorf("Fetch returned %v for no attachments", got)
	}
	if len(p.refs) != 0 || len(store.put) != 0 {
		t.Errorf("asked for %v and wrote %d objects", p.refs, len(store.put))
	}
}

// providertest refuses this combination, so it can only be an adapter that
// never ran the suite. It must not panic on a type assertion.
func TestAnAdapterThatCannotFetchStoresNothing(t *testing.T) {
	t.Parallel()

	g, _, store, team := fetchFixture(t)

	got := g.Fetch(t.Context(), noFetch{&fetchProvider{}}, WebhookRequest{}, nil, team,
		[]Attachment{claim("a", "holiday.png")})

	if got != nil {
		t.Errorf("Fetch returned %v from an adapter with no Fetcher", got)
	}
	if len(store.put) != 0 {
		t.Errorf("%d objects written", len(store.put))
	}
}

// One bad file must not take the message with it, and must not take the other
// files either.
func TestAFileThatFailsIsDroppedAndTheRestSurvive(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		body   []byte
		reason string
	}{
		"nothing came back":     {nil, "an absent ref"},
		"empty file":            {[]byte{}, "zero bytes"},
		"a type we do not take": {[]byte("MZ\x90\x00\x03\x00\x00\x00executable"), "sniffs as application/octet-stream"},
		"over the size limit":   {oversizePNG(), "too large"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g, p, _, team := fetchFixture(t)
			p.bodies["bad"] = tc.body

			got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
				[]Attachment{claim("bad", "bad.dat"), claim("a", "good.png")})

			if len(got) != 1 {
				t.Fatalf("stored %d attachments, want 1 — the bad one %s",
					len(got), tc.reason)
			}
			if got[0].Filename != "good.png" {
				t.Errorf("the file that survived is not the good one: %+v", got)
			}
		})
	}
}

// An oversized claim is refused before a request is spent on it.
func TestAnImpossibleClaimIsNotEvenFetched(t *testing.T) {
	t.Parallel()

	g, p, store, team := fetchFixture(t)

	got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
		[]Attachment{{Ref: "a", ClaimedSize: objects.MaxAttachment + 1}})

	if len(got) != 0 {
		t.Errorf("stored %v", got)
	}
	if len(p.refs) != 0 {
		t.Errorf("fetched %v despite the claim being impossible", p.refs)
	}
	if len(store.put) != 0 {
		t.Errorf("%d objects written", len(store.put))
	}
}

// A file we refuse must not reach the bucket. Sniffing after storing would
// leave the bytes behind with no row naming them.
func TestARefusedFileIsNeverWritten(t *testing.T) {
	t.Parallel()

	g, p, store, team := fetchFixture(t)
	p.bodies["exe"] = []byte("MZ\x90\x00\x03\x00\x00\x00executable")

	got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
		[]Attachment{claim("exe", "setup.exe")})

	if len(got) != 0 {
		t.Fatalf("stored %+v", got)
	}
	if len(store.put) != 0 {
		t.Errorf("%d objects written for a refused file", len(store.put))
	}
}

// An SVG carrying a script sniffs as text/plain, which is on the allow list,
// so it is stored — and is inert only for as long as the read path serves it
// as the type recorded here. This test exists to make that dependency visible:
// if it ever starts recording image/svg+xml, the stored file executes.
func TestAScriptedSVGIsRecordedAsText(t *testing.T) {
	t.Parallel()

	g, p, _, team := fetchFixture(t)
	p.bodies["svg"] = []byte(
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)

	got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
		[]Attachment{claim("svg", "logo.svg")})

	if len(got) != 1 {
		t.Fatalf("stored %d, want 1", len(got))
	}
	if !strings.HasPrefix(got[0].MediaType, "text/plain") {
		t.Errorf("MediaType = %q, want text/plain — an SVG served as its own "+
			"type executes in a browser", got[0].MediaType)
	}
}

func TestAFailedWriteDropsTheAttachment(t *testing.T) {
	t.Parallel()

	g, p, store, team := fetchFixture(t)
	store.err = errors.New("bucket is on fire")

	if got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
		[]Attachment{claim("a", "holiday.png")}); len(got) != 0 {
		t.Errorf("Fetch reported %+v as stored after Put failed", got)
	}
}

func TestAFetchErrorDropsTheAttachment(t *testing.T) {
	t.Parallel()

	g, p, _, team := fetchFixture(t)
	p.err = errors.New("provider said no")

	if got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
		[]Attachment{claim("a", "holiday.png")}); len(got) != 0 {
		t.Errorf("Fetch reported %+v as stored", got)
	}
}

// The budget bounds the delivery, not each file. Once it is spent the rest are
// abandoned rather than each waiting for its own timeout.
func TestTheBudgetStopsTheWholeDelivery(t *testing.T) {
	t.Parallel()

	g, p, store, team := fetchFixture(t)
	p.delay = fetchTimeout + 200*time.Millisecond
	for _, ref := range []string{"b", "c", "d", "e", "f"} {
		p.bodies[ref] = pngBytes
	}

	start := time.Now()
	got := g.Fetch(t.Context(), p, WebhookRequest{}, nil, team,
		[]Attachment{
			claim("a", "1.png"), claim("b", "2.png"), claim("c", "3.png"),
			claim("d", "4.png"), claim("e", "5.png"), claim("f", "6.png"),
		})
	elapsed := time.Since(start)

	if len(got) != 0 || len(store.put) != 0 {
		t.Errorf("stored %+v", got)
	}

	// Six files each waiting out fetchTimeout would be six seconds.
	if elapsed > fetchBudget+time.Second {
		t.Errorf("took %s for a %s budget; the budget is not bounding it", elapsed, fetchBudget)
	}
	if len(p.refs) == len(got)+6 {
		t.Errorf("every file was attempted (%v) despite the budget", p.refs)
	}
}

func TestCleanFilename(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ raw, want string }{
		"an ordinary name":      {"holiday.png", "holiday.png"},
		"a unix path":           {"../../etc/passwd", "passwd"},
		"a windows path":        {`C:\Users\asha\report.pdf`, "report.pdf"},
		"a trailing separator":  {"folder/", ""},
		"control characters":    {"in\x1b[2Kvoice.pdf", "in [2Kvoice.pdf"},
		"a newline":             {"in\nvoice.pdf", "in voice.pdf"},
		"collapsing whitespace": {"  my   file .png ", "my file .png"},
		"empty":                 {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := cleanFilename(tc.raw); got != tc.want {
				t.Errorf("cleanFilename(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// The column is bounded at 512 characters, so a provider sending a megabyte of
// filename is refused here rather than by the database.
func TestAnEnormousFilenameIsClipped(t *testing.T) {
	t.Parallel()

	got := cleanFilename(strings.Repeat("é", 5000) + ".png")
	if n := len([]rune(got)); n != filenameMax {
		t.Errorf("clipped to %d characters, want %d", n, filenameMax)
	}
}

// Two ceilings that are easy to mistake for one. maxBody bounds the request;
// MaxAttachment bounds a file. For a provider whose bytes arrive inside the
// delivery — custom does, and any adapter carrying base64 will — the request
// limit is the real one, and it is 25 times smaller.
//
// This is not a bug to fix by raising maxBody: the webhook listener is public
// and unauthenticated, and the body is read before the channel is looked up,
// so every byte allowed there is a byte a stranger can make us buffer.
func TestTheRequestLimitIsTheRealLimitForInlineBytes(t *testing.T) {
	t.Parallel()

	if maxBody >= objects.MaxAttachment {
		t.Fatalf("maxBody is %d and MaxAttachment is %d; an inline attachment is "+
			"no longer bounded by the request", maxBody, objects.MaxAttachment)
	}

	// Base64 costs a third on top, so what actually fits is smaller again.
	const overhead = 4.0 / 3.0
	if fits := float64(maxBody) / overhead; fits > objects.MaxAttachment {
		t.Errorf("a base64 body within maxBody could carry %.0f bytes", fits)
	}
}
