package stack

import (
	"bufio"
	"fmt"
	"io"
	"os"
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

type wizard struct {
	opts  *cli.Options
	in    *bufio.Reader
	ask   bool
	quiet bool
	creds map[string]string
}

func newWizard(opts *cli.Options, quiet bool) *wizard {
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && !opts.NonInteractive && !quiet

	return &wizard{
		opts:  opts,
		in:    bufio.NewReader(os.Stdin),
		ask:   interactive,
		quiet: quiet,
		creds: map[string]string{},
	}
}

func (w *wizard) Run() (engine.Settings, map[string]string, error) {
	settings := engine.DefaultSettings()

	if !w.ask {
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
	return settings, w.creds, settings.Validate()
}

func (w *wizard) objects(s *engine.Settings) error {
	answer, err := w.pick("Where should files be kept?",
		"Transcripts, uploads and anything an agent produces.",
		[]choice{
			{"filesystem", "On this machine", "In the install directory. Backed up when you back that up."},
			{"memory", "In memory", "Lost every time it restarts. For trying it out."},
			{"s3", "S3 or compatible", "AWS, MinIO, Cloudflare R2. Needs a bucket and a key."},
		})
	if err != nil {
		return err
	}
	s.ObjectsBackend = answer

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
	w.say("Model gateway")
	w.say("  Where Openarity will send model requests. LiteLLM and OmniRoute both")
	w.say("  speak the OpenAI API, so either one is just a base URL.")
	w.say("  Nothing calls it yet — the agent loop is not built — so this is recorded")
	w.say("  for when it is.")

	url, err := w.text("URL", "", s.ModelGatewayURL)
	if err != nil {
		return err
	}
	s.ModelGatewayURL = url
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
