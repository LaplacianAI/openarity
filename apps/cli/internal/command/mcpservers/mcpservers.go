package mcpservers

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const writePermission = "tool:write"

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mcp-servers",
		Aliases: []string{"mcp"},
		Short:   "Register the MCP servers a team's agents can be granted, and list their tools",
		Long: "A server is a URL spoken to over streamable HTTP, or a command started\n" +
			"and spoken to over stdio. Its credentials are references into the\n" +
			"secret store, teams/<team_id>/mcp/<name>#<key>, never the secret: the\n" +
			"team writes the secret there and the brain reads it when it connects.\n\n" +
			"Seeing a team's servers and their tools needs only to be in the team.\n" +
			"Registering, replacing, discovering or deleting one needs tool:write.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newGetCmd(opts),
		newCreateCmd(opts),
		newReplaceCmd(opts),
		newDeleteCmd(opts),
		newToolsCmd(opts),
		newDiscoverCmd(opts),
	)
	return cmd
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List a team's MCP servers",
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
			page, err := cli.Result(api.ListMCPServersWithResponse(cmd.Context(), team,
				&client.ListMCPServersParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.MCPServer]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no MCP servers",
				More:       "oa mcp-servers list " + args[0],
				Row: func(table *printer.Table, s client.MCPServer) {
					table.Row(opts.Styles.Value.Render(s.Name), transport(s), discovered(s),
						opts.Styles.Muted.Render(s.ID.String()))
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}

func newGetCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <team> <server>",
		Short: "Show an MCP server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			srv, err := cli.Result(api.GetMCPServerWithResponse(cmd.Context(), team, id))
			if err != nil {
				return err
			}
			return printServer(opts, srv, "")
		},
	}
}

// definition is a server as the command line describes one: the URL or the
// command after --, and the references that go with each.
type definition struct {
	url     string
	authRef string
	env     []string
	bare    bool
}

func (d *definition) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&d.url, "url", "", "a remote server, spoken to over streamable HTTP")
	cmd.Flags().StringVar(&d.authRef, "auth-secret-ref", "",
		"with --url: the bearer token's reference, teams/<team_id>/mcp/<name>#<key>")
	cmd.Flags().StringArrayVar(&d.env, "env", nil,
		"with a command: NAME=teams/<team_id>/mcp/<name>#<key>, once per variable")
	cmd.Flags().BoolVar(&d.bare, "bare", false,
		"tools reach the model as <tool> rather than <server>__<tool>")
}

// request builds the body. argv is what followed --, and it is the command;
// the brain says which combinations it refuses, and this only refuses what
// cannot be expressed at all.
func (d *definition) request(name string, argv []string) (client.MCPServerRequest, error) {
	body := client.MCPServerRequest{Name: strings.TrimSpace(name)}
	if body.Name == "" {
		return body, errors.New("a server needs a name")
	}
	if d.bare {
		body.Bare = &d.bare
	}

	switch {
	case d.url != "" && len(argv) > 0:
		return body, errors.New("give --url or a command after --, not both")
	case d.url != "":
		body.URL = &d.url
		if d.authRef != "" {
			body.AuthSecretRef = &d.authRef
		}
		if len(d.env) > 0 {
			return body, errors.New("--env sets variables for a command server; a url server takes --auth-secret-ref")
		}
	case len(argv) > 0:
		body.Command = &argv
		if d.authRef != "" {
			return body, errors.New("--auth-secret-ref is for a url server; a command server takes --env")
		}
		env := map[string]string{}
		for _, kv := range d.env {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return body, fmt.Errorf("--env %q is not NAME=reference", kv)
			}
			env[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		body.Env = &env
	default:
		return body, errors.New("give --url, or the command to start after --")
	}
	return body, nil
}

func newCreateCmd(opts *cli.Options) *cobra.Command {
	var def definition

	cmd := &cobra.Command{
		Use:   "create <team> <name> [-- command args...]",
		Short: "Register an MCP server",
		Long: "Needs tool:write in the team. Its tools are not known until it is\n" +
			"discovered, which `oa mcp-servers discover` does for a url server.\n\n" +
			"    oa mcp-servers create platform github --url https://api.githubcopilot.com/mcp/ \\\n" +
			"        --auth-secret-ref teams/<team_id>/mcp/github#token\n" +
			"    oa mcp-servers create platform files -- npx -y @modelcontextprotocol/server-filesystem /data",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := def.request(args[1], args[2:])
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

			srv, err := cli.Created(api.CreateMCPServerWithResponse(cmd.Context(), team, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printServer(opts, srv, "registered")
		},
	}

	def.flags(cmd)
	return cmd
}

func newReplaceCmd(opts *cli.Options) *cobra.Command {
	var def definition

	cmd := &cobra.Command{
		Use:   "replace <team> <server> <name> [-- command args...]",
		Short: "Replace an MCP server's definition",
		Long: "Needs tool:write in the team. A replace is whole. A server whose\n" +
			"transport changes is discovered again, because its old tools describe\n" +
			"a different server.",
		Args: cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := def.request(args[2], args[3:])
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
			id, err := cli.ResolveMCPServer(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			srv, err := cli.Result(api.UpdateMCPServerWithResponse(cmd.Context(), team, id, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printServer(opts, srv, "replaced")
		},
	}

	def.flags(cmd)
	return cmd
}

func newDeleteCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <team> <server>",
		Aliases: []string{"rm"},
		Short:   "Delete an MCP server and its tools",
		Long: "Needs tool:write in the team. A server an agent is granted is not\n" +
			"deleted: revoke the grant first. The secrets it referred to stay in the\n" +
			"secret store; they are the team's to remove.",
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
			id, err := cli.ResolveMCPServer(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			if err := cli.NoContent(api.DeleteMCPServerWithResponse(cmd.Context(), team, id)); err != nil {
				return cli.Needs(err, writePermission)
			}

			return opts.Out.Print(
				client.MCPServer{ID: id, TeamID: team, Name: args[1], Env: map[string]string{}},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("deleted"),
							opts.Styles.Value.Render(args[1]),
							opts.Styles.Muted.Render(id.String()))
					},
				})
		},
	}
}

func newToolsCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "tools <team> <server>",
		Short: "List the tools an MCP server offered when it was last discovered",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			list, err := cli.Result(api.ListMCPToolsWithResponse(cmd.Context(), team, id))
			if err != nil {
				return err
			}
			return printTools(opts, list)
		},
	}
}

func newDiscoverCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "discover <team> <server>",
		Short: "Ask an MCP server for its tools now, and record them",
		Long: "Needs tool:write in the team. The brain connects, from where it runs,\n" +
			"and only to a public address or a range its operator allowed. A command\n" +
			"server is not started by the brain to discover it.",
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
			id, err := cli.ResolveMCPServer(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			list, err := cli.Result(api.DiscoverMCPServerWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printTools(opts, list)
		},
	}
}

func printTools(opts *cli.Options, list *client.MCPToolList) error {
	if list.Items == nil {
		list.Items = make([]client.MCPTool, 0)
	}
	if len(list.Items) == 0 {
		opts.Out.Note(opts.Styles.Muted.Render("no tools — has the server been discovered?"))
	}
	return opts.Out.Print(list, printer.Options{
		Table: func(table *printer.Table) {
			for _, tool := range list.Items {
				// A tool's name and description are the server's words, not the
				// team's, so they are quoted before they reach a terminal.
				table.Row(opts.Styles.Value.Render(strconv.QuoteToGraphic(tool.Name)),
					strconv.QuoteToGraphic(oneLine(tool.Description)))
			}
		},
	})
}

func printServer(opts *cli.Options, s *client.MCPServer, did string) error {
	return opts.Out.Print(s, printer.Options{
		Table: func(table *printer.Table) {
			head := transport(*s)
			if did != "" {
				head = opts.Styles.OK.Render(did)
			}
			table.Row(head, opts.Styles.Value.Render(s.Name), opts.Styles.Muted.Render(s.ID.String()))
			if s.URL != nil {
				table.Row("url", *s.URL, "")
			}
			if s.AuthSecretRef != nil {
				table.Row("auth", *s.AuthSecretRef, "")
			}
			if s.Command != nil {
				table.Row("command", strings.Join(*s.Command, " "), "")
			}
			for k, v := range s.Env {
				table.Row("env", k, v)
			}
			table.Row("discovered", discovered(*s), "")
		},
	})
}

func transport(s client.MCPServer) string {
	if s.Command != nil {
		return "command"
	}
	return "url"
}

func discovered(s client.MCPServer) string {
	if s.DiscoveredAt == nil {
		return "never"
	}
	return s.DiscoveredAt.UTC().Format("2006-01-02 15:04")
}

// oneLine keeps a description to one row: a tool's description is the
// server's text, and some run to paragraphs.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const width = 80
	if r := []rune(s); len(r) > width {
		return string(r[:width-1]) + "…"
	}
	return s
}
