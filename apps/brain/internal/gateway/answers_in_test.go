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
		if _, known, err := ResolveSender(t.Context(), s, uuid.New(), Answering{Where: tc.where}, tc.in); err != nil || known {
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
	if !addressed(Answering{}, SessionDirect, in) {
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

// The owner writing to the account itself asks the bot wherever the channel
// answers, a channel that answers only in groups included; from before the
// bot was there, it still asks nothing.
func TestTheOwnerWritingToTheAccountIsAnsweredWhereverTheChannelAnswers(t *testing.T) {
	t.Parallel()

	for _, where := range []AnswersIn{AnswersEverywhere, AnswersInGroups, AnswersInDirect} {
		a := newAnswering()
		a.ch.AnswersIn = where
		d := delivery("m-1", "summarise today")
		d.ToSelf = true
		if err := a.deliver(t, d); err != nil {
			t.Fatalf("%s: Deliver: %v", where, err)
		}
		if len(a.store.created) != 1 {
			t.Errorf("%s: started %d runs; want one", where, len(a.store.created))
		}
	}

	old := Inbound{Session: Session{Kind: SessionDirect}, ToSelf: true, Historical: true}
	if addressed(Answering{Where: AnswersEverywhere}, SessionDirect, old) {
		t.Error("a note to self from before the bot was there asks it")
	}
}

// With a trigger word, a message asks only when it holds the word, in a
// direct chat and a group alike, and a mention of the number no longer asks;
// where the channel answers still holds, and the owner's note to self needs
// no word.
func TestATriggerWordIsWhatAsksTheChannel(t *testing.T) {
	t.Parallel()

	word := Answering{Where: AnswersEverywhere, Trigger: "@Fduv"}
	msg := func(text string, mentionsUs bool) Inbound {
		in := Inbound{Text: text}
		if mentionsUs {
			in.Mentions = []Mention{{SenderRef: "us", IsUs: true}}
		}
		return in
	}
	for _, tc := range []struct {
		name string
		a    Answering
		kind SessionKind
		in   Inbound
		want bool
	}{
		{"a direct chat without the word", word, SessionDirect, msg("are you free?", false), false},
		{"a direct chat with it", word, SessionDirect, msg("@Fduv are you free?", false), true},
		{"a group mention without the word", word, SessionGroup, msg("@Shrijeeth lunch?", true), false},
		{"a group message with the word", word, SessionGroup, msg("@fduv summarise", false), true},
		{"the word outside where it answers", Answering{Where: AnswersInGroups, Trigger: "@Fduv"}, SessionDirect, msg("@Fduv hi", false), false},
		{"the word from a bot", word, SessionGroup, Inbound{Text: "@Fduv", Author: Author{IsBot: true}}, false},
		{"the word from before", word, SessionGroup, Inbound{Text: "@Fduv", Historical: true}, false},
		{"a note to self without it", word, SessionDirect, Inbound{Text: "summarise", ToSelf: true}, true},
		{"no word: a direct chat", Answering{Where: AnswersEverywhere}, SessionDirect, msg("hi", false), true},
		{"no word: a group mention", Answering{Where: AnswersEverywhere}, SessionGroup, msg("hi", true), true},
	} {
		if got := addressed(tc.a, tc.kind, tc.in); got != tc.want {
			t.Errorf("%s: addressed = %v; want %v", tc.name, got, tc.want)
		}
	}
}

// The word reaches the rule from the stored channel, so a stranger without
// it is not queued, and one with it is.
func TestAStrangerIsQueuedOnlyWhenTheyUseTheWord(t *testing.T) {
	t.Parallel()

	ch := db.Channel{ID: uuid.New(), AnswersIn: "everywhere", TriggerWord: new("@Fduv")}
	s := newFakeSenders()
	for _, text := range []string{"hello?", "@Fduv hello?"} {
		in := message("u-new", "Asha")
		in.Text = text
		if _, _, err := ResolveSender(t.Context(), s, ch.ID, AnsweringOf(ch), in); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.recorded) != 1 {
		t.Errorf("queued %d times; want once, for the message with the word", len(s.recorded))
	}
	in := message("u-new", "Asha")
	in.Text = "hello?"
	if _, told, err := TellWaiting(t.Context(), s, db.Channel{
		ID: ch.ID, TeamID: uuid.New(), NotifyStrangers: true,
		AnswersIn: "everywhere", TriggerWord: ch.TriggerWord,
	}, in); err != nil || told {
		t.Errorf("TellWaiting = %v, %v; a stranger without the word is told nothing", told, err)
	}
}
