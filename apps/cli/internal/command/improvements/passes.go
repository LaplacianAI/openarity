package improvements

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const passesPermission = "improvement:approve"

// triggerCell is why a pass ran: the patterns it was handed, or the daily one.
func triggerCell(p *client.ImproverPass) string {
	if p.Trigger == client.ImproverPassTriggerDaily {
		return "daily"
	}
	if len(p.Signatures) == 0 {
		return "pattern"
	}
	return "pattern " + cli.Safe(strings.Join(p.Signatures, ", "))
}

// A pass cut short says so in its status, where a narrow terminal still
// shows it.
func passStatusCell(opts *cli.Options, p *client.ImproverPass) string {
	s := p.Status
	switch s {
	case client.ImproverPassStatusDone:
		if p.CutShort {
			return opts.Styles.Warn.Render("done (cut short)")
		}
		return opts.Styles.OK.Render(string(s))
	case client.ImproverPassStatusFailed:
		return opts.Styles.Err.Render(string(s))
	case client.ImproverPassStatusRunning:
		return opts.Styles.Warn.Render(string(s))
	default:
		return cli.Safe(string(s))
	}
}

// tokensCell is what a pass cost; a running one has not said yet.
func tokensCell(p *client.ImproverPass) string {
	if p.Status == client.ImproverPassStatusRunning {
		return "—"
	}
	return strconv.FormatInt(p.InputTokens, 10) + " in / " + strconv.FormatInt(p.OutputTokens, 10) + " out"
}

// outcomeCell is what came of a pass: why it failed, or what it proposed.
func outcomeCell(p *client.ImproverPass) string {
	switch {
	case p.Status == client.ImproverPassStatusRunning:
		return "—"
	case p.Status == client.ImproverPassStatusFailed && p.Error != nil:
		return cli.Safe(*p.Error)
	}
	return strconv.Itoa(int(p.Proposed)) + " proposed / " + strconv.Itoa(int(p.ScreenedOut)) + " screened out"
}

func newPassesCmd(opts *cli.Options) *cobra.Command {
	var (
		paging cli.Paging
		agent  string
	)

	cmd := &cobra.Command{
		Use:   "passes <team>",
		Short: "List the improver's passes: what ran, why, and what it proposed",
		Long: "Newest first. A pass is one run of the improver over an agent, started\n" +
			"either because a pattern recurred in enough runs (the pattern shown is\n" +
			"part:target:cause) or by the daily pass. Each shows how many proposals it\n" +
			"made, how many the screen refused for carrying one run's specifics, and\n" +
			"the tokens it cost, or why it failed. --agent keeps one agent's, by name\n" +
			"or id; -o json carries the full record, including its report. A report\n" +
			"drawn on a conversation you cannot read is withheld (report_withheld).\n\n" +
			"Needs " + passesPermission + ".\n\n" +
			"    oa improvements passes platform\n" +
			"    oa improvements passes platform --agent support-bot -o json",
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
			params := &client.ListImproverPassesParams{}
			if agent != "" {
				id, err := cli.ResolveAgent(cmd.Context(), api, team, agent)
				if err != nil {
					return err
				}
				params.Agent = &id
			}

			params.Limit, params.Cursor = paging.Values()
			page, err := cli.Result(api.ListImproverPassesWithResponse(cmd.Context(), team, params))
			if err != nil {
				return cli.Needs(err, passesPermission)
			}

			more := "oa improvements passes " + args[0]
			if agent != "" {
				more += " --agent " + shellWord(agent)
			}
			return cli.PrintPage(opts, cli.Page[client.ImproverPass]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no improver passes",
				More:       more,
				Row: func(table *printer.Table, p client.ImproverPass) {
					table.Row(
						opts.Styles.Value.Render(cli.Safe(p.Agent.Name)),
						triggerCell(&p),
						passStatusCell(opts, &p),
						outcomeCell(&p),
						tokensCell(&p),
						opts.Styles.Muted.Render(p.StartedAt.Local().Format(stampLayout)),
						opts.Styles.Muted.Render(p.ID.String()),
					)
				},
			})
		},
	}

	paging.Flags(cmd)
	cmd.Flags().StringVar(&agent, "agent", "", "only this agent's passes, by name or id")
	return cmd
}
