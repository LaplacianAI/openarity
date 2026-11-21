package docker_test

import (
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/docker"
)

func TestSplitRefTakesATagADigestOrLatest(t *testing.T) {
	t.Parallel()
	cases := []struct{ ref, repo, tag string }{
		{"ghcr.io/o/r:sha-abc", "ghcr.io/o/r", "sha-abc"},
		{"ghcr.io/o/r", "ghcr.io/o/r", "latest"}, // an empty tag would pull every tag
		{"ghcr.io/o/r@sha256:ab12", "ghcr.io/o/r", "sha256:ab12"},
		{"ghcr.io/o/r:1@sha256:ab12", "ghcr.io/o/r", "sha256:ab12"},
		{"localhost:5000/r", "localhost:5000/r", "latest"}, // the colon is a port
		{"localhost:5000/r:2", "localhost:5000/r", "2"},
		{"room:1", "room", "1"},
	}
	for _, c := range cases {
		if repo, tag := docker.SplitRef(c.ref); repo != c.repo || tag != c.tag {
			t.Errorf("SplitRef(%q) = %q, %q; want %q, %q", c.ref, repo, tag, c.repo, c.tag)
		}
	}
}

func TestHasImageAsksTheDaemonAboutTheNameAndReadsA404AsAbsent(t *testing.T) {
	t.Parallel()
	c, calls := daemon(t, func(c call) (int, string) {
		if strings.Contains(c.Path, "present") {
			return 200, `{"Id":"sha256:x"}`
		}
		return 404, `{"message":"No such image: x"}`
	})
	// In order: the request checked below is the first one made.
	for _, tc := range []struct {
		ref  string
		want bool
	}{{"ghcr.io/o/present:1", true}, {"ghcr.io/o/absent:1", false}} {
		got, err := c.HasImage(t.Context(), tc.ref)
		if err != nil || got != tc.want {
			t.Errorf("HasImage(%q) = %v, %v; want %v", tc.ref, got, err, tc.want)
		}
	}
	got := calls()
	if got[0] != (call{Method: "GET", Path: "/v1.44/images/ghcr.io/o/present:1/json"}) {
		t.Fatalf("request = %+v", got[0])
	}
}

func TestHasImageReportsAnyOtherFailure(t *testing.T) {
	t.Parallel()
	c, _ := daemon(t, func(call) (int, string) { return 500, `{"message":"boom"}` })
	if _, err := c.HasImage(t.Context(), "a/b:1"); err == nil {
		t.Fatal("a 500 was read as absent")
	}
}

func TestPullImageSendsTheRepositoryAndTagApartAndReadsTheStreamToItsEnd(t *testing.T) {
	t.Parallel()
	stream := `{"status":"Pulling from o/r","id":"1"}` + "\n" + `{"status":"Downloaded newer image for ghcr.io/o/r:t"}` + "\n"
	cases := []struct{ ref, query string }{
		{"ghcr.io/o/r:t", "fromImage=ghcr.io%2Fo%2Fr&tag=t"},
		{"ghcr.io/o/r", "fromImage=ghcr.io%2Fo%2Fr&tag=latest"},
		{"ghcr.io/o/r@sha256:ab12", "fromImage=ghcr.io%2Fo%2Fr&tag=sha256%3Aab12"},
	}
	for _, tc := range cases {
		c, calls := daemon(t, func(call) (int, string) { return 200, stream })
		if err := c.PullImage(t.Context(), tc.ref); err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		got := calls()
		if len(got) != 1 || got[0] != (call{Method: "POST", Path: "/v1.44/images/create", Query: tc.query}) {
			t.Errorf("%s: requests = %+v", tc.ref, got)
		}
	}
}

// The daemon has already answered 200 when the registry says no: the failure
// is a message in the stream.
func TestAPullThatFailsInTheStreamFailsEvenThoughTheStatusIs200(t *testing.T) {
	t.Parallel()
	stream := `{"status":"Pulling from o/r"}` + "\n" +
		`{"status":"Downloading","progressDetail":{"current":1}}` + "\n" +
		`{"errorDetail":{"message":"unauthorized: denied"},"error":"unauthorized: denied"}` + "\n"
	c, _ := daemon(t, func(call) (int, string) { return 200, stream })
	err := c.PullImage(t.Context(), "ghcr.io/o/r:t")
	if err == nil || !strings.Contains(err.Error(), "unauthorized: denied") {
		t.Fatalf("err = %v", err)
	}
	// only the legacy field set
	c, _ = daemon(t, func(call) (int, string) { return 200, `{"error":"manifest unknown"}` + "\n" })
	if err := c.PullImage(t.Context(), "ghcr.io/o/r:t"); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("err = %v", err)
	}
}

func TestAPullThatTheDaemonRefusesUpFrontFailsWithItsMessage(t *testing.T) {
	t.Parallel()
	c, _ := daemon(t, func(call) (int, string) {
		return 404, `{"message":"pull access denied for r, repository does not exist"}`
	})
	err := c.PullImage(t.Context(), "ghcr.io/o/r:t")
	if !docker.IsNotFound(err) || !strings.Contains(err.Error(), "repository does not exist") {
		t.Fatalf("err = %v", err)
	}
}

func TestAStreamCutOffMidMessageIsAFailureNotASuccess(t *testing.T) {
	t.Parallel()
	c, _ := daemon(t, func(call) (int, string) { return 200, `{"status":"Pulling"}` + "\n" + `{"status":"Down` })
	if err := c.PullImage(t.Context(), "ghcr.io/o/r:t"); err == nil {
		t.Fatal("a truncated stream was read as success")
	}
}
