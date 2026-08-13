package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func newTeamsCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "Create teams and see the ones you belong to",
		Long: "A team owns agents and tools, and membership is what decides who can\n" +
			"see them. Only a super admin can create one.",
	}

	cmd.AddCommand(
		newTeamsListCmd(opts),
		newTeamsCreateCmd(opts),
	)
	return cmd
}

func newTeamsListCmd(opts *options) *cobra.Command {
	var (
		limit  int32
		cursor string
	)

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the teams you can see",
		Long: "One page per call. When more remain the response carries a cursor;\n" +
			"pass it back with --cursor to fetch the next page.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := opts.api(cmd.Context())
			if err != nil {
				return err
			}

			params := &client.ListTeamsParams{}
			if cmd.Flags().Changed("limit") {
				params.Limit = &limit
			}
			if trimmed := strings.TrimSpace(cursor); trimmed != "" {
				params.Cursor = &trimmed
			}

			res, err := api.ListTeamsWithResponse(cmd.Context(), params)
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body)
			}
			page := *res.JSON200

			if len(page.Items) == 0 {
				opts.out.Note(opts.styles.Muted.Render("no teams"))
			}

			err = opts.out.Print(page, printer.Options{
				Table: func(table *printer.Table) {
					for _, team := range page.Items {
						role := opts.styles.Muted.Render("not a member")
						if team.Role != nil {
							role = *team.Role
						}
						table.Row(opts.styles.Value.Render(team.Name), role,
							opts.styles.Muted.Render(team.ID.String()))
					}
				},
			})
			if err != nil {
				return err
			}

			if page.NextCursor != nil {
				opts.out.Note(opts.styles.Muted.Render(
					fmt.Sprintf("more — `oa teams list --cursor %s`", *page.NextCursor)))
			}
			return nil
		},
	}

	cmd.Flags().Int32Var(&limit, "limit", 0, "rows per page; the brain clamps anything above its maximum")
	cmd.Flags().StringVar(&cursor, "cursor", "", "an opaque position, taken verbatim from a previous page")
	return cmd
}

func newTeamsCreateCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a team",
		Long:  "Super admins only. The name is trimmed and must be unique.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("a team needs a name")
			}

			api, err := opts.api(cmd.Context())
			if err != nil {
				return err
			}

			res, err := api.CreateTeamWithResponse(cmd.Context(),
				client.CreateTeamJSONRequestBody{Name: name})
			if err != nil {
				return err
			}
			if res.JSON201 == nil {
				return apiError(res.HTTPResponse, res.Body)
			}
			team := *res.JSON201

			return opts.out.Print(team, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.styles.OK.Render("created"),
						opts.styles.Value.Render(team.Name),
						opts.styles.Muted.Render(team.ID.String()))
				},
			})
		},
	}
}
