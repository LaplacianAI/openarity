package triggers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Where a schedule answers.
const (
	// TargetOwn is a conversation of its own on the dashboard.
	TargetOwn = "own"
	// TargetChat is a chat its maker talked in: a direct conversation, a
	// thread, a group.
	TargetChat = "chat"
	// TargetChannel is a channel's or a group's top level, a new post each
	// time; it needs channel:write.
	TargetChannel = "channel"
	// TargetDirect is its maker's direct conversation with the bot, opened
	// by the platform when it can (Slack).
	TargetDirect = "direct"
)

// Why a place is listed but cannot be picked.
const (
	ReasonNeedsChannelWrite = "needs_channel_write"
	ReasonNotInChannel      = "not_in_channel"
)

const (
	// chatsSince is how lately a person must have talked in a chat for it to
	// be theirs to pick.
	chatsSince = 90 * 24 * time.Hour
	// maxTargetRows bounds what each group reads before the search narrows
	// it: a search finds among a person's most recent places, not all of them.
	maxTargetRows = 200
	// maxLabel bounds a stored label, as the table does.
	maxLabel = 200
	// snippet is how much of a person's last words a chat is shown by.
	snippet = 80
)

// Chats looks things up on a channel's platform: the gateway's.
type Chats interface {
	OpensDirect(provider string) bool
	ListsChannels(provider string) bool
	// Listers is every provider that lists its channels.
	Listers() []string
	OpenDirect(ctx context.Context, team, channel uuid.UUID, provider, user string) (string, error)
	ChannelName(ctx context.Context, team, channel uuid.UUID, provider, ref string) (string, error)
	ListChannels(ctx context.Context, team, channel uuid.UUID, provider string) (gateway.ChannelList, error)
}

// A Target is where a schedule is asked to answer, as a picked place carries
// it back: a session for a chat or a group, a channel and the platform's id
// for a listed channel, a channel for a direct conversation.
type Target struct {
	Kind      string
	SessionID uuid.UUID
	ChannelID uuid.UUID
	Ref       string
}

// A TargetItem is one place a person may pick, or may see and not pick.
type TargetItem struct {
	Kind, Label, Detail   string
	ChannelID             uuid.UUID
	ChannelName, Provider string
	// At is when the person last talked there, for a chat.
	At        *time.Time
	Available bool
	Reason    string
	Target    Target
}

// A TargetProblem is a channel whose places could not be listed: the
// platform said no, said for a person.
type TargetProblem struct {
	ChannelID      uuid.UUID
	Provider, Text string
}

// TargetPage is a page of places, the search narrowed.
type TargetPage struct {
	Items    []TargetItem
	More     bool
	Problems []TargetProblem
}

// TargetQuery narrows the places listed: a search, one kind, a page.
type TargetQuery struct {
	Q      string
	Kind   string
	Offset int
	Limit  int
}

// Targets lists where the caller may have a schedule answer, newest chats
// first, then channels and groups, then their direct conversations. A place
// they may not use is listed with why, so the picker can say it. Pages are by
// offset over the list as it is when asked, so a place that appears between
// two pages may shift the next by one.
func (s *Service) Targets(ctx context.Context, who Who, q TargetQuery) (TargetPage, error) {
	if q.Kind != "" && q.Kind != TargetChat && q.Kind != TargetChannel && q.Kind != TargetDirect {
		return TargetPage{}, refuse(Invalid, "kind is chat, channel or direct")
	}
	var page TargetPage
	var all []TargetItem
	want := func(k string) bool { return q.Kind == "" || q.Kind == k }
	if want(TargetChat) {
		items, err := s.chatItems(ctx, who)
		if err != nil {
			return TargetPage{}, err
		}
		all = append(all, items...)
	}
	if want(TargetChannel) {
		items, problems, err := s.channelItems(ctx, who)
		if err != nil {
			return TargetPage{}, err
		}
		all, page.Problems = append(all, items...), problems
	}
	if want(TargetDirect) {
		items, err := s.directItems(ctx, who)
		if err != nil {
			return TargetPage{}, err
		}
		all = append(all, items...)
	}
	all = slices.DeleteFunc(all, func(it TargetItem) bool { return !matches(it, q.Q) })
	start := min(q.Offset, len(all))
	end := min(start+q.Limit, len(all))
	page.Items, page.More = all[start:end], end < len(all)
	return page, nil
}

// matches is a place whose words hold the search, case aside.
func matches(it TargetItem, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(it.Label+"\n"+it.Detail+"\n"+it.ChannelName), q)
}

func (s *Service) chatItems(ctx context.Context, who Who) ([]TargetItem, error) {
	rows, err := s.store.ListTargetChats(ctx, db.ListTargetChatsParams{
		TeamID: who.Team, UserID: who.User, Since: s.now().Add(-chatsSince), PageSize: maxTargetRows,
	})
	if err != nil {
		return nil, fmt.Errorf("list the chats the caller talked in: %w", err)
	}
	out := make([]TargetItem, len(rows))
	for i, r := range rows {
		at := r.LastSaid
		reason := ""
		if postsToGroup(TargetChat, r.Kind) && !who.ChannelWrite {
			reason = ReasonNeedsChannelWrite // a group's chat is its top level
		}
		out[i] = TargetItem{
			Kind: TargetChat, Label: chatLabel(r.Kind), Detail: "You: " + clipped(r.LastText, snippet),
			ChannelID: r.ChannelID, ChannelName: r.ChannelName, Provider: r.Provider, At: &at,
			Available: reason == "", Reason: reason,
			Target: Target{Kind: TargetChat, SessionID: r.ID},
		}
	}
	return out, nil
}

func chatLabel(kind string) string {
	switch gateway.SessionKind(kind) {
	case gateway.SessionDirect:
		return "Your direct messages"
	case gateway.SessionThread:
		return "A thread"
	case gateway.SessionGroup:
		return "A group"
	}
	return "A chat"
}

// postsToGroup is a schedule whose answers a group reads: at a channel's top
// level, or in a group chat. Its maker needs channel:write, when it is made
// and each time it fires.
func postsToGroup(target, sessionKind string) bool {
	return target == TargetChannel || (target == TargetChat && sessionKind == string(gateway.SessionGroup))
}

// channelItems is the channels a schedule may post at the top level of: a
// platform's own list where it gives one (Slack), and the groups the bot has
// heard from elsewhere (Telegram, WhatsApp). Without channel:write each is
// listed, not pickable, and a private channel is left out: its name is not
// for one who may not post there. A workspace that cannot be listed is a
// problem beside the rest, not a failure of them.
func (s *Service) channelItems(ctx context.Context, who Who) ([]TargetItem, []TargetProblem, error) {
	reason := ""
	if !who.ChannelWrite {
		reason = ReasonNeedsChannelWrite
	}
	var out []TargetItem
	var problems []TargetProblem
	if providers := s.chats.Listers(); len(providers) > 0 {
		chans, err := s.store.ListChannelsOn(ctx, db.ListChannelsOnParams{TeamID: who.Team, Providers: providers})
		if err != nil {
			return nil, nil, fmt.Errorf("list the team's channels: %w", err)
		}
		for _, ch := range chans {
			listed, err := s.chats.ListChannels(ctx, who.Team, ch.ID, ch.Provider)
			var refused *gateway.Refusal
			switch {
			case errors.As(err, &refused):
				problems = append(problems, TargetProblem{ChannelID: ch.ID, Provider: ch.Provider, Text: refused.Msg})
				continue
			case err != nil:
				s.logger.WarnContext(ctx, "A workspace's channels could not be listed",
					"channel_id", ch.ID, "provider", ch.Provider, "error", err)
				problems = append(problems, TargetProblem{
					ChannelID: ch.ID, Provider: ch.Provider, Text: "its channels could not be listed just now; try again in a moment",
				})
				continue
			}
			if !listed.Complete {
				problems = append(problems, TargetProblem{
					ChannelID: ch.ID, Provider: ch.Provider,
					Text: "it has more channels than are listed here (the first 3,000); one further down cannot be picked yet",
				})
			}
			for _, pc := range listed.Channels {
				if pc.Private && !who.ChannelWrite {
					continue
				}
				it := TargetItem{
					Kind: TargetChannel, Label: "#" + pc.Name, Detail: placeDetail(pc),
					ChannelID: ch.ID, ChannelName: ch.Name, Provider: ch.Provider, Available: reason == "", Reason: reason,
					Target: Target{Kind: TargetChannel, ChannelID: ch.ID, Ref: pc.Ref},
				}
				if it.Available && !pc.Member {
					it.Available, it.Reason = false, ReasonNotInChannel
				}
				out = append(out, it)
			}
		}
		slices.SortStableFunc(out, func(a, b TargetItem) int {
			return strings.Compare(a.ChannelName+"\x00"+a.Label, b.ChannelName+"\x00"+b.Label)
		})
	}
	groups, err := s.groupItems(ctx, who, reason)
	if err != nil {
		return nil, nil, err
	}
	return append(out, groups...), problems, nil
}

func placeDetail(pc gateway.PlatformChannel) string {
	d := "Public"
	if pc.Private {
		d = "Private"
	}
	switch pc.Members {
	case 0:
	case 1:
		d += " · 1 member"
	default:
		d += " · " + strconv.Itoa(pc.Members) + " members"
	}
	return d
}

func (s *Service) groupItems(ctx context.Context, who Who, reason string) ([]TargetItem, error) {
	rows, err := s.store.ListTargetGroups(ctx, db.ListTargetGroupsParams{TeamID: who.Team, Listers: s.chats.Listers(), PageSize: maxTargetRows})
	if err != nil {
		return nil, fmt.Errorf("list the groups the bot is in: %w", err)
	}
	out := make([]TargetItem, 0, len(rows))
	for _, r := range rows {
		at := r.LastMessageAt
		out = append(out, TargetItem{
			Kind: TargetChannel, Label: "A group", Detail: "Last heard from " + at.UTC().Format("2 Jan 2006"),
			ChannelID: r.ChannelID, ChannelName: r.ChannelName, Provider: r.Provider, At: &at,
			Available: reason == "", Reason: reason,
			Target: Target{Kind: TargetChannel, SessionID: r.ID},
		})
	}
	return out, nil
}

// directItems is the channels where the platform opens the caller's direct
// conversation with the bot by itself. Elsewhere it is one of their chats.
func (s *Service) directItems(ctx context.Context, who Who) ([]TargetItem, error) {
	rows, err := s.store.ListSenderChannels(ctx, db.ListSenderChannelsParams{TeamID: who.Team, UserID: who.User})
	if err != nil {
		return nil, fmt.Errorf("list the channels the caller is let in on: %w", err)
	}
	var out []TargetItem
	for _, r := range rows {
		if !s.chats.OpensDirect(r.Provider) {
			continue
		}
		out = append(out, TargetItem{
			Kind: TargetDirect, Label: "Your direct messages", Detail: "Sent by the bot to you alone",
			ChannelID: r.ID, ChannelName: r.Name, Provider: r.Provider, Available: true,
			Target: Target{Kind: TargetDirect, ChannelID: r.ID},
		})
	}
	return out, nil
}

// noChats is a service that asks no platform: no channel lists, nothing opens.
type noChats struct{}

func (noChats) OpensDirect(string) bool   { return false }
func (noChats) ListsChannels(string) bool { return false }
func (noChats) Listers() []string         { return nil }
func (noChats) OpenDirect(context.Context, uuid.UUID, uuid.UUID, string, string) (string, error) {
	return "", gateway.ErrCannot
}

func (noChats) ChannelName(context.Context, uuid.UUID, uuid.UUID, string, string) (string, error) {
	return "", gateway.ErrCannot
}

func (noChats) ListChannels(context.Context, uuid.UUID, uuid.UUID, string) (gateway.ChannelList, error) {
	return gateway.ChannelList{}, gateway.ErrCannot
}
