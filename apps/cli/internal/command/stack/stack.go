// Package stack runs a personal install: Postgres, dex, the brain and its
// worker, as processes on this machine rather than containers.
//
// It is the one command group that does not talk to a brain over HTTP, so it
// reaches for internal/stack rather than opts.API.
package stack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

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
		newInfoCmd(opts, find),
		newPassphraseCmd(opts, find),
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

			// Always reported, one way or the other. Silence through a
			// download, a cluster and sixteen migrations is indistinguishable
			// from a hang, and was mistaken for one.
			report := engine.TextReporter(opts.Stderr)
			if asJSON {
				report = engine.JSONReporter(opts.Stdout)
			}

			platform := engine.ThisPlatform()
			downloader := &engine.Downloader{Report: report}

			finder := engine.Finder(engine.DownloadingFinder{
				Layout:     layout,
				Platform:   platform,
				Downloader: downloader,
				Tag:        releaseTag,
				Override:   binDir,
			})

			settings, credentials, err := newWizard(opts, asJSON, given, layout.Root).Run(cmd.Context())
			if err != nil {
				return err
			}

			setup := &engine.Setup{
				Layout:      layout,
				Finder:      finder,
				Settings:    settings,
				Credentials: credentials,
				Report:      report,
				Steps:       realSteps(downloader, platform),
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

			// Everything a person needs to get back in, together, because the
			// passphrase is shown once and the rest is worth having beside
			// it: the address to open, who to sign in as, and the directory
			// holding all of it.
			opts.Out.Note("")
			opts.Out.Note("Openarity is running at " + result.URL)
			if result.Passphrase != "" {
				opts.Out.Note("  sign in as   " + engine.DexUser)
				opts.Out.Note("  passphrase   " + result.Passphrase)
			}
			opts.Out.Note("  installed in " + layout.Root)
			if result.Passphrase != "" {
				opts.Out.Note("")
				opts.Out.Note("Write the passphrase down — it is not stored anywhere and cannot be shown again.")
			}

			// The gateway's own dashboard, which is not where the brain talks
			// to it: the brain uses /v1, a person uses the root.
			if result.GatewayPassword != "" {
				opts.Out.Note("")
				opts.Out.Note(fmt.Sprintf("The model gateway's own screen is at http://127.0.0.1:%d",
					result.Plan.Ports.Gateway))
				opts.Out.Note("  password     " + result.GatewayPassword)
				opts.Out.Note("Made for you because none was given, and kept with the install.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&binDir, "bin-dir", "",
		"binaries to use instead of downloading them; anything missing is still fetched")
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
	cmd.Flags().StringVar(&given.ModelBackend, "model-backend", "",
		"where the model gateway comes from: external, litellm or omniroute")
	cmd.Flags().StringVar(&given.ModelPath, "model-path", "",
		"where a gateway of our own installs its runtime and packages")
	return cmd
}

func newStartCmd(opts *cli.Options, find layoutFunc) *cobra.Command {
	var detached bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start an install, and stay running",
		Long: "Holds the four processes for as long as it runs, so stopping this\n" +
			"stops them. Ctrl-C, or `oa stack stop` from another terminal.\n" +
			"--detach starts them and returns instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			layout, state, err := requireInstall(find)
			if err != nil {
				return err
			}

			// Two things start an install and both are meant to: setup,
			// and the autostart unit at login — which on macOS also fires
			// the moment the agent is loaded, which is during setup. Exiting
			// 0 rather than refusing, because launchd restarts an agent that
			// fails and would spin for as long as the other one lives.
			if pid := running(layout); pid != 0 {
				opts.Out.Note(fmt.Sprintf(
					"Openarity is already running at http://127.0.0.1:%d/ui, supervised by pid %d",
					state.Ports.API, pid))
				return nil
			}

			// The same start setup does: spawn the supervisor, wait until the
			// brain answers, and return. For the installer window, which has
			// no terminal to hold, and for anybody who wants their prompt
			// back.
			if detached {
				if err := spawnSupervisor(layout.Root); err != nil {
					return err
				}
				if err := waitForReady(cmd.Context(), state.Ports.API); err != nil {
					return err
				}
				opts.Out.Note(fmt.Sprintf("Openarity is running at http://127.0.0.1:%d/ui", state.Ports.API))
				return nil
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

			// Blocks until Ctrl-C or `oa stack stop`. Ctrl-C arrives through
			// cmd.Context(), which carries the signal handler main installed.
			// `stop` arrives that way too on Unix, where it is a SIGTERM; on
			// Windows no signal reaches one process, so it sets a named event
			// and this is what waits on it.
			stopped, done, err := waitForStop(layout.Root)
			if err != nil {
				return err
			}
			defer done()

			select {
			case <-cmd.Context().Done():
			case <-stopped:
			}

			// WithoutCancel: the context is already cancelled — that is why
			// we are here — and a shutdown that skipped itself for that
			// reason would leave four processes running.

			return stack.Stop(withoutCancel(cmd))
		},
	}

	cmd.Flags().BoolVar(&detached, "detach", false,
		"start it and return, instead of holding the processes here")
	return cmd
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
			if err := interrupt(layout.Root, proc); err != nil {
				return fmt.Errorf("stack: stopping the supervisor (pid %d): %w", pid, err)
			}

			// Waited for, not assumed. Asking a supervisor to stop and
			// returning immediately meant `oa stack info` still said running
			// a moment later, and the installer window's Start button —
			// acting on exactly that answer — refused with "already running"
			// and did nothing. Postgres shutting down is the slow part, and
			// it is worth the second.
			if err := waitForStopped(cmd.Context(), pid); err != nil {
				return err
			}

			opts.Out.Note("stopped")
			return nil
		},
	}
}

// infoView answers one question — is Openarity already here — without
// failing when the answer is no.
//
// `status` refuses with "no install at ..." and `setup` refuses with "already
// installed at ... — run `oa stack start`", both of which are the right thing
// to tell a person at a terminal and neither of which a window can act on. It
// opened its form regardless, took every answer again, and failed at the end
// with a sentence about a command nobody had run.
type infoView struct {
	Installed bool   `json:"installed" yaml:"installed"`
	Running   bool   `json:"running" yaml:"running"`
	URL       string `json:"url,omitempty" yaml:"url,omitempty"`
	SignIn    string `json:"sign_in,omitempty" yaml:"sign_in,omitempty"`
	Root      string `json:"root" yaml:"root"`
}

func newInfoCmd(opts *cli.Options, find layoutFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Say whether Openarity is installed here, and where to reach it",
		Long: "Answers rather than refuses: an install that is not there is\n" +
			"installed: false, not an error. Written for the installer window,\n" +
			"which has to decide what to show before it shows anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			layout, err := find()
			if err != nil {
				return err
			}

			view := infoView{Root: layout.Root}
			if !layout.Installed() {
				return print(opts, view)
			}
			view.Installed = true

			state, err := engine.LoadState(layout.State)
			if err != nil {
				return err
			}
			view.URL = fmt.Sprintf("http://127.0.0.1:%d/ui", state.Ports.API)
			view.SignIn = engine.DexUser
			view.Running = running(layout) != 0

			return print(opts, view)
		},
	}
}

func print(opts *cli.Options, view infoView) error {
	return opts.Out.Print(view, printer.Options{
		Table: func(table *printer.Table) {
			if !view.Installed {
				table.Row("installed", "no")
				table.Row("would install into", view.Root)
				return
			}
			table.Row("installed", view.Root)
			table.Row("running", map[bool]string{true: "yes", false: "no"}[view.Running])
			table.Row("address", opts.Styles.Value.Render(view.URL))
			table.Row("sign in as", view.SignIn)
		},
	})
}

// howLongToWaitForStopped is generous because the slow part is Postgres
// flushing, and the failure it guards against is waiting forever rather than
// waiting a while.
const howLongToWaitForStopped = 2 * time.Minute

func waitForStopped(ctx context.Context, pid int) error {
	deadline := time.Now().Add(howLongToWaitForStopped)
	for alive(pid) {
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"stack: the supervisor (pid %d) was asked to stop %s ago and is still running",
				pid, howLongToWaitForStopped)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// newPassphraseCmd replaces the sign-in nobody can recover.
//
// The passphrase is shown once and kept only as a bcrypt hash, which is the
// right design and left exactly one answer for losing it: delete the install
// directory and start again — throwing away the database, the identity and
// every connection to get a new password. This replaces the hash and leaves
// the rest alone.
func newPassphraseCmd(opts *cli.Options, find layoutFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "passphrase",
		Short: "Set a new sign-in passphrase, and show it once",
		Long: "For a passphrase that was lost. It cannot be recovered — only a\n" +
			"bcrypt hash is kept — so this makes a new one and restarts the\n" +
			"identity provider, which reads its configuration only at startup.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			layout, state, err := requireInstall(find)
			if err != nil {
				return err
			}

			passphrase, err := engine.ResetPassphrase(filepath.Join(layout.Dex, "config.yaml"), "")
			if err != nil {
				return err
			}

			// Shown before anything else is attempted. It is already written
			// into dex's configuration by this point and exists nowhere else,
			// so a restart that fails after this line must not take it with
			// it — which is exactly what the first version did, on a machine
			// whose secret store had gone away: the reset succeeded, the
			// restart did not, and the new passphrase was lost with the old
			// one.
			opts.Out.Note("")
			opts.Out.Note("  sign in as   " + engine.DexUser)
			opts.Out.Note("  passphrase   " + passphrase)
			opts.Out.Note("")
			opts.Out.Note("Write it down — it is not stored anywhere and cannot be shown again.")

			// dex reads its configuration at startup and nowhere else, so
			// until it is restarted the old hash is what the sign-in page is
			// still checking against.
			if pid := running(layout); pid != 0 {
				return restart(cmd.Context(), opts, layout, state)
			}
			opts.Out.Note("It works the next time Openarity starts.")
			return nil
		},
	}
}

// restart stops the install and starts it again, which is what it takes for
// dex to read a configuration it only reads at startup.
func restart(ctx context.Context, opts *cli.Options, layout engine.Layout, state engine.State) error {
	pid, err := readPID(layout)
	if err != nil || pid == 0 {
		return err
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("stack: finding the supervisor (pid %d): %w", pid, err)
	}
	if err := interrupt(layout.Root, proc); err != nil {
		return fmt.Errorf("stack: stopping the supervisor (pid %d): %w", pid, err)
	}
	if err := waitForStopped(ctx, pid); err != nil {
		return err
	}

	opts.Out.Note("restarting")
	if err := spawnSupervisor(layout.Root); err != nil {
		return err
	}
	return waitForReady(ctx, state.Ports.API)
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
				"gateway":  state.Ports.Gateway,
				"dex":      state.Ports.Dex,
				"brain":    state.Ports.API,
				"worker":   0,
			}

			// make with a length, never a nil slice: nil marshals to null and
			// `jq length` fails on it, which is exactly what an install with
			// nothing running would produce.
			views := make([]statusView, 0, len(componentsOf(state)))
			for _, name := range componentsOf(state) {
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
func componentsOf(state engine.State) []string {
	names := []string{"postgres"}
	if state.Settings.RunsGateway() {
		names = append(names, "gateway")
	}
	return append(names, "dex", "brain", "worker")
}

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
