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

	mu     sync.Mutex
	listed map[uuid.UUID]listing
}

// A listing is a workspace's channels as they were when last asked.
type listing struct {
	at       time.Time
	channels []PlatformChannel
	err      error
}

const (
	// listFor is how long a workspace's channels are kept: Slack has no
	// search by name and lets a list be asked for only so often, so a person
	// typing a search reads the kept list, not Slack.
	listFor = 5 * time.Minute
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
// *Refusal is the platform's no, kept as long, so a missing scope is not
// asked about on every keystroke.
func (c *Chats) ListChannels(ctx context.Context, team, channel uuid.UUID, provider string) ([]PlatformChannel, error) {
	l, ok := c.reg.ChannelLister(provider)
	if !ok {
		return nil, ErrCannot
	}
	c.mu.Lock()
	kept, ok := c.listed[channel]
	c.mu.Unlock()
	if ok && c.now().Sub(kept.at) < listFor {
		return kept.channels, kept.err
	}
	creds, err := c.credentials(ctx, team, channel, provider)
	if err != nil {
		return nil, err
	}
	var all []PlatformChannel
	cursor := ""
	for range maxListPages {
		req, err := l.ListRequest(creds, cursor)
		if err != nil {
			return nil, &Refusal{Msg: err.Error()}
		}
		body, err := c.ask(ctx, channel, req)
		if err != nil {
			return nil, err
		}
		page, next, err := l.ListPage(body)
		if err != nil {
			// The platform's no is kept; a failure to ask is not.
			refused := &Refusal{Msg: err.Error()}
			c.keep(channel, listing{at: c.now(), err: refused})
			return nil, refused
		}
		all = append(all, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	c.keep(channel, listing{at: c.now(), channels: all})
	return all, nil
}

func (c *Chats) keep(channel uuid.UUID, l listing) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listed[channel] = l
}

// OpensDirect is whether a provider opens a direct conversation by itself.
func (c *Chats) OpensDirect(provider string) bool {
	_, ok := c.reg.DirectOpener(provider)
	return ok
}

// FindsChannels is whether a provider looks up a channel by its id.
func (c *Chats) FindsChannels(provider string) bool {
	_, ok := c.reg.ChannelFinder(provider)
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
