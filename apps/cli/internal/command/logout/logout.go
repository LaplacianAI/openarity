package logout

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

type logoutView struct {
	Context string `json:"context" yaml:"context"`
	Server  string `json:"server" yaml:"server"`
}

func New(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Discard the current context's credential",
		Long: "Removes the stored token for the current context only — other\n" +
			"contexts keep theirs.\n\n" +
			"It does not tell the identity provider anything. An access token\n" +
			"cannot be withdrawn before it expires, so anything already issued\n" +
			"stays valid for its few remaining minutes wherever else it is held.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			name := opts.Saved.ActiveName()
			if name == "" {
				return errors.New("there is no context to log out of")
			}

			if err := opts.Credentials.Delete(name); err != nil {
				return err
			}

			return opts.Out.Print(logoutView{
				Context: name,
				Server:  opts.Settings.Server.Value,
			}, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(
						opts.Styles.OK.Render("logged out"),
						opts.Styles.Value.Render(name),
						opts.Settings.Server.Value)
				},
			})
		},
	}
}
