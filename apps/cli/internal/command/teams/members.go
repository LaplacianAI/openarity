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
	User   string    `json:"user" yaml:"user"`
	Role   string    `json:"role,omitempty" yaml:"role,omitempty"`
	Member bool      `json:"member" yaml:"member"`
}

func newMembersCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "members",
		Short: "See and change who is in a team",
		Long: "Membership is what decides who can see a team's agents and tools.\n" +
			"Reading it needs only to see the team; changing it needs membership:write.",
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
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List a team's members",
		Long: "The team is a name or an id. One page per call; when more remain the\n" +
			"response carries a cursor, which --cursor takes back.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
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
		Use:   "add <team> <user>",
		Short: "Add someone to a team",
		Long: "Both arguments take a name or an id: a team name, and the subject the\n" +
			"person authenticates as. They must have logged in at least once, since\n" +
			"a user row is created on first sight and there is nothing to add before\n" +
			"that.\n\n" +
			"A subject is sent as-is and resolved by the brain, so adding somebody\n" +
			"you can name needs no permission to read the directory — only\n" +
			"membership:write in this team.\n\n" +
			"The role must name a row in the brain's `roles` table — it ships with\n" +
			"admin and member, and a deployment may add its own. An unknown one is\n" +
			"rejected by the database, so the brain answers 400 rather than storing\n" +
			"something nothing can grant.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Checked before anything else: it needs no network, and failing it
			// after a lookup spends a round trip to reject something knowable
			// from the argument list.
			named := strings.TrimSpace(role)
			if named == "" {
				return fmt.Errorf("--role cannot be empty — a member without a role can do nothing")
			}

			who := strings.TrimSpace(args[1])
			if who == "" {
				return fmt.Errorf("name somebody to add, by subject or user id")
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			body := client.AddTeamMemberJSONRequestBody{Role: named}
			if id, err := uuid.Parse(who); err == nil {
				body.UserID = &id
			} else {
				body.Subject = &who
			}

			if err := cli.NoContent(
				api.AddTeamMemberWithResponse(cmd.Context(), team, body)); err != nil {
				return err
			}

			return opts.Out.Print(
				membershipView{Team: team, User: who, Role: named, Member: true},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("added"),
							opts.Styles.Value.Render(who),
							opts.Styles.Muted.Render("as "+named))
					},
				})
		},
	}

	cmd.Flags().StringVar(&role, "role", "", "the role to grant in this team, for example member")
	_ = cmd.MarkFlagRequired("role")

	return cmd
}

func newMembersRemoveCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <team> <user>",
		Aliases: []string{"rm"},
		Short:   "Take someone out of a team",
		Long: "Both arguments take a name or an id. A subject is looked up among the\n" +
			"team's own members rather than in the directory, so you can only name\n" +
			"somebody you can already see.\n\n" +
			"Removing someone who is not a member succeeds: the caller asked for a\n" +
			"state and that state holds, which is what makes this safe in a script\n" +
			"that runs twice.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			user, err := cli.ResolveMember(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			if err := cli.NoContent(api.RemoveTeamMemberWithResponse(cmd.Context(), team, user)); err != nil {
				return err
			}

			return opts.Out.Print(
				membershipView{Team: team, User: user.String(), Member: false},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("removed"),
							opts.Styles.Value.Render(user.String()))
					},
				})
		},
	}
}
