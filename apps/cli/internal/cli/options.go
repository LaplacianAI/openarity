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

// Options is everything a command may reach. It is the whole surface: a
// command package can use what is here and nothing else, which is what stops
// one of them doing surgery on a Config map from the far side of the program.
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

// Load runs before every command. It resolves each setting, builds the styles
// and the printer against the writer they print to, and dials nothing.
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

	// An unrecognised format is a hard error rather than a fall back to the
	// table: a table written into a file that expected JSON is worse than
	// failing. A wrong theme still shows the data, which is why that one
	// degrades instead.
	chosenOutput, ok := output.Parse(o.Settings.Output.Value)
	if !ok {
		return fmt.Errorf("%q is not an output format — use one of %s",
			o.Settings.Output.Value, output.Names())
	}
	o.Out = printer.New(o.Stdout, chosenOutput)

	o.bare, err = NewClient(o.Settings.Server.Value)
	return err
}

// API resolves a credential and returns a client that sends it. Commands that
// talk to the brain call this rather than building their own.
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

// WarnOverride says so when an exported variable beats what was just written.
// Without it, `oa config set` reports success on a value that will not take
// effect in this shell, which is the most confusing possible outcome.
func (o *Options) WarnOverride(setting config.Setting) error {
	if strings.HasPrefix(setting.Source, "OPENARITY_") {
		fmt.Fprintln(o.Stdout, o.Styles.Warn.Render(
			setting.Source+" is set and overrides this for the current shell"))
	}
	return nil
}
