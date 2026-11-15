package gateway

import (
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A channel that answers only in groups leaves direct chats alone: stored,
// since the sender was let in, and starting nothing. A mention in a group is
// answered as ever.
func TestAChannelThatAnswersInGroupsLeavesDirectChatsAlone(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.ch.AnswersIn = AnswersInGroups
	if err := a.deliver(t, delivery("m-1", "are you free tonight?")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 || len(a.store.stored) != 1 {
		t.Fatalf("a direct message started %d runs, stored %d; want none and one", len(a.store.created), len(a.store.stored))
	}
	if err := a.deliver(t, inGroup(delivery("m-2", "@bot why did it fail?"), true)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 1 {
		t.Errorf("a group mention started %d runs; want one", len(a.store.created))
	}
}

func TestAChannelThatAnswersInDirectChatsLeavesGroupsAlone(t *testing.T) {
	t.Parallel()

	a := newAnswering()
	a.ch.AnswersIn = AnswersInDirect
	if err := a.deliver(t, inGroup(delivery("m-1", "@bot why?"), true)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 0 {
		t.Fatalf("a group mention started %d runs; want none", len(a.store.created))
	}
	if err := a.deliver(t, delivery("m-2", "why?")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(a.store.created) != 1 {
		t.Errorf("a direct message started %d runs; want one", len(a.store.created))
	}
}

// Outside where a channel answers, a stranger has asked nothing: not queued,
// and not told they are waiting.
func TestAStrangerOutsideWhereItAnswersIsNeitherQueuedNorTold(t *testing.T) {
	t.Parallel()

	direct := message("u-new", "Asha")
	group := message("u-new", "Asha")
	group.Session = Session{Ref: "room-1", Kind: SessionGroup}
	group.Mentions = []Mention{{SenderRef: "B-us", IsUs: true}}

	for name, tc := range map[string]struct {
		where AnswersIn
		in    Inbound
	}{
		"a direct chat, answering in groups": {AnswersInGroups, direct},
		"a group, answering in direct chats": {AnswersInDirect, group},
	} {
		s := newFakeSenders()
		if _, known, err := ResolveSender(t.Context(), s, uuid.New(), tc.where, tc.in); err != nil || known {
			t.Fatalf("%s: ResolveSender = %v, %v", name, known, err)
		}
		if len(s.recorded) != 0 {
			t.Errorf("%s: the stranger was queued", name)
		}
		ch := db.Channel{ID: uuid.New(), TeamID: uuid.New(), NotifyStrangers: true, AnswersIn: string(tc.where)}
		if _, told, err := TellWaiting(t.Context(), s, ch, tc.in); err != nil || told {
			t.Errorf("%s: TellWaiting = %v, %v; want nothing told", name, told, err)
		}
	}
}

// An empty scope is a channel from before there was one: everywhere.
func TestNoScopeIsEverywhere(t *testing.T) {
	t.Parallel()

	in := message("u-1", "Asha")
	if !addressed("", SessionDirect, in) {
		t.Error("a direct message to a channel with no scope asks nothing")
	}
	for _, a := range []AnswersIn{AnswersEverywhere, AnswersInGroups, AnswersInDirect} {
		if !a.Valid() {
			t.Errorf("%q is not valid", a)
		}
	}
	if AnswersIn("Groups").Valid() || AnswersIn("").Valid() {
		t.Error("a place no channel answers was taken as valid")
	}
}
