package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

const Schema = `
CREATE TABLE IF NOT EXISTS agent_messages (
	session  text  NOT NULL,
	position int   NOT NULL,
	body     json  NOT NULL,
	PRIMARY KEY (session, position)
);

CREATE TABLE IF NOT EXISTS agent_steers (
	id      bigserial PRIMARY KEY,
	session text NOT NULL,
	text    text NOT NULL
);

CREATE INDEX IF NOT EXISTS agent_steers_session ON agent_steers (session, id);
`

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Save(ctx context.Context, session string, msgs []agent.Message) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM agent_messages WHERE session = $1`, session); err != nil {
			return fmt.Errorf("clearing what was there: %w", err)
		}
		for i, m := range msgs {
			body, err := json.Marshal(m)
			if err != nil {
				return fmt.Errorf("encoding message %d: %w", i, err)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO agent_messages (session, position, body) VALUES ($1, $2, $3)`,
				session, i, body); err != nil {
				return fmt.Errorf("saving message %d: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("saving session %s: %w", session, err)
	}
	return nil
}

func (s *Store) Messages(ctx context.Context, session string) ([]agent.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT body FROM agent_messages WHERE session = $1 ORDER BY position`, session)
	if err != nil {
		return nil, fmt.Errorf("reading session %s: %w", session, err)
	}

	msgs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (agent.Message, error) {
		var body []byte
		if err := row.Scan(&body); err != nil {
			return agent.Message{}, err
		}
		var m agent.Message
		err := json.Unmarshal(body, &m)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("reading session %s: %w", session, err)
	}
	return msgs, nil
}

func (s *Store) AddSteer(ctx context.Context, session, text string) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO agent_steers (session, text) VALUES ($1, $2)`, session, text); err != nil {
		return fmt.Errorf("leaving a steer for session %s: %w", session, err)
	}
	return nil
}

func (s *Store) TakeSteers(ctx context.Context, session string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`DELETE FROM agent_steers WHERE session = $1 RETURNING text`, session)
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}

	texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
	}
	return texts, nil
}

var _ agent.Store = (*Store)(nil)
