package agent

import (
	"context"
	"errors"
)

type Store interface {
	Save(ctx context.Context, session string, msgs []Message) error
	Messages(ctx context.Context, session string) ([]Message, error)
	AddSteer(ctx context.Context, session, text string) error
	TakeSteers(ctx context.Context, session string) ([]string, error)
}

type Session interface {
	Save(ctx context.Context, msgs []Message) error
	Messages(ctx context.Context) ([]Message, error)
	Steer(ctx context.Context, text string) error
	TakeSteers(ctx context.Context) ([]string, error)
}

func Open(id string, store Store) (Session, error) {
	if id == "" {
		return nil, errors.New(
			"a session needs an id; an empty one would collect every conversation in a single bucket")
	}
	if store == nil {
		return nil, errors.New(
			"a session needs a Store; NewMemoryStore() is one that lives as long as the process")
	}
	return &session{id: id, store: store}, nil
}

type session struct {
	id    string
	store Store
}

func (s *session) Save(ctx context.Context, msgs []Message) error {
	return s.store.Save(ctx, s.id, msgs)
}

func (s *session) Messages(ctx context.Context) ([]Message, error) {
	return s.store.Messages(ctx, s.id)
}

func (s *session) Steer(ctx context.Context, text string) error {
	return s.store.AddSteer(ctx, s.id, text)
}

func (s *session) TakeSteers(ctx context.Context) ([]string, error) {
	return s.store.TakeSteers(ctx, s.id)
}

var _ Session = (*session)(nil)
