package main

import (
	"context"
	"errors"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/approvals"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// decider is the runtime's Decide behind the approvals API: the answer
// written, and the run handed back to the workers. Its refusals are named in
// the API's own words, which is all the handler knows of the runtime.
type decider struct {
	store   runtime.TxStore
	resumer runtime.Resumer
}

func (d decider) Decide(ctx context.Context, a approvals.Answer) (db.Approval, error) {
	row, err := runtime.Decide(ctx, d.store, d.resumer, runtime.Answer{
		ApprovalID: a.ApprovalID, TeamID: a.TeamID, By: a.By, Approve: a.Approve,
		Scope: a.Scope, Reason: a.Reason, Via: a.Via, MayDecide: a.MayDecide, MaySelf: a.MaySelf,
	})
	switch {
	case errors.Is(err, runtime.ErrNotAllowed):
		err = errors.Join(approvals.ErrNotAllowed, err)
	case errors.Is(err, runtime.ErrNotPending):
		err = errors.Join(approvals.ErrNotPending, err)
	case errors.Is(err, runtime.ErrExpired):
		err = errors.Join(approvals.ErrExpired, err)
	}
	return row, err
}
