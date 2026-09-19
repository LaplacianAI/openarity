package mongodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

const (
	Sessions = "agent_sessions"
	Steers   = "agent_steers"
)

type Store struct{ db *mongo.Database }

func New(db *mongo.Database) *Store { return &Store{db: db} }

type transcript struct {
	ID       string   `bson:"_id"`
	Messages [][]byte `bson:"messages"`
}

type steer struct {
	Session string `bson:"session"`
	Text    string `bson:"text"`
}

func (s *Store) Save(ctx context.Context, session string, msgs []agent.Message) error {
	bodies := make([][]byte, 0, len(msgs))
	for i, m := range msgs {
		body, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("saving session %s: encoding message %d: %w", session, i, err)
		}
		bodies = append(bodies, body)
	}

	_, err := s.db.Collection(Sessions).ReplaceOne(ctx,
		bson.M{"_id": session},
		transcript{ID: session, Messages: bodies},
		options.Replace().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("saving session %s: %w", session, err)
	}
	return nil
}

func (s *Store) Messages(ctx context.Context, session string) ([]agent.Message, error) {
	var found transcript
	err := s.db.Collection(Sessions).
		FindOne(ctx, bson.M{"_id": session}).
		Decode(&found)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading session %s: %w", session, err)
	}

	msgs := make([]agent.Message, 0, len(found.Messages))
	for _, body := range found.Messages {
		var m agent.Message
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("decoding a message of session %s: %w", session, err)
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func (s *Store) AddSteer(ctx context.Context, session, text string) error {
	if _, err := s.db.Collection(Steers).
		InsertOne(ctx, steer{Session: session, Text: text}); err != nil {
		return fmt.Errorf("leaving a steer for session %s: %w", session, err)
	}
	return nil
}

func (s *Store) TakeSteers(ctx context.Context, session string) ([]string, error) {
	sorted := options.FindOneAndDelete().SetSort(bson.D{{Key: "_id", Value: 1}})

	var texts []string
	for {
		var taken steer
		err := s.db.Collection(Steers).
			FindOneAndDelete(ctx, bson.M{"session": session}, sorted).
			Decode(&taken)
		switch {
		case errors.Is(err, mongo.ErrNoDocuments):
			return texts, nil
		case err != nil:
			return nil, fmt.Errorf("taking the steers for session %s: %w", session, err)
		}
		texts = append(texts, taken.Text)
	}
}

var _ agent.Store = (*Store)(nil)
