package users

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "Find the id of someone who has logged in",
		Long: "Only people who have logged in at least once appear here: a user row\n" +
			"is created on first sight, never synced from the identity provider.\n\n" +
			"Adding somebody to a team does not need this — `oa teams members add`\n" +
			"takes a subject and the brain resolves it. This is for the other\n" +
			"question: who is there at all.",
	}

	cmd.AddCommand(newListCmd(opts))
	return cmd
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list [subject]",
		Aliases: []string{"ls"},
		Short:   "List users, or find one by subject",
		Long: "With a subject it matches exactly. There is no prefix search on\n" +
			"purpose: a directory that answers \"who starts with s\" is one\n" +
			"somebody can walk.\n\n" +
			"Needs membership:write in some team, which in practice means being an\n" +
			"admin of one, or a super admin.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params := client.ListUsersParams{}
			params.Limit, params.Cursor = paging.Values()

			more := "oa users list"
			if len(args) == 1 {
				subject := strings.TrimSpace(args[0])
				if subject == "" {
					return errors.New("give a subject to look for, or no argument to list everybody")
				}
				params.Subject = &subject
				more += " " + subject
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			page, err := cli.Result(api.ListUsersWithResponse(cmd.Context(), &params))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.User]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "nobody — they have to run `oa login` once before they appear",
				More:       more,
				Row: func(table *printer.Table, user client.User) {
					email := opts.Styles.Muted.Render("no email")
					if user.Email != nil {
						email = string(*user.Email)
					}
					table.Row(opts.Styles.Value.Render(user.Subject),
						opts.Styles.Muted.Render(user.Issuer), email,
						opts.Styles.Muted.Render(user.ID.String()))
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}
