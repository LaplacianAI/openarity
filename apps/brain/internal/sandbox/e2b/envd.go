package e2b

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// envd speaks Connect (connectrpc.com/docs/protocol) with JSON messages. Start
// is a server stream: the body is one envelope, a flag byte and a 4-byte
// big-endian length and the message, and the answer is envelopes back, the
// first of them {"event":{"start":{"pid":N}}}. The last, flagged 0x02, ends
// the stream and may carry an error.
const (
	flagEndStream = 0x02
	// maxEnvelope bounds one message from envd; the first is a few bytes.
	maxEnvelope = 1 << 20
	// defaultUser is who E2B's SDKs run commands as; envd takes it as the
	// user of HTTP Basic, with no password.
	defaultUser = "user"
)

type processConfig struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
}

type startRequest struct {
	Process processConfig `json:"process"`
	// Stdin false: the shim reads none, and a pipe nobody closes is a leak.
	Stdin bool `json:"stdin"`
}

type processEvent struct {
	Event struct {
		Start *struct {
			PID uint32 `json:"pid"`
		} `json:"start"`
		End *struct {
			ExitCode int    `json:"exitCode"`
			Status   string `json:"status"`
			Error    string `json:"error"`
		} `json:"end"`
	} `json:"event"`
}

// maxStartRequest bounds the start request envd is sent.
const maxStartRequest = 1 << 20

// startProcess asks envd to run argv, waits for its start event and lets go of
// the stream. envd keeps the process running when the stream is dropped, as it
// does for E2B's own SDKs' background commands.
func (c *Client) startProcess(ctx context.Context, r room, token string, argv []string) error {
	// exec, so the login shell is replaced by the command rather than left
	// as its parent: a shell kept running would hold the room's environment,
	// its secrets among them, where the command's own children could read it.
	msg, err := json.Marshal(startRequest{Process: processConfig{
		Cmd:  "/bin/bash",
		Args: []string{"-l", "-c", "exec " + shellJoin(argv)},
	}})
	if err != nil {
		return fmt.Errorf("e2b: encode the start request: %w", err)
	}
	// A command is a few hundred bytes; the bound keeps the frame's length
	// field, and the allocation, from ever overflowing.
	if len(msg) > maxStartRequest {
		return fmt.Errorf("%w: the command is %d bytes, over %d", sandbox.ErrRefused, len(msg), maxStartRequest)
	}
	frame := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(msg))) //nolint:gosec // G115: bounded by maxStartRequest above
	frame = append(frame, msg...)

	// Dropping the stream is how Start returns; cancelling it ends the read.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+host(r, envdPort)+"/process.Process/Start", bytes.NewReader(frame))
	if err != nil {
		return fmt.Errorf("e2b: build the start request: %w", err)
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(defaultUser+":")))
	if token != "" {
		req.Header.Set("X-Access-Token", token)
	}
	secrets := []string{c.apiKey, token, r.Traffic}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("e2b: start the command: %w", scrubErr(err, secrets))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return connectError(resp.StatusCode, raw, secrets)
	}
	for {
		flags, payload, err := readEnvelope(resp.Body)
		if err != nil {
			return fmt.Errorf("e2b: the command's stream ended before it started: %w", err)
		}
		if flags&flagEndStream != 0 {
			var end struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(payload, &end)
			if end.Error != nil {
				return connectErrorText(end.Error.Code, end.Error.Message, secrets)
			}
			return errors.New("e2b: the command's stream ended before it started")
		}
		var ev processEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return errors.New("e2b: envd sent a message that is not JSON")
		}
		switch {
		case ev.Event.Start != nil:
			return nil
		case ev.Event.End != nil:
			// It exited before anyone could ask: a command not found, say.
			return fmt.Errorf("e2b: the command ended at once (%s)%s",
				cut(scrub(ev.Event.End.Status, secrets)), exitDetail(ev.Event.End.Error, secrets))
		}
		// A keepalive, or data: keep reading for the start.
	}
}

func exitDetail(e string, secrets []string) string {
	if e == "" {
		return ""
	}
	return ": " + cut(scrub(e, secrets))
}

func readEnvelope(r io.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > maxEnvelope {
		return 0, nil, errors.New("an envelope is too large")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return head[0], payload, nil
}

// connectError reads a Connect error sent as a plain HTTP error: JSON with a
// code and a message. A 404 is a sandbox E2B's proxy no longer knows.
func connectError(status int, raw []byte, secrets []string) error {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	err := connectErrorText(e.Code, e.Message, secrets)
	text := fmt.Sprintf("e2b: start the command: %d", status)
	if err != nil {
		text = fmt.Sprintf("%s %s", text, err)
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("%w (%s)", sandbox.ErrNotFound, text)
	}
	return errors.New(text)
}

func connectErrorText(code, message string, secrets []string) error {
	m := cut(scrub(message, secrets))
	switch {
	case code != "" && m != "":
		return fmt.Errorf("%s: %s", code, m)
	case code != "":
		return errors.New(code)
	case m != "":
		return errors.New(m)
	}
	return nil
}

// shellJoin quotes argv into one string for bash -c: each word in single
// quotes, a quote inside one written '\”.
func shellJoin(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(words, " ")
}
