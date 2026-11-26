package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

// A schedule may answer its maker in the bot's direct messages with them, or
// post at a channel's top level. Both are looked up when the schedule is
// made, as the bot sees them, so a refusal reaches the person making it
// rather than a reply that is given up later.

// DirectRequest opens, or finds, the bot's direct conversation with user:
// conversations.open, which needs the im:write scope.
func (provider) DirectRequest(creds gateway.Credentials, user string) (gateway.OutboundRequest, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return gateway.OutboundRequest{}, errors.New("slack: no bot token")
	}
	if !userPattern.MatchString(user) {
		return gateway.OutboundRequest{}, fmt.Errorf("slack: %q is not a Slack user", user)
	}
	req := form(token, "conversations.open", url.Values{"users": {user}})
	req.Accepted = nil // DirectRef reads the answer, refusals included
	return req, nil
}

// DirectRef reads conversations.open's answer into the conversation's id.
func (provider) DirectRef(body []byte) (string, error) {
	var a struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return "", errors.New("the answer Slack gave could not be read")
	}
	switch {
	case !a.OK && a.Error == "missing_scope":
		return "", errors.New("the Slack app needs the im:write scope to message you directly: " +
			"add it under OAuth & Permissions, then Reinstall to Workspace")
	case !a.OK && a.Error == "user_not_found":
		return "", errors.New("slack does not know you on this workspace")
	case !a.OK:
		return "", fmt.Errorf("slack would not open a direct conversation: %s", clip(a.Error))
	case !channelPattern.MatchString(a.Channel.ID):
		return "", errors.New("slack opened a conversation with no id")
	}
	return a.Channel.ID, nil
}

// ChannelRequest asks about a channel as the bot sees it: conversations.info.
func (provider) ChannelRequest(creds gateway.Credentials, ref string) (gateway.OutboundRequest, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return gateway.OutboundRequest{}, errors.New("slack: no bot token")
	}
	if !channelPattern.MatchString(ref) || ref[0] == 'D' {
		return gateway.OutboundRequest{}, fmt.Errorf("%q is not a Slack channel id, such as C0123ABCD", ref)
	}
	req := form(token, "conversations.info", url.Values{"channel": {ref}})
	req.Accepted = nil // ChannelName reads the answer, refusals included
	return req, nil
}

// ChannelName reads conversations.info's answer: a channel the bot is in, by
// name. One it is not in would refuse every post.
func (provider) ChannelName(body []byte) (string, error) {
	var a struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			Name       string `json:"name"`
			IsMember   bool   `json:"is_member"`
			IsArchived bool   `json:"is_archived"`
			IsIM       bool   `json:"is_im"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return "", errors.New("the answer Slack gave could not be read")
	}
	switch {
	case !a.OK && a.Error == "channel_not_found":
		return "", errors.New("slack knows no such channel, or the app is not in it: invite the app to the channel first")
	case !a.OK && a.Error == "missing_scope":
		return "", errors.New("the Slack app may not read that channel: it needs channels:read for a public one, " +
			"groups:read for a private one")
	case !a.OK:
		return "", fmt.Errorf("slack would not say what the channel is: %s", clip(a.Error))
	case a.Channel.IsIM:
		return "", errors.New("that is a direct message, not a channel")
	case a.Channel.IsArchived:
		return "", errors.New("that channel is archived")
	case !a.Channel.IsMember:
		return "", errors.New("the app is not in that channel: invite it first, with /invite")
	}
	return a.Channel.Name, nil
}

// listPage is how many channels one conversations.list call asks for.
const listPage = "200"

// ListRequest asks for one page of the workspace's channels, public and
// private, archived left out: conversations.list, which needs channels:read
// and groups:read. A private channel is listed only if the bot is in it.
func (provider) ListRequest(creds gateway.Credentials, cursor string) (gateway.OutboundRequest, error) {
	token := creds.Get(KeyBotToken)
	if token == "" {
		return gateway.OutboundRequest{}, errors.New("slack: no bot token")
	}
	v := url.Values{"types": {"public_channel,private_channel"}, "exclude_archived": {"true"}, "limit": {listPage}}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	req := form(token, "conversations.list", v)
	req.Accepted = nil // ListPage reads the answer, refusals included
	return req, nil
}

// ListPage reads one page of conversations.list.
func (provider) ListPage(body []byte) ([]gateway.PlatformChannel, string, error) {
	var a struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Channels []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			IsPrivate  bool   `json:"is_private"`
			IsMember   bool   `json:"is_member"`
			IsArchived bool   `json:"is_archived"`
			NumMembers int    `json:"num_members"`
		} `json:"channels"`
		Meta struct {
			Next string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, "", errors.New("the answer Slack gave could not be read")
	}
	switch {
	case !a.OK && a.Error == "missing_scope":
		return nil, "", errors.New("the Slack app needs the channels:read and groups:read scopes to list channels: " +
			"add them under OAuth & Permissions, then Reinstall to Workspace")
	case !a.OK:
		return nil, "", fmt.Errorf("slack would not list the channels: %s", clip(a.Error))
	}
	out := make([]gateway.PlatformChannel, 0, len(a.Channels))
	for _, c := range a.Channels {
		if c.IsArchived || !channelPattern.MatchString(c.ID) {
			continue
		}
		out = append(out, gateway.PlatformChannel{
			Ref: c.ID, Name: c.Name, Private: c.IsPrivate, Member: c.IsMember, Members: c.NumMembers,
		})
	}
	return out, a.Meta.Next, nil
}
