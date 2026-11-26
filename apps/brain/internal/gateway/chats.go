package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

// ErrCannot is a provider that has no way to do what was asked: it opens no
// direct conversation, or looks up no channel.
var ErrCannot = errors.New("the channel's provider cannot do that")

// A Refusal is the platform saying no, in words for the person who asked:
// the app lacks a scope, or the bot is not in the channel.
type Refusal struct{ Msg string }

func (r *Refusal) Error() string { return r.Msg }

// Chats looks things up on a channel's platform, with the channel's own
// credentials, when a schedule is made: the maker's direct conversation with
// the bot, or a channel the schedule will post in.
type Chats struct {
	secrets Secrets
	reg     Registry
	poster  Poster
	now     func() time.Time

	mu      sync.Mutex
	listed  map[uuid.UUID]listing
	flights singleflight.Group
}

// A ChannelList is a workspace's channels. Complete is false when it has more
// than one listing reads (maxListPages), and only those were read.
type ChannelList struct {
	Channels []PlatformChannel
	Complete bool
}

// A listing is a workspace's channels, or the platform's no, as they were
// when last asked, kept until its time is up.
type listing struct {
	until time.Time
	list  ChannelList
	err   error
}

const (
	// listFor is how long a workspace's channels are kept: Slack has no
	// search by name and lets a list be asked for only so often, so a person
	// typing a search reads the kept list, not Slack.
	listFor = 5 * time.Minute
	// listTimeout bounds one listing, every page of it, apart from the
	// request that started it.
	listTimeout = time.Minute
	// refusedFor is how long the platform's no is kept: long enough that a
	// person typing a search does not ask on every keystroke, short enough
	// that a scope just added, or a rate limit, clears soon.
	refusedFor = 30 * time.Second
	// maxListPages bounds one listing at a few thousand channels.
	maxListPages = 15
)

// NewChats asks the platforms through poster, which must refuse what a team
// may not reach, with credentials read from secrets.
func NewChats(sec Secrets, reg Registry, poster Poster) *Chats {
	return &Chats{secrets: sec, reg: reg, poster: poster, now: time.Now, listed: map[uuid.UUID]listing{}}
}

// Listers is every provider that lists its workspace's channels, sorted.
func (c *Chats) Listers() []string {
	return slices.Sorted(maps.Keys(c.reg.listers))
}

// ListsChannels is whether a provider lists its workspace's channels.
func (c *Chats) ListsChannels(provider string) bool {
	_, ok := c.reg.ChannelLister(provider)
	return ok
}

// ListChannels is the channel's workspace's channels, kept for listFor. A
// *Refusal is the platform's no, kept for refusedFor. A failure to ask is not
// kept. Callers asking at once for one workspace share one listing, which
// runs apart from any one of them: one who goes away leaves it to the rest.
func (c *Chats) ListChannels(ctx context.Context, team, channel uuid.UUID, provider string) (ChannelList, error) {
	l, ok := c.reg.ChannelLister(provider)
	if !ok {
		return ChannelList{}, ErrCannot
	}
	if kept, ok := c.fresh(channel); ok {
		return kept.list, kept.err
	}
	flight := c.flights.DoChan(channel.String(), func() (v any, err error) {
		defer func() {
			// DoChan panics on a goroutine of its own, which nothing recovers.
			if r := recover(); r != nil {
				err = fmt.Errorf("listing the channels: %v", r)
			}
		}()
		if kept, ok := c.fresh(channel); ok { // a listing just ended kept it
			return kept.list, kept.err
		}
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listTimeout)
		defer cancel()
		return c.list(lctx, team, channel, provider, l)
	})
	select {
	case <-ctx.Done():
		return ChannelList{}, ctx.Err()
	case r := <-flight:
		if r.Err != nil {
			return ChannelList{}, r.Err
		}
		list, ok := r.Val.(ChannelList)
		if !ok {
			return ChannelList{}, fmt.Errorf("listing the channels gave %T", r.Val)
		}
		return list, nil
	}
}

// fresh is a workspace's listing while it is kept.
func (c *Chats) fresh(channel uuid.UUID) (listing, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept, ok := c.listed[channel]
	return kept, ok && c.now().Before(kept.until)
}

func (c *Chats) list(ctx context.Context, team, channel uuid.UUID, provider string, l ChannelLister) (ChannelList, error) {
	creds, err := c.credentials(ctx, team, channel, provider)
	if err != nil {
		return ChannelList{}, err
	}
	var all ChannelList
	cursor := ""
	for range maxListPages {
		req, err := l.ListRequest(creds, cursor)
		if err != nil {
			return ChannelList{}, &Refusal{Msg: err.Error()}
		}
		body, err := c.ask(ctx, channel, req)
		if err != nil {
			return ChannelList{}, err
		}
		page, next, err := l.ListPage(body)
		if err != nil {
			refused := &Refusal{Msg: err.Error()}
			c.keep(channel, listing{until: c.now().Add(refusedFor), err: refused})
			return ChannelList{}, refused
		}
		all.Channels = append(all.Channels, page...)
		if next == "" {
			all.Complete = true
			break
		}
		cursor = next
	}
	c.keep(channel, listing{until: c.now().Add(listFor), list: all})
	return all, nil
}

// keep keeps a listing, and lets go of those past their time, so a channel
// deleted is not kept for ever.
func (c *Chats) keep(channel uuid.UUID, l listing) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	maps.DeleteFunc(c.listed, func(_ uuid.UUID, k listing) bool { return !now.Before(k.until) })
	c.listed[channel] = l
}

// OpensDirect is whether a provider opens a direct conversation by itself.
func (c *Chats) OpensDirect(provider string) bool {
	_, ok := c.reg.DirectOpener(provider)
	return ok
}

// OpenDirect is the session ref of the bot's direct conversation with user
// on the channel. A *Refusal is the platform's no, said for a person.
func (c *Chats) OpenDirect(ctx context.Context, team, channel uuid.UUID, provider, user string) (string, error) {
	d, ok := c.reg.DirectOpener(provider)
	if !ok {
		return "", ErrCannot
	}
	creds, err := c.credentials(ctx, team, channel, provider)
	if err != nil {
		return "", err
	}
	req, err := d.DirectRequest(creds, user)
	if err != nil {
		return "", &Refusal{Msg: err.Error()}
	}
	body, err := c.ask(ctx, channel, req)
	if err != nil {
		return "", err
	}
	ref, err := d.DirectRef(body)
	if err != nil {
		return "", &Refusal{Msg: err.Error()}
	}
	return ref, nil
}

// ChannelName is the name of a channel the bot can post in, by its id. A
// *Refusal is the platform's no, said for a person.
func (c *Chats) ChannelName(ctx context.Context, team, channel uuid.UUID, provider, ref string) (string, error) {
	f, ok := c.reg.ChannelFinder(provider)
	if !ok {
		return "", ErrCannot
	}
	creds, err := c.credentials(ctx, team, channel, provider)
	if err != nil {
		return "", err
	}
	req, err := f.ChannelRequest(creds, ref)
	if err != nil {
		return "", &Refusal{Msg: err.Error()}
	}
	body, err := c.ask(ctx, channel, req)
	if err != nil {
		return "", err
	}
	name, err := f.ChannelName(body)
	if err != nil {
		return "", &Refusal{Msg: err.Error()}
	}
	return name, nil
}

// ask posts req and keeps its answer for the provider to read.
func (c *Chats) ask(ctx context.Context, channel uuid.UUID, req OutboundRequest) ([]byte, error) {
	var answer []byte
	accept := req.Accepted
	req.Accepted = func(status int, body []byte) error {
		answer = bytes.Clone(body)
		if accept != nil {
			return accept(status, body)
		}
		return nil
	}
	if err := c.poster.Post(ctx, channel, req); err != nil {
		return nil, fmt.Errorf("asking the platform: %w", err)
	}
	return answer, nil
}

func (c *Chats) credentials(ctx context.Context, team, channel uuid.UUID, provider string) (Credentials, error) {
	p, ok := c.reg.Get(provider)
	if !ok {
		return nil, fmt.Errorf("no %s provider is registered", provider)
	}
	path := secrets.Path(team, secrets.KindChannel, channel)
	creds := make(Credentials, len(p.Keys()))
	for _, key := range p.Keys() {
		v, err := c.secrets.Get(ctx, path, key)
		if err != nil {
			return nil, fmt.Errorf("reading the channel's %s: %w", key, err)
		}
		creds[key] = v
	}
	return creds, nil
}
