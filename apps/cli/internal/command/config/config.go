package config

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
	"github.com/LaplacianAI/openarity/apps/cli/internal/theme"
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and change saved settings",
		Long: "Settings live in a file; an exported variable overrides it for that\n" +
			"shell only. `oa config show` says which one won for each value.",
	}

	cmd.AddCommand(
		newShowCmd(opts),
		newSetCmd(opts),
		newUnsetCmd(opts),
		newPathCmd(opts),
	)
	return cmd
}

func newShowCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the effective settings and where each came from",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			views := settingViews(opts.Settings)

			return opts.Out.Print(views, printer.Options{
				Table: func(table *printer.Table) {
					for _, view := range views {
						source := ""
						if view.Source != "" {
							source = opts.Styles.Muted.Render("(" + view.Source + ")")
						}
						table.Row(opts.Styles.Label.Render(view.Name),
							opts.Styles.Value.Render(view.Value), source)
					}
				},
			})
		},
	}
}

func newSetCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Write a setting to the config file",
		Long: "Keys: server, theme, output.\n\n" +
			"The token is written by `oa login` rather than by hand, so it is not\n" +
			"settable here — a credential typed as a shell argument lands in your\n" +
			"history.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.Saved
			key, value := args[0], strings.TrimSpace(args[1])

			switch key {
			case "server":
				if value == "" {
					return fmt.Errorf("server cannot be empty — use `oa config unset server`")
				}
				setActiveServer(&saved, value)
			case "theme":
				chosen, ok := theme.Parse(value)
				if !ok {
					return fmt.Errorf("%q is not a theme — use one of %s", value, theme.Names())
				}
				saved.Theme = string(chosen)
			case "output":
				chosen, ok := output.Parse(value)
				if !ok {
					return fmt.Errorf("%q is not an output format — use one of %s", value, output.Names())
				}
				saved.Output = string(chosen)
			default:
				return fmt.Errorf("unknown setting: %s", key)
			}

			if err := config.Save(saved); err != nil {
				return err
			}

			return confirm(opts, key, value)
		},
	}
}

func newUnsetCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a setting from the config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.Saved

			switch args[0] {
			case "server":
				updateActive(&saved, func(c *config.Context) { c.Server = "" })
			case "theme":
				saved.Theme = ""
			case "token":
				updateActive(&saved, func(c *config.Context) { c.Token = "" })
			case "output":
				saved.Output = ""
			default:
				return fmt.Errorf("%q is not a setting — try server, theme, token or output", args[0])
			}

			if err := config.Save(saved); err != nil {
				return err
			}
			fmt.Fprintf(opts.Stdout, "%s %s\n",
				opts.Styles.OK.Render("unset"), opts.Styles.Value.Render(args[0]))
			return nil
		},
	}
}

func newPathCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file location",
		Long: "Prints it whether or not the file exists, so it can be used to create\n" +
			"one: `mkdir -p $(dirname $(oa config path))`.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			fmt.Fprintln(opts.Stdout, path)
			return nil
		},
	}
}

func confirm(o *cli.Options, key, value string) error {
	fmt.Fprintf(o.Stdout, "%s %s %s\n",
		o.Styles.OK.Render("set"), o.Styles.Label.Render(key), o.Styles.Value.Render(value))

	var overriding config.Setting
	switch key {
	case "server":
		overriding = o.Settings.Server
	case "theme":
		overriding = o.Settings.Theme
	case "output":
		overriding = o.Settings.Output
	}

	return o.WarnOverride(overriding)
}

const defaultContextName = "default"

func setActiveServer(cfg *config.Config, server string) {
	updateActive(cfg, func(c *config.Context) { c.Server = server })
}

func updateActive(cfg *config.Config, apply func(*config.Context)) {
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]config.Context{}
	}

	name := cfg.ActiveName()
	if name == "" {
		name = defaultContextName
		cfg.Current = name
	}

	active := cfg.Contexts[name]
	apply(&active)
	cfg.Contexts[name] = active
}
