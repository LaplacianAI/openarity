package store

import (
	"testing"

	"github.com/google/uuid"
)

// messageAnswers is 20261130090000_message_answers.sql, which marks a message
// that was an answer to a request; beforeMessageAnswers is the migration
// before it.
const (
	messageAnswers       = 20261130090000
	beforeMessageAnswers = teamCompactionTokens
)

// A message from before the column is something said, not an answer, so the
// conversation reads it as it did. Rolled back, the column goes and the
// message stays; migrated again, it is something said again.
func TestAMessageFromBeforeAnswersIsSomethingSaid(t *testing.T) {
	s := migrationStore(t)
	p, err := s.provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), beforeMessageAnswers); err != nil {
		t.Fatalf("UpTo(%d): %v", beforeMessageAnswers, err)
	}
	team := scalar[uuid.UUID](t, s, `INSERT INTO teams (name) VALUES ('platform') RETURNING id`)
	ch := scalar[uuid.UUID](t, s, `INSERT INTO channels (team_id, provider, name) VALUES ($1, 'custom', 'support') RETURNING id`, team)
	session := scalar[uuid.UUID](t, s,
		`INSERT INTO sessions (team_id, channel_id, provider_ref, kind) VALUES ($1, $2, 'c-1', 'group') RETURNING id`, team, ch)
	user := scalar[uuid.UUID](t, s, `INSERT INTO users (issuer, subject) VALUES ('https://id.test', 'asha') RETURNING id`)
	message := scalar[uuid.UUID](t, s,
		`INSERT INTO messages (channel_id, session_id, user_id, external_id, text) VALUES ($1, $2, $3, 'm-1', 'hello') RETURNING id`,
		ch, session, user)

	if _, err := p.UpTo(t.Context(), messageAnswers); err != nil {
		t.Fatalf("UpTo(%d): %v", messageAnswers, err)
	}
	if scalar[bool](t, s, `SELECT answer FROM messages WHERE id = $1`, message) {
		t.Fatal("a message from before the column reads as an answer")
	}

	if _, err := s.pool.Exec(t.Context(), `UPDATE messages SET answer = true WHERE id = $1`, message); err != nil {
		t.Fatalf("marking it an answer: %v", err)
	}
	downTo(t, s, beforeMessageAnswers)
	if n := scalar[int](t, s, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'messages' AND column_name = 'answer'`); n != 0 {
		t.Error("the answer column is left after the rollback")
	}
	if got := scalar[string](t, s, `SELECT text FROM messages WHERE id = $1`, message); got != "hello" {
		t.Errorf("the rollback changed the message to %q", got)
	}

	if _, err := p.UpTo(t.Context(), messageAnswers); err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	if scalar[bool](t, s, `SELECT answer FROM messages WHERE id = $1`, message) {
		t.Error("migrated again, the message reads as an answer")
	}
}
