package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/cli/internal/auth"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
	"github.com/LaplacianAI/openarity/apps/cli/internal/credential/store"
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

	Credentials credential.Store
	credential  credential.Credential

	bare *client.ClientWithResponses
}

func (o *Options) SaveLogin(token *auth.Token) error {
	name := o.Saved.ActiveName()
	if name == "" {
		return errors.New(
			"there is no context to log in to — run `oa context create <name> --server <address>`")
	}

	cred := credential.Credential{
		Token:   token.Access,
		Refresh: token.Refresh,
		Expiry:  token.Expiry,
	}
	if err := o.Credentials.Set(name, cred); err != nil {
		return err
	}
	o.credential = cred

	return nil
}

func NewOptions(stdout, stderr io.Writer) *Options {
	return &Options{Stdout: stdout, Stderr: stderr}
}

func (o *Options) renewIfExpired(ctx context.Context) error {
	if o.Settings.Token.Source != o.Credentials.Location() {
		return nil
	}
	if !o.credential.IsExpired(time.Now()) || !o.credential.CanRefresh() {
		return nil
	}

	provider, err := o.Provider(ctx)
	if err != nil {
		return err
	}

	token, err := provider.Refresh(ctx, o.credential.Refresh)
	if err != nil {
		if errors.Is(err, auth.ErrRefreshRejected) {
			_ = o.Credentials.Delete(o.Saved.ActiveName())
		}
		return err
	}

	renewed := credential.Credential{
		Token:   token.Access,
		Refresh: token.Refresh,
		Expiry:  token.Expiry,
	}
	if err := o.Credentials.Set(o.Saved.ActiveName(), renewed); err != nil {
		return err
	}
	o.credential = renewed

	return nil
}

func (o *Options) Provider(ctx context.Context) (*auth.Provider, error) {
	res, err := o.bare.GetAuthConfigWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("ask the server how to authenticate: %w", err)
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf(
			"the server did not say how to authenticate: %s", res.HTTPResponse.Status)
	}

	if res.JSON200.Oidc == nil {
		return nil, fmt.Errorf(
			"%s has no identity provider configured, so there is nothing to log in to",
			o.Settings.Server.Value)
	}

	return auth.NewProvider(ctx, &http.Client{Timeout: requestTimeout},
		res.JSON200.Oidc.Issuer, res.JSON200.Oidc.ClientID)
}

func (o *Options) Load() error {
	saved, err := config.Load()
	if err != nil {
		return err
	}
	o.Saved = saved

	dir, err := config.Dir()
	if err != nil {
		return err
	}
	o.Credentials = store.Open(dir)

	o.credential, err = o.Credentials.Get(saved.ActiveName())
	if err != nil {
		return err
	}

	path, _ := config.Path()
	o.Settings = config.Resolve(config.Input{
		ServerFlag:         o.ServerFlag,
		TokenFlag:          o.TokenFlag,
		OutputFlag:         o.OutputFlag,
		Env:                os.Getenv,
		Saved:              saved,
		Path:               path,
		Credential:         o.credential,
		CredentialLocation: o.Credentials.Location(),
	})

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
	if err := o.renewIfExpired(ctx); err != nil {
		return nil, err
	}

	token, err := auth.Resolve(
		ctx, o.bare, o.Settings.Server.Value, o.TokenFlag, o.credential.Token, os.Getenv)
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

func (o *Options) WarnOverride(setting config.Setting) error {
	if strings.HasPrefix(setting.Source, "OPENARITY_") {
		fmt.Fprintln(o.Stdout, o.Styles.Warn.Render(
			setting.Source+" is set and overrides this for the current shell"))
	}
	return nil
}

func NewClient(server string, opts ...client.ClientOption) (*client.ClientWithResponses, error) {
	opts = append(opts, client.WithHTTPClient(&http.Client{Timeout: requestTimeout}))

	api, err := client.NewClientWithResponses(server, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable address: %w", server, err)
	}
	return api, nil
}
