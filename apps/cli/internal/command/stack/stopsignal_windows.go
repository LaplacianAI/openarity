//go:build windows

package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// A named event, rather than a console control event.
//
// GenerateConsoleCtrlEvent takes a process *group*, not a process. The
// supervisor is not a group leader — it is started by whatever the person
// used, and a process cannot put itself in a new group — so the event reaches
// every process sharing the console, `oa stack stop` included. It died of the
// signal it sent, exit 0xC000013A, before the supervisor had finished
// stopping anything. Nor can that be worked around: SetConsoleCtrlHandler's
// ignore-me flag covers CTRL_C and not CTRL_BREAK, and CTRL_C would take the
// shell down with it.
//
// A named event has neither problem. It names one install, reaches exactly the
// process waiting on it, and has nothing to do with consoles.
func stopEventName(root string) string {
	// Local\ rather than Global\: per-session, so two people on one machine do
	// not stop each other's install, and no privilege is needed to create it.
	// The root is hashed because an event name may not contain a backslash and
	// is capped at MAX_PATH.
	sum := sha256.Sum256([]byte(strings.ToLower(root)))
	return `Local\openarity-stop-` + hex.EncodeToString(sum[:8])
}

// waitForStop creates the event this install is stopped by, and closes the
// returned channel when somebody sets it.
func waitForStop(root string) (<-chan struct{}, func(), error) {
	name, err := windows.UTF16PtrFromString(stopEventName(root))
	if err != nil {
		return nil, func() {}, err
	}

	// Manual reset, so a stop that arrives before the wait is not missed.
	handle, err := windows.CreateEvent(nil, 1, 0, name)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return nil, func() {}, fmt.Errorf("stack: creating the stop event: %w", err)
	}

	stopped := make(chan struct{})
	go func() {
		_, _ = windows.WaitForSingleObject(handle, windows.INFINITE)
		close(stopped)
	}()

	return stopped, func() { _ = windows.CloseHandle(handle) }, nil
}

// askToStop sets the event the supervisor waits on.
func askToStop(root string) error {
	name, err := windows.UTF16PtrFromString(stopEventName(root))
	if err != nil {
		return err
	}

	handle, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		return fmt.Errorf("stack: nothing is waiting to be stopped: %w", err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	return windows.SetEvent(handle)
}
