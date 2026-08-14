package teams

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

type membershipView struct {
	Team   uuid.UUID `json:"team" yaml:"team"`
	UserID uuid.UUID `json:"user_id" yaml:"user_id"`
	Role   string    `json:"role,omitempty" yaml:"role,omitempty"`
	Member bool      `json:"member" yaml:"member"`
}

func newMembersCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "members",
		Short: "See and change who is in a team",
		Long: "Membership is what decides who can see a team's agents and tools.\n" +
			"Reading it needs only to see the team; changing it needs member:write.",
	}

	cmd.AddCommand(
		newMembersListCmd(opts),
		newMembersAddCmd(opts),
		newMembersRemoveCmd(opts),
	)

	return cmd
}

func newMembersListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list <team-id>",
		Aliases: []string{"ls"},
		Short:   "List a team's members",
		Long: "One page per call. When more remain the response carries a cursor;\n" +
			"pass it back with --cursor to fetch the next page.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			team, err := cli.ParseUUID("team id", args[0])
			if err != nil {
				return err
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListTeamMembersWithResponse(cmd.Context(), team,
				&client.ListTeamMembersParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.Member]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no members",
				More:       "oa teams members list " + team.String(),
				Row: func(table *printer.Table, member client.Member) {
					email := opts.Styles.Muted.Render("no email")
					if member.Email != nil {
						email = string(*member.Email)
					}
					table.Row(opts.Styles.Value.Render(member.Subject), member.Role, email,
						opts.Styles.Muted.Render(member.UserID.String()))
				},
			})
		},
	}

	paging.Flags(cmd)

	return cmd
}

func newMembersAddCmd(opts *cli.Options) *cobra.Command {
	var role string

	cmd := &cobra.Command{
		Use:   "add <team-id> <user-id>",
		Short: "Add someone to a team",
		Long: "The role must name a row in the brain's `roles` table — it ships with\n" +
			"admin and developer, and a deployment may add its own. An unknown one\n" +
			"is rejected by the database, so the brain answers 400 rather than\n" +
			"storing something nothing can grant.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			team, err := cli.ParseUUID("team id", args[0])
			if err != nil {
				return err
			}
			user, err := cli.ParseUUID("user id", args[1])
			if err != nil {
				return err
			}

			named := strings.TrimSpace(role)
			if named == "" {
				return fmt.Errorf("--role cannot be empty — a member without a role can do nothing")
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			err = cli.NoContent(api.AddTeamMemberWithResponse(cmd.Context(), team,
				client.AddTeamMemberJSONRequestBody{UserID: user, Role: named}))
			if err != nil {
				return err
			}

			return opts.Out.Print(
				membershipView{Team: team, UserID: user, Role: named, Member: true},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("added"),
							opts.Styles.Value.Render(user.String()),
							opts.Styles.Muted.Render("as "+named))
					},
				})
		},
	}

	cmd.Flags().StringVar(&role, "role", "", "the role to grant in this team, for example developer")
	_ = cmd.MarkFlagRequired("role")

	return cmd
}

func newMembersRemoveCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <team-id> <user-id>",
		Aliases: []string{"rm"},
		Short:   "Take someone out of a team",
		Long: "Removing someone who is not a member succeeds. The caller asked for a\n" +
			"state and that state holds, which is what makes this safe in a script\n" +
			"that runs twice.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			team, err := cli.ParseUUID("team id", args[0])
			if err != nil {
				return err
			}
			user, err := cli.ParseUUID("user id", args[1])
			if err != nil {
				return err
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			if err := cli.NoContent(api.RemoveTeamMemberWithResponse(cmd.Context(), team, user)); err != nil {
				return err
			}

			return opts.Out.Print(
				membershipView{Team: team, UserID: user, Member: false},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("removed"),
							opts.Styles.Value.Render(user.String()))
					},
				})
		},
	}
}
