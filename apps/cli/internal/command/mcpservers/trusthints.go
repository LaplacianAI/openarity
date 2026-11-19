package mcpservers

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

func newTrustHintsCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "trust-hints <team> <server> on|off",
		Short: "Believe a server's own read-only and destructive hints when asking for approval",
		Long: "A server may describe each tool as read-only or destructive. By default\n" +
			"the brain does not take its word when it decides whether a call needs\n" +
			"approval; with trust on it does, so a tool the server calls read-only\n" +
			"goes past approvals. Turn it on only for a server you run or trust.\n" +
			"Needs team:settings.\n\n" +
			"    oa mcp-servers trust-hints platform github on",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			var trust bool
			switch strings.ToLower(strings.TrimSpace(args[2])) {
			case "on":
				trust = true
			case "off":
			default:
				return fmt.Errorf("say on or off, not %q", args[2])
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			id, err := cli.ResolveMCPServer(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			srv, err := cli.Result(api.SetMCPServerTrustHintsWithResponse(cmd.Context(), team, id,
				client.SetMCPServerTrustHintsJSONRequestBody{Trust: trust}))
			if err != nil {
				return cli.Needs(err, settingsPermission)
			}
			if err := printServer(opts, srv, "trust hints "+args[2]); err != nil {
				return err
			}
			if trust {
				opts.Out.Note(opts.Styles.Warn.Render(
					"a server's own read-only hints can now let its tools past approvals"))
			}
			return nil
		},
	}
}
