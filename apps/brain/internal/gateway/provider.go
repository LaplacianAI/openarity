package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
)

const (
	KeySigning     = "signing_secret"
	KeyVerifyToken = "verify_token"
)

type WebhookRequest struct {
	Suffix     string
	Method     string
	Header     http.Header
	Query      url.Values
	Body       []byte
	ReceivedAt time.Time
}

type Route struct {
	Method string
	Suffix string
}

type Credentials map[string]string

func (c Credentials) Get(key string) string { return c[key] }

type Provider interface {
	Name() string
	Routes() []Route
	Keys() []string
	Verify(req WebhookRequest, creds Credentials) error
	Parse(req WebhookRequest, creds Credentials) (Result, error)
}

type Fetcher interface {
	FetchAttachment(
		ctx context.Context, req WebhookRequest, ref string, creds Credentials,
	) ([]byte, error)
}

// Downloader fetches a file the provider holds rather than one sent in the
// delivery. It builds each request and the gateway sends it, with the reply
// client (public addresses and the operator's MCP_PRIVATE_NETWORKS, no
// redirect), so the adapter still holds no client:
// the first request from the ref, each next from the answer before, until
// one is final and its answer is the file.
type Downloader interface {
	DownloadRequest(ref string, prev []byte, creds Credentials) (req OutboundRequest, final bool, err error)
}

// Budgeted is a provider that gives its files longer than the defaults, which
// are sized to the shortest ack window, Slack's three seconds. A provider
// whose webhook waits for its answer may take longer; one that does not must
// not, or it loses the message to save a file.
type Budgeted interface {
	FetchBudget() (perFile, total time.Duration)
}

// Replier sends a run's answer back to the conversation it came from. It is
// beside Provider rather than on it: a provider with nowhere to post back has
// no honest implementation, and a stub returning nil would read as sent.
//
// Reply is called again for the same Outbound when an attempt fails or its
// result is lost, so a provider passes out.ID along for the receiver to drop
// a repeat by.
type Replier interface {
	// Reply is pure. A long reply is several requests, sent in order, and
	// the same inputs must split the same way, so a retry resumes where it
	// stopped rather than sending a part twice.
	Reply(ch Channel, out Outbound, creds Credentials, at time.Time) ([]OutboundRequest, error)
}

// Setup completes a channel's credentials from the provider's own API when
// the channel is connected: Telegram's bot id and username, from getMe.
type Setup interface {
	// Supplied is the keys a person gives when connecting the channel.
	Supplied() []string
	SetupRequest(supplied Credentials) (OutboundRequest, error)
	// Complete reads the answer and returns the keys to store beside
	// what was supplied, and anything the person should know about how
	// the account is set up: Telegram's privacy mode. An error here
	// refuses the channel; a warning does not.
	Complete(status int, body []byte) (Credentials, []string, error)
}

// Connector is a provider that can be told where to post: Telegram's
// setWebhook. The channels API points it at a channel when the channel is
// created and lets it go when the channel is deleted, so a person never runs
// the step by hand. Pure like Reply: it builds the requests and reads the
// answers; the brain sends them.
type Connector interface {
	ConnectRequest(hookURL string, creds Credentials) (OutboundRequest, error)
	DisconnectRequest(creds Credentials) (OutboundRequest, error)
	// Answered reads the provider's answer to either request.
	Answered(status int, body []byte) error
}

// URLReplier is a Replier that posts to an address its channel names, the
// reply URL, rather than into the conversation through the provider's own
// API. Only for these does a channel take a reply URL.
type URLReplier interface {
	Replier
	RepliesToURL()
}

// EventReplier is a Replier that also sends a run's steps, each a reply of
// kind "event" in the conversation, when its channel's reply_mode is events.
// Only for these does a channel take that mode.
type EventReplier interface {
	Replier
	RepliesWithEvents()
}

// FileReplier is a Replier that also sends a file a run handed its person,
// a reply of kind "file" with Outbound.File set. Only on these channels is
// a run given send_file. Its own package builds the file message, as it
// parses its webhooks; the sender loads the bytes first, so Reply stays pure.
type FileReplier interface {
	Replier
	RepliesWithFiles()
}

// FileUploader is a FileReplier whose platform takes a file in steps, each
// built from the answers before it: Slack's upload address, then the bytes,
// then the share. FileRequest is pure, like Reply: the step is len(answers),
// and every answer is passed, as a late step can need an early one's.
type FileUploader interface {
	FileReplier
	FileRequest(ch Channel, out Outbound, creds Credentials, answers [][]byte) (req OutboundRequest, final bool, err error)
}

// SecretIssuer is a provider whose platform issues the signing secret, as
// Slack's app settings do. The brain cannot generate one for it: a delivery
// is signed with the platform's, so a channel is refused without it.
type SecretIssuer interface {
	IssuesSigningSecret()
}

// Socket is a provider that holds a connection to its platform instead of
// taking webhooks: WhatsApp's linked device. Nothing arrives over HTTP to
// verify, so it declares no routes and need declare no keys; its messages
// reach the inbox from the process holding the connection, which also sends
// its channels' replies and notices through a Poster of its own. The worker's
// sender leaves those alone.
type Socket interface {
	HoldsConnection()
}

// Notifier tells a sender nobody has let in where they stand, in the
// conversation they wrote in: that they are waiting, and later that they are
// in. Optional, like Replier. Notice is pure.
type Notifier interface {
	Notice(ch Channel, n Notice, creds Credentials) (OutboundRequest, error)
}

type NoticeKind string

const (
	NoticeWaiting  NoticeKind = "waiting"
	NoticeAdmitted NoticeKind = "admitted"
)

// Notice is one notice, as a provider needs it. The brain owns the words: the
// provider names the sender, in its own way, before Text.
type Notice struct {
	// ID is the same on every attempt to send this notice.
	ID      string
	Kind    NoticeKind
	Session Session
	// InReplyTo is the provider's id for the message it answers; empty quotes
	// nothing.
	InReplyTo string
	Sender    Author
	Text      string
}

// NoticeText is what a notice says after the sender's name.
func NoticeText(k NoticeKind) string {
	switch k {
	case NoticeWaiting:
		return "I can't answer you yet: someone on the team has to let you in first. I'll tell you here when they have."
	case NoticeAdmitted:
		return "you're in. Send your message again and I'll answer it."
	default:
		return ""
	}
}

type OutboundRequest struct {
	// Method is POST when empty, which every reply is.
	Method string
	URL    string
	Header http.Header
	Body   []byte
	// Accepted reads a 2xx answer, for a provider that says no inside one:
	// Slack answers 200 with "ok": false. It is called only on a 2xx, with
	// at most the first 64 KiB of the body, and an error is the request
	// failing. Nil takes every 2xx as done.
	Accepted func(status int, body []byte) error
}

// Outbound is one reply, as a provider needs it.
type Outbound struct {
	// ID is the same on every attempt to send this reply.
	ID string
	// Session is the conversation it goes to, as the provider named it.
	Session Session
	// InReplyTo is the provider's id for the message it answers. Empty when it
	// answers several that came in while the team was working.
	InReplyTo string
	// Kind is "answer", "apology" when the run failed, "event": one step
	// of a run still working, sent before its answer and quoting nothing,
	// "approval": a run asking its person, "notice": the brain's short
	// word on an answer given in the chat, or "file": a file the run sent,
	// in File, with Text its name.
	Kind string
	Text string
	// File is a file reply's file, loaded for this attempt.
	File *OutFile
	// Prompt is an approval's requests, for the platform's buttons: those
	// with Ask set are its person's to answer here. Text already says how
	// to answer by typing, so a platform with no controls sends Text alone.
	Prompt *approval.Prompt
	// Notice is a notice's detail, for answering a tap where it was made.
	Notice *ReplyNotice
}

// OutFile is a file a run sent, as a platform sends it: its name, the type
// its bytes were sniffed as when it was kept, and the line sent with it.
type OutFile struct {
	Name, MediaType, Caption string
	Data                     []byte
}

// ReplyNotice is what a platform needs to send a notice the way the answer
// it is about was given.
type ReplyNotice struct {
	// Handle is the tap's, as the platform gave it in Answer.Handle: empty
	// for a typed reply, which is answered with Text in the conversation.
	Handle json.RawMessage `json:"handle,omitempty"`
	// Private is a word for the one person who tapped (only Sam can answer
	// this): Slack sends it ephemeral, Telegram as an alert, and a platform
	// that cannot, in the conversation.
	Private bool `json:"private,omitempty"`
	// Settled is a tap that decided its request: the prompt's controls are
	// replaced by Text, so an old prompt cannot be tapped again.
	Settled bool `json:"settled,omitempty"`
	// Form asks for the reason before declining: the request a form the
	// platform opens is for. Only for DeclineAsk.
	Form *approval.PromptRequest `json:"form,omitempty"`
}

type Registry struct {
	providers map[string]Provider
	repliers  map[string]Replier
	setups    map[string]Setup
	toURL     map[string]bool
	events    map[string]bool
	files     map[string]bool
	uploaders map[string]FileUploader
	connects  map[string]Connector
	notifiers map[string]Notifier
	sockets   map[string]bool
	issuers   map[string]bool
}

func NewRegistry(providers ...Provider) (Registry, error) {
	reg := Registry{
		providers: map[string]Provider{}, repliers: map[string]Replier{}, setups: map[string]Setup{},
		toURL: map[string]bool{}, events: map[string]bool{}, files: map[string]bool{}, uploaders: map[string]FileUploader{}, connects: map[string]Connector{}, notifiers: map[string]Notifier{},
		sockets: map[string]bool{}, issuers: map[string]bool{},
	}
	for _, p := range providers {
		name := p.Name()
		if name == "" {
			return Registry{}, fmt.Errorf("gateway: provider %T has an empty name", p)
		}
		if _, dup := reg.providers[name]; dup {
			return Registry{}, fmt.Errorf("gateway: two providers named %q", name)
		}
		if _, ok := p.(Socket); ok {
			// Mounted, a route would be a webhook nothing verifies.
			if len(p.Routes()) > 0 {
				return Registry{}, fmt.Errorf("gateway: provider %q holds a connection and declares routes", name)
			}
			reg.sockets[name] = true
		} else {
			if err := checkRoutes(name, p.Routes()); err != nil {
				return Registry{}, err
			}
			if len(p.Keys()) == 0 {
				return Registry{}, fmt.Errorf("gateway: provider %q declares no secret keys, so it cannot verify anything", name)
			}
		}
		reg.providers[name] = p
		if rp, ok := p.(Replier); ok {
			reg.repliers[name] = rp
		}
		if _, ok := p.(URLReplier); ok {
			reg.toURL[name] = true
		}
		if _, ok := p.(EventReplier); ok {
			reg.events[name] = true
		}
		if _, ok := p.(FileReplier); ok {
			reg.files[name] = true
		}
		if u, ok := p.(FileUploader); ok {
			reg.uploaders[name] = u
		}
		if c, ok := p.(Connector); ok {
			reg.connects[name] = c
		}
		if n, ok := p.(Notifier); ok {
			reg.notifiers[name] = n
		}
		if _, ok := p.(SecretIssuer); ok {
			reg.issuers[name] = true
		}
		if s, ok := p.(Setup); ok {
			for _, k := range s.Supplied() {
				if !slices.Contains(p.Keys(), k) {
					return Registry{}, fmt.Errorf("gateway: provider %q is supplied %q, which it does not declare", name, k)
				}
			}
			reg.setups[name] = s
		}
	}
	return reg, nil
}

func (r Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Replier is how the named provider sends replies, if it sends them at all.
func (r Registry) Replier(name string) (Replier, bool) {
	rp, ok := r.repliers[name]
	return rp, ok
}

// Uploader is how the named provider sends a file in steps, if it does.
func (r Registry) Uploader(name string) (FileUploader, bool) {
	u, ok := r.uploaders[name]
	return u, ok
}

// Connector is how the named provider is pointed at a channel, if it can be.
func (r Registry) Connector(name string) (Connector, bool) {
	c, ok := r.connects[name]
	return c, ok
}

// Notifier is how the named provider tells a waiting sender, if it can.
func (r Registry) Notifier(name string) (Notifier, bool) {
	n, ok := r.notifiers[name]
	return n, ok
}

// TakesReplyURL is whether the named provider's channels say where replies go.
func (r Registry) TakesReplyURL(name string) bool { return r.toURL[name] }

// RepliesWithEvents is whether the named provider sends a run's steps.
func (r Registry) RepliesWithEvents(name string) bool { return r.events[name] }

// SendsFiles is whether the provider sends a file a run hands its person.
func (r Registry) SendsFiles(name string) bool { return r.files[name] }

// Setup is how the named provider completes its credentials, if it needs to.
func (r Registry) Setup(name string) (Setup, bool) {
	s, ok := r.setups[name]
	return s, ok
}

// IssuesSigningSecret is whether the named provider's platform issues its
// channels' signing secret, which a person then supplies.
func (r Registry) IssuesSigningSecret(name string) bool { return r.issuers[name] }

// HoldsConnection is whether the named provider's channels are a socket's,
// sent by the process that holds it.
func (r Registry) HoldsConnection(name string) bool { return r.sockets[name] }

// Sockets is every provider that holds a connection, sorted.
func (r Registry) Sockets() []string {
	return slices.Sorted(maps.Keys(r.sockets))
}

// Names is every registered provider, sorted.
func (r Registry) Names() []string {
	return slices.Sorted(maps.Keys(r.providers))
}

func checkRoutes(name string, routes []Route) error {
	if len(routes) == 0 {
		return fmt.Errorf("gateway: provider %q declares no routes", name)
	}

	seen := make(map[Route]bool, len(routes))
	for _, rt := range routes {
		switch {
		case rt.Method == "":
			return fmt.Errorf("gateway: provider %q declares a route with no method", name)
		case rt.Suffix != "" && !strings.HasPrefix(rt.Suffix, "/"):
			return fmt.Errorf("gateway: provider %q has suffix %q, which must start with /", name, rt.Suffix)
		case strings.HasSuffix(rt.Suffix, "/"):
			return fmt.Errorf("gateway: provider %q has suffix %q, which must not end with /", name, rt.Suffix)
		case seen[rt]:
			return fmt.Errorf("gateway: provider %q declares %s %q twice", name, rt.Method, rt.Suffix)
		}
		seen[rt] = true
	}
	return nil
}
