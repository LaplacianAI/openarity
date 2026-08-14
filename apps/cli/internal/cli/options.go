package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/cli/internal/auth"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
	"github.com/LaplacianAI/openarity/apps/cli/internal/theme"
	"github.com/LaplacianAI/openarity/apps/cli/internal/ui"
)

const requestTimeout = 30 * time.Second

type Options struct {
	Stdout io.Writer
	Stderr io.Writer
	Styles *ui.Styles
	Out    printer.Printer

	ServerFlag     string
	TokenFlag      string
	OutputFlag     string
	NonInteractive bool

	Saved    config.Config
	Settings config.Settings

	bare *client.ClientWithResponses
}

func NewOptions(stdout, stderr io.Writer) *Options {
	return &Options{Stdout: stdout, Stderr: stderr}
}

func (o *Options) Load() error {
	saved, err := config.Load()
	if err != nil {
		return err
	}
	o.Saved = saved

	path, _ := config.Path()
	o.Settings = config.Resolve(o.ServerFlag, o.TokenFlag, o.OutputFlag, os.Getenv, saved, path)

	chosenTheme, _ := theme.Parse(o.Settings.Theme.Value)
	o.Styles = ui.New(o.Stdout, chosenTheme)

	chosenOutput, ok := output.Parse(o.Settings.Output.Value)
	if !ok {
		return fmt.Errorf("%q is not an output format — use one of %s",
			o.Settings.Output.Value, output.Names())
	}
	o.Out = printer.New(o.Stdout, chosenOutput)

	o.bare, err = NewClient(o.Settings.Server.Value)
	return err
}

func (o *Options) API(ctx context.Context) (*client.ClientWithResponses, error) {
	token, err := auth.Resolve(ctx, o.bare, o.Settings.Server.Value, o.TokenFlag, o.Saved.Active().Token, os.Getenv)
	if err != nil {
		return nil, err
	}

	return NewClient(o.Settings.Server.Value, client.WithRequestEditorFn(
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		},
	))
}

func NewClient(server string, opts ...client.ClientOption) (*client.ClientWithResponses, error) {
	opts = append(opts, client.WithHTTPClient(&http.Client{Timeout: requestTimeout}))

	api, err := client.NewClientWithResponses(server, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable address: %w", server, err)
	}
	return api, nil
}

func (o *Options) WarnOverride(setting config.Setting) error {
	if strings.HasPrefix(setting.Source, "OPENARITY_") {
		fmt.Fprintln(o.Stdout, o.Styles.Warn.Render(
			setting.Source+" is set and overrides this for the current shell"))
	}
	return nil
}
