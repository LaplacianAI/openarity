package teams

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "Create, delete and restore teams, and see the ones you belong to",
		Long: "A team owns agents and tools, and membership is what decides who can\n" +
			"see them. Only a super admin can create one.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newCreateCmd(opts),
		newDeleteCmd(opts),
		newRestoreCmd(opts),
		newMembersCmd(opts),
		newSettingsCmd(opts),
	)
	return cmd
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging
	var deleted bool

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the teams you can see",
		Long: "One page per call. When more remain the response carries a cursor;\n" +
			"pass it back with --cursor to fetch the next page.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			limit, cursor := paging.Values()
			params := &client.ListTeamsParams{Limit: limit, Cursor: cursor}
			if deleted {
				params.Deleted = &deleted
			}
			page, err := cli.Result(api.ListTeamsWithResponse(cmd.Context(), params))
			if err != nil {
				return err
			}

			if deleted {
				return printDeleted(opts, page)
			}
			return cli.PrintPage(opts, cli.Page[client.Team]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no teams",
				More:       "oa teams list",
				Row: func(table *printer.Table, team client.Team) {
					role := opts.Styles.Muted.Render("not a member")
					if team.Role != nil {
						role = *team.Role
					}
					table.Row(opts.Styles.Value.Render(team.Name), role,
						opts.Styles.Muted.Render(team.ID.String()))
				},
			})
		},
	}

	paging.Flags(cmd)
	cmd.Flags().BoolVar(&deleted, "deleted", false,
		"only deleted teams, with when each is purged (super admins only)")
	return cmd
}

func printDeleted(opts *cli.Options, page *client.TeamPage) error {
	return cli.PrintPage(opts, cli.Page[client.Team]{
		Items:      page.Items,
		NextCursor: page.NextCursor,
		Empty:      "no deleted teams",
		More:       "oa teams list --deleted",
		Row: func(table *printer.Table, team client.Team) {
			table.Row(opts.Styles.Value.Render(team.Name),
				opts.Styles.Muted.Render("deleted "+stamp(team.DeletedAt)),
				"purges "+stamp(team.PurgeAt),
				opts.Styles.Muted.Render(team.ID.String()))
		},
	})
}

func stamp(at *time.Time) string {
	if at == nil {
		return "-"
	}
	return at.UTC().Format("2006-01-02 15:04 UTC")
}

func newDeleteCmd(opts *cli.Options) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete <team>",
		Short: "Delete a team; it can be restored until it is purged",
		Long: "Super admins only. The team disappears for its members at once and its\n" +
			"name is free. It is erased for good once the brain's TEAM_PURGE_AFTER has\n" +
			"passed, and until then `oa teams restore` brings it back whole. Refused\n" +
			"while a run is queued or running in it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.NonInteractive && !yes {
				return errors.New("deleting a team asks for its name to be typed — pass --yes to skip that")
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			id, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			team, err := cli.Result(api.GetTeamWithResponse(cmd.Context(), id))
			if err != nil {
				return err
			}

			if !yes {
				if err := confirmName(cmd, opts, team.Name); err != nil {
					return err
				}
			}

			if err := cli.NoContent(api.DeleteTeamWithResponse(cmd.Context(), id)); err != nil {
				return err
			}

			// The purge date is the brain's to say: TEAM_PURGE_AFTER is its setting.
			gone, err := cli.Result(api.GetTeamWithResponse(cmd.Context(), id))
			if err != nil {
				return fmt.Errorf("the team was deleted, but reading it back failed: %w", err)
			}
			return opts.Out.Print(gone, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("deleted"),
						opts.Styles.Value.Render(gone.Name),
						opts.Styles.Muted.Render("purged on "+stamp(gone.PurgeAt)))
				},
			})
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "delete without asking for the name")
	return cmd
}

// confirmName asks on stderr, so the prompt is seen under -o json too, and
// takes only the exact name: a y is too easy to type at the wrong team.
func confirmName(cmd *cobra.Command, opts *cli.Options, name string) error {
	fmt.Fprintf(opts.Stderr,
		"Delete team %q? It can be restored until it is purged. Type the name to confirm: ", name)

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("no name was typed, so nothing was deleted")
	}
	if strings.TrimRight(line, "\r\n") != name {
		return errors.New("that is not the team's name, so nothing was deleted")
	}
	return nil
}

func newRestoreCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <team>",
		Short: "Bring back a deleted team before it is purged",
		Long: "Super admins only. The name is looked up among deleted teams, so a live team\n" +
			"that has taken it since is never the one restored; restoring then fails\n" +
			"until that team is renamed or deleted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			id, err := cli.ResolveDeletedTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			team, err := cli.Result(api.RestoreTeamWithResponse(cmd.Context(), id))
			if err != nil {
				return err
			}
			return opts.Out.Print(team, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("restored"),
						opts.Styles.Value.Render(team.Name),
						opts.Styles.Muted.Render("("+team.ID.String()+")"))
				},
			})
		},
	}
}

func newCreateCmd(opts *cli.Options) *cobra.Command {
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

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			team, err := cli.Created(api.CreateTeamWithResponse(cmd.Context(),
				client.CreateTeamJSONRequestBody{Name: name}))
			if err != nil {
				return err
			}

			return opts.Out.Print(team, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("created"),
						opts.Styles.Value.Render(team.Name),
						opts.Styles.Muted.Render(team.ID.String()))
				},
			})
		},
	}
}
