package agents

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	writePermission = "agent:write"
	maxDefinition   = 1 << 20
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Define the agents a team runs, and what they are granted",
		Long: "An agent is a model, a pattern for working through a task, instructions,\n" +
			"and the MCP servers and skills it is granted. Grants stay inside the team:\n" +
			"an agent cannot be given another team's server or skill.\n\n" +
			"Seeing a team's agents needs only to be in the team. Defining, replacing\n" +
			"or deleting one needs agent:write.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newGetCmd(opts),
		newCreateCmd(opts),
		newReplaceCmd(opts),
		newDeleteCmd(opts),
	)
	return cmd
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List a team's agents",
		Args:    cobra.ExactArgs(1),
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
			page, err := cli.Result(api.ListAgentsWithResponse(cmd.Context(), team,
				&client.ListAgentsParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.AgentSummary]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no agents",
				More:       "oa agents list " + args[0],
				Row: func(table *printer.Table, a client.AgentSummary) {
					table.Row(opts.Styles.Value.Render(cli.Safe(a.Name)), cli.Safe(a.Model.Name), cli.Safe(a.Pattern),
						opts.Styles.Muted.Render(a.ID.String()))
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}

func newGetCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <team> <agent>",
		Short: "Show an agent, with its instructions and its grants",
		Long: "Both arguments take a name or an id. `-o yaml` prints a definition that\n" +
			"`oa agents replace -f` takes back, after the fields the brain sets are\n" +
			"removed.",
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
			id, err := cli.ResolveAgent(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			agent, err := cli.Result(api.GetAgentWithResponse(cmd.Context(), team, id))
			if err != nil {
				return err
			}
			return printAgent(opts, agent, "")
		},
	}
}

// definition is how create and replace read an agent: a file for the body,
// and grants by name on the command line, because a definition naming
// servers and skills by id is a file nobody can read or write by hand. The
// file's delegates are names too, read out of it before the rest is decoded.
type definition struct {
	file      string
	servers   []string
	skills    []string
	delegates *[]string
}

func (d *definition) flags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&d.file, "file", "f", "",
		"the agent as YAML or JSON, or - for stdin; the fields are the API's AgentRequest")
	cmd.Flags().StringArrayVar(&d.servers, "mcp-server", nil,
		"grant an MCP server by name or id, every tool or only some: github, or github=search_code,create_issue")
	cmd.Flags().StringArrayVar(&d.skills, "skill", nil,
		"grant a skill by name or id")
	_ = cmd.MarkFlagRequired("file")
}

func (d *definition) read(in io.Reader) (client.AgentRequest, error) {
	var raw []byte
	var err error
	if d.file == "-" {
		raw, err = io.ReadAll(io.LimitReader(in, maxDefinition+1))
	} else {
		raw, err = os.ReadFile(d.file)
	}
	if err != nil {
		return client.AgentRequest{}, fmt.Errorf("read the agent: %w", err)
	}
	if len(raw) > maxDefinition {
		return client.AgentRequest{}, fmt.Errorf("the agent is over %d bytes — is that the right file?", maxDefinition)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return client.AgentRequest{}, fmt.Errorf("read the agent: %w", err)
	}
	var body client.AgentRequest
	if len(doc.Content) > 0 {
		root := doc.Content[0]
		if d.delegates, err = takeDelegates(root); err != nil {
			return client.AgentRequest{}, err
		}
		if err := root.Decode(&body); err != nil {
			return client.AgentRequest{}, fmt.Errorf("read the agent: %w", err)
		}
	}
	if strings.TrimSpace(body.Name) == "" {
		return client.AgentRequest{}, errors.New("the agent has no name")
	}
	return body, nil
}

// takeDelegates removes the delegates key from the definition and returns
// what it named, so names and ids alike are resolved rather than decoded as
// ids. Nil when the file does not say.
func takeDelegates(root *yaml.Node) (*[]string, error) {
	if root.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "delegates" {
			continue
		}
		var names []string
		if err := root.Content[i+1].Decode(&names); err != nil {
			return nil, errors.New("delegates is a list of the agents it may hand work to, by name or id")
		}
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
		return &names, nil
	}
	return nil, nil
}

// grant adds the named servers and skills to what the file granted. A grant
// already in the file is kept; naming the same thing twice is the brain's to
// refuse.
func (d *definition) grant(cmd *cobra.Command, api *client.ClientWithResponses, team uuid.UUID, body *client.AgentRequest) error {
	for _, ref := range d.servers {
		name, tools, limited := strings.Cut(ref, "=")
		id, err := cli.ResolveMCPServer(cmd.Context(), api, team, name)
		if err != nil {
			return err
		}

		g := client.MCPServerGrant{ID: id}
		if limited {
			allow := []string{}
			for tool := range strings.SplitSeq(tools, ",") {
				if tool = strings.TrimSpace(tool); tool != "" {
					allow = append(allow, tool)
				}
			}
			if len(allow) == 0 {
				return fmt.Errorf("%q names no tools after the = — leave it off to grant every tool", ref)
			}
			g.Allow = &allow
		}

		if body.McpServers == nil {
			body.McpServers = &[]client.MCPServerGrant{}
		}
		*body.McpServers = append(*body.McpServers, g)
	}

	for _, ref := range d.skills {
		id, err := cli.ResolveSkill(cmd.Context(), api, team, ref)
		if err != nil {
			return err
		}
		if body.Skills == nil {
			body.Skills = &[]uuid.UUID{}
		}
		*body.Skills = append(*body.Skills, id)
	}

	if d.delegates != nil {
		ids := make([]uuid.UUID, 0, len(*d.delegates))
		for _, ref := range *d.delegates {
			id, err := cli.ResolveAgent(cmd.Context(), api, team, ref)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		body.Delegates = &ids
	}
	return nil
}

func newCreateCmd(opts *cli.Options) *cobra.Command {
	var def definition

	cmd := &cobra.Command{
		Use:   "create <team>",
		Short: "Define an agent",
		Long: "Needs agent:write in the team. The agent and its grants are written in\n" +
			"one step, so it never exists without what it was meant to have.\n\n" +
			"    # triage.yaml\n" +
			"    name: triage\n" +
			"    # what it does and when to use it: this is how other agents find it\n" +
			"    description: Sorts incoming issues and labels them\n" +
			"    instructions: Read the issue. Label it. Say why.\n" +
			"    model: {name: anthropic/claude-sonnet-5-5, max_tokens: 1024}\n" +
			"    pattern: react\n" +
			"    max_steps: 5\n" +
			"    # whom it may hand work to: none (alone, the default), team, or listed\n" +
			"    delegation: listed\n" +
			"    delegates: [billing]   # by name or id; read only with listed\n" +
			"    # web_search and web_fetch; off unless set\n" +
			"    web: true\n\n" +
			"    oa agents create platform -f triage.yaml \\\n" +
			"        --mcp-server github=search_code --skill pdf-processing",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := def.read(cmd.InOrStdin())
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
			if err := def.grant(cmd, api, team, &body); err != nil {
				return err
			}

			agent, err := cli.Created(api.CreateAgentWithResponse(cmd.Context(), team, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printAgent(opts, agent, "created")
		},
	}

	def.flags(cmd)
	return cmd
}

func newReplaceCmd(opts *cli.Options) *cobra.Command {
	var def definition

	cmd := &cobra.Command{
		Use:   "replace <team> <agent>",
		Short: "Replace an agent's definition and grants",
		Long: "Needs agent:write in the team. A replace is whole: a grant the new\n" +
			"definition does not name is revoked, and a field it leaves out goes back\n" +
			"to its default. The agent keeps its id and its kind.\n\n" +
			"To change one thing, start from what is there, which keeps every grant:\n\n" +
			"    oa agents get platform triage -o yaml > triage.yaml\n" +
			"    # edit triage.yaml\n" +
			"    oa agents replace platform triage -f triage.yaml",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := def.read(cmd.InOrStdin())
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
			id, err := cli.ResolveAgent(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}
			if err := def.grant(cmd, api, team, &body); err != nil {
				return err
			}

			agent, err := cli.Result(api.ReplaceAgentWithResponse(cmd.Context(), team, id, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printAgent(opts, agent, "replaced")
		},
	}

	def.flags(cmd)
	return cmd
}

func newDeleteCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <team> <agent>",
		Aliases: []string{"rm"},
		Short:   "Delete an agent",
		Long: "Needs agent:write in the team. Its grants go with it; the servers and\n" +
			"skills it was granted stay.",
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
			id, err := cli.ResolveAgent(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			if err := cli.NoContent(api.DeleteAgentWithResponse(cmd.Context(), team, id)); err != nil {
				return cli.Needs(err, writePermission)
			}

			return opts.Out.Print(
				client.AgentSummary{ID: id, TeamID: team, Name: args[1]},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("deleted"),
							opts.Styles.Value.Render(cli.Safe(args[1])),
							opts.Styles.Muted.Render(id.String()))
					},
				})
		},
	}
}

func printAgent(opts *cli.Options, a *client.Agent, did string) error {
	return opts.Out.Print(a, printer.Options{
		Table: func(table *printer.Table) {
			head := cli.Safe(a.Kind)
			if did != "" {
				head = opts.Styles.OK.Render(did)
			}
			table.Row(head, opts.Styles.Value.Render(cli.Safe(a.Name)), opts.Styles.Muted.Render(a.ID.String()))
			table.Row("description", strconv.QuoteToGraphic(a.Description), "")
			table.Row("model", cli.Safe(a.Model.Name), strconv.Itoa(int(a.Model.MaxTokens))+" tokens")
			table.Row("pattern", cli.Safe(a.Pattern), strconv.Itoa(int(a.MaxSteps))+" steps")
			budget := "the default"
			if a.TokenBudget != nil {
				budget = strconv.FormatInt(*a.TokenBudget, 10) + " tokens"
			}
			table.Row("token budget", budget, "per run")
			for _, g := range a.McpServers {
				only := "every tool"
				if g.Allow != nil {
					only = cli.Safe(strings.Join(*g.Allow, ", "))
				}
				table.Row("mcp server", g.ID.String(), only)
			}
			for _, s := range a.Skills {
				table.Row("skill", s.String(), "")
			}
			table.Row("delegation", cli.Safe(string(a.Delegation)), delegationNote(a.Delegation))
			for _, id := range a.Delegates {
				table.Row("delegate", id.String(), "")
			}
			if a.Web {
				table.Row("web", "on", "web_search and web_fetch")
			} else {
				table.Row("web", "off", "")
			}
		},
	})
}

func delegationNote(d client.Delegation) string {
	switch d {
	case client.DelegationTeam:
		return "any other agent in the team"
	case client.DelegationListed:
		return "only its delegates"
	case client.DelegationNone:
		return "works alone"
	default:
		return ""
	}
}
