package mcpservers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

// signInLasts is how long the brain keeps a sign-in begun: past it, the
// browser's callback finds nothing, so waiting longer waits for nothing.
// pollEvery is how often connect asks whether it came back. Both are vars so
// a test does not wait minutes.
var (
	signInLasts = 10 * time.Minute
	pollEvery   = 2 * time.Second
)

func newConnectCmd(opts *cli.Options) *cobra.Command {
	var noWait bool

	cmd := &cobra.Command{
		Use:   "connect <team> <server>",
		Short: "Sign in to an MCP server that signs in with OAuth",
		Long: "Needs tool:write in the team. Prints the address to sign in at: open\n" +
			"it in any browser, as the person whose account the server should use.\n" +
			"The sign-in lasts ten minutes, and this waits for it unless --no-wait.\n" +
			"Connecting a server already connected signs in again, in its place.",
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

			begun := time.Now()
			started, err := cli.Result(api.StartMCPServerOAuthWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			// Stderr, as oa login's prompt is: under -o json a Note is silent,
			// and a sign-in nobody can see the address of cannot be made.
			fmt.Fprintf(opts.Stderr, "%s %s\n", opts.Styles.Label.Render("open"), opts.Styles.Value.Render(started.AuthorizeURL))
			if noWait {
				return opts.Out.Print(started, printer.Options{Table: func(*printer.Table) {}})
			}

			fmt.Fprintln(opts.Stderr, opts.Styles.Muted.Render("waiting for the sign-in, up to ten minutes — Ctrl-C to stop"))
			srv, err := awaitSignIn(cmd.Context(), api, team, id, begun)
			if err != nil {
				return err
			}
			return printServer(opts, srv, "connected")
		},
	}

	cmd.Flags().BoolVar(&noWait, "no-wait", false, "print the address and return, rather than wait for the sign-in")
	return cmd
}

var errSignInTimedOut = errors.New(
	"no sign-in came back within ten minutes: the address has expired, run oa mcp-servers connect again")

// awaitSignIn polls until the server holds a sign-in made since begun: one
// connected before is the old one, still standing while the person reads the
// consent screen. A sign-in declined writes nothing, so it is the timeout
// that says so.
func awaitSignIn(parent context.Context, api *client.ClientWithResponses, team, id uuid.UUID, begun time.Time) (*client.MCPServer, error) {
	ctx, cancel := context.WithTimeout(parent, signInLasts)
	defer cancel()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			// A wait stopped by the caller is theirs, not the sign-in's limit.
			if err := parent.Err(); err != nil {
				return nil, err
			}
			return nil, errSignInTimedOut
		case <-tick.C:
		}
		srv, err := cli.Result(api.GetMCPServerWithResponse(ctx, team, id))
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			return nil, err
		}
		if c := srv.Connection; c != nil && c.Status == client.MCPServerConnectionStatusConnected && !c.ConnectedAt.Before(begun) {
			return srv, nil
		}
	}
}

func newDisconnectCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <team> <server>",
		Short: "End an MCP server's sign-in",
		Long: "Needs tool:write in the team. The tokens are deleted, and the\n" +
			"authorization server is asked to revoke them. Calls to the server fail\n" +
			"until someone connects it again; the server and its tools stay.",
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

			if err := cli.NoContent(api.DisconnectMCPServerOAuthWithResponse(cmd.Context(), team, id)); err != nil {
				return cli.Needs(err, writePermission)
			}
			return opts.Out.Print(
				client.MCPServer{ID: id, TeamID: team, Name: args[1], Env: map[string]string{}, AuthKind: client.MCPServerAuthKindOauth},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("disconnected"),
							opts.Styles.Value.Render(cli.Safe(args[1])),
							opts.Styles.Muted.Render(id.String()))
					},
				})
		},
	}
}

// authOf is the list's AUTH column: how the server is reached, and for one
// that signs in, whether it is.
func authOf(s client.MCPServer) string {
	switch {
	case s.AuthKind == client.MCPServerAuthKindOauth && s.Connection == nil:
		return "oauth: not connected"
	case s.AuthKind == client.MCPServerAuthKindOauth && s.Connection.Status == client.MCPServerConnectionStatusNeedsSignIn:
		return "oauth: needs sign-in"
	case s.AuthKind == client.MCPServerAuthKindOauth:
		return "oauth: connected"
	case s.AuthSecretRef != nil || len(s.Env) > 0:
		return "token"
	}
	return "none"
}

func printSignIn(opts *cli.Options, table *printer.Table, s *client.MCPServer) {
	if s.OauthClientID != nil {
		table.Row("client", cli.Safe(*s.OauthClientID), "")
	} else if r := s.OauthRegistration; r != nil {
		table.Row("client", cli.Safe(r.ClientID)+opts.Styles.Muted.Render(
			", registered by openarity "+r.RegisteredAt.UTC().Format("2006-01-02 15:04")), "")
	}
	if s.OauthClientSecretRef != nil {
		table.Row("secret", cli.Safe(*s.OauthClientSecretRef), "")
	}
	if s.OauthScopes != nil {
		table.Row("scopes", cli.Safe(strings.Join(*s.OauthScopes, " ")), "")
	}
	c := s.Connection
	if c == nil {
		table.Row("sign-in", opts.Styles.Muted.Render("not connected — oa mcp-servers connect"), "")
		return
	}
	when := c.ConnectedAt.UTC().Format("2006-01-02 15:04")
	if c.Status == client.MCPServerConnectionStatusNeedsSignIn {
		reason := "it stopped working"
		if c.Reason != nil {
			reason = *c.Reason
		}
		table.Row("sign-in", opts.Styles.Warn.Render("needs signing in again: "+cli.Safe(reason)), when)
		return
	}
	table.Row("sign-in", opts.Styles.OK.Render("connected"), when)
	if len(c.ScopesGranted) > 0 {
		table.Row("granted", cli.Safe(strings.Join(c.ScopesGranted, " ")), "")
	}
	if c.ConnectedBy != nil {
		table.Row("by", fmt.Sprint(*c.ConnectedBy), "")
	}
}
