// Package providertest is the conformance suite every gateway adapter runs.
// It asserts what the handler assumes and cannot check for itself: that a
// forgery is refused, that Parse survives anything, and that a message the
// handler is given has the fields it will go on to use.
//
// An adapter author's whole test file is one call to Run.
package providertest

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// Fixtures is one real, correctly signed request and the credentials that
// sign it. Recorded from the provider rather than constructed, so the suite
// tests the adapter against what actually arrives.
type Fixtures struct {
	// Creds is what the handler would fetch from the secret store. It must
	// contain every key the provider declares.
	Creds gateway.Credentials

	// Request is a valid, signed delivery carrying exactly one message.
	Request gateway.WebhookRequest

	// WantExternalID is the id Parse must find in Request.Body.
	WantExternalID string

	// EnforcesFreshness says the provider signs a timestamp and refuses a
	// stale delivery. Slack and Discord do; Telegram sends a bare shared
	// token with no timestamp at all, and cannot.
	EnforcesFreshness bool

	// AttachmentRequest is a valid, signed delivery carrying a message with
	// at least one attachment. Recorded from the provider like Request is —
	// attachment payloads are where adapters differ most, and a constructed
	// one tests the adapter against the author's memory of the format.
	AttachmentRequest gateway.WebhookRequest

	// CarriesNoAttachments declares that this provider has no concept of one.
	// Required when AttachmentRequest is empty, so that "there are none" and
	// "nobody wrote the fixture" are different answers rather than the same
	// silence.
	CarriesNoAttachments bool
}

// Run drives the whole contract. A passing adapter is one the handler can use
// without reading its internals.
func Run(t *testing.T, p gateway.Provider, f Fixtures) {
	t.Helper()

	t.Run("declares itself", func(t *testing.T) { declares(t, p, f) })
	t.Run("refuses a forgery", func(t *testing.T) { refusesForgery(t, p, f) })
	t.Run("survives junk", func(t *testing.T) { survivesJunk(t, p) })
	t.Run("produces usable messages", func(t *testing.T) { produces(t, p, f) })
	t.Run("names its attachments", func(t *testing.T) { attachments(t, p, f) })
}

func declares(t *testing.T, p gateway.Provider, f Fixtures) {
	t.Helper()

	if p.Name() == "" {
		t.Error("Name() is empty; it must match the channels.provider column")
	}
	if strings.ToLower(p.Name()) != p.Name() {
		t.Errorf("Name() = %q; it appears in a URL, so it is lower case", p.Name())
	}

	if len(p.Routes()) == 0 {
		t.Error("Routes() is empty; nothing would be mounted for this provider")
	}
	for _, rt := range p.Routes() {
		if strings.ToUpper(rt.Method) != rt.Method {
			t.Errorf("route method %q is not upper case; ServeMux matches it exactly", rt.Method)
		}
		if rt.Suffix != "" && !strings.HasPrefix(rt.Suffix, "/") {
			t.Errorf("route suffix %q does not start with /; it is appended to the hook path", rt.Suffix)
		}
	}

	// A provider that asks for no secret has nothing to check a signature
	// against, so its Verify can only be returning nil.
	if len(p.Keys()) == 0 {
		t.Fatal("Keys() is empty; the adapter has no secret to verify against")
	}
	for _, k := range p.Keys() {
		if f.Creds.Get(k) == "" {
			t.Errorf("the fixture has no value for declared key %q, so the suite cannot verify anything", k)
		}
	}
}

func refusesForgery(t *testing.T, p gateway.Provider, f Fixtures) {
	t.Helper()

	if err := p.Verify(f.Request, f.Creds); err != nil {
		t.Fatalf("Verify rejected a request the provider itself signed: %v", err)
	}

	t.Run("a tampered body", func(t *testing.T) {
		req := f.Request
		req.Body = append(bytes.Clone(f.Request.Body), ' ')
		if err := p.Verify(req, f.Creds); err == nil {
			t.Error("Verify accepted a body the signature does not cover")
		}
	})

	t.Run("a wrong secret", func(t *testing.T) {
		wrong := gateway.Credentials{}
		for k, v := range f.Creds {
			wrong[k] = v + "x"
		}
		if err := p.Verify(f.Request, wrong); err == nil {
			t.Error("Verify accepted the wrong secret")
		}
	})

	// hmac.Equal("", "") is true, so an adapter that does not refuse an empty
	// key turns a missing secret into a forgery oracle: anyone who signs with
	// "" is accepted.
	t.Run("an empty secret", func(t *testing.T) {
		for _, k := range p.Keys() {
			blank := gateway.Credentials{}
			for ck, v := range f.Creds {
				blank[ck] = v
			}
			blank[k] = ""
			if err := p.Verify(f.Request, blank); err == nil {
				t.Errorf("Verify accepted an empty %q", k)
			}
		}
	})

	t.Run("no credentials at all", func(t *testing.T) {
		if err := p.Verify(f.Request, nil); err == nil {
			t.Error("Verify accepted a request with no credentials")
		}
	})

	t.Run("no signature header", func(t *testing.T) {
		req := f.Request
		req.Header = nil
		if err := p.Verify(req, f.Creds); err == nil {
			t.Error("Verify accepted a request carrying no signature")
		}
	})

	// Both absent at once, which an HMAC adapter refuses for free but a
	// shared-token one does not: an absent header reads as "" and an absent
	// secret reads as "", so the comparison succeeds. Telegram authenticates
	// by shared token, so this shape is not hypothetical.
	t.Run("neither credentials nor a signature", func(t *testing.T) {
		req := f.Request
		req.Header = nil
		if err := p.Verify(req, nil); err == nil {
			t.Error("Verify accepted a request with no signature and no secret")
		}
	})

	if f.EnforcesFreshness {
		// A captured delivery stays valid forever unless the signed
		// timestamp is checked against when it arrived. ReceivedAt is on the
		// request rather than read from the clock so this is testable at all.
		t.Run("a replay", func(t *testing.T) {
			req := f.Request
			req.ReceivedAt = f.Request.ReceivedAt.Add(24 * time.Hour)
			if err := p.Verify(req, f.Creds); err == nil {
				t.Error("Verify accepted a delivery a day older than the signed timestamp")
			}
		})
	}

	t.Run("without altering the body", func(t *testing.T) {
		body := bytes.Clone(f.Request.Body)
		_ = p.Verify(f.Request, f.Creds)
		if !bytes.Equal(body, f.Request.Body) {
			t.Error("Verify mutated Body; the signature covers those exact bytes")
		}
	})
}

// survivesJunk runs Parse on bytes a stranger chose. It may return an error
// for any of these, but it may not panic: a panic on the webhook path is a
// request that takes the process with it, and the sender controls the input.
func survivesJunk(t *testing.T, p gateway.Provider) {
	t.Helper()

	for name, body := range map[string][]byte{
		"nil":              nil,
		"empty":            {},
		"not json":         []byte("not json at all"),
		"truncated":        []byte(`{"event":`),
		"json null":        []byte("null"),
		"json array":       []byte("[]"),
		"json string":      []byte(`"a message"`),
		"empty object":     []byte("{}"),
		"null fields":      []byte(`{"event":null,"author":null,"session":null}`),
		"wrong types":      []byte(`{"id":42,"text":[],"author":"nope"}`),
		"deeply nested":    []byte(strings.Repeat(`{"a":`, 200) + "1" + strings.Repeat("}", 200)),
		"huge number":      []byte(`{"size":` + strings.Repeat("9", 400) + `}`),
		"nul bytes":        {0, 0, 0, 0},
		"invalid utf-8":    {'{', '"', 'a', '"', ':', '"', 0xff, 0xfe, '"', '}'},
		"unicode overflow": []byte(`{"text":"\ud800"}`),
	} {
		t.Run(name, func(t *testing.T) {
			req := gateway.WebhookRequest{
				Method: "POST",
				Body:   body,
			}
			// Recovering here fails this input rather than ending the run, so
			// every case is reported instead of only the first.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse panicked on %s input: %v", name, r)
				}
			}()

			// Whatever comes back is the adapter's business. A malformed
			// message for malformed input is dropped by the handler and
			// stored nowhere; a panic takes the process down.
			_, _ = p.Parse(req)
		})
	}
}

func produces(t *testing.T, p gateway.Provider, f Fixtures) {
	t.Helper()

	res, err := p.Parse(f.Request)
	if err != nil {
		t.Fatalf("Parse on the valid fixture: %v", err)
	}
	if len(res.Messages) == 0 {
		t.Fatal("Parse returned no messages for a message fixture")
	}

	if got := res.Messages[0].ExternalID; got != f.WantExternalID {
		t.Errorf("ExternalID = %q, want %q", got, f.WantExternalID)
	}

	// The same rules the handler applies, called rather than restated — a
	// suite with its own copy of the contract drifts from the contract.
	for i, m := range res.Messages {
		if err := m.Validate(); err != nil {
			t.Errorf("message %d is not one the handler can use: %v", i, err)
		}
	}

	// Parse is required to be pure, so the same bytes give the same answer.
	// An adapter reading the clock or a counter fails here, and would
	// otherwise surface as a flake somewhere far away.
	t.Run("is deterministic", func(t *testing.T) {
		again, err := p.Parse(f.Request)
		if err != nil {
			t.Fatalf("Parse on the second call: %v", err)
		}
		if !reflect.DeepEqual(res, again) {
			t.Error("Parse returned a different result for the same request")
		}
	})

	t.Run("without altering the body", func(t *testing.T) {
		body := bytes.Clone(f.Request.Body)
		_, _ = p.Parse(f.Request)
		if !bytes.Equal(body, f.Request.Body) {
			t.Error("Parse mutated Body; the handler still needs it for logging and replay")
		}
	})
}

// An adapter names attachments; it never fetches them. Downloading needs a
// credential and a network call, so the adapter returns a Ref and the handler
// — which already resolved Keys() into Credentials — decides whether and when.
//
// What this suite cannot check is what FetchAttachment returns: that needs the
// provider's own server. It checks the half that is checkable, which is that
// every ref is resolvable in principle.
func attachments(t *testing.T, p gateway.Provider, f Fixtures) {
	t.Helper()

	_, fetches := p.(gateway.Fetcher)

	if f.CarriesNoAttachments {
		if len(f.AttachmentRequest.Body) > 0 {
			t.Error("CarriesNoAttachments is set and an AttachmentRequest was given; " +
				"one of them is wrong")
		}

		// Implementing Fetcher while claiming to have no attachments is dead
		// code that reads as a working feature — the shape this whole suite
		// exists to catch.
		if fetches {
			t.Error("the provider implements gateway.Fetcher but declares that it " +
				"carries no attachments, so nothing can ever call it")
		}
		return
	}

	if len(f.AttachmentRequest.Body) == 0 {
		t.Fatal("no AttachmentRequest, and CarriesNoAttachments is not set. " +
			"Give a recorded delivery with an attachment, or say the provider has none")
	}

	res, err := p.Parse(f.AttachmentRequest)
	if err != nil {
		t.Fatalf("Parse on the attachment fixture: %v", err)
	}

	var found []gateway.Attachment
	for i, m := range res.Messages {
		if err := m.Validate(); err != nil {
			t.Errorf("message %d is not one the handler can use: %v", i, err)
		}
		found = append(found, m.Attachments...)
	}
	if len(found) == 0 {
		t.Fatal("the attachment fixture parsed to no attachments, so this case is " +
			"passing without exercising anything")
	}

	// Ref is the only field the handler trusts, and the only one it cannot
	// work without. Everything else is what the provider claimed.
	//
	// Unique within the delivery, because FetchAttachment is given a ref and
	// nothing else. Two attachments sharing one means the second resolves to
	// the first one's bytes, stored under the second one's filename — which
	// looks exactly like a successful ingest.
	seen := make(map[string]int, len(found))
	for i, a := range found {
		if a.Ref == "" {
			t.Errorf("attachment %d has no Ref, so nothing can fetch it", i)
		} else if first, dup := seen[a.Ref]; dup {
			t.Errorf("attachments %d and %d share the ref %q, so one of them "+
				"cannot be fetched", first, i, a.Ref)
		} else {
			seen[a.Ref] = i
		}

		if a.ClaimedSize < 0 {
			t.Errorf("attachment %d claims a size of %d", i, a.ClaimedSize)
		}
	}

	// Refs with no way to resolve them is broken by construction: it compiles,
	// it parses, and it fails on the first real message.
	if !fetches {
		t.Error("the provider produces attachments but does not implement " +
			"gateway.Fetcher, so their refs can never be resolved")
	}
}
