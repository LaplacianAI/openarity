package blueprints

import (
	"io"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

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
			res, err := api.ExportBlueprintWithResponse(cmd.Context(), team, params)
			bp, err := cli.Result(res, err)
			if err != nil {
				return err
			}
			if f, ok := output.Parse(opts.Settings.Output.Value); ok && f == output.JSON {
				return opts.Out.Print(bp, printer.Options{})
			}
			return writeYAML(opts.Stdout, res.Body)
		},
	}

	cmd.Flags().StringArrayVar(&agents, "agent", nil, "only this agent, by name; repeat for more")
	cmd.Flags().StringArrayVar(&servers, "mcp-server", nil, "only this MCP server, by name; repeat for more")
	return cmd
}

// writeYAML prints a blueprint as YAML in the order the brain wrote it, name
// first, rather than the generated type's alphabetical one: it is a file
// people read and edit. JSON is YAML, so the body parses as it is.
func writeYAML(w io.Writer, body []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return err
	}
	block(&doc)
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	return enc.Close()
}

// block drops the styles JSON came in, braces and quotes, so the encoder
// writes plain YAML, still quoting a string that would read as another type.
// A null field goes too: left out, it means the same and reads as less.
func block(n *yaml.Node) {
	n.Style = 0
	if n.Kind == yaml.MappingNode {
		kept := n.Content[:0]
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i+1].Tag != "!!null" {
				kept = append(kept, n.Content[i], n.Content[i+1])
			}
		}
		n.Content = kept
	}
	for _, c := range n.Content {
		block(c)
	}
}
