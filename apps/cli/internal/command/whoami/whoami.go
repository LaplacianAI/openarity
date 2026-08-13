package whoami

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

func New(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the current credential authenticates as",
		Long: "Resolves the credential and asks the brain who it belongs to.\n\n" +
			"This is the quickest check that a login worked, and the only way to see\n" +
			"which teams you are in and with what role.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			res, err := api.GetWhoamiWithResponse(cmd.Context())
			if err != nil {
				return fmt.Errorf("call %s: %w", opts.Settings.Server.Value, err)
			}
			if res.JSON200 == nil {
				return cli.APIError(res.HTTPResponse, res.Body)
			}
			return printWhoami(opts, *res.JSON200)
		},
	}
}

func printWhoami(o *cli.Options, me client.Whoami) error {
	w := tabwriter.NewWriter(o.Stdout, 0, 0, 2, ' ', 0)

	fmt.Fprintf(w, "%s\t%s\n", o.Styles.Label.Render("kind"), o.Styles.Value.Render(string(me.Kind)))
	fmt.Fprintf(w, "%s\t%s\n", o.Styles.Label.Render("subject"), o.Styles.Value.Render(me.Subject))

	if me.Issuer != nil && *me.Issuer != "" {
		fmt.Fprintf(w, "%s\t%s\n", o.Styles.Label.Render("issuer"), o.Styles.Value.Render(*me.Issuer))
	}
	if me.Email != nil && *me.Email != "" {
		fmt.Fprintf(w, "%s\t%s\n", o.Styles.Label.Render("email"), o.Styles.Value.Render(string(*me.Email)))
	}

	if len(me.Teams) == 0 {
		fmt.Fprintf(w, "%s\t%s\n", o.Styles.Label.Render("teams"), o.Styles.Muted.Render("none"))
		return w.Flush()
	}

	for i, team := range me.Teams {
		label := ""
		if i == 0 {
			label = o.Styles.Label.Render("teams")
		}
		fmt.Fprintf(w, "%s\t%s %s\n", label,
			o.Styles.Value.Render(team.Name), o.Styles.Muted.Render("("+team.Role+")"))
	}
	return w.Flush()
}
