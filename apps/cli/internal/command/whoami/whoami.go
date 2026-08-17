package whoami

import (
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func New(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the current credential authenticates as",
		Long: "Resolves the credential and asks the brain who it belongs to.\n\n" +
			"This is the quickest check that a login worked, and the only way to see\n" +
			"which teams you are in and with what role.\n\n" +
			"`-o json` is the form to script against: it carries your own user id and\n" +
			"the teams as objects, where the table renders them for reading.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			me, err := cli.Result(api.GetWhoamiWithResponse(cmd.Context()))
			if err != nil {
				return err
			}
			return printWhoami(opts, *me)
		},
	}
}

func viewOf(me client.Whoami) whoamiView {
	view := whoamiView{
		Kind:    string(me.Kind),
		Subject: me.Subject,
		ID:      me.ID.String(),
		Teams:   make([]teamView, 0, len(me.Teams)),
	}

	if me.Issuer != nil {
		view.Issuer = *me.Issuer
	}
	if me.Email != nil {
		view.Email = string(*me.Email)
	}

	for _, team := range me.Teams {
		view.Teams = append(view.Teams, teamView{
			ID:   team.ID.String(),
			Name: team.Name,
			Role: team.Role,
		})
	}

	return view
}

func printWhoami(o *cli.Options, me client.Whoami) error {
	view := viewOf(me)

	return o.Out.Print(view, printer.Options{
		Table: func(table *printer.Table) {
			table.Row(o.Styles.Label.Render("kind"), o.Styles.Value.Render(view.Kind))
			table.Row(o.Styles.Label.Render("subject"), o.Styles.Value.Render(view.Subject))
			table.Row(o.Styles.Label.Render("id"), o.Styles.Muted.Render(view.ID))

			if view.Issuer != "" {
				table.Row(o.Styles.Label.Render("issuer"), o.Styles.Value.Render(view.Issuer))
			}
			if view.Email != "" {
				table.Row(o.Styles.Label.Render("email"), o.Styles.Value.Render(view.Email))
			}

			if len(view.Teams) == 0 {
				table.Row(o.Styles.Label.Render("teams"), o.Styles.Muted.Render("none"))
				return
			}

			for i, team := range view.Teams {
				label := ""
				if i == 0 {
					label = o.Styles.Label.Render("teams")
				}
				table.Row(label, o.Styles.Value.Render(team.Name)+" "+
					o.Styles.Muted.Render("("+team.Role+")"))
			}
		},
	})
}
