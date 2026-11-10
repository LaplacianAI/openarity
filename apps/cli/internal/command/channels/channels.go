package channels

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const maxSecretBytes = 4096

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "Connect the places a team's messages arrive from",
		Long: "A channel is one provider instance — a Slack workspace, a support tool\n" +
			"posting to a webhook. Its id is the routing key in its own hook URL, and\n" +
			"its signing secret is what proves a request really came from it.\n\n" +
			"Seeing a team's channels needs only to be in the team. Connecting or\n" +
			"disconnecting one needs channel:write.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newCreateCmd(opts),
		newUpdateCmd(opts),
		newDeleteCmd(opts),
		newSendersCmd(opts),
	)
	return cmd
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List a team's channels",
		Long: "The team is a name or an id. One page per call; when more remain the\n" +
			"response carries a cursor, which --cursor takes back.\n\n" +
			"No signing secret is shown, here or anywhere else. The brain never\n" +
			"returns one after the moment it was created.",
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
			page, err := cli.Result(api.ListChannelsWithResponse(cmd.Context(), team,
				&client.ListChannelsParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.ListedChannel]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no channels",
				More:       "oa channels list " + args[0],
				Row: func(table *printer.Table, ch client.ListedChannel) {
					table.Row(opts.Styles.Value.Render(ch.Name), ch.Provider,
						opts.Styles.Muted.Render(ch.ID.String()))
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}

func newCreateCmd(opts *cli.Options) *cobra.Command {
	var provider, replyURL string
	var secretStdin bool
	var credentialStdin string

	cmd := &cobra.Command{
		Use:   "create <team> <name>",
		Short: "Connect a channel",
		Long: "Needs channel:write in the team. The name is unique within it, and is\n" +
			"case-insensitive: \"Support\" and \"support\" are the same channel.\n\n" +
			"By default the brain generates the signing secret and prints it once.\n" +
			"That is what you want for a custom integration, where the scheme is\n" +
			"ours and there is nothing to match.\n\n" +
			"When the provider issues its own — Slack's Signing Secret, Meta's App\n" +
			"Secret — pass --secret-stdin and pipe it in:\n\n" +
			"    printf %s \"$SLACK_SIGNING_SECRET\" | oa channels create platform slack \\\n" +
			"        --provider slack --secret-stdin\n\n" +
			"A provider that needs a credential of its own, such as Telegram's bot\n" +
			"token, takes it with --credential-stdin NAME, which reads that one\n" +
			"value from stdin:\n\n" +
			"    printf %s \"$BOT_TOKEN\" | oa channels create platform bot \\\n" +
			"        --provider telegram --credential-stdin bot_token\n\n" +
			"It cannot be combined with --secret-stdin: there is one stdin.\n\n" +
			"There is deliberately no --secret flag. Arguments are readable by\n" +
			"every process on the machine through ps, and they land in shell\n" +
			"history.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[1])
			if name == "" {
				return fmt.Errorf("a channel needs a name")
			}
			if strings.TrimSpace(provider) == "" {
				return fmt.Errorf("--provider cannot be empty — it says which adapter parses this channel")
			}

			body := client.CreateChannelJSONRequestBody{
				Provider: strings.TrimSpace(provider),
				Name:     name,
			}
			if cmd.Flags().Changed("reply-url") {
				u := strings.TrimSpace(replyURL)
				body.ReplyURL = &u
			}
			if secretStdin && cmd.Flags().Changed("credential-stdin") {
				return fmt.Errorf("--secret-stdin and --credential-stdin both read stdin — pass only one")
			}
			if cmd.Flags().Changed("credential-stdin") {
				credName := strings.TrimSpace(credentialStdin)
				if credName == "" {
					return fmt.Errorf("--credential-stdin needs the credential's name, for example bot_token")
				}
				value, err := readStdin(cmd.InOrStdin(), "the "+credName+" credential",
					"pipe it in, or drop --credential-stdin")
				if err != nil {
					return err
				}
				body.Credentials = &map[string]string{credName: value}
			}
			if secretStdin {
				secret, err := readSecret(cmd.InOrStdin())
				if err != nil {
					return err
				}
				body.SigningSecret = &secret
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			ch, err := cli.Created(api.CreateChannelWithResponse(cmd.Context(), team, body))
			if err != nil {
				return err
			}

			// The note goes to stderr, so `-o json > channel.json` stays a
			// document. The secret itself is part of the output, because a
			// table redirected to a file must still contain it.
			if ch.SigningSecret != nil {
				opts.Out.Note(opts.Styles.Muted.Render(
					"the signing secret is shown once and cannot be read back"))
			}

			switch {
			case ch.Connected:
				opts.Out.Note(cli.Safe(ch.Provider) + " now sends this channel's messages to the brain.")
			case ch.Provider == "telegram" && ch.HookURL != nil && ch.SigningSecret != nil:
				// The brain had no public https address to point the bot at.
				opts.Out.Note(telegramHint(*ch.HookURL, *ch.SigningSecret))
			}

			return opts.Out.Print(ch, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("connected"),
						opts.Styles.Value.Render(ch.Name),
						opts.Styles.Muted.Render(ch.Provider+" "+ch.ID.String()))
					if ch.SigningSecret != nil {
						table.Row(opts.Styles.OK.Render("secret"),
							opts.Styles.Value.Render(*ch.SigningSecret), "")
					}
				},
			})
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "",
		"which adapter parses this channel's webhooks, for example custom")
	cmd.Flags().BoolVar(&secretStdin, "secret-stdin", false,
		"read the provider's own signing secret from stdin instead of generating one")
	cmd.Flags().StringVar(&credentialStdin, "credential-stdin", "",
		"read the value of the named credential, such as bot_token, from stdin")
	cmd.Flags().StringVar(&replyURL, "reply-url", "",
		"where the team's replies to this channel's conversations are posted")
	_ = cmd.MarkFlagRequired("provider")

	return cmd
}

func newUpdateCmd(opts *cli.Options) *cobra.Command {
	var replyURL string
	var noReplyURL, notify, noNotify bool

	cmd := &cobra.Command{
		Use:   "update <team> <channel>",
		Short: "Change where a channel's replies go, or whether it tells strangers they are waiting",
		Long: "Both arguments take a name or an id. Needs channel:write in the team.\n" +
			"Change either or both; what you leave out stays as it is.\n\n" +
			"--reply-url sets where the team's replies are posted; --no-reply-url\n" +
			"stops them, and any reply still waiting is given up rather than sent\n" +
			"later. The URL must be http or https, with no credentials, query or\n" +
			"fragment: every member can read it, and the receiver checks a reply by\n" +
			"its signature instead.\n\n" +
			"--notify-strangers and --no-notify-strangers switch what a sender nobody\n" +
			"has let in is told: that they are waiting, once, and that they are in.\n" +
			"Telegram channels only.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			setURL := cmd.Flags().Changed("reply-url")
			if setURL && noReplyURL {
				return fmt.Errorf("pass one of --reply-url or --no-reply-url, not both")
			}
			if notify && noNotify {
				return fmt.Errorf("pass one of --notify-strangers or --no-notify-strangers, not both")
			}
			if !setURL && !noReplyURL && !notify && !noNotify {
				return fmt.Errorf("pass --reply-url, --no-reply-url, --notify-strangers or --no-notify-strangers")
			}
			body := client.UpdateChannelJSONRequestBody{}
			switch {
			case setURL:
				u := strings.TrimSpace(replyURL)
				if u == "" {
					return fmt.Errorf("--reply-url cannot be empty — use --no-reply-url to stop replies")
				}
				body.ReplyURL = &u
			case noReplyURL:
				none := ""
				body.ReplyURL = &none
			}
			if notify || noNotify {
				body.NotifyStrangers = &notify
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			channel, err := cli.ResolveChannel(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			ch, err := cli.Result(api.UpdateChannelWithResponse(cmd.Context(), team, channel, body))
			if err != nil {
				return cli.Needs(err, "channel:write")
			}

			return opts.Out.Print(ch, printer.Options{
				Table: func(table *printer.Table) {
					var what []string
					switch {
					case body.ReplyURL != nil && ch.ReplyURL == nil, body.ReplyURL != nil && *ch.ReplyURL == "":
						what = append(what, opts.Styles.Muted.Render("replies stopped"))
					case body.ReplyURL != nil:
						what = append(what, opts.Styles.Muted.Render("replies to ")+cli.Safe(*ch.ReplyURL))
					}
					if body.NotifyStrangers != nil {
						state := "off"
						if ch.NotifyStrangers {
							state = "on"
						}
						what = append(what, opts.Styles.Muted.Render("strangers are told they are waiting: "+state))
					}
					table.Row(opts.Styles.OK.Render("updated"), opts.Styles.Value.Render(ch.Name),
						strings.Join(what, opts.Styles.Muted.Render("; ")))
				},
			})
		},
	}

	cmd.Flags().StringVar(&replyURL, "reply-url", "", "where the team's replies are posted")
	cmd.Flags().BoolVar(&noReplyURL, "no-reply-url", false, "stop sending replies")
	cmd.Flags().BoolVar(&notify, "notify-strangers", false, "tell a sender nobody has let in that they are waiting, and that they are in")
	cmd.Flags().BoolVar(&noNotify, "no-notify-strangers", false, "stop telling them")
	return cmd
}

// telegramHint is the one step Telegram leaves to the person: pointing the bot
// at the channel. The token stays a placeholder; this process never echoes it.
func telegramHint(hookURL, secret string) string {
	return "Point the bot at this channel, replacing <bot token>:\n\n" +
		"  curl https://api.telegram.org/bot<bot token>/setWebhook \\\n" +
		"    -d url=" + hookURL + " \\\n" +
		"    -d secret_token=" + secret + " \\\n" +
		"    -d 'allowed_updates=[\"message\"]'"
}

func readSecret(in io.Reader) (string, error) {
	return readStdin(in, "the signing secret", "pipe the secret in, or drop --secret-stdin to have one generated")
}

func readStdin(in io.Reader, what, advice string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(in, maxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s from stdin: %w", what, err)
	}
	if len(raw) > maxSecretBytes {
		return "", fmt.Errorf("%s on stdin is over %d bytes — is that the right pipe?", what, maxSecretBytes)
	}

	value := strings.TrimRight(string(raw), "\r\n")
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("nothing arrived on stdin — %s", advice)
	}
	return value, nil
}

func newDeleteCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <team> <channel>",
		Aliases: []string{"rm"},
		Short:   "Disconnect a channel",
		Long: "Both arguments take a name or an id. Needs channel:write in the team.\n\n" +
			"The signing secret is deleted with the channel, so anything still\n" +
			"posting to its hook URL stops being able to. Reconnecting means a new\n" +
			"channel with a new id, and every sender approved on the old one is\n" +
			"gone with it.",
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
			channel, err := cli.ResolveChannel(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			if err := cli.NoContent(
				api.DeleteChannelWithResponse(cmd.Context(), team, channel)); err != nil {
				return err
			}

			return opts.Out.Print(
				client.Channel{ID: channel, TeamID: team, Name: args[1]},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("disconnected"),
							opts.Styles.Value.Render(args[1]),
							opts.Styles.Muted.Render(channel.String()))
					},
				})
		},
	}
}
