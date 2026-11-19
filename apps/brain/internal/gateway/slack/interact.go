package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// InteractivitySuffix is where Slack's Interactivity Request URL points,
// after the channel's hook path: the button taps and the decline modal.
const InteractivitySuffix = "/interactivity"

const (
	// The action ids of the three buttons under an approval. Which request a
	// button answers is its value.
	actionApprove    = "openarity_approve"
	actionApproveRun = "openarity_approve_run"
	actionDecline    = "openarity_decline"

	// declineCallback names the decline-reason modal; reasonBlock and
	// reasonAction are where its one input is read back from.
	declineCallback = "openarity_decline_reason"
	reasonBlock     = "reason"
	reasonAction    = "reason"

	// maxPayload bounds an interactivity body: Slack's are a few KiB.
	maxPayload = 256 << 10
	// maxApprovalID bounds what a button value may name.
	maxApprovalID = 200
	// maxReason bounds a decline reason, in characters.
	maxReason = 500
	// maxID bounds a trigger or view id.
	maxID = 256
	// maxOriginal bounds the words of the tapped message kept in a handle.
	maxOriginal = 3000
)

var (
	channelPattern = regexp.MustCompile(`^[CDG][A-Z0-9]{2,}$`)
	tsPattern      = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{6}$`)
)

// decisions is what each button decides, and what the conversation reads as
// pressed.
var decisions = map[string]struct {
	decision gateway.Decision
	text     string
}{
	actionApprove:    {gateway.Approve, "Approve"},
	actionApproveRun: {gateway.ApproveRun, "Approve for this run"},
	actionDecline:    {gateway.DeclineAsk, "Decline"},
}

// interaction is the payload of an interactivity delivery, as far as the
// adapter reads it.
type interaction struct {
	Type      string `json:"type"`
	TriggerID string `json:"trigger_id"`
	User      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	ResponseURL string `json:"response_url"`
	Channel     struct {
		ID string `json:"id"`
	} `json:"channel"`
	Container struct {
		ChannelID   string `json:"channel_id"`
		ThreadTS    string `json:"thread_ts"`
		IsEphemeral bool   `json:"is_ephemeral"`
	} `json:"container"`
	Message struct {
		ThreadTS string `json:"thread_ts"`
		Text     string `json:"text"`
		Blocks   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"blocks"`
	} `json:"message"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	View *struct {
		ID              string `json:"id"`
		CallbackID      string `json:"callback_id"`
		PrivateMetadata string `json:"private_metadata"`
		State           struct {
			Values map[string]map[string]struct {
				Value string `json:"value"`
			} `json:"values"`
		} `json:"state"`
	} `json:"view"`
}

// handle is what a tap leaves for the notice that answers it, in
// Answer.Handle: where Slack takes the answer, and which conversation the tap
// was in, which the decline modal carries on.
type handle struct {
	ResponseURL string              `json:"response_url,omitempty"`
	TriggerID   string              `json:"trigger_id,omitempty"`
	Ref         string              `json:"ref"`
	Kind        gateway.SessionKind `json:"kind"`
	// Original is the words of the message that was tapped, clipped, so the
	// outcome can be written under them. A modal submission has none.
	Original string `json:"original,omitempty"`
}

// modalState is the decline modal's private_metadata. A view_submission does
// not say which thread it came from, so the modal carries it.
type modalState struct {
	Ref      string              `json:"ref"`
	Kind     gateway.SessionKind `json:"kind"`
	Approval string              `json:"approval"`
	URL      string              `json:"url"`
}

// parseInteraction reads what Slack posts to the Interactivity Request URL:
// a form with one field, payload, holding JSON. Only a tap on one of our
// buttons and the decline modal's submission are anything to do.
func parseInteraction(req gateway.WebhookRequest) (gateway.Result, error) {
	if len(req.Body) > maxPayload {
		return gateway.Result{}, errors.New("slack: an interactivity payload too large to be Slack's")
	}
	form, err := url.ParseQuery(string(req.Body))
	if err != nil {
		return gateway.Result{}, fmt.Errorf("slack: %w", err)
	}
	var p interaction
	if err := json.Unmarshal([]byte(form.Get("payload")), &p); err != nil {
		return gateway.Result{}, fmt.Errorf("slack: the interactivity payload: %w", err)
	}
	switch p.Type {
	case "block_actions":
		return tapOf(&p, req)
	case "view_submission":
		return submissionOf(&p, req)
	}
	return gateway.Result{}, nil
}

func tapOf(p *interaction, req gateway.WebhookRequest) (gateway.Result, error) {
	if p.Container.IsEphemeral {
		return gateway.Result{}, nil
	}
	var id, text string
	var decision gateway.Decision
	for _, a := range p.Actions {
		if d, ok := decisions[a.ActionID]; ok {
			id, decision, text = a.Value, d.decision, d.text
			break
		}
	}
	if decision == "" {
		return gateway.Result{}, nil
	}
	if id == "" || len(id) > maxApprovalID {
		return gateway.Result{}, errors.New("slack: a button naming no usable approval")
	}
	if p.TriggerID == "" || len(p.TriggerID) > maxID {
		return gateway.Result{}, errors.New("slack: a tap with no trigger id")
	}
	if !userPattern.MatchString(p.User.ID) {
		return gateway.Result{}, errors.New("slack: a tap by no user")
	}
	if !slackURL(p.ResponseURL) {
		return gateway.Result{}, errors.New("slack: a response_url that is not Slack's")
	}
	session, ok := tapSession(p)
	if !ok {
		return gateway.Result{}, nil
	}
	raw, err := json.Marshal(handle{
		ResponseURL: p.ResponseURL, TriggerID: p.TriggerID, Ref: session.Ref, Kind: session.Kind,
		Original: clipRunes(p.original(), maxOriginal),
	})
	if err != nil {
		return gateway.Result{}, fmt.Errorf("slack: %w", err)
	}
	return gateway.Result{Messages: []gateway.Inbound{{
		ExternalID: "tap:" + p.TriggerID,
		Author:     gateway.Author{Ref: p.User.ID, DisplayName: p.name()},
		Session:    session,
		Text:       text,
		SentAt:     req.ReceivedAt,
		Answer:     &gateway.Answer{ApprovalID: id, Decision: decision, Handle: raw},
	}}}, nil
}

func submissionOf(p *interaction, req gateway.WebhookRequest) (gateway.Result, error) {
	v := p.View
	if v == nil || v.CallbackID != declineCallback {
		return gateway.Result{}, nil
	}
	if v.ID == "" || len(v.ID) > maxID {
		return gateway.Result{}, errors.New("slack: a modal with no id")
	}
	if !userPattern.MatchString(p.User.ID) {
		return gateway.Result{}, errors.New("slack: a submission by no user")
	}
	var m modalState
	if err := json.Unmarshal([]byte(v.PrivateMetadata), &m); err != nil {
		return gateway.Result{}, errors.New("slack: a modal that carries nothing of ours")
	}
	if m.Approval == "" || len(m.Approval) > maxApprovalID || !validSession(m.Ref, m.Kind) || !slackURL(m.URL) {
		return gateway.Result{}, errors.New("slack: a modal whose metadata names no conversation or approval")
	}
	reason := strings.TrimSpace(v.State.Values[reasonBlock][reasonAction].Value)
	if utf8.RuneCountInString(reason) > maxReason {
		reason = string([]rune(reason)[:maxReason])
	}
	raw, err := json.Marshal(handle{ResponseURL: m.URL, Ref: m.Ref, Kind: m.Kind})
	if err != nil {
		return gateway.Result{}, fmt.Errorf("slack: %w", err)
	}
	return gateway.Result{Messages: []gateway.Inbound{{
		ExternalID: "modal:" + v.ID,
		Author:     gateway.Author{Ref: p.User.ID, DisplayName: p.name()},
		Session:    gateway.Session{Ref: m.Ref, Kind: m.Kind},
		Text:       "Decline",
		SentAt:     req.ReceivedAt,
		Answer:     &gateway.Answer{ApprovalID: m.Approval, Decision: gateway.Decline, Reason: reason, Handle: raw},
	}}}, nil
}

// original is the tapped message words: its markdown block, which is what
// Reply drew, or else its fallback text.
func (p *interaction) original() string {
	for _, b := range p.Message.Blocks {
		if b.Type == "markdown" && strings.TrimSpace(b.Text) != "" {
			return strings.TrimSpace(b.Text)
		}
	}
	return strings.TrimSpace(p.Message.Text)
}

func (p *interaction) name() string {
	if p.User.Username != "" {
		return p.User.Username
	}
	return p.User.Name
}

// tapSession is the conversation the tapped message is in, named as the
// messages in it are: a direct message by its channel, anything else by the
// thread the bot answered in. A message in no thread is nobody's.
func tapSession(p *interaction) (gateway.Session, bool) {
	channel := p.Channel.ID
	if channel == "" {
		channel = p.Container.ChannelID
	}
	if !channelPattern.MatchString(channel) {
		return gateway.Session{}, false
	}
	if strings.HasPrefix(channel, "D") {
		return gateway.Session{Ref: channel, Kind: gateway.SessionDirect}, true
	}
	thread := p.Message.ThreadTS
	if thread == "" {
		thread = p.Container.ThreadTS
	}
	if !tsPattern.MatchString(thread) {
		return gateway.Session{}, false
	}
	return gateway.Session{Ref: channel + ":" + thread, Kind: gateway.SessionThread}, true
}

// validSession is whether a ref and kind are ones sessionOf could have made
// for a conversation with the bot.
func validSession(ref string, kind gateway.SessionKind) bool {
	channel, thread, threaded := strings.Cut(ref, ":")
	if !channelPattern.MatchString(channel) {
		return false
	}
	switch kind {
	case gateway.SessionDirect:
		return !threaded
	case gateway.SessionThread:
		return threaded && tsPattern.MatchString(thread)
	case gateway.SessionGroup:
		// Never the ref of a conversation with the bot: see sessionOf.
	}
	return false
}

// slackURL is whether an address is one Slack answers a tap at: only that is
// ever posted to for a tap, and what is posted names no credential.
func slackURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "hooks.slack.com" && u.User == nil
}
