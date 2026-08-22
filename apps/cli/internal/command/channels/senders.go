package channels

import (
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const seenLayout = "2006-01-02 15:04"

func itoa(n int32) string { return strconv.FormatInt(int64(n), 10) }

func newSendersCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "senders",
		Short: "Who may speak to an agent through a channel",
		Long: "A channel's hook URL is public, so anyone who finds it can post to it.\n" +
			"Nothing they send is stored until somebody here says which user they\n" +
			"are: until then only their provider-side id and the name they chose\n" +
			"are recorded, and their messages are dropped.\n\n" +
			"All four need channel:write in the team — approving somebody lets them\n" +
			"instruct an agent as a named user, which is the same kind of act as\n" +
			"connecting the channel.",
	}

	cmd.AddCommand(
		newPendingCmd(opts),
		newSendersListCmd(opts),
		newApproveCmd(opts),
		newRemoveSenderCmd(opts),
	)
	return cmd
}

func channelOf(
	cmd *cobra.Command, opts *cli.Options, teamRef, channelRef string,
) (*client.ClientWithResponses, uuid.UUID, uuid.UUID, error) {
	api, err := opts.API(cmd.Context())
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}

	team, err := cli.ResolveTeam(cmd.Context(), api, teamRef)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}

	channel, err := cli.ResolveChannel(cmd.Context(), api, team, channelRef)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}

	return api, team, channel, nil
}

func newPendingCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:   "pending <team> <channel>",
		Short: "List senders waiting to be approved",
		Long: "Everyone who has posted to this channel's hook and could not be\n" +
			"matched to a user. The queue holds at most fifty per channel, so a\n" +
			"flood cannot bury the person you are waiting for.\n\n" +
			"The name is whatever they typed into their profile, and two people\n" +
			"can share one. The ref is what approval actually matches on.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, channel, err := channelOf(cmd, opts, args[0], args[1])
			if err != nil {
				return err
			}

			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListPendingSendersWithResponse(cmd.Context(), team, channel,
				&client.ListPendingSendersParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.PendingSender]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "nobody is waiting",
				More:       "oa channels senders pending " + args[0] + " " + args[1],
				Row: func(table *printer.Table, s client.PendingSender) {
					table.Row(
						opts.Styles.Value.Render(s.SenderRef),
						s.SenderName,
						opts.Styles.Muted.Render(itoa(s.SeenCount)),
						opts.Styles.Muted.Render(s.LastSeen.Format(seenLayout)),
					)
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}

func newSendersListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list <team> <channel>",
		Aliases: []string{"ls"},
		Short:   "List approved senders",
		Long: "Who may speak through this channel, and as whom. The user id is the\n" +
			"account their messages are attributed to — `oa teams members list`\n" +
			"turns it back into a person.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, channel, err := channelOf(cmd, opts, args[0], args[1])
			if err != nil {
				return err
			}

			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListChannelSendersWithResponse(cmd.Context(), team, channel,
				&client.ListChannelSendersParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.ChannelSender]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "nobody is approved",
				More:       "oa channels senders list " + args[0] + " " + args[1],
				Row: func(table *printer.Table, s client.ChannelSender) {
					table.Row(
						opts.Styles.Value.Render(s.SenderRef),
						opts.Styles.Muted.Render(s.UserID.String()),
						opts.Styles.Muted.Render(s.CreatedAt.Format(seenLayout)),
					)
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}

func newApproveCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "approve <team> <channel> <sender-ref> <user>",
		Short: "Let a sender speak as a user",
		Long: "The ref comes from `oa channels senders pending`, copied exactly —\n" +
			"it is the provider's id, not the display name, because two people\n" +
			"can share a name and only one of them is the one you meant.\n\n" +
			"The user is a subject or an id, and must already be in the team.\n" +
			"Approving somebody already approved moves them, so a mistake is one\n" +
			"command to correct rather than a remove and an add.",
		Args: cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, channel, err := channelOf(cmd, opts, args[0], args[1])
			if err != nil {
				return err
			}

			ref := strings.TrimSpace(args[2])
			if ref == "" {
				return errors.New("name a sender by the ref shown in `oa channels senders pending`")
			}

			user, err := cli.ResolveMember(cmd.Context(), api, team, args[3])
			if err != nil {
				return err
			}

			if err := cli.NoContent(api.ApproveChannelSenderWithResponse(
				cmd.Context(), team, channel, client.ApproveChannelSenderJSONRequestBody{
					SenderRef: ref,
					UserID:    user,
				})); err != nil {
				return err
			}

			return opts.Out.Print(
				client.ChannelSender{SenderRef: ref, UserID: user},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("approved"),
							opts.Styles.Value.Render(ref),
							opts.Styles.Muted.Render(user.String()))
					},
				})
		},
	}
}

func newRemoveSenderCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <team> <channel> <sender-ref>",
		Aliases: []string{"rm"},
		Short:   "Take a sender's access away, or dismiss a pending one",
		Long: "One command for both, because the ref is all you have and it does not\n" +
			"say which list it is in.\n\n" +
			"Not a block. Their next message puts them back in the pending queue,\n" +
			"which is what makes a mistake recoverable — and what makes this alone\n" +
			"no defence against somebody persistent.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, team, channel, err := channelOf(cmd, opts, args[0], args[1])
			if err != nil {
				return err
			}

			ref := strings.TrimSpace(args[2])
			if ref == "" {
				return errors.New("name a sender by its ref")
			}

			if err := cli.NoContent(api.RemoveChannelSenderWithResponse(
				cmd.Context(), team, channel,
				&client.RemoveChannelSenderParams{Ref: ref})); err != nil {
				return err
			}

			return opts.Out.Print(
				client.ChannelSender{SenderRef: ref},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("removed"),
							opts.Styles.Value.Render(ref))
					},
				})
		},
	}
}
