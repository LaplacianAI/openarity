package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

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
		return s.placeChat(ctx, who, t)
	case TargetChannel:
		if !who.ChannelWrite {
			return nil, refuse(Forbidden, "posting at a channel's top level takes channel:write")
		}
		if t.SessionID != uuid.Nil {
			return s.placeGroup(ctx, who, t)
		}
		return s.placeListed(ctx, who, t)
	case TargetDirect:
		return s.placeDirect(ctx, who, t)
	}
	return nil, refuse(Invalid, "a target is own, chat, channel or direct")
}

// placeChat is a chat the caller talked in lately. A group's chat is its top
// level, so it takes channel:write as a channel does.
func (s *Service) placeChat(ctx context.Context, who Who, t Target) (*placed, error) {
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
	if postsToGroup(TargetChat, r.Kind) && !who.ChannelWrite {
		return nil, refuse(Forbidden, "answering in a group takes channel:write: everyone in it reads the answer")
	}
	return &placed{
		target: TargetChat, label: placeLabel(r.ChannelName, chatLabel(r.Kind)), channel: r.ChannelID,
		ref: r.ProviderRef, kind: r.Kind, user: r.UserID,
	}, nil
}

// placeGroup is a group the bot has heard from, on a platform that lists no
// channels: its top level is the group's own conversation.
func (s *Service) placeGroup(ctx context.Context, who Who, t Target) (*placed, error) {
	rows, err := s.store.ListTargetGroups(ctx, db.ListTargetGroupsParams{
		TeamID: who.Team, Listers: s.chats.Listers(), SessionID: &t.SessionID, PageSize: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("read the group: %w", err)
	}
	if len(rows) == 0 {
		return nil, refuse(Invalid, "that is not a group the bot is in")
	}
	r := rows[0]
	return &placed{
		target: TargetChannel, label: placeLabel(r.ChannelName, "A group"), channel: r.ChannelID,
		ref: r.ProviderRef, kind: string(gateway.SessionGroup),
	}, nil
}

// placeListed is a channel from its platform's list, looked up again there:
// the bot must still be in it.
func (s *Service) placeListed(ctx context.Context, who Who, t Target) (*placed, error) {
	chans, err := s.store.ListChannelsOn(ctx, db.ListChannelsOnParams{TeamID: who.Team, Providers: s.chats.Listers(), ChannelID: &t.ChannelID})
	if err != nil {
		return nil, fmt.Errorf("read the channel: %w", err)
	}
	if len(chans) == 0 || t.Ref == "" {
		return nil, refuse(Invalid, "no such channel in this team")
	}
	ch := chans[0]
	name, err := s.chats.ChannelName(ctx, who.Team, ch.ID, ch.Provider, t.Ref)
	if err := platformRefusal(err); err != nil {
		return nil, err
	}
	return &placed{
		target: TargetChannel, label: placeLabel(ch.Name, "#"+name), channel: ch.ID,
		ref: t.Ref, kind: string(gateway.SessionGroup),
	}, nil
}

// placeDirect is the caller's direct conversation with the bot, opened by the
// platform now.
func (s *Service) placeDirect(ctx context.Context, who Who, t Target) (*placed, error) {
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
		target: TargetDirect, label: placeLabel(r.Name, "Your direct messages"), channel: r.ID,
		ref: ref, kind: string(gateway.SessionDirect), user: &who.User,
	}, nil
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

func placeLabel(channel, place string) string {
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
