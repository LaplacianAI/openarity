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
	"github.com/LaplacianAI/openarity/sdk/agent/tools/mcp"
)

const (
	// Max is the largest file read: the most kept as an attachment.
	Max = mcp.MaxKeptBytes
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

// Get reads the file at path from the room whose /file route is ep. A path is
// the room's to resolve: relative to where its process runs, or absolute.
func Get(ctx context.Context, hc *http.Client, ep sandbox.Endpoint, path string) ([]byte, error) {
	// Added to the address's own query, which can hold a provider's token.
	u, err := url.Parse(ep.URL)
	if err != nil {
		// Never the URL, or url's error, which quotes it.
		return nil, errors.New("the room's address is not a URL")
	}
	q := u.Query()
	q.Set("path", path)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, errors.New("the room's address is not one a request can be made to")
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
			return nil, ctx.Err()
		}
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return nil, fmt.Errorf("%w: %s", ErrGone, cause(err))
		}
		return nil, fmt.Errorf("reading the file from the room: %s", cause(err))
	}
	defer resp.Body.Close()

	if resp.Header.Get(problemHeader) == "1" {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxProblem))
		return nil, &Problem{Msg: strings.TrimSpace(string(msg))}
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Unmarked, so not the room's /file: the provider has no such room,
		// or the room's image is older than the route.
		return nil, fmt.Errorf("%w: it answered %d without the room's mark", ErrGone, resp.StatusCode)
	default:
		return nil, fmt.Errorf("the room answered %d", resp.StatusCode)
	}
	if resp.ContentLength > Max {
		return nil, &Problem{Msg: fmt.Sprintf("%s is %d bytes; a file is kept up to %d", path, resp.ContentLength, Max)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, Max+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("reading the file from the room: %s", cause(err))
	}
	if len(data) > Max {
		return nil, &Problem{Msg: fmt.Sprintf("%s is over %d bytes; a file is kept up to %d", path, Max, Max)}
	}
	// A body cut short of its length is net/http's error above: a file that
	// shrank while it was sent is never taken for the whole of it.
	return data, nil
}

// cause is a failed request's cause without the URL it was for.
func cause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}
