package mcpservers

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

// maxSecretValue is the brain's limit on one value.
const maxSecretValue = 16 << 10

func newSecretCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "secret <team> <name> <key>",
		Short: "Write one of the team's MCP secrets, read from stdin",
		Long: "Writes teams/<team_id>/mcp/<name> in the secret store with <key> set to\n" +
			"what arrives on stdin, so a server names it as <name>#<key>. The value\n" +
			"is never an argument, which would leave it in your shell history, and\n" +
			"is never printed or sent back. A write replaces the keys the secret\n" +
			"held. Needs tool:write.\n\n" +
			"    printf %s \"$STRIPE_KEY\" | oa mcp-servers secret support stripe token\n" +
			"    oa mcp-servers replace support stripe --url https://… --auth-secret stripe#token",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, key := strings.TrimSpace(args[1]), strings.TrimSpace(args[2])
			value, err := readValue(cmd.InOrStdin())
			if err != nil {
				return err
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			body := client.PutMCPSecretJSONRequestBody{Values: map[string]string{key: value}}
			if err := cli.NoContent(api.PutMCPSecretWithResponse(cmd.Context(), team, name, body)); err != nil {
				return cli.Needs(err, writePermission)
			}
			opts.Out.Note(opts.Styles.OK.Render("written") + " " + cli.Line(name) + "#" + cli.Line(key) +
				opts.Styles.Muted.Render(" — name it in a server as "+cli.Line(name)+"#"+cli.Line(key)))
			return nil
		},
	}
}

func readValue(in io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(in, maxSecretValue+1))
	if err != nil {
		return "", fmt.Errorf("read the value from stdin: %w", err)
	}
	if len(raw) > maxSecretValue {
		return "", fmt.Errorf("the value on stdin is over %d KiB — is that the right pipe?", maxSecretValue>>10)
	}
	value := strings.TrimRight(string(raw), "\r\n")
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("nothing arrived on stdin — pipe the value in, for example printf %%s \"$TOKEN\" | oa mcp-servers secret …")
	}
	return value, nil
}
