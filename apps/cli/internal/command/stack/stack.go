// Package stack runs a personal install: Postgres, dex, the brain and its
// worker, as processes on this machine rather than containers.
//
// It is the one command group that does not talk to a brain over HTTP, so it
// reaches for internal/stack rather than opts.API.
package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

// The supervisor's own PID, written while it holds the children. `oa stack
// start` blocks; this is how stop and status find it afterwards.
const pidFile = "supervisor.pid"

const releaseTag = ""

func New(opts *cli.Options) *cobra.Command {
	var root string

	cmd := &cobra.Command{
		Use:   "stack",
		Short: "Run Openarity on this machine",
		Long: "A personal install: Postgres, dex, the brain and its worker, started\n" +
			"and stopped together. Everything lives under one directory, listens on\n" +
			"loopback, and needs no Docker.",
	}

	// Persistent, so every verb takes it and none of them declares it twice.
	// The default is computed rather than baked in — a path from the machine
	// that built the binary would be wrong on every other machine.
	cmd.PersistentFlags().StringVar(&root, "root", "",
		"where the install lives (default: the platform's application data directory)")

	find := func() (engine.Layout, error) { return resolveLayout(root) }

	cmd.AddCommand(
		newSetupCmd(opts, find),
		newStartCmd(opts, find),
		newStopCmd(opts, find),
		newStatusCmd(opts, find),
	)
	return cmd
}

// resolveLayout turns the flag, or the platform default, into a Layout.
func resolveLayout(root string) (engine.Layout, error) {
	if root != "" {
		return engine.NewLayout(root), nil
	}

	dir, err := engine.Dir(runtime.GOOS, os.Getenv)
	if err != nil {
		return engine.Layout{}, err
	}
	return engine.NewLayout(dir), nil
}

type layoutFunc func() (engine.Layout, error)

// requireInstall is the difference between a sentence a person can act on and
// "no such file or directory" from three layers down.
func requireInstall(find layoutFunc) (engine.Layout, engine.State, error) {
	layout, err := find()
	if err != nil {
		return engine.Layout{}, engine.State{}, err
	}
	if !layout.Installed() {
		return engine.Layout{}, engine.State{}, fmt.Errorf(
			"no install at %s — run `oa stack setup` first", layout.Root)
	}

	state, err := engine.LoadState(layout.State)
	if err != nil {
		return engine.Layout{}, engine.State{}, err
	}
	return layout, state, nil
}

func newSetupCmd(opts *cli.Options, find layoutFunc) *cobra.Command {
	var (
		binDir      string
		noAutostart bool
		asJSON      bool
		given       Answers
	)

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install and start Openarity on this machine",
		Long: "Downloads nothing yet: point --bin-dir at a directory holding postgres,\n" +
			"dex and brain, or have them on PATH. Prints a passphrase once, then\n" +
			"opens the dashboard.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			layout, err := find()
			if err != nil {
				return err
			}

			// The refusal reaches the person as a sentence. ErrInstalled on
			// its own is a sentinel nobody prints.
			if layout.Installed() {
				return fmt.Errorf("already installed at %s — run `oa stack start`", layout.Root)
			}

			var report engine.Reporter
			if asJSON {
				report = engine.JSONReporter(opts.Stdout)
			}

			var finder engine.Finder = engine.DownloadingFinder{
				Layout:     layout,
				Platform:   engine.ThisPlatform(),
				Downloader: &engine.Downloader{Report: report},
				Tag:        releaseTag,
			}
			if binDir != "" {
				finder = engine.LocalFinder{Dir: binDir}
			}

			settings, credentials, err := newWizard(opts, asJSON, given).Run()
			if err != nil {
				return err
			}

			setup := &engine.Setup{
				Layout:      layout,
				Finder:      finder,
				Settings:    settings,
				Credentials: credentials,
				Report:      report,
				Steps:       realSteps(),
				Versions:    engine.Versions{Postgres: "local", Dex: "local", Brain: "local"},
			}

			result, err := setup.Run(cmd.Context())
			if err != nil {
				if errors.Is(err, engine.ErrInstalled) {
					return fmt.Errorf("already installed at %s — run `oa stack start`", layout.Root)
				}
				return err
			}

			// Through Note, not Print: this is prose for a person, and it
			// must stay out of the way of anything parsing the output. The
			// passphrase is printed here and stored nowhere.
			if !noAutostart {
				if err := enableAutostart(cmd.Context(), layout); err != nil {
					opts.Out.Note("Openarity will not start again by itself: " + err.Error())
				} else {
					opts.Out.Note("It will start again when you log in. `oa stack setup --no-autostart` skips this.")
				}
			}

			opts.Out.Note("Openarity is running at " + result.URL)
			if result.Passphrase != "" {
				opts.Out.Note("Sign in as dev@openarity.local")
				opts.Out.Note("Passphrase: " + result.Passphrase)
				opts.Out.Note("Write it down — it is not stored anywhere and cannot be shown again.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&binDir, "bin-dir", "",
		"a directory holding postgres, dex and brain (default: look on PATH)")
	cmd.Flags().BoolVar(&noAutostart, "no-autostart", false,
		"do not start Openarity when you log in")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"report progress as one JSON event per line, for a program rather than a person")

	// Given together, these answer the wizard so it asks nothing — which is
	// how the installer window drives it, a sidecar having no terminal to
	// prompt at. Credentials are deliberately absent; the wizard reads those
	// from the environment.
	cmd.Flags().StringVar(&given.Objects, "objects", "", "where files are kept: filesystem, memory or s3")
	cmd.Flags().StringVar(&given.Endpoint, "objects-endpoint", "", "an S3-compatible endpoint, blank for AWS")
	cmd.Flags().StringVar(&given.Bucket, "objects-bucket", "", "the bucket, which must already exist")
	cmd.Flags().StringVar(&given.Region, "objects-region", "", "the bucket's region")
	cmd.Flags().StringVar(&given.Secrets, "secrets", "", "where credentials are kept: static, openbao or vault")
	cmd.Flags().StringVar(&given.Address, "secrets-addr", "", "the address of an external secret store")
	cmd.Flags().StringVar(&given.KVMount, "secrets-mount", "", "the KV mount to use")
	cmd.Flags().StringVar(&given.Gateway, "model-gateway", "", "the base URL of a model gateway")
	return cmd
}

func newStartCmd(opts *cli.Options, find layoutFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start an install, and stay running",
		Long: "Holds the four processes for as long as it runs, so stopping this\n" +
			"stops them. Ctrl-C, or `oa stack stop` from another terminal.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			layout, state, err := requireInstall(find)
			if err != nil {
				return err
			}

			stack, err := build(cmd.Context(), layout, state)
			if err != nil {
				return err
			}

			if err := writePID(layout); err != nil {
				return err
			}
			defer func() { _ = os.Remove(filepath.Join(layout.Root, pidFile)) }()

			if err := stack.Start(cmd.Context()); err != nil {
				return err
			}
			opts.Out.Note(fmt.Sprintf("Openarity is running at http://127.0.0.1:%d/ui", state.Ports.API))

			// Blocks until Ctrl-C or `oa stack stop`. cmd.Context() carries
			// the signal handler main installed, so both arrive the same way.
			<-cmd.Context().Done()

			// WithoutCancel: the context is already cancelled — that is why
			// we are here — and a shutdown that skipped itself for that
			// reason would leave four processes running.

			return stack.Stop(withoutCancel(cmd))
		},
	}
}

func newStopCmd(opts *cli.Options, find layoutFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop a running install",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			layout, _, err := requireInstall(find)
			if err != nil {
				return err
			}

			pid, err := readPID(layout)
			if err != nil {
				return err
			}
			if pid == 0 {
				opts.Out.Note("nothing is running")
				return nil
			}

			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("stack: finding the supervisor (pid %d): %w", pid, err)
			}

			// The supervisor stops the children itself, in reverse order,
			// with the escalation Child.Stop carries. Signalling the children
			// from here would race with it and skip pg_ctl.
			if err := interrupt(proc); err != nil {
				return fmt.Errorf("stack: stopping the supervisor (pid %d): %w", pid, err)
			}

			opts.Out.Note("stopping")
			return nil
		},
	}
}

// statusView is what `oa status` prints. Both tags, always: a field with only
// a json tag gets a lowercased Go name in yaml, so the two formats disagree.
type statusView struct {
	Name    string `json:"name" yaml:"name"`
	Running bool   `json:"running" yaml:"running"`
	PID     int    `json:"pid" yaml:"pid"`
	Port    int    `json:"port" yaml:"port"`
	Ready   bool   `json:"ready" yaml:"ready"`
}

func newStatusCmd(opts *cli.Options, find layoutFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what is running, and where",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			layout, state, err := requireInstall(find)
			if err != nil {
				return err
			}

			pid, err := readPID(layout)
			if err != nil {
				return err
			}
			up := pid != 0 && alive(pid)

			ports := map[string]int{
				"postgres": state.Ports.Postgres,
				"dex":      state.Ports.Dex,
				"brain":    state.Ports.API,
				"worker":   0,
			}

			// make with a length, never a nil slice: nil marshals to null and
			// `jq length` fails on it, which is exactly what an install with
			// nothing running would produce.
			views := make([]statusView, 0, len(componentNames))
			for _, name := range componentNames {
				views = append(views, statusView{
					Name:    name,
					Running: up,
					Port:    ports[name],
					PID:     0,
					Ready:   up,
				})
			}

			return opts.Out.Print(views, printer.Options{
				Table: func(table *printer.Table) {
					for _, v := range views {
						table.Row(
							opts.Styles.Value.Render(v.Name),
							runningWord(v.Running),
							portWord(v.Port),
						)
					}
				},
			})
		},
	}
}

// The order they start in, which is the order worth reading them in.
var componentNames = []string{"postgres", "dex", "brain", "worker"}

func runningWord(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}

func portWord(port int) string {
	if port == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", port)
}
