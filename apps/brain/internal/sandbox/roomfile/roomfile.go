// Package roomfile reads one file out of a sandbox room: a file a command MCP
// server saved in its room, or one a run wrote in its workspace. The room
// serves it at /file behind the room's token; this is the brain's side.
package roomfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

const (
	// Max is the largest file read from a room: the most keep_file keeps.
	// Keep it equal to the sandbox's roomfile.MaxDownload.
	Max = 100 << 20
	// Route is where a room serves its files.
	Route = "/file"
	// problemHeader marks an answer as the room's own refusal.
	problemHeader = "X-Room-Problem"
	// maxProblem is the most of a refusal read: a line for the model.
	maxProblem = 4 << 10
)

// A Problem is the room's refusal, for the model: a file that is not there,
// a directory, one too large. Its text is the room's.
type Problem struct{ Msg string }

func (p *Problem) Error() string { return p.Msg }

// ErrGone is a room that did not take the call: nothing listens at its
// address, or the provider says it has no such room.
var ErrGone = errors.New("the room is gone")

// Open reads the file at path from the room whose /file route is ep: its
// body, streamed, and its length, or -1 when the room did not say. A path is
// the room's to resolve: relative to where its process runs, or absolute.
// net/http ends the body at its length and fails one that ends short of it;
// the body fails a read past Max, for a room that gave no length. So a file
// is never taken for more or less than the room sent. It is the caller's to
// close.
func Open(ctx context.Context, hc *http.Client, ep sandbox.Endpoint, path string) (io.ReadCloser, int64, error) {
	// Added to the address's own query, which can hold a provider's token.
	u, err := url.Parse(ep.URL)
	if err != nil {
		// Never the URL, or url's error, which quotes it.
		return nil, 0, errors.New("the room's address is not a URL")
	}
	q := u.Query()
	q.Set("path", path)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, 0, errors.New("the room's address is not one a request can be made to")
	}
	for k, vs := range ep.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// Never the URL: a provider's address can carry its token.
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return nil, 0, fmt.Errorf("%w: %s", ErrGone, cause(err))
		}
		return nil, 0, fmt.Errorf("reading the file from the room: %s", cause(err))
	}

	if resp.Header.Get(problemHeader) == "1" {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxProblem))
		_ = resp.Body.Close()
		return nil, 0, &Problem{Msg: strings.TrimSpace(string(msg))}
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		_ = resp.Body.Close()
		// Unmarked, so not the room's /file: the provider has no such room,
		// or the room's image is older than the route.
		return nil, 0, fmt.Errorf("%w: it answered %d without the room's mark", ErrGone, resp.StatusCode)
	default:
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("the room answered %d", resp.StatusCode)
	}
	if resp.ContentLength > Max {
		_ = resp.Body.Close()
		return nil, 0, &Problem{Msg: fmt.Sprintf("%s is %d bytes; a file is kept up to %d", path, resp.ContentLength, Max)}
	}
	return &bounded{body: resp.Body, left: Max, path: path}, resp.ContentLength, nil
}

// bounded is a room's file that may not pass Max.
type bounded struct {
	body io.ReadCloser
	left int64
	path string
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, b.over()
	}
	// One byte past the limit is asked for, so a body that goes on is seen.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.body.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n + int(b.left), b.over()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("reading the file from the room: %s", cause(err))
	}
	return n, err
}

func (b *bounded) over() error {
	return &Problem{Msg: fmt.Sprintf("%s is over %d bytes; a file is kept up to %d", b.path, Max, Max)}
}

func (b *bounded) Close() error { return b.body.Close() }

// cause is a failed request's cause without the URL it was for.
func cause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}
