package stack

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

		CreateDBs: func(ctx context.Context, p engine.Plan) error {
			for _, name := range []string{"openarity", "dex"} {
				cmd := command(ctx, p.Binaries["postgres"], "--single", "-D", p.Layout.Data, "postgres")
				cmd.Stdin = strings.NewReader("CREATE DATABASE " + name + ";\n")
				if err := capture(cmd, "creating "+name); err != nil && !alreadyExists(err) {
					return err
				}
			}
			return startPostgres(ctx, p)
		},

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

	p := engine.Plan{Layout: layout, Ports: state.Ports, Binaries: binaries}
	log := filepath.Join(layout.Logs, "openarity.log")

	postgres := &engine.Child{
		Name: "postgres", Path: binaries["postgres"], Log: log,
		Args: []string{
			"-D", layout.Data, "-p", strconv.Itoa(state.Ports.Postgres),
			"-c", "listen_addresses=127.0.0.1",
		},
	}
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

	return &engine.Stack{
		ReadyTimeout: readyTimeout,
		StopGrace:    stopGrace,
		Components: []engine.Component{
			{
				Name: "postgres", Child: postgres,
				Ready: func(ctx context.Context) error {
					var d net.Dialer
					conn, err := d.DialContext(ctx, "tcp",
						net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Ports.Postgres)))
					if err != nil {
						return err
					}
					return conn.Close()
				},
				Shutdown: func(ctx context.Context) error {
					return stopPostgres(ctx, p)
				},
			},
			{Name: "dex", Child: dex, Ready: probe(state.Ports.Dex, "/healthz")},
			{Name: "brain", Child: brain, Ready: probe(state.Ports.API, "/readyz")},
			{Name: "worker", Child: worker},
		},
	}, nil
}

func brainEnv(p engine.Plan) []string {
	return []string{
		"OPENARITY_POSTGRES_DSN=" + dsn(p, "openarity"),
		fmt.Sprintf("OPENARITY_API_BIND=127.0.0.1:%d", p.Ports.API),
		fmt.Sprintf("OPENARITY_WEBHOOK_BIND=127.0.0.1:%d", p.Ports.Webhook),
		"OPENARITY_OIDC_ENABLED=true",
		fmt.Sprintf("OPENARITY_OIDC_ISSUER=http://127.0.0.1:%d", p.Ports.Dex),
		"OPENARITY_OIDC_AUDIENCE=openarity",
		// The first person to sign in claims an install with no super admin.
		// It is the whole bootstrap: there is no other way to become one on
		// a machine with no operator.
		"OPENARITY_BOOTSTRAP_FIRST_USER=true",
		"OPENARITY_OBJECTS_BACKEND=filesystem",
		"OPENARITY_OBJECTS_PATH=" + filepath.Join(p.Layout.Root, "objects"),
		"OPENARITY_ENVIRONMENT=development",
	}
}

// dsn points at the Unix socket in the data directory rather than at a TCP
// port. Nothing outside this install can reach it, and there is no password
// to store anywhere as a result.
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
		"    database: dex\n" +
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
func capture(cmd *exec.Cmd, what string) error {
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w\n%s", what, err, strings.TrimSpace(string(out)))
}

func alreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
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
