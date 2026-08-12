package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/auth"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/ui"
)

const requestTimeout = 30 * time.Second

type options struct {
	stdout io.Writer
	stderr io.Writer
	styles *ui.Styles

	serverFlag     string
	tokenFlag      string
	nonInteractive bool

	saved  config.Config
	server string
	bare   *client.ClientWithResponses
}

func newClient(server string, opts ...client.ClientOption) (*client.ClientWithResponses, error) {
	opts = append(opts, client.WithHTTPClient(&http.Client{Timeout: requestTimeout}))

	api, err := client.NewClientWithResponses(server, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable address: %w", server, err)
	}
	return api, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (o *options) load() error {
	saved, err := config.Load()
	if err != nil {
		return err
	}
	o.saved = saved

	o.server = firstNonEmpty(o.serverFlag, os.Getenv("OPENARITY_SERVER"), saved.Server, config.DefaultServer)

	o.bare, err = newClient(o.server)
	return err
}

func (o *options) api(ctx context.Context) (*client.ClientWithResponses, error) {
	token, err := auth.Resolve(ctx, o.bare, o.tokenFlag, o.saved.Token, os.Getenv)
	if err != nil {
		return nil, err
	}

	return newClient(o.server, client.WithRequestEditorFn(
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		},
	))
}

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	opts := &options{stdout: stdout, stderr: stderr, styles: ui.New(stdout, os.Getenv)}

	root := &cobra.Command{
		Use:   "oa",
		Short: "The Openarity CLI",
		Long: "oa talks to a brain over its HTTP API.\n\n" +
			"Against a development brain it needs no setup: run `oa whoami` and it\n" +
			"finds the shared token already in your shell. Anywhere else, `oa login`.",
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error { return opts.load() },
	}

	root.PersistentFlags().StringVar(&opts.serverFlag, "server", "",
		"the brain's API address (default $OPENARITY_SERVER, then the saved config, then "+config.DefaultServer+")")
	root.PersistentFlags().StringVar(&opts.tokenFlag, "token", "",
		"the credential to send, instead of the saved one")
	root.PersistentFlags().BoolVar(&opts.nonInteractive, "non-interactive", false,
		"never prompt; fail instead of asking")

	root.AddCommand(newWhoamiCmd(opts))

	return root
}
