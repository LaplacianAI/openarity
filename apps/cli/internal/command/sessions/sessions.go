package sessions

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const stampLayout = "2006-01-02 15:04"

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "The conversations a team is having",
		Long: "A session is one conversation: a Slack thread, a WhatsApp number, a\n" +
			"support ticket a webhook is posting about. Messages hang off it, and\n" +
			"in time so will the workspace an agent works in.\n\n" +
			"It belongs to a team rather than to a channel, because a channel is\n" +
			"only one way a conversation starts. Being in the team is the whole\n" +
			"qualification to read one — an admin reading everybody's messages on\n" +
			"their behalf is not a permission that should exist.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newReadCmd(opts),
	)
	return cmd
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var (
		paging  cli.Paging
		channel string
	)

	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List a team's conversations",
		Long: "Most recently spoken in first, so the one you are looking for is\n" +
			"usually on the first page.\n\n" +
			"--channel narrows it to the ones that arrived on a single channel.\n" +
			"Without it you also see the sessions that arrived on none: a session\n" +
			"started from the dashboard or the API has no webhook behind it, and\n" +
			"shows no ref.",
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
			page, err := listSessions(cmd.Context(), api, team, channel, limit, cursor)
			if err != nil {
				return err
			}

			more := "oa sessions list " + args[0]
			if channel != "" {
				more += " --channel " + channel
			}

			return cli.PrintPage(opts, cli.Page[client.Session]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no conversations yet",
				More:       more,
				Row: func(table *printer.Table, s client.Session) {
					table.Row(
						opts.Styles.Value.Render(s.ID.String()),
						string(s.Kind),
						opts.Styles.Muted.Render(ref(s.ProviderRef)),
						opts.Styles.Muted.Render(s.LastMessageAt.Format(stampLayout)),
					)
				},
			})
		},
	}

	paging.Flags(cmd)
	cmd.Flags().StringVar(&channel, "channel", "",
		"only the conversations that arrived on this channel, by name or id")
	return cmd
}

func listSessions(
	ctx context.Context, api *client.ClientWithResponses,
	team uuid.UUID, channel string, limit *int32, cursor *string,
) (*client.SessionPage, error) {
	if channel == "" {
		return cli.Result(api.ListSessionsWithResponse(ctx, team,
			&client.ListSessionsParams{Limit: limit, Cursor: cursor}))
	}

	id, err := cli.ResolveChannel(ctx, api, team, channel)
	if err != nil {
		return nil, err
	}

	return cli.Result(api.ListChannelSessionsWithResponse(ctx, team, id,
		&client.ListChannelSessionsParams{Limit: limit, Cursor: cursor}))
}

func ref(provider *string) string {
	if provider == nil {
		return "—"
	}
	return strconv.QuoteToGraphic(*provider)
}

func newReadCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:   "read <team> <session>",
		Short: "Read a conversation",
		Long: "Newest first, ordered by when each message arrived and never by when\n" +
			"its sender says they sent it. That clock belongs to whoever is typing,\n" +
			"and somebody setting it deliberately would otherwise reorder a\n" +
			"conversation they do not own.\n\n" +
			"Every message here came from a sender somebody approved. Anyone\n" +
			"else's was dropped before it was written, so this is not a filtered\n" +
			"view — it is everything that was ever stored.\n\n" +
			"The text is quoted because a stranger chose it. Use -o json to get it\n" +
			"exactly as it arrived.",
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

			session, err := uuid.Parse(strings.TrimSpace(args[1]))
			if err != nil {
				return fmt.Errorf("%q is not a session id — `oa sessions list %s` shows them",
					args[1], args[0])
			}

			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListSessionMessagesWithResponse(cmd.Context(), team, session,
				&client.ListSessionMessagesParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.Message]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "nothing has been said here",
				More:       "oa sessions read " + args[0] + " " + args[1],
				Row: func(table *printer.Table, m client.Message) {
					table.Row(
						opts.Styles.Muted.Render(m.ReceivedAt.Format(stampLayout)),
						opts.Styles.Muted.Render(m.UserID.String()),
						opts.Styles.Value.Render(strconv.QuoteToGraphic(m.Text)),
					)
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}
