package reaper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

var ErrOverdue = errors.New("an erasure has not completed")

const (
	batchSize    = 100
	retryAfter   = 5 * time.Minute
	overdueAfter = 24 * time.Hour
)

type Item struct {
	Ref      string
	TeamID   uuid.UUID
	Attempts int32
}

type Outcome int

const (
	Applied Outcome = iota
	Superseded
)

type Effect interface {
	Name() string
	Claim(ctx context.Context, retryBefore time.Time, batch int32) ([]Item, error)
	Do(ctx context.Context, item Item) (Outcome, error)
	Forget(ctx context.Context, item Item) error
	Backlog(ctx context.Context) (outstanding int64, oldest time.Time, err error)
}

type Sweeper struct {
	effect Effect
	logger *slog.Logger
}

func New(e Effect, logger *slog.Logger) *Sweeper {
	return &Sweeper{effect: e, logger: logger}
}

type Result struct {
	Effect string

	Applied    int
	Superseded int
	Failed     int

	Outstanding int64
	Oldest      time.Time
}

func (r Result) Overdue() bool {
	return !r.Oldest.IsZero() && time.Since(r.Oldest) > overdueAfter
}

func (s *Sweeper) Sweep(ctx context.Context) (Result, error) {
	res := Result{Effect: s.effect.Name()}

	if err := s.drain(ctx, &res); err != nil {
		return res, err
	}

	return s.backlog(context.WithoutCancel(ctx), res), nil
}

func (s *Sweeper) drain(ctx context.Context, res *Result) error {
	for ctx.Err() == nil {
		claimed, err := s.effect.Claim(ctx, time.Now().Add(-retryAfter), batchSize)
		if err != nil {
			return fmt.Errorf("%s: claiming a batch: %w", s.effect.Name(), err)
		}
		if len(claimed) == 0 {
			return nil
		}

		for _, item := range claimed {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			s.one(ctx, item, res)
		}
	}
	return nil
}

func (s *Sweeper) one(ctx context.Context, item Item, res *Result) {
	outcome, err := s.effect.Do(ctx, item)
	if err != nil {
		level := slog.LevelWarn
		if item.Attempts > 3 {
			level = slog.LevelError
		}
		s.logger.Log(ctx, level, "reaper: effect refused",
			"effect", s.effect.Name(), "ref", item.Ref, "team_id", item.TeamID,
			"attempts", item.Attempts, "error", err)
		res.Failed++
		return
	}

	if err := s.effect.Forget(ctx, item); err != nil {
		s.logger.ErrorContext(ctx, "reaper: tombstone not cleared",
			"effect", s.effect.Name(), "ref", item.Ref, "error", err)
		res.Failed++
		return
	}

	switch outcome {
	case Applied:
		res.Applied++
	case Superseded:
		res.Superseded++
	}
}

func (s *Sweeper) backlog(ctx context.Context, res Result) Result {
	outstanding, oldest, err := s.effect.Backlog(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "reaper: reading the backlog",
			"effect", s.effect.Name(), "error", err)
		return res
	}

	res.Outstanding, res.Oldest = outstanding, oldest

	if res.Overdue() {
		s.logger.ErrorContext(ctx, "reaper: an erasure is overdue",
			"effect", s.effect.Name(), "outstanding", outstanding,
			"oldest", oldest, "age", time.Since(oldest).Round(time.Minute))
	}
	return res
}
