package gateway

import (
	"context"
	"errors"
	"sync"
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

// gated is echo held until its gate opens, telling when a post is waiting,
// and failing as a real poster does once its request's context is done.
type gated struct {
	echo
	mu      sync.Mutex
	waiting chan struct{}
	gate    chan struct{}
}

func (g *gated) Post(ctx context.Context, ch uuid.UUID, req OutboundRequest) error {
	g.waiting <- struct{}{}
	<-g.gate
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.echo.Post(ctx, ch, req)
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
	if err != nil || len(got.Channels) != 2 || !got.Complete || e.asked != 2 {
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

// The platform's no is a Refusal said for a person, and is kept briefly, so a
// missing scope is not asked about on every keystroke, and a scope just added
// shows soon.
func TestARefusalToListIsKeptBriefly(t *testing.T) {
	t.Parallel()
	l := &lister{err: errors.New("the app needs channels:read")}
	c, e := newChats(t, l)
	clock := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	team, ch := uuid.New(), uuid.New()
	_, err := c.ListChannels(t.Context(), team, ch, "lister")
	var refused *Refusal
	if !errors.As(err, &refused) || refused.Msg != "the app needs channels:read" {
		t.Fatalf("ListChannels = %v, want the platform's no", err)
	}
	if _, err := c.ListChannels(t.Context(), team, ch, "lister"); !errors.As(err, &refused) || e.asked != 1 {
		t.Errorf("asked %d times, want the refusal kept", e.asked)
	}
	clock = clock.Add(refusedFor)
	l.err, l.pages = nil, [][]PlatformChannel{{{Ref: "C1", Name: "a"}}}
	if got, err := c.ListChannels(t.Context(), team, ch, "lister"); err != nil || len(got.Channels) != 1 || e.asked != 2 {
		t.Errorf("after %s ListChannels = %v, %v after %d asks; want it asked again", refusedFor, got, err, e.asked)
	}
	if _, err := c.ListChannels(t.Context(), team, ch, "nobody"); !errors.Is(err, ErrCannot) {
		t.Errorf("a provider that lists nothing = %v, want ErrCannot", err)
	}
}

// A workspace with more channels than a listing reads is kept as far as it
// was read, and says it is not complete.
func TestAListTooLongIsKeptAndSaidIncomplete(t *testing.T) {
	t.Parallel()
	pages := make([][]PlatformChannel, maxListPages+1)
	for i := range pages {
		pages[i] = []PlatformChannel{{Ref: "C", Name: "c"}}
	}
	c, e := newChats(t, &lister{pages: pages})
	team, ch := uuid.New(), uuid.New()
	got, err := c.ListChannels(t.Context(), team, ch, "lister")
	if err != nil || got.Complete || len(got.Channels) != maxListPages || e.asked != maxListPages {
		t.Fatalf("ListChannels = %d channels, complete %v, %v after %d asks; want %d, incomplete",
			len(got.Channels), got.Complete, err, e.asked, maxListPages)
	}
	if _, err := c.ListChannels(t.Context(), team, ch, "lister"); err != nil || e.asked != maxListPages {
		t.Errorf("asked %d times, want the incomplete list kept", e.asked)
	}
}

// The person who started a listing going away does not fail the others
// waiting for it: the listing runs apart from them, once.
func TestACancelledLeaderDoesNotFailItsWaiters(t *testing.T) {
	t.Parallel()
	l := &lister{pages: [][]PlatformChannel{{{Ref: "C1", Name: "a"}}}}
	reg, err := NewRegistry(l)
	if err != nil {
		t.Fatal(err)
	}
	g := &gated{waiting: make(chan struct{}, 4), gate: make(chan struct{})}
	c := NewChats(&fakeSecrets{values: map[string]string{KeySigning: "s"}}, reg, g)
	team, ch := uuid.New(), uuid.New()

	leader, cancel := context.WithCancel(t.Context())
	led := make(chan error, 1)
	go func() {
		_, err := c.ListChannels(leader, team, ch, "lister")
		led <- err
	}()
	<-g.waiting // the listing is asking the platform
	waited := make(chan error, 1)
	var got ChannelList
	go func() {
		var err error
		got, err = c.ListChannels(t.Context(), team, ch, "lister")
		waited <- err
	}()
	cancel()
	if err := <-led; !errors.Is(err, context.Canceled) {
		t.Errorf("the leader = %v, want its own cancel", err)
	}
	time.Sleep(20 * time.Millisecond) // let the waiter join, if it has not
	close(g.gate)
	if err := <-waited; err != nil || len(got.Channels) != 1 {
		t.Errorf("the waiter = %+v, %v; want the list", got, err)
	}
	if g.asked != 1 {
		t.Errorf("asked %d times, want one listing", g.asked)
	}
}

// Keeping a listing lets go of those past their time.
func TestAnExpiredListingIsLetGo(t *testing.T) {
	t.Parallel()
	c, _ := newChats(t, &lister{pages: [][]PlatformChannel{{{Ref: "C1", Name: "a"}}}})
	clock := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	team, old, fresh := uuid.New(), uuid.New(), uuid.New()
	if _, err := c.ListChannels(t.Context(), team, old, "lister"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(listFor)
	if _, err := c.ListChannels(t.Context(), team, fresh, "lister"); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.listed[old]; ok || len(c.listed) != 1 {
		t.Errorf("kept %d listings, the expired one among them: %v", len(c.listed), ok)
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
