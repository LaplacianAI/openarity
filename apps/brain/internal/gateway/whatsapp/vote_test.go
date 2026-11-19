package whatsapp

import (
	"reflect"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

var oneHash = [][]byte{{1, 2, 3}}

func TestAVoteIsAnAnswerInTheChatsSession(t *testing.T) {
	in, ok := MapVote(direct(nil), us, oneHash)
	if !ok {
		t.Fatal("dropped")
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("not valid: %v", err)
	}
	if in.ExternalID != "3EB0C767D82B8A1E" || in.Author.Ref != priya.String() || in.Text != VoteText ||
		in.Session.Ref != priya.String() || in.Session.Kind != gateway.SessionDirect {
		t.Errorf("mapped %+v", in)
	}
	if in.Answer == nil || !reflect.DeepEqual(in.Answer.Hashes, oneHash) || in.Answer.ApprovalID != "" || in.Answer.Code != "" {
		t.Errorf("answer %+v; want the hashes alone", in.Answer)
	}
	// The same session as the chat's messages.
	if msg := mapped(t, direct(text("hi"))); msg.Session != in.Session {
		t.Errorf("session %+v; a message in the chat has %+v", in.Session, msg.Session)
	}
}

func TestAVoteInAGroupIsTheGroupsSessionByTheVotersNumber(t *testing.T) {
	m := inGroup(nil)
	m.Info.Sender = types.NewJID("55555", types.HiddenUserServer)
	m.Info.SenderAlt = priya
	in, ok := MapVote(m, us, oneHash)
	if !ok || in.Session.Ref != group.String() || in.Session.Kind != gateway.SessionGroup || in.Author.Ref != priya.String() {
		t.Fatalf("mapped %+v, %v", in, ok)
	}
}

func TestAnOwnersVoteIsByTheirNumber(t *testing.T) {
	m := direct(nil)
	m.Info.IsFromMe, m.Info.Chat, m.Info.Sender = true, priya, us.PN
	in, ok := MapVote(m, us, oneHash)
	if !ok || in.Author.Ref != us.PN.String() || in.Session.Ref != priya.String() {
		t.Fatalf("mapped %+v, %v", in, ok)
	}
	if _, ok := MapVote(m, Self{}, oneHash); ok {
		t.Error("an own vote was kept by a device that does not know its number")
	}
}

func TestAVoteThatIsNotKeptIsDropped(t *testing.T) {
	if _, ok := MapVote(direct(nil), us, nil); ok {
		t.Error("a retracted vote was kept")
	}
	if _, ok := MapVote(nil, us, oneHash); ok {
		t.Error("no event was kept")
	}
	m := direct(nil)
	m.Info.Chat = types.NewJID("status", types.BroadcastServer)
	if _, ok := MapVote(m, us, oneHash); ok {
		t.Error("a vote in a broadcast was kept")
	}
}
