package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/markdown"
)

// maxAsks is how many requests of one prompt draw buttons: a message holds
// 50 blocks, and each request takes two. A request past it is still answered
// by typing, as the text says.
const maxAsks = 20

// maxLabel bounds the line naming a request above its buttons.
const maxLabel = 200

// block is one Block Kit block, of the few kinds a reply draws.
type block struct {
	Type     string    `json:"type"`
	BlockID  string    `json:"block_id,omitempty"`
	Text     string    `json:"text,omitempty"`
	Elements []element `json:"elements,omitempty"`
}

// element is a button, or the line of a context block.
type element struct {
	Type     string `json:"type"`
	Text     any    `json:"text,omitempty"`
	ActionID string `json:"action_id,omitempty"`
	Value    string `json:"value,omitempty"`
	Style    string `json:"style,omitempty"`
}

type plain struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func plainText(s string) plain { return plain{Type: "plain_text", Text: s} }

// postBlocks is chat.postMessage with blocks, which markdown_text cannot be
// sent with: the answer goes in a markdown block, and Text is only the
// notification's.
type postBlocks struct {
	Channel     string  `json:"channel"`
	ThreadTS    string  `json:"thread_ts,omitempty"`
	Text        string  `json:"text"`
	Blocks      []block `json:"blocks"`
	UnfurlLinks bool    `json:"unfurl_links"`
	UnfurlMedia bool    `json:"unfurl_media"`
}

func postJSON(token string, body postBlocks) (gateway.OutboundRequest, error) {
	return postAs(token, "chat.postMessage", body)
}

func postAs(token, method string, body any) (gateway.OutboundRequest, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return gateway.OutboundRequest{}, fmt.Errorf("slack: encoding the message: %w", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Authorization", "Bearer "+token)
	return gateway.OutboundRequest{URL: apiBase + method, Header: h, Body: raw, Accepted: accepted}, nil
}

// approvalBlocks is the buttons under an approval, for each request the chat
// asks its person to answer. A request it does not has none: it went to an
// approver, and a button a person here pressed would only be refused.
func approvalBlocks(p *approval.Prompt) []block {
	if p == nil {
		return nil
	}
	var asks []approval.PromptRequest
	for _, r := range p.Requests {
		if r.Ask && r.ID != "" && len(r.ID) <= maxApprovalID && len(asks) < maxAsks {
			asks = append(asks, r)
		}
	}
	var out []block
	for _, r := range asks {
		if len(asks) > 1 {
			// More than one request: say which a row of buttons is for.
			label := approval.ToolName(r.Tool)
			if r.Summary != "" {
				label += ": " + r.Summary
			}
			out = append(out, block{Type: "context", Elements: []element{{Type: "plain_text", Text: clipRunes(label, maxLabel)}}})
		}
		out = append(out, block{Type: "actions", BlockID: "approval:" + r.ID, Elements: []element{
			{Type: "button", Text: plainText("Approve"), ActionID: actionApprove, Value: r.ID, Style: "primary"},
			{Type: "button", Text: plainText("Approve for this run"), ActionID: actionApproveRun, Value: r.ID},
			{Type: "button", Text: plainText("Decline…"), ActionID: actionDecline, Value: r.ID, Style: "danger"},
		}})
	}
	return out
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// tapNotice answers a tap where it was made, through the response_url or
// trigger_id Slack gave with it. What is meant for the tapper alone is never
// sent where anyone could read it.
func tapNotice(token string, out gateway.Outbound) (gateway.OutboundRequest, error) {
	n := out.Notice
	var h handle
	if err := json.Unmarshal(n.Handle, &h); err != nil {
		return gateway.OutboundRequest{}, fmt.Errorf("%w: slack: the tap's handle could not be read", gateway.ErrUnsendable)
	}
	if !validSession(h.Ref, h.Kind) {
		return gateway.OutboundRequest{}, fmt.Errorf("%w: slack: the tap's handle names no conversation", gateway.ErrUnsendable)
	}
	if n.Form != nil {
		return declineForm(token, h, n.Form.ID)
	}
	if !slackURL(h.ResponseURL) {
		return gateway.OutboundRequest{}, fmt.Errorf("%w: slack: the tap's response_url is not Slack's", gateway.ErrUnsendable)
	}
	words := out.Text
	if n.Settled && !n.Private && h.Original != "" {
		// The outcome goes under what the agent said, not in its place.
		words = h.Original + "\n\n" + out.Text
	}
	parts := markdown.Split(Sanitize(words), messageLimits)
	if len(parts) == 0 {
		return gateway.OutboundRequest{}, errors.New("slack: nothing to send")
	}
	text := parts[0]
	body := struct {
		ResponseType    string  `json:"response_type,omitempty"`
		ReplaceOriginal bool    `json:"replace_original"`
		ThreadTS        string  `json:"thread_ts,omitempty"`
		Text            string  `json:"text"`
		Blocks          []block `json:"blocks"`
	}{Text: text, Blocks: []block{{Type: "markdown", Text: text}}}
	switch {
	case n.Private:
		// Private wins over Settled: a word for the tapper alone is never
		// written into the message everybody reads.
		body.ResponseType = "ephemeral"
		_, body.ThreadTS, _ = target(h.Ref)
	case n.Settled:
		body.ReplaceOriginal = true
	default:
		body.ResponseType = "in_channel"
		_, body.ThreadTS, _ = target(h.Ref)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return gateway.OutboundRequest{}, fmt.Errorf("slack: encoding the message: %w", err)
	}
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json; charset=utf-8")
	// No Authorization: a response_url is its own credential, and the bot's
	// token goes only to Slack's API.
	return gateway.OutboundRequest{URL: h.ResponseURL, Header: hdr, Body: raw, Accepted: acceptedURL}, nil
}

// acceptedURL reads a response_url's answer: plain "ok" on success. One that
// is JSON and says "ok": false is a refusal.
func acceptedURL(status int, body []byte) error {
	var a struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(body, &a) == nil && a.OK != nil {
		return accepted(status, body)
	}
	return nil
}

// declineForm opens the modal that asks for a reason, with views.open and
// the tap's trigger_id. The modal carries the conversation and the request it
// is for, because its submission says neither.
func declineForm(token string, h handle, approvalID string) (gateway.OutboundRequest, error) {
	switch {
	case h.TriggerID == "" || len(h.TriggerID) > maxID:
		return gateway.OutboundRequest{}, fmt.Errorf("%w: slack: no trigger id to open a form with", gateway.ErrUnsendable)
	case approvalID == "" || len(approvalID) > maxApprovalID:
		return gateway.OutboundRequest{}, fmt.Errorf("%w: slack: a form for no approval", gateway.ErrUnsendable)
	case !slackURL(h.ResponseURL):
		return gateway.OutboundRequest{}, fmt.Errorf("%w: slack: the tap's response_url is not Slack's", gateway.ErrUnsendable)
	}
	meta, err := json.Marshal(modalState{Ref: h.Ref, Kind: h.Kind, Approval: approvalID, URL: h.ResponseURL})
	if err != nil {
		return gateway.OutboundRequest{}, fmt.Errorf("slack: %w", err)
	}
	type input struct {
		Type     string `json:"type"`
		BlockID  string `json:"block_id"`
		Optional bool   `json:"optional"`
		Label    plain  `json:"label"`
		Element  struct {
			Type      string `json:"type"`
			ActionID  string `json:"action_id"`
			Multiline bool   `json:"multiline"`
			MaxLength int    `json:"max_length"`
		} `json:"element"`
	}
	in := input{Type: "input", BlockID: reasonBlock, Optional: true, Label: plainText("Reason")}
	in.Element.Type, in.Element.ActionID, in.Element.Multiline, in.Element.MaxLength = "plain_text_input", reasonAction, true, maxReason
	body := map[string]any{
		"trigger_id": h.TriggerID,
		"view": map[string]any{
			"type":             "modal",
			"callback_id":      declineCallback,
			"private_metadata": string(meta),
			"title":            plainText("Decline"),
			"submit":           plainText("Decline"),
			"close":            plainText("Cancel"),
			"blocks":           []input{in},
		},
	}
	return postAs(token, "views.open", body)
}
