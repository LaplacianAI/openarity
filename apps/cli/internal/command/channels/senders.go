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
			"Listing needs channel:read in the team; approving and removing need\n" +
			"sender:approve, because approving somebody lets them instruct an agent\n" +
			"as a named user. Neither lets you change the channel itself.",
	}

	cmd.AddCommand(
		newPendingCmd(opts),
		newSendersListCmd(opts),
		newApproveCmd(opts),
		newUpdateSenderCmd(opts),
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
						opts.Styles.Value.Render(strconv.QuoteToGraphic(s.SenderRef)),
						strconv.QuoteToGraphic(s.SenderName),
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
						opts.Styles.Value.Render(strconv.QuoteToGraphic(s.SenderRef)),
						cli.Safe(deref(s.Name)),
						opts.Styles.Value.Render(deref(s.Role)),
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
	var someoneNew bool
	var role, name string

	cmd := &cobra.Command{
		Use:   "approve <team> <channel> <sender-ref> [user]",
		Short: "Let a sender speak as a user, or as someone new",
		Long: "The ref comes from `oa channels senders pending`, copied exactly \u2014\n" +
			"it is the provider's id, not the display name, because two people\n" +
			"can share a name and only one of them is the one you meant.\n\n" +
			"The user is a subject or an id, and must already be in the team.\n" +
			"Approving somebody already approved moves them, so a mistake is one\n" +
			"command to correct rather than a remove and an add.\n\n" +
			"With --new and no user, the sender is let in as someone new: a user\n" +
			"of their own, who never logs in, with --role (guest unless you say)\n" +
			"and --name (the name they gave while waiting unless you say).",
		Example: "  oa channels senders approve platform mine 919876543210 ann@example.com\n" +
			"  oa channels senders approve platform mine 919876543210 --new --name Ravi",
		Args: cobra.RangeArgs(3, 4),
		RunE: func(cmd *cobra.Command, args []string) error {
			if someoneNew == (len(args) == 4) {
				return errors.New("name a user, or give --new to let the sender in as someone new; not both")
			}
			api, team, channel, err := channelOf(cmd, opts, args[0], args[1])
			if err != nil {
				return err
			}

			ref := strings.TrimSpace(args[2])
			if ref == "" {
				return errors.New("name a sender by the ref shown in `oa channels senders pending`")
			}

			body := client.ApproveChannelSenderJSONRequestBody{SenderRef: ref}
			shown := ""
			if someoneNew {
				body.New = &client.NewSender{Role: role}
				if name != "" {
					body.New.Name = &name
				}
				shown = "as a new " + role
			} else {
				user, err := cli.ResolveMember(cmd.Context(), api, team, args[3])
				if err != nil {
					return err
				}
				body.UserID = &user
				shown = user.String()
			}

			if err := cli.NoContent(api.ApproveChannelSenderWithResponse(cmd.Context(), team, channel, body)); err != nil {
				return cli.Needs(err, "sender:approve")
			}

			view := client.ChannelSender{SenderRef: ref}
			if body.UserID != nil {
				view.UserID = *body.UserID
			}
			return opts.Out.Print(view, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("approved"),
						opts.Styles.Value.Render(strconv.QuoteToGraphic(ref)),
						opts.Styles.Muted.Render(shown))
				},
			})
		},
	}

	cmd.Flags().BoolVar(&someoneNew, "new", false, "let the sender in as someone new, with a user of their own")
	cmd.Flags().StringVar(&role, "role", "guest", "with --new, the role they are given")
	cmd.Flags().StringVar(&name, "name", "", "with --new, their name")
	return cmd
}

func newUpdateSenderCmd(opts *cli.Options) *cobra.Command {
	var user, role, name string

	cmd := &cobra.Command{
		Use:   "update <team> <channel> <sender-ref>",
		Short: "Change who an approved sender speaks as, their role, or their name",
		Long: "--user moves the sender to another member, checked as approving is.\n" +
			"--role and --name change the user the sender speaks as, in place:\n" +
			"that needs membership:write, and the team's last admin keeps their\n" +
			"role. A name is a channel user's only, one let in with --new.\n\n" +
			"Moving a sender off a channel user they were the only sender for\n" +
			"deletes that user, and anything they own.",
		Example: "  oa channels senders update platform mine 919876543210 --role member\n" +
			"  oa channels senders update platform mine 919876543210 --user ann@example.com",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (user != "") == (role != "" || name != "") {
				return errors.New("give --user, or --role and --name; not both")
			}
			api, team, channel, err := channelOf(cmd, opts, args[0], args[1])
			if err != nil {
				return err
			}
			ref := strings.TrimSpace(args[2])
			if ref == "" {
				return errors.New("name a sender by its ref")
			}

			if user != "" {
				id, err := cli.ResolveMember(cmd.Context(), api, team, user)
				if err != nil {
					return err
				}
				if err := cli.NoContent(api.ApproveChannelSenderWithResponse(cmd.Context(), team, channel,
					client.ApproveChannelSenderJSONRequestBody{SenderRef: ref, UserID: &id})); err != nil {
					return cli.Needs(err, "sender:approve")
				}
				return printUpdated(opts, ref, "now speaks as "+id.String())
			}

			speaker, err := speakerOf(cmd, api, team, channel, ref)
			if err != nil {
				return err
			}
			change := client.UpdateTeamMemberJSONRequestBody{}
			if role != "" {
				change.Role = &role
			}
			if name != "" {
				change.Name = &name
			}
			if err := cli.NoContent(api.UpdateTeamMemberWithResponse(cmd.Context(), team, speaker, change)); err != nil {
				return cli.Needs(err, "membership:write")
			}
			return printUpdated(opts, ref, "updated")
		},
	}

	cmd.Flags().StringVar(&user, "user", "", "the member the sender speaks as from now on: a subject or an id")
	cmd.Flags().StringVar(&role, "role", "", "the role of the user the sender speaks as")
	cmd.Flags().StringVar(&name, "name", "", "the name of the channel user the sender speaks as")
	return cmd
}

func printUpdated(opts *cli.Options, ref, what string) error {
	return opts.Out.Print(client.ChannelSender{SenderRef: ref}, printer.Options{
		Table: func(table *printer.Table) {
			table.Row(opts.Styles.OK.Render("updated"),
				opts.Styles.Value.Render(strconv.QuoteToGraphic(ref)),
				opts.Styles.Muted.Render(what))
		},
	})
}

// speakerOf is the user an approved sender speaks as, read from the
// channel's approved list a page at a time.
func speakerOf(
	cmd *cobra.Command, api *client.ClientWithResponses, team, channel uuid.UUID, ref string,
) (uuid.UUID, error) {
	limit := int32(100)
	var cursor *string
	for range 50 {
		page, err := cli.Result(api.ListChannelSendersWithResponse(cmd.Context(), team, channel,
			&client.ListChannelSendersParams{Limit: &limit, Cursor: cursor}))
		if err != nil {
			return uuid.Nil, err
		}
		for _, s := range page.Items {
			if s.SenderRef == ref {
				return s.UserID, nil
			}
		}
		if page.NextCursor == nil {
			return uuid.Nil, errors.New("no approved sender has that ref; `oa channels senders list` shows them")
		}
		cursor = page.NextCursor
	}
	return uuid.Nil, errors.New("too many approved senders to search; give the member to `oa teams members update`")
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
							opts.Styles.Value.Render(strconv.QuoteToGraphic(ref)))
					},
				})
		},
	}
}
