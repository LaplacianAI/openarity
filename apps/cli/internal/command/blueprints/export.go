package blueprints

import (
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func NewExport(opts *cli.Options) *cobra.Command {
	var agents, servers []string

	cmd := &cobra.Command{
		Use:   "export <team>",
		Short: "Print a team as a blueprint, to edit and plan",
		Long: "Prints the team's MCP servers and custom agents as a blueprint that\n" +
			"oa plan and oa apply take: grants by name, secret references as name#key.\n" +
			"Planning it unchanged says nothing would change, so an edit starts from\n" +
			"each item whole and changes only what was edited. Skills are left out,\n" +
			"since a skill is its files and one the file does not name is left alone;\n" +
			"agents still grant them by name. Built-in agents are never in it.\n\n" +
			"YAML, or JSON with -o json.\n\n" +
			"    oa export support > team.yaml\n" +
			"    oa export support --agent triage --mcp-server helpdesk",
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

			params := &client.ExportBlueprintParams{}
			if len(agents) > 0 {
				params.Agent = &agents
			}
			if len(servers) > 0 {
				params.McpServer = &servers
			}
			bp, err := cli.Result(api.ExportBlueprintWithResponse(cmd.Context(), team, params))
			if err != nil {
				return err
			}

			// A blueprint is a file, never a table.
			out := opts.Out
			if isTable(opts) {
				out = printer.New(opts.Stdout, output.YAML)
			}
			return out.Print(bp, printer.Options{})
		},
	}

	cmd.Flags().StringArrayVar(&agents, "agent", nil, "only this agent, by name; repeat for more")
	cmd.Flags().StringArrayVar(&servers, "mcp-server", nil, "only this MCP server, by name; repeat for more")
	return cmd
}
