package mcpservers

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

var (
	team  = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	other = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	root  = "teams/" + team.String() + "/mcp/"
)

func ptr(s string) *string { return &s }

func urlServer(u string) serverRequest {
	return serverRequest{Name: "github", URL: ptr(u)}
}

func commandServer(env map[string]string) serverRequest {
	return serverRequest{Name: "fs", Command: []string{"npx", "-y", "server-fs"}, Env: env}
}

func TestAURLServerIsAccepted(t *testing.T) {
	t.Parallel()

	req := urlServer("https://mcp.example.com/v1")
	req.AuthSecretRef = ptr(root + "github#token")
	req.Bare = true

	got, err := parse(team, req)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Name != "github" || *got.URL != "https://mcp.example.com/v1" || got.Command != nil ||
		*got.AuthSecretRef != root+"github#token" || !got.Bare {
		t.Errorf("fields = %+v", got)
	}
	if string(got.Env) != "{}" {
		t.Errorf("env = %s, want {} so the column's object check holds", got.Env)
	}
}

func TestACommandServerIsAcceptedWithItsEnvAsJSON(t *testing.T) {
	t.Parallel()

	got, err := parse(team, commandServer(map[string]string{
		"TOKEN":   root + "fs#token",
		"_NESTED": root + "a/b-c/d_e#k-1",
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := `{"TOKEN":"` + root + `fs#token","_NESTED":"` + root + `a/b-c/d_e#k-1"}`
	if string(got.Env) != want {
		t.Errorf("env = %s, want %s", got.Env, want)
	}
	if got.URL != nil || got.AuthSecretRef != nil || len(got.Command) != 3 {
		t.Errorf("fields = %+v", got)
	}
}

func TestBothTransportsIsRefused(t *testing.T) {
	t.Parallel()

	req := urlServer("https://mcp.example.com")
	req.Command = []string{"npx"}

	if _, err := parse(team, req); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("err = %v, want both transports refused", err)
	}
}

func TestNeitherTransportIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := parse(team, serverRequest{Name: "x"}); err == nil || !strings.Contains(err.Error(), "needs a url or a command") {
		t.Errorf("err = %v, want a missing transport refused", err)
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	for name, ok := range map[string]bool{
		"a":                     true,
		"A_b-9":                 true,
		strings.Repeat("a", 64): true,
		"":                      false,
		strings.Repeat("a", 65): false,
		"a.b":                   false,
		"a b":                   false,
		"a__b/c":                false,
		"é":                     false,
	} {
		req := urlServer("https://mcp.example.com")
		req.Name = name
		_, err := parse(team, req)
		if ok && err != nil {
			t.Errorf("name %q refused: %v", name, err)
		}
		if !ok && (err == nil || !strings.HasPrefix(err.Error(), "name ")) {
			t.Errorf("name %q: err = %v, want the name refused", name, err)
		}
	}
}

func TestURLsThatAreRefused(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"ftp://mcp.example.com":           "absolute http or https",
		"/relative":                       "absolute http or https",
		"mcp.example.com":                 "absolute http or https",
		"https://":                        "absolute http or https",
		"https://%zz":                     "absolute http or https",
		"https://u:p@mcp.example.com":     "user, password or query",
		"https://u@mcp.example.com":       "user, password or query",
		"https://mcp.example.com?key=sk1": "user, password or query",
		"https://mcp.example.com/?":       "user, password or query",
		"https://mcp.example.com/#frag":   "#fragment",
	} {
		_, err := parse(team, urlServer(raw))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("url %q: err = %v, want %q", raw, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "sk1") || strings.Contains(err.Error(), "u:p")) {
			t.Errorf("url %q: the refusal repeats the credential: %v", raw, err)
		}
	}
}

func TestPlainHTTPIsAccepted(t *testing.T) {
	t.Parallel()

	if _, err := parse(team, urlServer("http://localhost:8080/mcp")); err != nil {
		t.Errorf("parse: %v", err)
	}
}

func TestEnvOnAURLServerIsRefused(t *testing.T) {
	t.Parallel()

	req := urlServer("https://mcp.example.com")
	req.Env = map[string]string{"TOKEN": root + "x#k"}

	if _, err := parse(team, req); err == nil || !strings.Contains(err.Error(), "env is for a command server") {
		t.Errorf("err = %v", err)
	}
}

func TestAnEmptyEnvOnAURLServerIsNotAnEnv(t *testing.T) {
	t.Parallel()

	req := urlServer("https://mcp.example.com")
	req.Env = map[string]string{}

	if _, err := parse(team, req); err != nil {
		t.Errorf("parse: %v", err)
	}
}

func TestAuthSecretRefOnACommandServerIsRefused(t *testing.T) {
	t.Parallel()

	req := commandServer(nil)
	req.AuthSecretRef = ptr(root + "x#k")

	if _, err := parse(team, req); err == nil || !strings.Contains(err.Error(), "auth_secret_ref is for a url server") {
		t.Errorf("err = %v", err)
	}
}

func TestACommandMustNameAProgram(t *testing.T) {
	t.Parallel()

	for _, cmd := range [][]string{{}, {""}, {"", "arg"}} {
		req := serverRequest{Name: "fs", Command: cmd}
		if _, err := parse(team, req); err == nil || !strings.Contains(err.Error(), "name a program") {
			t.Errorf("command %q: err = %v", cmd, err)
		}
	}
}

func TestEnvKeysMustBeVariableNames(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"", "1A", "A-B", "A B", "A=B", "É"} {
		_, err := parse(team, commandServer(map[string]string{key: root + "x#k"}))
		if err == nil || !strings.Contains(err.Error(), "not an environment variable name") {
			t.Errorf("key %q: err = %v", key, err)
		}
	}
}

func TestAnEnvValueThatIsNotASecretPathIsRefusedAndNotStored(t *testing.T) {
	t.Parallel()

	const secret = "ghp_abcdef0123456789"
	got, err := parse(team, commandServer(map[string]string{"GITHUB_TOKEN": secret}))
	if err == nil {
		t.Fatal("a literal secret was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the refusal repeats the secret: %v", err)
	}
	if !strings.Contains(err.Error(), "env GITHUB_TOKEN is a secret reference, "+root+"<name>#<key>") {
		t.Errorf("err = %v, want the field and the shape named", err)
	}
	if got.Env != nil {
		t.Errorf("a refused request returned env %s", got.Env)
	}
}

func TestReferences(t *testing.T) {
	t.Parallel()

	for ref, ok := range map[string]bool{
		root + "x#k":                                            true,
		root + "a/b/c#k_1-2":                                    true,
		"teams/" + other.String() + "/mcp/x#k":                  false,
		"teams/" + team.String() + "/attachments#data_key":      false,
		"teams/" + team.String() + "/channels/x#signing_secret": false,
		"teams/" + team.String() + "/mcp#k":                     false,
		root + "../attachments#data_key":                        false,
		root + "./x#k":                                          false,
		root + "x/..#k":                                         false,
		root + "#k":                                             false,
		root + "a//b#k":                                         false,
		root + "a/#k":                                           false,
		root + "x":                                              false,
		root + "x#":                                             false,
		root + "x#k#k":                                          false,
		root + "x#k.v":                                          false,
		root + "x #k":                                           false,
		"/" + root + "x#k":                                      false,
		strings.ToUpper(root) + "x#k":                           false,
		"":                                                      false,
	} {
		req := urlServer("https://mcp.example.com")
		req.AuthSecretRef = ptr(ref)
		_, err := parse(team, req)
		if ok && err != nil {
			t.Errorf("ref %q refused: %v", ref, err)
		}
		if !ok && (err == nil || !strings.HasPrefix(err.Error(), "auth_secret_ref is a secret reference")) {
			t.Errorf("ref %q: err = %v, want refused", ref, err)
		}
	}
}

func TestASecretPathOfAnotherTeamIsRefused(t *testing.T) {
	t.Parallel()

	theirs := "teams/" + other.String() + "/mcp/github#token"
	_, err := parse(team, commandServer(map[string]string{"TOKEN": theirs}))
	if err == nil {
		t.Fatal("another team's secret path was accepted")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("err = %v, want this team's root named", err)
	}
}

// Map order is random, so without sorting the keys a request with two bad
// values would name either one.
func TestTheFirstBadEnvKeyIsNamedInOrder(t *testing.T) {
	t.Parallel()

	env := map[string]string{}
	for _, k := range []string{"Z", "M", "B", "Q", "A", "X", "C"} {
		env[k] = "literal"
	}

	for range 50 {
		_, err := parse(team, commandServer(env))
		if err == nil || !strings.HasPrefix(err.Error(), "env A ") {
			t.Fatalf("err = %v, want env A named first", err)
		}
	}
}
