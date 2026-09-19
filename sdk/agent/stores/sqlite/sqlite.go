package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

const schema = `
CREATE TABLE IF NOT EXISTS agent_messages (
	session  TEXT    NOT NULL,
	position INTEGER NOT NULL,
	body     TEXT    NOT NULL,
	PRIMARY KEY (session, position)
);
CREATE TABLE IF NOT EXISTS agent_steers (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	session TEXT NOT NULL,
	text    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_steers_session ON agent_steers (session, id);
`

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("creating the session tables in %s: %w", path, err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Save(ctx context.Context, session string, msgs []agent.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("saving session %s: %w", session, err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_messages WHERE session = ?`, session); err != nil {
		return fmt.Errorf("clearing session %s: %w", session, err)
	}

	for i, m := range msgs {
		body, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("encoding message %d of session %s: %w", i, session, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agent_messages (session, position, body) VALUES (?, ?, ?)`,
			session, i, string(body)); err != nil {
			return fmt.Errorf("saving message %d of session %s: %w", i, session, err)
		}
	}

	return tx.Commit()
}

func (s *Store) Messages(ctx context.Context, session string) ([]agent.Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT body FROM agent_messages WHERE session = ? ORDER BY position`, session)
	if err != nil {
		return nil, fmt.Errorf("reading session %s: %w", session, err)
	}
	defer rows.Close() //nolint:errcheck

	var msgs []agent.Message
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("reading session %s: %w", session, err)
		}
		var m agent.Message
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			return nil, fmt.Errorf("decoding a message of session %s: %w", session, err)
		}
		msgs = append(msgs, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading session %s: %w", session, err)
	}

	return msgs, nil
}

func (s *Store) AddSteer(ctx context.Context, session, text string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_steers (session, text) VALUES (?, ?)`, session, text); err != nil {
		return fmt.Errorf("leaving a steer for session %s: %w", session, err)
	}
	return nil
}

func (s *Store) TakeSteers(ctx context.Context, session string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM agent_steers WHERE session = ? RETURNING text`, session)
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	defer rows.Close() //nolint:errcheck

	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
		}
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	return texts, nil
}

var _ agent.Store = (*Store)(nil)
