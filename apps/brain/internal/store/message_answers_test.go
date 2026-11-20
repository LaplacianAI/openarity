package store

import (
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// answers is a tap the person made in c: stored as the record of an action,
// not as something said.
func (c conversation) answers(t *testing.T, s *Store, externalID string) uuid.UUID {
	t.Helper()
	m := message(c.channel, c.session, c.user, externalID, "Voted in the poll")
	m.Answer = true
	return insertMessage(t, s, m)
}

// A run's answer quotes the newest thing its people said, never a tap made
// while it worked: a reply threaded under a poll vote reads as answering it.
func TestAReplyNeverQuotesAnAnswer(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "quotes")
	asked := c.says(t, s, "m-1", "fetch the article")
	r, err := c.startRun(t, s.Queries, &asked, "fetch the article")
	if err != nil {
		t.Fatalf("CreateChannelRun: %v", err)
	}
	running(t, s, r.ID)
	c.answers(t, s, "vote-1")
	endChannelRun(t, s, r.ID, "succeeded", "Here it is.", "")

	var quoted *uuid.UUID
	if err := s.pool.QueryRow(t.Context(), `SELECT in_reply_to FROM replies WHERE run_id = $1`, r.ID).Scan(&quoted); err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	if quoted == nil || *quoted != asked {
		t.Errorf("the reply quotes %v, want %s, what was asked, not the vote", quoted, asked)
	}
}

// A session's last message, as its lists show it, is the last thing said,
// never a tap.
func TestASessionsLastMessageIsNeverAnAnswer(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "lists")
	c.says(t, s, "m-1", "fetch the article")
	c.answers(t, s, "vote-1")

	byChannel, err := s.ListSessionsByChannel(t.Context(), db.ListSessionsByChannelParams{
		ChannelID: &c.channel.ID, PageSize: 10, Moderator: true,
	})
	if err != nil {
		t.Fatalf("ListSessionsByChannel: %v", err)
	}
	byTeam, err := s.ListSessionsByTeam(t.Context(), db.ListSessionsByTeamParams{
		TeamID: c.team, PageSize: 10, Moderator: true,
	})
	if err != nil {
		t.Fatalf("ListSessionsByTeam: %v", err)
	}
	if len(byChannel) != 1 || byChannel[0].LastMessage == nil || *byChannel[0].LastMessage != "fetch the article" {
		t.Errorf("by channel, the last message is %+v, want what was said", byChannel)
	}
	if len(byTeam) != 1 || byTeam[0].LastMessage == nil || *byTeam[0].LastMessage != "fetch the article" {
		t.Errorf("by team, the last message is %+v, want what was said", byTeam)
	}
}
