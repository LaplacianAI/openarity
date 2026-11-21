package e2b

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// A template is built in three calls, as E2B's SDK makes them
// (e2b@2.63.0, src/template/buildApi.ts):
//
//   - POST /v3/templates {name, cpuCount, memoryMB}: a build of the template
//     of that name in the key's account, made if there is none. It answers
//     with the template's id and the build's.
//   - POST /v2/templates/{templateID}/builds/{buildID} {fromImage, steps,
//     force}: start that build from an image.
//   - GET /templates/{templateID}/builds/{buildID}/status: building,
//     waiting, ready or error, with the reason for an error.

// Room is the size every room's template is built at.
const (
	RoomCPUs     = 2
	RoomMemoryMB = 2048
)

type requestBuild struct {
	Name     string `json:"name"`
	CPUCount int    `json:"cpuCount"`
	MemoryMB int    `json:"memoryMB"`
}

type triggerBuild struct {
	FromImage string     `json:"fromImage"`
	Steps     []struct{} `json:"steps"`
	Force     bool       `json:"force"`
}

// BuildTemplate starts a build of the template name from image, at the room's
// size. The build runs at E2B; BuildStatus says when it is done. A build that
// was asked for and could not be started is returned with the error, so its
// ids are not lost.
func (c *Client) BuildTemplate(ctx context.Context, name, image string) (sandbox.Build, error) {
	body, err := json.Marshal(requestBuild{Name: name, CPUCount: RoomCPUs, MemoryMB: RoomMemoryMB})
	if err != nil {
		return sandbox.Build{}, fmt.Errorf("e2b: encode the template request: %w", err)
	}
	var b sandbox.Build
	if _, err := c.api(ctx, http.MethodPost, "/v3/templates", body, &b, []string{c.apiKey}); err != nil {
		return sandbox.Build{}, err
	}
	if b.TemplateID == "" || b.BuildID == "" {
		return sandbox.Build{}, errors.New("e2b: POST /v3/templates named no template or build")
	}

	body, err = json.Marshal(triggerBuild{FromImage: image, Steps: []struct{}{}})
	if err != nil {
		return b, fmt.Errorf("e2b: encode the build: %w", err)
	}
	path := "/v2/templates/" + url.PathEscape(b.TemplateID) + "/builds/" + url.PathEscape(b.BuildID)
	if _, err := c.api(ctx, http.MethodPost, path, body, nil, []string{c.apiKey}); err != nil {
		return b, err
	}
	return b, nil
}

type buildStatus struct {
	Status string `json:"status"`
	Reason *struct {
		Message string `json:"message"`
	} `json:"reason"`
}

// BuildStatus is where b is, and for a failed build E2B's reason. A build
// E2B no longer has is failed.
func (c *Client) BuildStatus(ctx context.Context, b sandbox.Build) (sandbox.BuildState, string, error) {
	path := "/templates/" + url.PathEscape(b.TemplateID) + "/builds/" + url.PathEscape(b.BuildID) + "/status"
	var s buildStatus
	if _, err := c.api(ctx, http.MethodGet, path, nil, &s, []string{c.apiKey}); err != nil {
		if errors.Is(err, sandbox.ErrNotFound) {
			return sandbox.BuildFailed, "E2B has no such build", nil
		}
		return "", "", err
	}
	switch s.Status {
	case "building", "waiting":
		return sandbox.BuildRunning, "", nil
	case "ready":
		return sandbox.BuildReady, "", nil
	case "error":
		reason := "the build failed"
		if s.Reason != nil && s.Reason.Message != "" {
			reason = cut(s.Reason.Message)
		}
		return sandbox.BuildFailed, reason, nil
	default:
		return "", "", fmt.Errorf("e2b: a build in a state not known: %q", cut(s.Status))
	}
}
