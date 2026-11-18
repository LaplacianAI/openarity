package agent

import (
	"errors"
	"fmt"
)

// ErrSuspended is a tool saying "not now: stop the run here and ask me again
// later". A pattern that can wait stops after the results it already has and
// returns a Result whose Messages end in the turn with the call still open,
// with an error that matches ErrSuspended; the runner saves that transcript
// as it saves any other. Running the transcript again finishes the turn
// first, calling the open calls in order, so the tool is asked again with
// the same call id (ToolCallID) and decides what has changed since.
//
// Why it waits is the caller's: a person to approve the call, a job to
// finish, a sign-in to complete.
var ErrSuspended = errors.New("the run is suspended on a tool call")

// Suspend is the error a tool returns to suspend the run, saying why.
func Suspend(why string) error {
	if why == "" {
		return ErrSuspended
	}
	return fmt.Errorf("%w: %s", ErrSuspended, why)
}
