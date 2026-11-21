package workspace

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const (
	// DefaultTimeout is a command's when the caller names none.
	DefaultTimeout = 2 * time.Minute
	// MaxTimeout is the longest a command may run. The brain gives a tool
	// call five minutes, so a command ends, and says so, before the call
	// it serves is cut off.
	MaxTimeout = 270 * time.Second
	// maxOutput is what is kept of each of stdout and stderr: its first and
	// last halves, since a build's error is at its end and its cause often
	// near its start.
	maxOutput = 256 << 10
	// pipeGrace is how long the output is waited for once the shell has
	// exited, while something it started still holds the pipe.
	pipeGrace = 2 * time.Second
)

// ExecResult is what a command did.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	// Truncated is whether either output was cut to its two ends.
	Truncated bool `json:"truncated"`
}

// run runs command with bash in dir, as its own process group, and ends the
// whole group when the command does, so what a command leaves running in the
// background goes with it. A process that leaves the group (setsid, a
// daemon) is not followed; it lives until the room goes, within the room's
// memory, CPU and pids limits.
func run(ctx context.Context, dir string, env []string, command string, timeout time.Duration) ExecResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr ends
	stdout.limit, stderr.limit = maxOutput, maxOutput
	cmd := exec.CommandContext(ctx, "bash", "-c", command) //nolint:gosec // running the agent's command is the point
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = pipeGrace

	err := cmd.Run()
	// Whatever the shell left running in the background goes with it.
	_ = killGroup(cmd)

	res := ExecResult{TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case errors.Is(err, exec.ErrWaitDelay):
		// The shell exited; something it started held the output open.
		res.ExitCode = cmd.ProcessState.ExitCode()
	default:
		res.ExitCode = -1
		_, _ = stderr.Write([]byte(err.Error()))
	}
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	res.Truncated = stdout.cut || stderr.cut
	return res
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// ends keeps the first and last limit/2 bytes written to it.
type ends struct {
	limit int
	head  []byte
	tail  []byte // a window of the latest bytes past the head
	cut   bool
}

func (e *ends) Write(p []byte) (int, error) {
	n := len(p)
	half := e.limit / 2
	if room := half - len(e.head); room > 0 {
		take := min(room, len(p))
		e.head = append(e.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	e.tail = append(e.tail, p...)
	if over := len(e.tail) - (e.limit - half); over > 0 {
		e.cut = true
		e.tail = append(e.tail[:0], e.tail[over:]...)
	}
	return n, nil
}

func (e *ends) String() string {
	if !e.cut {
		return string(e.head) + string(e.tail)
	}
	var b bytes.Buffer
	b.Write(e.head)
	b.WriteString("\n… output cut: its start and end are shown …\n")
	b.Write(e.tail)
	return b.String()
}
