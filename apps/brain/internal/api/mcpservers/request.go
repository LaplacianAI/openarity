package mcpservers

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

var (
	namePattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	RefPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*#[A-Za-z0-9_-]+$`)
)

type Fields struct {
	Name          string
	URL           *string
	Command       []string
	Env           []byte
	AuthSecretRef *string
	Bare          bool
	Vars          []byte
	Egress        []byte

	AuthKind        string
	ClientID        *string
	ClientSecretRef *string
	Scopes          []string
}

const (
	authStatic = "static"
	authOAuth  = "oauth"
	maxScopes  = 50
)

func Parse(teamID uuid.UUID, req Request) (Fields, error) {
	if !namePattern.MatchString(req.Name) {
		return Fields{}, errors.New("name must be 1-64 letters, digits, hyphens or underscores, because each tool is named name__tool")
	}

	var err error
	switch {
	case req.URL != nil && req.Command != nil:
		err = errors.New("a server has a url or a command, not both")
	case req.URL != nil:
		err = checkURLServer(teamID, req)
	case req.Command != nil:
		err = checkCommandServer(teamID, req)
	default:
		err = errors.New("a server needs a url or a command")
	}
	if err == nil {
		err = checkAuth(teamID, &req)
	}
	if err != nil {
		return Fields{}, err
	}

	// Stored as given, never null: an empty object or list.
	env, vars := req.Env, req.Vars
	if env == nil {
		env = map[string]string{}
	}
	if vars == nil {
		vars = map[string]string{}
	}
	encoded, err := json.Marshal(env)
	if err != nil {
		return Fields{}, err
	}
	varsJSON, err := json.Marshal(vars)
	if err != nil {
		return Fields{}, err
	}
	egress, err := json.Marshal(normalEgress(req.Egress))
	if err != nil {
		return Fields{}, err
	}

	return Fields{
		Name: req.Name, URL: req.URL, Command: req.Command, Env: encoded, Vars: varsJSON, Egress: egress,
		AuthSecretRef: req.AuthSecretRef, Bare: req.Bare,
		AuthKind: req.AuthKind, ClientID: req.OAuthClientID, ClientSecretRef: req.OAuthClientSecretRef, Scopes: req.OAuthScopes,
	}, nil
}

// checkAuth is how a server is reached. A server signs in, or carries a
// static token, never both: the token would be sent in place of the
// sign-in's, and nobody could tell which one a call used.
func checkAuth(teamID uuid.UUID, req *Request) error {
	if req.AuthKind == "" {
		req.AuthKind = authStatic
	}
	switch req.AuthKind {
	case authStatic:
		if req.OAuthClientID != nil || req.OAuthClientSecretRef != nil || req.OAuthScopes != nil {
			return errors.New("oauth_client_id, oauth_client_secret_ref and oauth_scopes are for a server whose auth_kind is oauth")
		}
		return nil
	case authOAuth:
	default:
		return errors.New("auth_kind must be static or oauth")
	}

	switch {
	case req.URL == nil:
		return errors.New("a server that signs in needs a url: a command server is given env")
	case req.AuthSecretRef != nil:
		return errors.New("a server that signs in carries no auth_secret_ref: its token comes from the sign-in")
	case req.OAuthClientID != nil && (strings.TrimSpace(*req.OAuthClientID) == "" || len(*req.OAuthClientID) > 512):
		return errors.New("oauth_client_id, when set, is the OAuth client registered for the server: leave it out to have openarity register one")
	case req.OAuthClientID == nil && req.OAuthClientSecretRef != nil:
		return errors.New("oauth_client_secret_ref is the secret of the client named in oauth_client_id, which is not set")
	case len(req.OAuthScopes) > maxScopes:
		return fmt.Errorf("oauth_scopes may name at most %d scopes", maxScopes)
	}
	for _, sc := range req.OAuthScopes {
		if sc == "" || strings.ContainsAny(sc, " \t\r\n") || len(sc) > 512 {
			return fmt.Errorf("oauth_scopes holds %q, which is not a scope", sc)
		}
	}
	if req.OAuthClientSecretRef != nil {
		return checkRef(teamID, "oauth_client_secret_ref", *req.OAuthClientSecretRef)
	}
	return nil
}

func checkURLServer(teamID uuid.UUID, req Request) error {
	u, err := url.Parse(*req.URL)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return errors.New("url must be an absolute http or https address")
	case u.User != nil || u.RawQuery != "" || u.ForceQuery:
		return errors.New("url may not carry a user, password or query string: it is stored and shown, so a credential goes in auth_secret_ref")
	case u.Fragment != "":
		return errors.New("url may not carry a #fragment")
	case len(req.Env) > 0:
		return errors.New("env is for a command server; a url server takes auth_secret_ref")
	case len(req.Vars) > 0 || len(req.Egress) > 0:
		return errors.New("vars and egress are for a command server, which runs in the team's sandbox; a url server runs where it is")
	case req.AuthSecretRef != nil:
		return checkRef(teamID, "auth_secret_ref", *req.AuthSecretRef)
	}
	return nil
}

func checkCommandServer(teamID uuid.UUID, req Request) error {
	if len(req.Command) == 0 || req.Command[0] == "" {
		return errors.New("command must name a program to run")
	}
	// A command is run as argv, with no shell, so a program named with a
	// space is a whole command line given as one item: it can never start.
	if strings.ContainsAny(req.Command[0], " \t\n") {
		return fmt.Errorf("command[0] is %q, a program name with a space in it: give each argument as an item of its own, such as [\"npx\", \"-y\", \"some-package\"]", req.Command[0])
	}
	if req.AuthSecretRef != nil {
		return errors.New("auth_secret_ref is for a url server; a command server takes env")
	}
	for _, k := range slices.Sorted(maps.Keys(req.Env)) {
		if !envKeyPattern.MatchString(k) {
			return fmt.Errorf("env key %q is not an environment variable name", k)
		}
		if err := checkRef(teamID, "env "+k, req.Env[k]); err != nil {
			return err
		}
	}
	if err := checkVars(req.Vars, req.Env); err != nil {
		return err
	}
	return checkEgress(req.Egress)
}

const (
	maxVars      = 64
	maxVarBytes  = 4096
	maxEgress    = 32
	maxHostBytes = 253
)

// checkVars are a command server's plain settings. A name is in vars or in
// env, never both: which one the room is given would be a guess.
func checkVars(vars, env map[string]string) error {
	if len(vars) > maxVars {
		return fmt.Errorf("vars holds at most %d settings", maxVars)
	}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		v := vars[k]
		switch {
		case !envKeyPattern.MatchString(k):
			return fmt.Errorf("vars key %q is not an environment variable name", k)
		case env[k] != "":
			return fmt.Errorf("%s is in both vars and env: a secret goes in env as a reference, a plain setting in vars", k)
		case len(v) > maxVarBytes:
			return fmt.Errorf("vars %s is longer than %d bytes", k, maxVarBytes)
		case strings.ContainsRune(v, 0):
			return fmt.Errorf("vars %s holds a NUL character", k)
		}
	}
	return nil
}

// checkEgress is where a command server's room may connect: a host name or a
// public address, and a port, each once. The room reaches nothing else.
func checkEgress(egress []sandbox.HostPort) error {
	if len(egress) > maxEgress {
		return fmt.Errorf("egress holds at most %d hosts", maxEgress)
	}
	seen := map[sandbox.HostPort]bool{}
	for _, e := range egress {
		host := normalHost(e.Host)
		switch {
		case e.Port < 1 || e.Port > 65535:
			return fmt.Errorf("egress %s: the port must be between 1 and 65535", e.Host)
		case !hostPattern.MatchString(host) || len(host) > maxHostBytes:
			return fmt.Errorf("egress %q is not a host name or an address", e.Host)
		}
		if addr, err := netip.ParseAddr(host); err == nil {
			addr = addr.Unmap()
			if !addr.IsGlobalUnicast() || addr.IsPrivate() {
				return fmt.Errorf("egress %s is not a public address: a room never reaches a private network", e.Host)
			}
		} else if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return fmt.Errorf("egress %s is this machine, which a room never reaches", e.Host)
		}
		key := sandbox.HostPort{Host: host, Port: e.Port}
		if seen[key] {
			return fmt.Errorf("egress names %s:%d twice", host, e.Port)
		}
		seen[key] = true
	}
	return nil
}

// hostPattern is a DNS name or an address literal; brackets are not taken.
var hostPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$|^[0-9a-f:.]+$`)

func normalHost(h string) string { return strings.ToLower(strings.TrimSuffix(h, ".")) }

// normalEgress is egress as stored: hosts in lower case, never null.
func normalEgress(egress []sandbox.HostPort) []sandbox.HostPort {
	out := make([]sandbox.HostPort, 0, len(egress))
	for _, e := range egress {
		out = append(out, sandbox.HostPort{Host: normalHost(e.Host), Port: e.Port})
	}
	return out
}

func checkRef(teamID uuid.UUID, field, ref string) error {
	root := secrets.TeamPath(teamID, secrets.KindMCP) + "/"
	rest, under := strings.CutPrefix(ref, root)
	if !under || !RefPattern.MatchString(rest) {
		return fmt.Errorf("%s is a secret reference, %s<name>#<key>, not a secret", field, root)
	}
	return nil
}
