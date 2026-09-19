package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql" // the driver this package exists to use

	"github.com/LaplacianAI/openarity/sdk/agent"
)

var Schema = []string{
	`CREATE TABLE IF NOT EXISTS agent_messages (
		session  VARCHAR(255) NOT NULL,
		position INT          NOT NULL,
		body     LONGTEXT     NOT NULL,
		PRIMARY KEY (session, position)
	)`,
	`CREATE TABLE IF NOT EXISTS agent_steers (
		id      BIGINT AUTO_INCREMENT PRIMARY KEY,
		session VARCHAR(255) NOT NULL,
		text    LONGTEXT     NOT NULL,
		INDEX agent_steers_session (session, id)
	)`,
}

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Save(ctx context.Context, session string, msgs []agent.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("saving session %s: %w", session, err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_messages WHERE session = ?`, session); err != nil {
		return fmt.Errorf("saving session %s: clearing what was there: %w", session, err)
	}

	for i, m := range msgs {
		body, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("saving session %s: encoding message %d: %w", session, i, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agent_messages (session, position, body) VALUES (?, ?, ?)`,
			session, i, string(body)); err != nil {
			return fmt.Errorf("saving session %s: message %d: %w", session, i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving session %s: %w", session, err)
	}

	return nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx,
		`SELECT id, text FROM agent_steers WHERE session = ? ORDER BY id FOR UPDATE`, session)
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}

	var (
		ids   []any
		texts []string
	)
	for rows.Next() {
		var (
			id   int64
			text string
		)
		if err := rows.Scan(&id, &text); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
		}
		ids = append(ids, id)
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}

	if len(ids) > 0 {
		query := `DELETE FROM agent_steers WHERE id IN (?` + //nolint:gosec
			strings.Repeat(`, ?`, len(ids)-1) + `)`
		if _, err := tx.ExecContext(ctx, query, ids...); err != nil {
			return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	return texts, nil
}

var _ agent.Store = (*Store)(nil)
