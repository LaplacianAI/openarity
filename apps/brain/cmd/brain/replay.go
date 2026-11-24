package main

import (
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/replay"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
)

// newReplayJob tries each stored proposal on past runs, when its team has
// that on: built as runs are, with what they are built with, less anything
// that acts on the world, and judged by the judge.
func newReplayJob(logger *slog.Logger, dbStore *store.Store, graphDeps *runtime.GraphDeps, d *runDeps) *replay.Job {
	replayer := runtime.NewReplayer(d.discoverer, dbStore, graphDeps, d.search, d.fetch, d.runner, d.endpoint)
	judge := improve.NewChecker(dbStore, improve.NewJudge(openaicompat.Factory(), d.endpoint), logger)
	return replay.NewJob(replay.New(dbStore, d.attachments, postgres.New(dbStore.Pool()), replayer, judge))
}
