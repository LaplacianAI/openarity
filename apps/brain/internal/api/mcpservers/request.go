package mcpservers

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

var (
	namePattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	refPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*#[A-Za-z0-9_-]+$`)
)

type fields struct {
	Name          string
	URL           *string
	Command       []string
	Env           []byte
	AuthSecretRef *string
	Bare          bool
}

func parse(teamID uuid.UUID, req serverRequest) (fields, error) {
	if !namePattern.MatchString(req.Name) {
		return fields{}, errors.New("name must be 1-64 letters, digits, hyphens or underscores, because each tool is named name__tool")
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
	if err != nil {
		return fields{}, err
	}

	env := req.Env
	if env == nil {
		env = map[string]string{}
	}
	encoded, err := json.Marshal(env)
	if err != nil {
		return fields{}, err
	}

	return fields{
		Name: req.Name, URL: req.URL, Command: req.Command, Env: encoded,
		AuthSecretRef: req.AuthSecretRef, Bare: req.Bare,
	}, nil
}

func checkURLServer(teamID uuid.UUID, req serverRequest) error {
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
	case req.AuthSecretRef != nil:
		return checkRef(teamID, "auth_secret_ref", *req.AuthSecretRef)
	}
	return nil
}

func checkCommandServer(teamID uuid.UUID, req serverRequest) error {
	if len(req.Command) == 0 || req.Command[0] == "" {
		return errors.New("command must name a program to run")
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
	return nil
}

func checkRef(teamID uuid.UUID, field, ref string) error {
	root := secrets.TeamPath(teamID, secrets.KindMCP) + "/"
	rest, under := strings.CutPrefix(ref, root)
	if !under || !refPattern.MatchString(rest) {
		return fmt.Errorf("%s is a secret reference, %s<name>#<key>, not a secret", field, root)
	}
	return nil
}
