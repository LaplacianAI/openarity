package approvals

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	minTTL = 5
	maxTTL = 1440
)

var modes = []client.ApprovalSettingsMode{
	client.ApprovalSettingsModeOff, client.ApprovalSettingsModeWrites,
	client.ApprovalSettingsModeDestructive, client.ApprovalSettingsModeAll,
}

func newSettingsCmd(opts *cli.Options) *cobra.Command {
	var (
		mode string
		ttl  int32
	)

	cmd := &cobra.Command{
		Use:   "settings <team>",
		Short: "Show when a team's agents ask, or change it",
		Long: "--mode says which calls ask: off, writes (every call that is not a\n" +
			"read), destructive (only those that destroy) or all. --ttl is how many\n" +
			"minutes a request waits before it expires, 5 to 1440. Either may be\n" +
			"given alone; what is not given stays as it is. With neither, the\n" +
			"settings are shown. Changing them needs team:settings.\n\n" +
			"    oa approvals settings platform\n" +
			"    oa approvals settings platform --mode writes --ttl 60",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			changing := cmd.Flags().Changed("mode") || cmd.Flags().Changed("ttl")

			next := client.ApprovalSettings{}
			if cmd.Flags().Changed("mode") {
				m := client.ApprovalSettingsMode(strings.ToLower(strings.TrimSpace(mode)))
				if !slices.Contains(modes, m) {
					return fmt.Errorf("%q is not a mode — off, writes, destructive or all", mode)
				}
				next.Mode = m
			}
			if cmd.Flags().Changed("ttl") {
				if ttl < minTTL || ttl > maxTTL {
					return fmt.Errorf("--ttl is minutes, from %d to %d", minTTL, maxTTL)
				}
				next.TTLMinutes = ttl
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			cur, err := cli.Result(api.GetApprovalSettingsWithResponse(cmd.Context(), team))
			if err != nil {
				return err
			}
			if !changing {
				return printSettings(opts, cur, "")
			}

			if next.Mode == "" {
				next.Mode = cur.Mode
			}
			if next.TTLMinutes == 0 {
				next.TTLMinutes = cur.TTLMinutes
			}
			saved, err := cli.Result(api.SaveApprovalSettingsWithResponse(cmd.Context(), team, next))
			if err != nil {
				return cli.Needs(err, settingsPermission)
			}
			return printSettings(opts, saved, "saved")
		},
	}

	cmd.Flags().StringVar(&mode, "mode", "", "which calls ask: off, writes, destructive or all")
	cmd.Flags().Int32Var(&ttl, "ttl", 0, "minutes a request waits before it expires, 5 to 1440")
	return cmd
}

func printSettings(opts *cli.Options, s *client.ApprovalSettings, did string) error {
	return opts.Out.Print(s, printer.Options{
		Table: func(table *printer.Table) {
			if did != "" {
				table.Row(opts.Styles.OK.Render(did), "", "")
			}
			table.Row("mode", opts.Styles.Value.Render(cli.Safe(string(s.Mode))), "")
			table.Row("ttl", fmt.Sprintf("%d minutes", s.TTLMinutes), "")
		},
	})
}
