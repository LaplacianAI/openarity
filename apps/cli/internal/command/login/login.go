package login

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

type loginView struct {
	Context   string    `json:"context" yaml:"context"`
	Server    string    `json:"server" yaml:"server"`
	ExpiresAt time.Time `json:"expires_at" yaml:"expires_at"`
}

func New(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in to the current context's brain",
		Long: "Prints an address and a short code. Open the address in any browser,\n" +
			"type the code, and approve — `oa` waits and stores the result under\n" +
			"the current context.\n\n" +
			"The code is what proves it is you approving, which is why the browser\n" +
			"and the terminal do not have to be on the same machine.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name := opts.Saved.ActiveName()
			if name == "" {
				return errors.New(
					"there is no context to log in to — " +
						"run `oa context create <name> --server <address>`")
			}

			provider, err := opts.Provider(cmd.Context())
			if err != nil {
				return err
			}

			device, err := provider.StartDevice(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Fprintf(opts.Stderr, "%s %s\n",
				opts.Styles.Label.Render("open"),
				opts.Styles.Value.Render(device.VerificationURI))
			fmt.Fprintf(opts.Stderr, "%s %s\n",
				opts.Styles.Label.Render("code"),
				opts.Styles.Value.Render(device.UserCode))
			if device.Complete != "" {
				fmt.Fprintln(opts.Stderr, opts.Styles.Muted.Render(
					"or open "+device.Complete+" — the code is already in it"))
			}
			fmt.Fprintln(opts.Stderr, opts.Styles.Muted.Render(
				"waiting for approval, up to "+device.ExpiresIn.String()+"…"))

			ctx, cancel := context.WithTimeout(cmd.Context(), device.ExpiresIn)
			defer cancel()

			token, err := provider.WaitForToken(ctx, device)
			if err != nil {
				return err
			}
			if err := opts.SaveLogin(token); err != nil {
				return err
			}

			return opts.Out.Print(loginView{
				Context:   name,
				Server:    opts.Settings.Server.Value,
				ExpiresAt: token.Expiry,
			}, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(
						opts.Styles.OK.Render("logged in"),
						opts.Styles.Value.Render(name),
						opts.Settings.Server.Value)
				},
			})
		},
	}
}
