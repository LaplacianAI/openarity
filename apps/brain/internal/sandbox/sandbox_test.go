package sandbox_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// Waiting ends a sandbox with no room, one still preparing, and one that did
// not answer, however they are wrapped; nothing else.
func TestWaitableIsWhatWaitingCanEnd(t *testing.T) {
	t.Parallel()
	for err, want := range map[error]bool{
		sandbox.ErrFull:        true,
		sandbox.ErrPreparing:   true,
		sandbox.ErrUnavailable: true,
		fmt.Errorf("making the room: %w: memory", sandbox.ErrFull):        true,
		fmt.Errorf("%w: %w", sandbox.ErrNotReady, sandbox.ErrUnavailable): false,
		sandbox.ErrRefused:    false,
		sandbox.ErrNotReady:   false,
		sandbox.ErrNoProvider: false,
		sandbox.ErrReplaced:   false,
		sandbox.ErrNotFound:   false,
		sandbox.ErrNoRoom:     false,
		errors.New("boom"):    false,
	} {
		if got := sandbox.Waitable(err); got != want {
			t.Errorf("Waitable(%v) = %v, want %v", err, got, want)
		}
	}
	if sandbox.Waitable(nil) {
		t.Error("Waitable(nil)")
	}
}

// A request with no answer is the provider unavailable, unless the caller
// gave up on it.
func TestUnansweredIsUnavailableUnlessTheCallerGaveUp(t *testing.T) {
	t.Parallel()
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	err := sandbox.Unanswered(t.Context(), dial)
	if !errors.Is(err, sandbox.ErrUnavailable) || !errors.Is(err, dial) {
		t.Fatalf("Unanswered = %v, want ErrUnavailable around the dial error", err)
	}
	for _, err := range []error{io.EOF, fmt.Errorf("read: %w", io.ErrUnexpectedEOF)} {
		if !errors.Is(sandbox.Unanswered(t.Context(), err), sandbox.ErrUnavailable) {
			t.Errorf("a connection closed before an answer (%v) is not unavailable", err)
		}
	}
	for _, err := range []error{errors.New("x509: certificate signed by unknown authority"), errors.New(`unsupported protocol scheme ""`)} {
		if errors.Is(sandbox.Unanswered(t.Context(), err), sandbox.ErrUnavailable) {
			t.Errorf("a setting to fix (%v) is waited for", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sandbox.Unanswered(ctx, context.Canceled); !errors.Is(err, context.Canceled) || errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("Unanswered on a cancelled context = %v, want it as it was", err)
	}
}

func TestDownIsAGatewaysStatus(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]bool{
		http.StatusBadGateway: true, http.StatusServiceUnavailable: true, http.StatusGatewayTimeout: true,
		http.StatusInternalServerError: false, http.StatusTooManyRequests: false, http.StatusNotFound: false,
	} {
		if got := sandbox.Down(status); got != want {
			t.Errorf("Down(%d) = %v, want %v", status, got, want)
		}
	}
}
