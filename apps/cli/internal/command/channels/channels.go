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
		newDeleteCmd(opts),
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

			return cli.PrintPage(opts, cli.Page[client.Channel]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no channels",
				More:       "oa channels list " + args[0],
				Row: func(table *printer.Table, ch client.Channel) {
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
	var provider string
	var secretStdin bool

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
	_ = cmd.MarkFlagRequired("provider")

	return cmd
}

func readSecret(in io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(in, maxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("read the signing secret from stdin: %w", err)
	}
	if len(raw) > maxSecretBytes {
		return "", fmt.Errorf("the signing secret on stdin is over %d bytes — is that the right pipe?", maxSecretBytes)
	}

	secret := strings.TrimRight(string(raw), "\r\n")
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("nothing arrived on stdin — pipe the secret in, or drop --secret-stdin to have one generated")
	}
	return secret, nil
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
