package context

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Switch between brains",
		Long: "A context is one brain and the credential that brain issued. A token\n" +
			"is only valid where it came from, so the two travel together.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newUseCmd(opts),
		newCreateCmd(opts),
		newRenameCmd(opts),
		newDeleteCmd(opts),
	)
	return cmd
}

func newListCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the saved contexts",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			views := contextViews(opts.Saved)
			if len(views) == 0 {
				opts.Out.Note(opts.Styles.Muted.Render(
					"no contexts — `oa context create <name> --server <url>`"))
			}

			return opts.Out.Print(views, printer.Options{
				Table: func(table *printer.Table) {
					for _, view := range views {
						marker, name := " ", opts.Styles.Value.Render(view.Name)
						if view.Active {
							marker, name = opts.Styles.OK.Render("*"), opts.Styles.Label.Render(view.Name)
						}

						token := "no token"
						if view.HasToken {
							token = "token saved"
						}

						table.Row(marker+" "+name, view.Server, opts.Styles.Muted.Render(token))
					}
				},
			})
		},
	}
}

func newUseCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Make a context the one every command talks to",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.Saved
			name := strings.TrimSpace(args[0])

			if _, ok := saved.Contexts[name]; !ok {
				return unknownContext(saved, name)
			}

			saved.Current = name
			if err := config.Save(saved); err != nil {
				return err
			}

			fmt.Fprintf(opts.Stdout, "%s %s\n",
				opts.Styles.OK.Render("using"), opts.Styles.Value.Render(name))
			return opts.WarnOverride(opts.Settings.Server)
		},
	}
}

func newCreateCmd(opts *cli.Options) *cobra.Command {
	var server string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Add a context and switch to it",
		Long: "The new context becomes the active one, the same way `gcloud config\n" +
			"configurations create` does. Log in afterwards to give it a credential.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.Saved
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

			fmt.Fprintf(opts.Stdout, "%s %s\n",
				opts.Styles.OK.Render("created"), opts.Styles.Value.Render(name))
			fmt.Fprintln(opts.Stdout, opts.Styles.Muted.Render("now using "+name))
			return opts.WarnOverride(opts.Settings.Server)
		},
	}

	cmd.Flags().StringVar(&server, "server", "", "the brain's API address, for example "+config.DefaultServer)
	_ = cmd.MarkFlagRequired("server")
	return cmd
}

func newRenameCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a context, keeping its address and credential",
		Long: "Delete and create would work, except that it discards the token and\n" +
			"you would have to log in again.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.Saved
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

			fmt.Fprintf(opts.Stdout, "%s %s %s\n",
				opts.Styles.OK.Render("renamed"),
				opts.Styles.Value.Render(from), opts.Styles.Value.Render("→ "+to))
			return nil
		},
	}
}

func newDeleteCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Forget a context and its credential",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			saved := opts.Saved
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

			fmt.Fprintf(opts.Stdout, "%s %s\n",
				opts.Styles.OK.Render("deleted"), opts.Styles.Value.Render(name))

			if now := saved.ActiveName(); now != "" && now != opts.Saved.ActiveName() {
				fmt.Fprintf(opts.Stdout, "%s\n", opts.Styles.Muted.Render("now using "+now))
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
