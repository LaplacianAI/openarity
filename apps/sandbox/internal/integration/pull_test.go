package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func (e *env) hasImage(ref string) bool {
	_, err := e.docker("image", "inspect", ref)
	return err == nil
}

func (e *env) create(image string) (int, string) {
	status, b := e.api("POST", "/v1/rooms", map[string]any{"image": image, "env": map[string]string{}, "egress": []egress{}})
	return status, string(b)
}

func TestSandboxdPullsAnAllowedImageItDoesNotHave(t *testing.T) {
	e := setup(t, "")
	img := pullRepo + ":3.20"
	_, _ = e.docker("rmi", "-f", img) // absent is what the test wants; failing to remove it is fine
	if e.hasImage(img) {
		t.Fatal("the image is still on the daemon")
	}

	status, body := e.create(img)
	if status != 201 {
		t.Fatalf("create = %d %s", status, body)
	}
	var out struct{ ID string }
	_ = json.Unmarshal([]byte(body), &out)
	t.Cleanup(func() { e.api("DELETE", "/v1/rooms/"+out.ID, nil) })
	if e.hasImage(img) {
		t.Fatal("create pulled the image: it is the start that needs it")
	}
	if status, b := e.api("POST", "/v1/rooms/"+out.ID+"/start", map[string]any{"argv": []string{"sleep", "60"}}); status != 204 {
		t.Fatalf("start = %d %s", status, b)
	}
	if !e.hasImage(img) {
		t.Fatal("sandboxd did not pull the image")
	}
}

func TestSandboxdRefusesAnImageThatIsNotAllowedAndPullsNothing(t *testing.T) {
	e := setup(t, "")
	img := "docker.io/library/busybox:1.36"
	_, _ = e.docker("rmi", "-f", img)
	status, body := e.create(img)
	if status != 422 || !strings.Contains(body, img) {
		t.Fatalf("create = %d %s", status, body)
	}
	if e.hasImage(img) {
		t.Fatal("a disallowed image was pulled")
	}
	if n := len(e.roomIDs()); n != 0 {
		t.Fatalf("%d rooms were made", n)
	}
}

func TestAStartWhosePullFailsIsAnErrorThatNamesTheImage(t *testing.T) {
	e := setup(t, "")
	img := pullRepo + ":no-such-tag-sandboxd-it"
	status, body := e.create(img)
	if status != 201 {
		t.Fatalf("create = %d %s", status, body)
	}
	var out struct{ ID string }
	_ = json.Unmarshal([]byte(body), &out)
	t.Cleanup(func() { e.api("DELETE", "/v1/rooms/"+out.ID, nil) })
	status, b := e.api("POST", "/v1/rooms/"+out.ID+"/start", map[string]any{"argv": []string{"sleep", "60"}})
	if status != 503 || !strings.Contains(string(b), img) {
		t.Fatalf("start = %d %s", status, b)
	}
}
