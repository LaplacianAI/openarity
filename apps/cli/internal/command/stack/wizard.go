package stack

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

type choice struct {
	value string
	label string
	about string
}

type Answers struct {
	ModelBackend string
	ModelPath    string

	// "mint" asks the secret store for an AppRole rather than being given
	// one. Anything else means it was pasted.
	SecretsAuth string

	Objects   string
	Endpoint  string
	Bucket    string
	Region    string
	MinIOPath string
	Secrets   string
	Address   string
	KVMount   string
	Gateway   string
}

func (a Answers) complete() bool {
	return a.Objects != "" && a.Secrets != ""
}

func (a Answers) settings(base engine.Settings) engine.Settings {
	out := base
	out.ObjectsBackend = a.Objects
	if a.ModelBackend != "" {
		out.ModelBackend = a.ModelBackend
	}
	if a.ModelPath != "" {
		out.ModelPath = a.ModelPath
	}
	if a.MinIOPath != "" {
		out.MinIOPath = a.MinIOPath
	}
	out.SecretsBackend = a.Secrets

	for target, value := range map[*string]string{
		&out.ObjectsEndpoint: a.Endpoint,
		&out.ObjectsBucket:   a.Bucket,
		&out.ObjectsRegion:   a.Region,
		&out.SecretsAddr:     a.Address,
		&out.SecretsKVMount:  a.KVMount,
		&out.ModelBaseURL:    a.Gateway,
	} {
		if value != "" {
			*target = value
		}
	}
	return out
}

type wizard struct {
	root  string
	opts  *cli.Options
	in    *bufio.Reader
	ask   bool
	quiet bool
	given Answers
	creds map[string]string
}

func newWizard(opts *cli.Options, quiet bool, given Answers, root string) *wizard {
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && !opts.NonInteractive && !quiet

	return &wizard{
		root:  root,
		opts:  opts,
		in:    bufio.NewReader(os.Stdin),
		ask:   interactive,
		quiet: quiet,
		given: given,
		creds: map[string]string{},
	}
}

func (w *wizard) Run(ctx context.Context) (engine.Settings, map[string]string, error) {
	settings := engine.DefaultSettings()

	// Credentials arrive in the environment rather than as flags: argv is
	// readable by every process on the machine, and an S3 secret key on a
	// command line is a secret key in `ps`.
	for _, key := range []string{
		"OPENARITY_OBJECTS_ACCESS_KEY",
		"OPENARITY_OBJECTS_SECRET_KEY",
		"OPENARITY_SECRETS_APPROLE_ID",
		"OPENARITY_SECRETS_APPROLE_SECRET",
		"OPENARITY_MODEL_API_KEY",
		"OPENARITY_GATEWAY_PASSWORD",
	} {
		if value := os.Getenv(key); value != "" {
			w.creds[key] = value
		}
	}

	if w.given.complete() {
		settings = w.given.settings(settings)
		w.fillPaths(&settings)
		if err := settings.Validate(); err != nil {
			return settings, w.creds, err
		}
		if err := w.mint(ctx, settings); err != nil {
			return settings, w.creds, err
		}
		return settings, w.creds, w.checkCredentials(settings)
	}

	if !w.ask {
		w.fillPaths(&settings)
		if !w.quiet {
			w.say("Setting up with the defaults: artifacts on disk, secrets in the brain's own process.")
			w.say("Run this in a terminal to choose differently.")
		}
		return settings, w.creds, nil
	}

	w.say("")
	w.say("Openarity keeps three things somewhere. Press enter to take the recommended answer.")
	w.say("")

	if err := w.objects(&settings); err != nil {
		return settings, nil, err
	}
	if err := w.secrets(&settings); err != nil {
		return settings, nil, err
	}
	if err := w.models(&settings); err != nil {
		return settings, nil, err
	}

	w.say("")
	w.fillPaths(&settings)
	if err := settings.Validate(); err != nil {
		return settings, w.creds, err
	}
	if err := w.mint(ctx, settings); err != nil {
		return settings, w.creds, err
	}
	return settings, w.creds, w.checkCredentials(settings)
}

// mint asks the secret store for the AppRole, when that is what was chosen.
//
// The admin token comes from the environment and is used here and nowhere
// else: it can do anything to that server, which is why this is a choice
// rather than what setup does when it can. Only the AppRole is kept.
func (w *wizard) mint(ctx context.Context, s engine.Settings) error {
	if w.given.SecretsAuth != "mint" || s.SecretsBackend == "static" {
		return nil
	}

	role, err := engine.MintAppRole(ctx, http.DefaultClient,
		s.SecretsAddr, os.Getenv(adminToken), s.SecretsKVMount)
	if err != nil {
		return err
	}

	w.creds["OPENARITY_SECRETS_APPROLE_ID"] = role.ID
	w.creds["OPENARITY_SECRETS_APPROLE_SECRET"] = role.Secret
	return nil
}

// The token that mints, which is never stored. Read from the environment for
// the same reason every other credential is: argv is readable by every
// process on this machine.
const adminToken = "OPENARITY_SECRETS_ADMIN_TOKEN"

// fillPaths supplies the directories nobody was asked for.
//
// Two fields in the installer window start empty and say so — "Blank puts it
// inside the install" — and the window sends that empty string as the answer.
// The terminal wizard never sees the problem, because there a blank answer
// takes the default it offered; the window skips the wizard entirely, a
// sidecar having no terminal to prompt at, and nothing filled the gap.
//
// Both refusals reached a person as "setup exited with Some(1)". The gateway
// was fixed first and MinIO was not, which is the whole reason this is one
// function over a list rather than a check per field: the next directory
// somebody adds should be a line here, not another bug report.
//
// Here rather than in Validate, because validating must not change what it is
// validating, and here rather than in Settings, which has no idea where the
// install is.
func (w *wizard) fillPaths(s *engine.Settings) {
	for _, field := range []struct {
		needed bool
		into   *string
		name   string
	}{
		{needed: s.RunsGateway(), into: &s.ModelPath, name: "gateway"},
		{needed: s.RunsMinIO(), into: &s.MinIOPath, name: "minio"},
	} {
		if field.needed && *field.into == "" {
			*field.into = filepath.Join(w.root, field.name)
		}
	}
}

// checkCredentials refuses what the brain would refuse, before anything is
// downloaded.
//
// A secret store is a dependency rather than a feature flag: the brain will
// not start without an AppRole, and neither will `brain migrate up`. Settings
// cannot say so — credentials are deliberately not in it, so that the state
// file can never hold one — so the check belongs here, where both halves are
// in the same hand.
//
// Discovered by choosing OpenBao in the installer window, which asked for the
// address and not for the AppRole. That failed several minutes in, after 70MB
// of Postgres and a cluster, with "validation failed: SECRETS_APPROLE_ID and
// SECRETS_APPROLE_SECRET are required". It now fails in about a second.
func (w *wizard) checkCredentials(s engine.Settings) error {
	if s.SecretsBackend == "static" {
		return nil
	}

	var missing []string
	for _, key := range []string{"OPENARITY_SECRETS_APPROLE_ID", "OPENARITY_SECRETS_APPROLE_SECRET"} {
		if w.creds[key] == "" {
			missing = append(missing, strings.TrimPrefix(key, "OPENARITY_"))
		}
	}
	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf(
		"stack: %s reaches its secret store with an AppRole, so %s cannot be blank",
		s.SecretsBackend, strings.Join(missing, " and "))
}

func (w *wizard) objects(s *engine.Settings) error {
	answer, err := w.pick("Where should files be kept?",
		"Transcripts, uploads and anything an agent produces.",
		[]choice{
			{"filesystem", "On this machine", "In the install directory. Backed up when you back that up."},
			{"memory", "In memory", "Lost every time it restarts. For trying it out."},
			{"minio", "Run MinIO here", "An object store of your own, started and stopped with everything else."},
			{"s3", "Somewhere else", "A bucket you already have — AWS, Cloudflare R2, a MinIO on another machine."},
		})
	if err != nil {
		return err
	}
	s.ObjectsBackend = answer

	if answer == "minio" {
		if s.MinIOPath, err = w.text("Where should MinIO keep its files?",
			"Anywhere with room. An external drive is fine.",
			filepath.Join(w.root, "minio")); err != nil {
			return err
		}
		if s.ObjectsBucket, err = w.text("Bucket", "Created for you.", "openarity"); err != nil {
			return err
		}
		return nil
	}

	if answer != "s3" {
		return nil
	}

	s.ObjectsEndpoint, err = w.text("Endpoint", "Blank for AWS itself. For MinIO, something like http://127.0.0.1:9000", "")
	if err != nil {
		return err
	}
	if s.ObjectsBucket, err = w.text("Bucket", "It must already exist.", "openarity"); err != nil {
		return err
	}
	if s.ObjectsRegion, err = w.text("Region", "MinIO ignores this.", "us-east-1"); err != nil {
		return err
	}

	access, err := w.text("Access key", "", "")
	if err != nil {
		return err
	}
	secret, err := w.secret("Secret key")
	if err != nil {
		return err
	}

	w.creds["OPENARITY_OBJECTS_ACCESS_KEY"] = access
	w.creds["OPENARITY_OBJECTS_SECRET_KEY"] = secret
	return nil
}

func (w *wizard) secrets(s *engine.Settings) error {
	answer, err := w.pick("Where should credentials be kept?",
		"The tokens Openarity uses to reach the services you connect to it.",
		[]choice{
			{"static", "In the brain itself", "Held in the process. Lost on restart, so every connection is set up again."},
			{"openbao", "OpenBao or Vault", "A server you already run. Survives restarts and can be rotated."},
		})
	if err != nil {
		return err
	}
	s.SecretsBackend = answer

	if answer == "static" {
		return nil
	}

	if s.SecretsAddr, err = w.text("Address", "For example http://127.0.0.1:8200", "http://127.0.0.1:8200"); err != nil {
		return err
	}
	if s.SecretsKVMount, err = w.text("KV mount", "", "secret"); err != nil {
		return err
	}

	how, err := w.pick("How should Openarity get its AppRole?",
		"The brain logs in with one. It is the only credential kept.",
		[]choice{
			{"paste", "I have one", "The two values `make bao-approle` prints."},
			{"mint", "Mint one for me", "Needs a token that may administer that server. It is used once and not stored."},
		})
	if err != nil {
		return err
	}

	if how == "mint" {
		token, err := w.secret("Admin token")
		if err != nil {
			return err
		}
		// Through the environment, so the rest of the flow reads it the same
		// way whether it came from here or from the installer window.
		if err := os.Setenv(adminToken, token); err != nil {
			return err
		}
		w.given.SecretsAuth = "mint"
		return nil
	}

	roleID, err := w.text("AppRole ID", "", "")
	if err != nil {
		return err
	}
	roleSecret, err := w.secret("AppRole secret")
	if err != nil {
		return err
	}

	w.creds["OPENARITY_SECRETS_APPROLE_ID"] = roleID
	w.creds["OPENARITY_SECRETS_APPROLE_SECRET"] = roleSecret
	return nil
}

func (w *wizard) models(s *engine.Settings) error {
	answer, err := w.pick("Where should Openarity get its models from?",
		"Anything that speaks the OpenAI API. Nothing calls it yet — the agent loop is not built — so this is recorded for when it is.",
		[]choice{
			{"external", "One you already run", "A gateway on this machine or your network. Nothing is downloaded."},
			{"here", "Run one here", "LiteLLM or OmniRoute, installed and started for you. A gigabyte or more."},
			{"openai", "OpenAI", "Needs a key."},
			{"other", "Something else", "Any other OpenAI-compatible endpoint."},
		})
	if err != nil {
		return err
	}

	if answer == "here" {
		return w.runAGateway(s)
	}

	s.ModelBackend = "external"

	fallback := s.ModelBaseURL
	if answer == "openai" {
		fallback = "https://api.openai.com/v1"
	}

	url, err := w.text("URL", "", fallback)
	if err != nil {
		return err
	}
	s.ModelBaseURL = url

	key, err := w.secret("API key, or blank for none")
	if err != nil {
		return err
	}
	if key != "" {
		w.creds["OPENARITY_MODEL_API_KEY"] = key
	}
	return nil
}

// The gateway is installed rather than pointed at, which means a runtime as
// well: neither publishes a binary. The sizes are measured and said out loud,
// because a gigabyte arriving unannounced on somebody's laptop is the kind of
// surprise that gets an installer uninstalled.
func (w *wizard) runAGateway(s *engine.Settings) error {
	answer, err := w.pick("Which one?",
		"Both speak the OpenAI API and both are started and stopped with everything else.",
		[]choice{
			{"litellm", "LiteLLM", "A proxy in front of every provider. About 1GB, and it brings its own Python."},
			{"omniroute", "OmniRoute", "A router with a dashboard of its own. About 3.7GB, and it brings its own Node."},
		})
	if err != nil {
		return err
	}
	s.ModelBackend = answer

	if s.ModelPath, err = w.text("Where should it install?",
		"Anywhere with room. An external drive is fine.",
		filepath.Join(w.root, "gateway")); err != nil {
		return err
	}

	if answer == "omniroute" {
		// Its own image defaults this to CHANGEME and warns in a log nobody
		// reads. Blank is fine — setup generates one and shows it once, the
		// same bargain as the sign-in passphrase.
		password, err := w.secret("Dashboard password, or blank to have one generated")
		if err != nil {
			return err
		}
		if password != "" {
			w.creds["OPENARITY_GATEWAY_PASSWORD"] = password
		}
	}

	key, err := w.secret("A provider API key to start it with, or blank for none")
	if err != nil {
		return err
	}
	if key != "" {
		w.creds["OPENARITY_MODEL_API_KEY"] = key
	}
	return nil
}

func (w *wizard) pick(question, about string, options []choice) (string, error) {
	w.say(question)
	if about != "" {
		w.say("  " + about)
	}
	w.say("")

	for i, o := range options {
		marker := " "
		if i == 0 {
			marker = "*"
		}
		w.say(fmt.Sprintf("  %s %d. %-20s %s", marker, i+1, o.label, o.about))
	}
	w.say("")

	for {
		answer, err := w.read(fmt.Sprintf("  Choose 1-%d [1]: ", len(options)))
		if err != nil {
			return "", err
		}
		if answer == "" {
			w.say("")
			return options[0].value, nil
		}

		n, err := strconv.Atoi(answer)
		if err != nil || n < 1 || n > len(options) {
			w.say(fmt.Sprintf("  %q is not one of them.", answer))
			continue
		}
		w.say("")
		return options[n-1].value, nil
	}
}

func (w *wizard) text(label, about, fallback string) (string, error) {
	if about != "" {
		w.say("  " + about)
	}

	prompt := "  " + label
	if fallback != "" {
		prompt += " [" + fallback + "]"
	}

	answer, err := w.read(prompt + ": ")
	if err != nil {
		return "", err
	}
	if answer == "" {
		return fallback, nil
	}
	return answer, nil
}

func (w *wizard) secret(label string) (string, error) {
	fmt.Fprint(w.opts.Stderr, "  "+label+": ")

	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(w.opts.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (w *wizard) read(prompt string) (string, error) {
	fmt.Fprint(w.opts.Stderr, prompt)

	line, err := w.in.ReadString('\n')
	if err != nil && !strings.HasSuffix(line, "\n") && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (w *wizard) say(line string) {
	fmt.Fprintln(w.opts.Stderr, line)
}
