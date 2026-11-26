package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// lister is a provider that lists channels from canned answers, one per page
// asked, and opens direct conversations.
type lister struct {
	fetchProvider
	pages [][]PlatformChannel
	err   error
}

func (*lister) Name() string { return "lister" }

func (l *lister) ChannelRequest(_ Credentials, ref string) (OutboundRequest, error) {
	return OutboundRequest{URL: "https://example.test/info", Body: []byte(ref)}, nil
}

func (*lister) ChannelName(body []byte) (string, error) { return "name-of-" + string(body), nil }

func (l *lister) ListRequest(_ Credentials, cursor string) (OutboundRequest, error) {
	return OutboundRequest{URL: "https://example.test/list", Body: []byte(cursor)}, nil
}

func (l *lister) ListPage(body []byte) ([]PlatformChannel, string, error) {
	if l.err != nil {
		return nil, "", l.err
	}
	i := 0
	if len(body) > 0 {
		i = int(body[0] - '0')
	}
	next := ""
	if i+1 < len(l.pages) {
		next = string(rune('0' + i + 1))
	}
	return l.pages[i], next, nil
}

func (*lister) DirectRequest(_ Credentials, user string) (OutboundRequest, error) {
	return OutboundRequest{URL: "https://example.test/open", Body: []byte(user)}, nil
}

func (*lister) DirectRef(body []byte) (string, error) {
	if string(body) == "U-refused" {
		return "", errors.New("the app needs im:write")
	}
	return "D-" + string(body), nil
}

// echo answers each request with its own body, and counts them.
type echo struct{ asked int }

func (e *echo) Post(_ context.Context, _ uuid.UUID, req OutboundRequest) error {
	e.asked++
	if req.Accepted != nil {
		return req.Accepted(200, req.Body)
	}
	return nil
}

func newChats(t *testing.T, l *lister) (*Chats, *echo) {
	t.Helper()
	reg, err := NewRegistry(l)
	if err != nil {
		t.Fatal(err)
	}
	e := &echo{}
	return NewChats(&fakeSecrets{values: map[string]string{KeySigning: "s"}}, reg, e), e
}

// A workspace's channels are read a page at a time, all of them, then kept:
// a person typing a search reads the kept list, not the platform.
func TestChannelsAreListedOnceAndKept(t *testing.T) {
	t.Parallel()
	l := &lister{pages: [][]PlatformChannel{{{Ref: "C1", Name: "a"}}, {{Ref: "C2", Name: "b"}}}}
	c, e := newChats(t, l)
	clock := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	team, ch := uuid.New(), uuid.New()

	got, err := c.ListChannels(t.Context(), team, ch, "lister")
	if err != nil || len(got) != 2 || e.asked != 2 {
		t.Fatalf("ListChannels = %v, %v after %d asks; want both pages", got, err, e.asked)
	}
	if _, err := c.ListChannels(t.Context(), team, ch, "lister"); err != nil || e.asked != 2 {
		t.Errorf("asked %d times within the minutes it is kept, want 2", e.asked)
	}
	clock = clock.Add(listFor)
	if _, err := c.ListChannels(t.Context(), team, ch, "lister"); err != nil || e.asked != 4 {
		t.Errorf("asked %d times once it went stale, want 4", e.asked)
	}
	if got := c.Listers(); len(got) != 1 || got[0] != "lister" {
		t.Errorf("Listers = %v", got)
	}
}

// The platform's no is a Refusal said for a person, and is kept as long as a
// list, so a missing scope is not asked about on every keystroke.
func TestARefusalToListIsKeptToo(t *testing.T) {
	t.Parallel()
	l := &lister{err: errors.New("the app needs channels:read")}
	c, e := newChats(t, l)
	team, ch := uuid.New(), uuid.New()
	_, err := c.ListChannels(t.Context(), team, ch, "lister")
	var refused *Refusal
	if !errors.As(err, &refused) || refused.Msg != "the app needs channels:read" {
		t.Fatalf("ListChannels = %v, want the platform's no", err)
	}
	if _, err := c.ListChannels(t.Context(), team, ch, "lister"); !errors.As(err, &refused) || e.asked != 1 {
		t.Errorf("asked %d times, want the refusal kept", e.asked)
	}
	if _, err := c.ListChannels(t.Context(), team, ch, "nobody"); !errors.Is(err, ErrCannot) {
		t.Errorf("a provider that lists nothing = %v, want ErrCannot", err)
	}
}

func TestADirectConversationIsOpenedOrRefusedForAPerson(t *testing.T) {
	t.Parallel()
	c, _ := newChats(t, &lister{})
	ref, err := c.OpenDirect(t.Context(), uuid.New(), uuid.New(), "lister", "U1")
	if err != nil || ref != "D-U1" {
		t.Errorf("OpenDirect = %q, %v", ref, err)
	}
	_, err = c.OpenDirect(t.Context(), uuid.New(), uuid.New(), "lister", "U-refused")
	var refused *Refusal
	if !errors.As(err, &refused) {
		t.Errorf("a refusal = %v, want it said for a person", err)
	}
	if name, err := c.ChannelName(t.Context(), uuid.New(), uuid.New(), "lister", "C9"); err != nil || name != "name-of-C9" {
		t.Errorf("ChannelName = %q, %v", name, err)
	}
	if !c.OpensDirect("lister") || c.OpensDirect("nobody") || !c.ListsChannels("lister") {
		t.Error("what the providers can do was misread")
	}
}
