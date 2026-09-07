package stack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	engine "github.com/LaplacianAI/openarity/apps/cli/internal/stack"
)

const (
	readyTimeout = 60 * time.Second
	stopGrace    = 10 * time.Second
)

func realSteps() engine.Steps {
	return engine.Steps{
		InitDB: func(ctx context.Context, p engine.Plan) error {
			return run(ctx, p, beside(p.Binaries["postgres"], "initdb"),
				"-D", p.Layout.Data, "--auth=scram-sha-256",
				"--pwfile="+p.Layout.Secret, "--username=openarity", "--encoding=UTF8")
		},

		CreateDBs: startPostgres,

		Migrate: func(ctx context.Context, p engine.Plan) error {
			cmd := command(ctx, p.Binaries["brain"], "migrate", "up")
			cmd.Env = brainEnv(p)
			return capture(cmd, "brain migrate up")
		},

		WriteDex: func(_ context.Context, p engine.Plan, hash string) error {
			return os.WriteFile(filepath.Join(p.Layout.Dex, "config.yaml"),
				[]byte(dexConfig(p, hash)), 0o600)
		},

		StartStack: stopPostgres,

		Open: openBrowser,
	}
}

func build(ctx context.Context, layout engine.Layout, state engine.State) (*engine.Stack, error) {
	settings := state.Settings

	binaries := state.Binaries
	if len(binaries) == 0 {
		finder := engine.LocalFinder{Dir: layout.Bin}

		binaries = map[string]string{}
		for _, name := range []string{"postgres", "dex", "brain"} {
			path, err := finder.Find(ctx, name)
			if err != nil {
				return nil, err
			}
			binaries[name] = path
		}
	}

	p := engine.Plan{Layout: layout, Settings: state.Settings, Ports: state.Ports, Binaries: binaries}
	log := filepath.Join(layout.Logs, "openarity.log")

	dex := &engine.Child{
		Name: "dex", Path: binaries["dex"], Log: log,
		Args: []string{"serve", filepath.Join(layout.Dex, "config.yaml")},
	}
	brain := &engine.Child{
		Name: "brain", Path: binaries["brain"], Log: log,
		Args: []string{"serve"}, Env: brainEnv(p),
	}
	worker := &engine.Child{
		Name: "worker", Path: binaries["brain"], Log: log,
		Args: []string{"worker"}, Env: brainEnv(p),
	}

	// Before the brain, which stores files in it. MINIO_ROOT_USER and
	// MINIO_ROOT_PASSWORD are the same pair the brain uses as its S3 access
	// key and secret — one credential, two names for it.
	var objectStore []engine.Component
	if settings.RunsMinIO() {
		credentials, err := engine.ReadCredentials(layout.Env)
		if err != nil {
			return nil, err
		}

		minio := &engine.Child{
			Name: "minio", Path: binaries["minio"], Log: log,
			Args: []string{
				"server", settings.MinIOPath,
				"--address", net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Ports.MinIO)),
			},
			Env: []string{
				"MINIO_ROOT_USER=" + credentials["OPENARITY_OBJECTS_ACCESS_KEY"],
				"MINIO_ROOT_PASSWORD=" + credentials["OPENARITY_OBJECTS_SECRET_KEY"],
				// Without this MinIO writes a console banner naming its own
				// root credentials, into the log a person attaches to a bug
				// report.
				"MINIO_BROWSER=off",
				// MinIO shells out, so it needs a PATH. Two named variables
				// rather than the parent's whole environment: the point of an
				// empty one is that no OPENARITY_ value leaks in and quietly
				// overrides what the installer wrote.
				"PATH=" + os.Getenv("PATH"),
				// Its own directory, so it writes no configuration into the
				// person's home.
				"HOME=" + layout.Root,
			},
		}

		objectStore = []engine.Component{{
			Name:  "minio",
			Child: minio,
			Ready: probe(state.Ports.MinIO, "/minio/health/live"),
		}}
	}

	postgres := engine.Component{
		Name:        "postgres",
		BeforeStart: func(ctx context.Context) error { return startPostgres(ctx, p) },
		Ready: func(ctx context.Context) error {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp",
				net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Ports.Postgres)))
			if err != nil {
				return err
			}
			return conn.Close()
		},
		Shutdown: func(ctx context.Context) error { return stopPostgres(ctx, p) },
	}

	components := []engine.Component{postgres}
	components = append(components, objectStore...)
	components = append(components,
		engine.Component{Name: "dex", Child: dex, Ready: probe(state.Ports.Dex, "/healthz")},
		engine.Component{Name: "brain", Child: brain, Ready: probe(state.Ports.API, "/readyz")},
		engine.Component{Name: "worker", Child: worker},
	)

	return &engine.Stack{
		ReadyTimeout: readyTimeout,
		StopGrace:    stopGrace,
		Components:   components,
	}, nil
}

func brainEnv(p engine.Plan) []string {
	out := []string{
		"OPENARITY_POSTGRES_DSN=" + dsn(p, database),
		fmt.Sprintf("OPENARITY_API_BIND=127.0.0.1:%d", p.Ports.API),
		fmt.Sprintf("OPENARITY_WEBHOOK_BIND=127.0.0.1:%d", p.Ports.Webhook),
		"OPENARITY_OIDC_ENABLED=true",
		fmt.Sprintf("OPENARITY_OIDC_ISSUER=http://127.0.0.1:%d", p.Ports.Dex),
		"OPENARITY_OIDC_AUDIENCE=openarity",
		// The first person to sign in claims an install with no super admin.
		// It is the whole bootstrap: there is no other way to become one on
		// a machine with no operator.
		"OPENARITY_BOOTSTRAP_FIRST_USER=true",
		"OPENARITY_OBJECTS_PATH=" + filepath.Join(p.Layout.Root, "objects"),
		"OPENARITY_ENVIRONMENT=" + p.Settings.Environment(),
	}

	out = append(out, p.Settings.Env()...)

	credentials, err := engine.ReadCredentials(p.Layout.Env)
	if err != nil {
		return out
	}
	for key, value := range credentials {
		out = append(out, key+"="+value)
	}
	return out
}

// dsn points at the Unix socket in the data directory rather than at a TCP
// port. Nothing outside this install can reach it, and there is no password
// to store anywhere as a result.
const database = "postgres"

func dsn(p engine.Plan, database string) string {
	return fmt.Sprintf("postgres://openarity:%s@127.0.0.1:%d/%s?sslmode=disable",
		url.QueryEscape(password(p)), p.Ports.Postgres, database)
}

func password(p engine.Plan) string {
	raw, err := os.ReadFile(p.Layout.Secret) //nolint:gosec // a path from Layout
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func dexConfig(p engine.Plan, hash string) string {
	issuer := fmt.Sprintf("http://127.0.0.1:%d", p.Ports.Dex)
	dashboard := fmt.Sprintf("http://127.0.0.1:%d", p.Ports.API)

	// Written rather than templated from deployment/dex/config.yaml: that one
	// uses sqlite3 storage, which a CGO-free dex does not have. The two are
	// deliberately different files and must not be assumed identical.
	return "" +
		"issuer: " + issuer + "\n" +
		"storage:\n" +
		"  type: postgres\n" +
		"  config:\n" +
		"    host: 127.0.0.1\n" +
		"    port: " + strconv.Itoa(p.Ports.Postgres) + "\n" +
		"    database: postgres\n" +
		"    user: openarity\n" +
		"    password: \"" + password(p) + "\"\n" +
		"    ssl:\n" +
		"      mode: disable\n" +
		"web:\n" +
		"  http: 127.0.0.1:" + strconv.Itoa(p.Ports.Dex) + "\n" +
		"  allowedOrigins:\n" +
		"    - " + dashboard + "\n" +
		"oauth2:\n" +
		"  skipApprovalScreen: true\n" +
		"staticClients:\n" +
		"  - id: openarity\n" +
		"    name: Openarity\n" +
		"    public: true\n" +
		"    redirectURIs:\n" +
		// dex redirects here internally during the device flow. Without it
		// `oa login` fails with "Unregistered redirect_uri".
		"      - /device/callback\n" +
		"      - " + dashboard + "/ui/callback\n" +
		"enablePasswordDB: true\n" +
		"staticPasswords:\n" +
		"  - email: dev@openarity.local\n" +
		"    username: dev\n" +
		// Pinned, so `sub` survives deleting and recreating the install —
		// otherwise every reinstall makes a new user and the old one keeps
		// the super-admin bit.
		"    userID: 0d1e9f3c-6a52-4f5d-8b71-2c4e6a8d0f13\n" +
		"    hash: \"" + hash + "\"\n"
}

// startPostgres and stopPostgres go through pg_ctl, which waits for the
// server to actually be up or down rather than returning once the signal is
// sent.
func startPostgres(ctx context.Context, p engine.Plan) error {
	return run(ctx, p, beside(p.Binaries["postgres"], "pg_ctl"),
		"-D", p.Layout.Data, "-w", "-l", filepath.Join(p.Layout.Logs, "postgres.log"),
		"-o", fmt.Sprintf("-p %d -c listen_addresses=127.0.0.1", p.Ports.Postgres),
		"start")
}

func stopPostgres(ctx context.Context, p engine.Plan) error {
	err := run(ctx, p, beside(p.Binaries["postgres"], "pg_ctl"), "-D", p.Layout.Data, "-m", "fast", "-w", "stop")
	// Already stopped is the state we wanted.
	if err != nil && strings.Contains(err.Error(), "not running") {
		return nil
	}
	return err
}

// beside finds a sibling of a known binary. initdb, createdb, pg_ctl and
// pg_isready ship in the same directory as postgres, so locating one locates
// all five.
func beside(known, name string) string {
	return filepath.Join(filepath.Dir(known), name+exeSuffix())
}

func exeSuffix() string {
	if os.PathSeparator == '\\' {
		return ".exe"
	}
	return ""
}

func command(ctx context.Context, path string, args ...string) *exec.Cmd {
	//nolint:gosec // paths come from Layout and the finder, never from input
	return exec.CommandContext(ctx, path, args...)
}

func run(ctx context.Context, _ engine.Plan, path string, args ...string) error {
	return capture(command(ctx, path, args...), filepath.Base(path))
}

// capture puts the tool's own output in the error. "exit status 1" is the
// least useful sentence a person can be shown, and initdb explains itself
// perfectly well if anyone passes the explanation on.
// capture runs a command and keeps what it said, through a file rather than a
// pipe.
//
// os/exec only builds a pipe and a copying goroutine when Stdout is not an
// *os.File, and Wait blocks until that goroutine reaches EOF — which needs
// every holder of the write end to let go, not just the process that was
// started. pg_ctl's whole job is to leave a postgres running, and on Windows a
// child inherits every inheritable handle, so that server holds the pipe open
// for as long as it serves. CombinedOutput therefore never returns: setup
// stopped at "Creating the database" and sat there until the job was cancelled.
//
// A file is passed to the child as a handle, so there is no goroutine to wait
// for and Wait returns when the process does. It is also what Child already
// does for the supervised processes, for the same reason.
func capture(cmd *exec.Cmd, what string) error {
	out, err := os.CreateTemp("", "openarity-*.log")
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(out.Name())
	}()

	cmd.Stdout, cmd.Stderr = out, out

	runErr := cmd.Run()
	if runErr == nil {
		return nil
	}

	// Read back through the handle already open. Opening the path again would
	// be a second handle on a file this process is holding, which Windows is
	// particular about.
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%s: %w", what, runErr)
	}
	said, err := io.ReadAll(out)
	if err != nil {
		return fmt.Errorf("%s: %w", what, runErr)
	}
	return fmt.Errorf("%s: %w\n%s", what, runErr, strings.TrimSpace(string(said)))
}

// probe reports readiness by asking over HTTP, which is the only thing that
// says a server is answering rather than merely listening.
func probe(port int, path string) func(context.Context) error {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)

	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = res.Body.Close() }()

		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("%s answered %d", url, res.StatusCode)
		}
		return nil
	}
}

func writePID(layout engine.Layout) error {
	return os.WriteFile(filepath.Join(layout.Root, pidFile),
		[]byte(strconv.Itoa(os.Getpid())), 0o600)
}

// readPID returns zero when there is no file, which means nothing is running
// rather than something went wrong.
func readPID(layout engine.Layout) (int, error) {
	raw, err := os.ReadFile(filepath.Join(layout.Root, pidFile)) //nolint:gosec // a path from Layout
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("stack: %s does not hold a pid: %w", pidFile, err)
	}
	return pid, nil
}

func withoutCancel(cmd *cobra.Command) context.Context {
	return context.WithoutCancel(cmd.Context())
}

func enableAutostart(ctx context.Context, layout engine.Layout) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	unit, err := engine.UnitFor(runtime.GOOS, home, exe, layout.Root)
	if err != nil {
		return err
	}
	return engine.InstallAutostart(ctx, unit)
}
