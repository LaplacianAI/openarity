package approvals

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func newRulesCmd(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Rules that settle a tool or a server ahead of the team's mode",
		Long: "A rule names one tool, as <server>__<tool>, or one server, and says\n" +
			"allow, ask or deny. A target has one rule; remove it to change it.\n" +
			"Seeing the rules needs only to be in the team; adding or removing one\n" +
			"needs team:settings.",
	}
	cmd.AddCommand(newRulesListCmd(opts), newRulesAddCmd(opts), newRulesRemoveCmd(opts))
	return cmd
}

func newRulesListCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List a team's approval rules",
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
			page, err := cli.Result(api.ListApprovalRulesWithResponse(cmd.Context(), team))
			if err != nil {
				return err
			}
			return cli.PrintPage(opts, cli.Page[client.ApprovalRule]{
				Items: page.Items,
				Empty: "no approval rules",
				Row: func(table *printer.Table, r client.ApprovalRule) {
					table.Row(string(r.TargetKind), opts.Styles.Value.Render(cli.Safe(r.Target)),
						modeCell(opts, string(r.Mode)), otherCell(opts, r.RequireOther),
						opts.Styles.Muted.Render(r.ID.String()))
				},
			})
		},
	}
}

func modeCell(opts *cli.Options, mode string) string {
	switch client.ApprovalRuleMode(mode) {
	case client.ApprovalRuleModeAllow:
		return opts.Styles.OK.Render(mode)
	case client.ApprovalRuleModeDeny:
		return opts.Styles.Err.Render(mode)
	case client.ApprovalRuleModeAsk:
		return opts.Styles.Warn.Render(mode)
	default:
		return cli.Safe(mode)
	}
}

func otherCell(opts *cli.Options, requireOther bool) string {
	if requireOther {
		return "someone else answers"
	}
	return opts.Styles.Muted.Render("-")
}

func newRulesAddCmd(opts *cli.Options) *cobra.Command {
	var (
		tool, server, mode string
		requireOther       bool
	)

	cmd := &cobra.Command{
		Use:   "add <team> (--tool NAME | --server NAME) --mode allow|ask|deny",
		Short: "Settle a tool or a server ahead of the team's mode",
		Long: "--tool takes <server>__<tool> or a built-in's name; --server takes a\n" +
			"server's name. --require-other wants somebody other than the person\n" +
			"the run works for to answer. A target already holding a rule is a\n" +
			"conflict: remove the old rule first. Needs team:settings.\n\n" +
			"    oa approvals rules add platform --tool github__delete_repo --mode deny\n" +
			"    oa approvals rules add platform --server github --mode ask --require-other",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tool, server = strings.TrimSpace(tool), strings.TrimSpace(server)
			if (tool == "") == (server == "") {
				return errors.New("name what the rule is for with exactly one of --tool and --server")
			}
			m := client.ApprovalRuleRequestMode(strings.ToLower(strings.TrimSpace(mode)))
			switch m {
			case client.ApprovalRuleRequestModeAllow, client.ApprovalRuleRequestModeAsk, client.ApprovalRuleRequestModeDeny:
			default:
				return fmt.Errorf("--mode is allow, ask or deny, not %q", mode)
			}

			body := client.CreateApprovalRuleJSONRequestBody{Mode: m, RequireOther: &requireOther}
			if tool != "" {
				body.TargetKind, body.Target = client.ApprovalRuleRequestTargetKindTool, tool
			} else {
				body.TargetKind, body.Target = client.ApprovalRuleRequestTargetKindServer, server
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			res, err := api.CreateApprovalRuleWithResponse(cmd.Context(), team, body)
			if err == nil && res.StatusCode() == http.StatusConflict {
				return fmt.Errorf("%s %s already has a rule — `oa approvals rules list %s` shows it, "+
					"and `rules rm` removes it", body.TargetKind, cli.Safe(body.Target), args[0])
			}
			rule, err := cli.Created(res, err)
			if err != nil {
				return cli.Needs(err, settingsPermission)
			}
			return opts.Out.Print(rule, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("added"), string(rule.TargetKind),
						opts.Styles.Value.Render(cli.Safe(rule.Target)), modeCell(opts, string(rule.Mode)),
						opts.Styles.Muted.Render(rule.ID.String()))
				},
			})
		},
	}

	cmd.Flags().StringVar(&tool, "tool", "", "the tool the rule is for, as <server>__<tool>")
	cmd.Flags().StringVar(&server, "server", "", "the MCP server the rule is for, by name")
	cmd.Flags().StringVar(&mode, "mode", "", "allow, ask or deny")
	cmd.Flags().BoolVar(&requireOther, "require-other", false,
		"somebody other than the person the run works for must answer")
	return cmd
}

type removedView struct {
	Removed string `json:"removed" yaml:"removed"`
}

func newRulesRemoveCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <team> <rule>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove a rule, so the team's mode decides for its target again",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(strings.TrimSpace(args[1]))
			if err != nil {
				return fmt.Errorf("%q is not a rule id — `oa approvals rules list %s` shows them", args[1], args[0])
			}
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			if err := cli.NoContent(api.DeleteApprovalRuleWithResponse(cmd.Context(), team, id)); err != nil {
				return cli.Needs(err, settingsPermission)
			}
			return opts.Out.Print(removedView{Removed: id.String()}, printer.Options{
				Table: func(table *printer.Table) {
					table.Row(opts.Styles.OK.Render("removed"), opts.Styles.Muted.Render(id.String()))
				},
			})
		},
	}
}
