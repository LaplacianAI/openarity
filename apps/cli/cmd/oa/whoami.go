package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

func newWhoamiCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the current credential authenticates as",
		Long: "Resolves the credential and asks the brain who it belongs to.\n\n" +
			"This is the quickest check that a login worked, and the only way to see\n" +
			"which teams you are in and with what role.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := opts.api(cmd.Context())
			if err != nil {
				return err
			}

			res, err := api.GetWhoamiWithResponse(cmd.Context())
			if err != nil {
				return fmt.Errorf("call %s: %w", opts.server, err)
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body)
			}
			return opts.printWhoami(*res.JSON200)
		},
	}
}

func (o *options) printWhoami(me client.Whoami) error {
	w := tabwriter.NewWriter(o.stdout, 0, 0, 2, ' ', 0)

	fmt.Fprintf(w, "%s\t%s\n", o.styles.Label.Render("kind"), o.styles.Value.Render(string(me.Kind)))
	fmt.Fprintf(w, "%s\t%s\n", o.styles.Label.Render("subject"), o.styles.Value.Render(me.Subject))

	if me.Issuer != nil && *me.Issuer != "" {
		fmt.Fprintf(w, "%s\t%s\n", o.styles.Label.Render("issuer"), o.styles.Value.Render(*me.Issuer))
	}
	if me.Email != nil && *me.Email != "" {
		fmt.Fprintf(w, "%s\t%s\n", o.styles.Label.Render("email"), o.styles.Value.Render(string(*me.Email)))
	}

	if len(me.Teams) == 0 {
		fmt.Fprintf(w, "%s\t%s\n", o.styles.Label.Render("teams"), o.styles.Muted.Render("none"))
		return w.Flush()
	}

	for i, team := range me.Teams {
		label := ""
		if i == 0 {
			label = o.styles.Label.Render("teams")
		}
		fmt.Fprintf(w, "%s\t%s %s\n", label,
			o.styles.Value.Render(team.Name), o.styles.Muted.Render("("+team.Role+")"))
	}
	return w.Flush()
}
