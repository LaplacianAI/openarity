package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func newContextCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Switch between brains",
		Long: "A context is one brain and the credential that brain issued. A token\n" +
			"is only valid where it came from, so the two travel together.",
	}

	cmd.AddCommand(
		newContextListCmd(opts),
		newContextUseCmd(opts),
		newContextCreateCmd(opts),
		newContextRenameCmd(opts),
		newContextDeleteCmd(opts),
	)
	return cmd
}

func newContextListCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the saved contexts",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			views := contextViews(opts.saved)
			if len(views) == 0 {
				opts.out.Note(opts.styles.Muted.Render(
					"no contexts — `oa context create <name> --server <url>`"))
			}

			return opts.out.Print(views, printer.Options{
				Table: func(table *printer.Table) {
					for _, view := range views {
						marker, name := " ", opts.styles.Value.Render(view.Name)
						if view.Active {
							marker, name = opts.styles.OK.Render("*"), opts.styles.Label.Render(view.Name)
						}

						token := "no token"
						if view.HasToken {
							token = "token saved"
						}

						table.Row(marker+" "+name, view.Server, opts.styles.Muted.Render(token))
					}
				},
			})
		},
	}
}

func newContextUseCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Make a context the one every command talks to",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.saved
			name := strings.TrimSpace(args[0])

			if _, ok := saved.Contexts[name]; !ok {
				return unknownContext(saved, name)
			}

			saved.Current = name
			if err := config.Save(saved); err != nil {
				return err
			}

			fmt.Fprintf(opts.stdout, "%s %s\n",
				opts.styles.OK.Render("using"), opts.styles.Value.Render(name))
			return opts.warnOverride(opts.settings.Server)
		},
	}
}

func newContextCreateCmd(opts *options) *cobra.Command {
	var server string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Add a context and switch to it",
		Long: "The new context becomes the active one, the same way `gcloud config\n" +
			"configurations create` does. Log in afterwards to give it a credential.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.saved
			name := strings.TrimSpace(args[0])
			address := strings.TrimSpace(server)

			if err := usableName(saved, args[0]); err != nil {
				return err
			}
			// A context with no address falls back to the built-in one, so a
			// typo'd flag would create something that silently points at
			// localhost and is named after a brain somewhere else.
			if address == "" {
				return fmt.Errorf("--server cannot be empty — the address is what a context is")
			}

			if saved.Contexts == nil {
				saved.Contexts = map[string]config.Context{}
			}
			saved.Contexts[name] = config.Context{Server: address}
			saved.Current = name

			if err := config.Save(saved); err != nil {
				return err
			}

			fmt.Fprintf(opts.stdout, "%s %s\n",
				opts.styles.OK.Render("created"), opts.styles.Value.Render(name))
			fmt.Fprintln(opts.stdout, opts.styles.Muted.Render("now using "+name))
			return opts.warnOverride(opts.settings.Server)
		},
	}

	cmd.Flags().StringVar(&server, "server", "", "the brain's API address, for example "+config.DefaultServer)
	_ = cmd.MarkFlagRequired("server")
	return cmd
}

func newContextRenameCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a context, keeping its address and credential",
		Long: "Delete and create would work, except that it discards the token and\n" +
			"you would have to log in again.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.saved
			from, to := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])

			moving, ok := saved.Contexts[from]
			if !ok {
				return unknownContext(saved, from)
			}
			if from == to {
				return fmt.Errorf("%s is already called that", from)
			}
			if err := usableName(saved, args[1]); err != nil {
				return err
			}

			delete(saved.Contexts, from)
			saved.Contexts[to] = moving
			if saved.Current == from {
				saved.Current = to
			}

			if err := config.Save(saved); err != nil {
				return err
			}

			fmt.Fprintf(opts.stdout, "%s %s %s\n",
				opts.styles.OK.Render("renamed"),
				opts.styles.Value.Render(from), opts.styles.Value.Render("→ "+to))
			return nil
		},
	}
}

func newContextDeleteCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Forget a context and its credential",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.saved
			name := strings.TrimSpace(args[0])

			if _, ok := saved.Contexts[name]; !ok {
				return unknownContext(saved, name)
			}

			delete(saved.Contexts, name)
			if saved.Current == name {
				saved.Current = ""
			}

			if err := config.Save(saved); err != nil {
				return err
			}

			fmt.Fprintf(opts.stdout, "%s %s\n",
				opts.styles.OK.Render("deleted"), opts.styles.Value.Render(name))

			if now := saved.ActiveName(); now != "" && now != opts.saved.ActiveName() {
				fmt.Fprintf(opts.stdout, "%s\n", opts.styles.Muted.Render("now using "+now))
			}
			return nil
		},
	}
}

func usableName(saved config.Config, raw string) error {
	name := strings.TrimSpace(raw)

	if name == "" || strings.ContainsAny(name, " \t") {
		return fmt.Errorf("%q is not a usable name — no spaces", raw)
	}
	if _, taken := saved.Contexts[name]; taken {
		return fmt.Errorf("%s already exists — `oa context use %s`, or delete it first", name, name)
	}
	return nil
}

func unknownContext(saved config.Config, name string) error {
	names := saved.ContextNames()
	if len(names) == 0 {
		return fmt.Errorf("no contexts yet — `oa context create %s --server <url>`", name)
	}
	return fmt.Errorf("%s is not a context — try %s", name, strings.Join(names, ", "))
}

func (o *options) warnOverride(setting config.Setting) error {
	if strings.HasPrefix(setting.Source, "OPENARITY_") {
		fmt.Fprintln(o.stdout, o.styles.Warn.Render(
			setting.Source+" is set and overrides this for the current shell"))
	}
	return nil
}
