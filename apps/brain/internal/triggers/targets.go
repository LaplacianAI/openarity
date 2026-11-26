package triggers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	// maxTargetRows bounds what each group reads before the search narrows it.
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
	ListChannels(ctx context.Context, team, channel uuid.UUID, provider string) ([]gateway.PlatformChannel, error)
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
// they may not use is listed with why, so the picker can say it.
func (s *Service) Targets(ctx context.Context, who Who, q TargetQuery) (TargetPage, error) {
	if q.Kind != "" && q.Kind != TargetChat && q.Kind != TargetChannel && q.Kind != TargetDirect {
		return TargetPage{}, refuse(Invalid, "kind is chat, channel or direct")
	}
	var page TargetPage
	var all []TargetItem
	want := func(k string) bool { return q.Kind == "" || q.Kind == k }
	if want(TargetChat) {
		items, err := s.chatItems(ctx, who, nil)
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
		items, err := s.directItems(ctx, who, nil)
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

func (s *Service) chatItems(ctx context.Context, who Who, session *uuid.UUID) ([]TargetItem, error) {
	rows, err := s.store.ListTargetChats(ctx, db.ListTargetChatsParams{
		TeamID: who.Team, UserID: who.User, Since: s.now().Add(-chatsSince), SessionID: session, PageSize: maxTargetRows,
	})
	if err != nil {
		return nil, fmt.Errorf("list the chats the caller talked in: %w", err)
	}
	out := make([]TargetItem, len(rows))
	for i, r := range rows {
		at := r.LastSaid
		out[i] = TargetItem{
			Kind: TargetChat, Label: chatLabel(r.Kind), Detail: "You: " + clipped(r.LastText, snippet),
			ChannelID: r.ChannelID, ChannelName: r.ChannelName, Provider: r.Provider, At: &at, Available: true,
			Target: Target{Kind: TargetChat, SessionID: r.ID},
		}
	}
	return out, nil
}

func chatLabel(kind string) string {
	switch kind {
	case "direct":
		return "Your direct messages"
	case "thread":
		return "A thread"
	default:
		return "A group"
	}
}

// channelItems is the channels a schedule may post at the top level of: a
// platform's own list where it gives one (Slack), and the groups the bot has
// heard from elsewhere (Telegram, WhatsApp). Without channel:write each is
// listed, not pickable.
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
				return nil, nil, fmt.Errorf("list channel %s's channels: %w", ch.ID, err)
			}
			for _, pc := range listed {
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
	groups, err := s.groupItems(ctx, who, nil, reason)
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

func (s *Service) groupItems(ctx context.Context, who Who, session *uuid.UUID, reason string) ([]TargetItem, error) {
	rows, err := s.store.ListTargetGroups(ctx, db.ListTargetGroupsParams{TeamID: who.Team, SessionID: session, PageSize: maxTargetRows})
	if err != nil {
		return nil, fmt.Errorf("list the groups the bot is in: %w", err)
	}
	out := make([]TargetItem, 0, len(rows))
	for _, r := range rows {
		if s.chats.ListsChannels(r.Provider) {
			continue // its channels are the platform's own list
		}
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
func (s *Service) directItems(ctx context.Context, who Who, channel *uuid.UUID) ([]TargetItem, error) {
	rows, err := s.store.ListSenderChannels(ctx, db.ListSenderChannelsParams{TeamID: who.Team, UserID: who.User, ChannelID: channel})
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

// placed is where a schedule's conversation is opened: the session it is
// made as, and how the place is shown.
type placed struct {
	target, label string
	channel       uuid.UUID
	ref, kind     string
	user          *uuid.UUID
}

// place checks a picked target again, as the caller may pick it now, and
// says where the schedule's conversation goes. It may ask the platform, so it
// runs before the transaction that saves the schedule.
func (s *Service) place(ctx context.Context, who Who, t Target) (*placed, error) {
	switch t.Kind {
	case "", TargetOwn:
		return nil, nil // its own conversation on the dashboard
	case TargetChat:
		if t.SessionID == uuid.Nil {
			return nil, refuse(Invalid, "a chat names its conversation")
		}
		row, err := s.store.ListTargetChats(ctx, db.ListTargetChatsParams{
			TeamID: who.Team, UserID: who.User, Since: s.now().Add(-chatsSince), SessionID: &t.SessionID, PageSize: 1,
		})
		if err != nil {
			return nil, fmt.Errorf("read the chat: %w", err)
		}
		if len(row) == 0 {
			return nil, refuse(Invalid, "that is not a chat you have talked in lately")
		}
		r := row[0]
		return &placed{
			target: TargetChat, label: label(r.ChannelName, chatLabel(r.Kind)), channel: r.ChannelID,
			ref: r.ProviderRef, kind: r.Kind, user: r.UserID,
		}, nil
	case TargetChannel:
		if !who.ChannelWrite {
			return nil, refuse(Forbidden, "posting at a channel's top level takes channel:write")
		}
		if t.SessionID != uuid.Nil {
			rows, err := s.store.ListTargetGroups(ctx, db.ListTargetGroupsParams{TeamID: who.Team, SessionID: &t.SessionID, PageSize: 1})
			if err != nil {
				return nil, fmt.Errorf("read the group: %w", err)
			}
			if len(rows) == 0 || s.chats.ListsChannels(rows[0].Provider) {
				return nil, refuse(Invalid, "that is not a group the bot is in")
			}
			r := rows[0]
			return &placed{
				target: TargetChannel, label: label(r.ChannelName, "A group"), channel: r.ChannelID,
				ref: r.ProviderRef, kind: "group",
			}, nil
		}
		chans, err := s.store.ListChannelsOn(ctx, db.ListChannelsOnParams{TeamID: who.Team, Providers: s.chats.Listers(), ChannelID: &t.ChannelID})
		if err != nil {
			return nil, fmt.Errorf("read the channel: %w", err)
		}
		if len(chans) == 0 || t.Ref == "" {
			return nil, refuse(Invalid, "no such channel in this team")
		}
		name, err := s.chats.ChannelName(ctx, who.Team, chans[0].ID, chans[0].Provider, t.Ref)
		if err := platformRefusal(err); err != nil {
			return nil, err
		}
		return &placed{
			target: TargetChannel, label: label(chans[0].Name, "#"+name), channel: chans[0].ID,
			ref: t.Ref, kind: "group",
		}, nil
	case TargetDirect:
		rows, err := s.store.ListSenderChannels(ctx, db.ListSenderChannelsParams{TeamID: who.Team, UserID: who.User, ChannelID: &t.ChannelID})
		if err != nil {
			return nil, fmt.Errorf("read the channel: %w", err)
		}
		if len(rows) == 0 || !s.chats.OpensDirect(rows[0].Provider) {
			return nil, refuse(Invalid, "the bot cannot open a direct conversation with you on that channel")
		}
		r := rows[0]
		ref, err := s.chats.OpenDirect(ctx, who.Team, r.ID, r.Provider, r.SenderRef)
		if err := platformRefusal(err); err != nil {
			return nil, err
		}
		return &placed{
			target: TargetDirect, label: label(r.Name, "Your direct messages"), channel: r.ID,
			ref: ref, kind: "direct", user: &who.User,
		}, nil
	}
	return nil, refuse(Invalid, "a target is own, chat, channel or direct")
}

// platformRefusal passes the platform's no on as a refusal said for a person,
// and anything else as a failure.
func platformRefusal(err error) error {
	var refused *gateway.Refusal
	if errors.As(err, &refused) {
		return refuse(Invalid, "%s", refused.Msg)
	}
	return err
}

func label(channel, place string) string {
	return clipped(channel+" · "+place, maxLabel)
}

// clipped is s to n runes, on one line.
func clipped(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
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

func (noChats) ListChannels(context.Context, uuid.UUID, uuid.UUID, string) ([]gateway.PlatformChannel, error) {
	return nil, gateway.ErrCannot
}
