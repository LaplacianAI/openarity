package stack

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/crypto/bcrypt"
)

type Plan struct {
	Layout   Layout
	Settings Settings
	Ports    Ports
	Versions Versions
	Arch     string

	Binaries map[string]string
}

type Steps struct {
	InitDB     func(context.Context, Plan) error
	CreateDBs  func(context.Context, Plan) error
	Migrate    func(context.Context, Plan) error
	WriteDex   func(ctx context.Context, p Plan, bcryptHash string) error
	Gateway    func(context.Context, Plan) error
	StartStack func(context.Context, Plan) error
	Open       func(url string) error
}

type Setup struct {
	Layout      Layout
	Finder      Finder
	Steps       Steps
	Versions    Versions
	Settings    Settings
	Credentials map[string]string
	Report      Reporter
}

type Result struct {
	Plan       Plan
	Passphrase string
	URL        string

	// The gateway's own dashboard password, when one was generated here
	// rather than chosen. Shown once, beside the passphrase, and stored only
	// in the credentials file.
	GatewayPassword string
}

var required = []string{"postgres", "dex", "brain"}

// MinIO is only needed by an install that runs one, so it is resolved
// separately rather than being a fourth entry above — otherwise every install
// would download 100MB it never starts.
func (s *Setup) needs() []string {
	if s.Settings.RunsMinIO() {
		return append(append([]string{}, required...), "minio")
	}
	return required
}

func (s *Setup) Run(ctx context.Context) (Result, error) {
	if s.Layout.Installed() {
		return Result{}, fmt.Errorf("%w: %s", ErrInstalled, s.Layout.State)
	}

	plan := Plan{
		Layout:   s.Layout,
		Settings: s.Settings,
		Versions: s.Versions,
		Arch:     runtime.GOARCH,
		Binaries: map[string]string{},
	}

	s.report(Event{Step: StepResolve, Phase: PhaseStarted})
	for _, name := range s.needs() {
		path, err := s.Finder.Find(ctx, name)
		if err != nil {
			s.report(Event{Step: StepResolve, Phase: PhaseFailed, Detail: err.Error()})
			return Result{}, err
		}
		plan.Binaries[name] = path
	}
	s.report(Event{Step: StepResolve, Phase: PhaseDone})

	if err := s.Layout.Create(); err != nil {
		return Result{}, err
	}

	if err := ensureSecret(s.Layout); err != nil {
		return Result{}, err
	}

	if err := s.Settings.Validate(); err != nil {
		return Result{}, err
	}

	if s.Settings.RunsMinIO() {
		if err := s.prepareMinIO(); err != nil {
			return Result{}, err
		}
	}

	gatewayPassword, err := s.prepareGateway()
	if err != nil {
		return Result{}, err
	}
	if err := WriteCredentials(s.Layout.Env, s.Credentials); err != nil {
		return Result{}, err
	}

	ports, err := pickPorts(ctx, s.Settings.RunsMinIO(), s.Settings.RunsGateway())
	if err != nil {
		return Result{}, err
	}
	plan.Ports = ports

	if s.Settings.RunsGateway() {
		s.Settings.ModelBaseURL = fmt.Sprintf("http://%s:%d/v1", loopback, ports.Gateway)
		plan.Settings = s.Settings
	}

	if s.Settings.RunsMinIO() && s.Settings.ObjectsEndpoint == "" {
		s.Settings.ObjectsEndpoint = fmt.Sprintf("http://%s:%d", loopback, ports.MinIO)
		plan.Settings = s.Settings
	}

	s.report(Event{Step: StepCluster, Phase: PhaseStarted})
	if !exists(filepath.Join(s.Layout.Data, "PG_VERSION")) {
		if err := s.Steps.InitDB(ctx, plan); err != nil {
			s.report(Event{Step: StepCluster, Phase: PhaseFailed, Detail: err.Error()})
			return Result{}, err
		}
	}
	if err := s.Steps.CreateDBs(ctx, plan); err != nil {
		s.report(Event{Step: StepCluster, Phase: PhaseFailed, Detail: err.Error()})
		return Result{}, err
	}
	s.report(Event{Step: StepCluster, Phase: PhaseDone})

	s.report(Event{Step: StepMigrate, Phase: PhaseStarted})
	if err := s.Steps.Migrate(ctx, plan); err != nil {
		s.report(Event{Step: StepMigrate, Phase: PhaseFailed, Detail: err.Error()})
		return Result{}, err
	}
	s.report(Event{Step: StepMigrate, Phase: PhaseDone})

	// After the migrations and before the identity provider, because it is
	// the long one — a runtime and a gigabyte of packages — and a person
	// watching would rather see the quick steps land first than watch the
	// first bar for four minutes.
	if s.Settings.RunsGateway() {
		s.report(Event{Step: StepGateway, Phase: PhaseStarted, Detail: s.Settings.ModelBackend})
		if err := s.Steps.Gateway(ctx, plan); err != nil {
			s.report(Event{Step: StepGateway, Phase: PhaseFailed, Detail: err.Error()})
			return Result{}, err
		}
		s.report(Event{Step: StepGateway, Phase: PhaseDone})
	}

	s.report(Event{Step: StepIdentity, Phase: PhaseStarted})
	passphrase, err := s.configureDex(ctx, plan)
	if err != nil {
		s.report(Event{Step: StepIdentity, Phase: PhaseFailed, Detail: err.Error()})
		return Result{}, err
	}
	s.report(Event{Step: StepIdentity, Phase: PhaseDone})

	s.report(Event{Step: StepStart, Phase: PhaseStarted})
	if err := s.Steps.StartStack(ctx, plan); err != nil {
		s.report(Event{Step: StepStart, Phase: PhaseFailed, Detail: err.Error()})
		return Result{}, err
	}
	s.report(Event{Step: StepStart, Phase: PhaseDone})

	if err := SaveState(s.Layout.State, State{
		Root:     s.Layout.Root,
		Versions: plan.Versions,
		Ports:    plan.Ports,
		Arch:     plan.Arch,
		Binaries: plan.Binaries,
		Settings: s.Settings,
	}); err != nil {
		return Result{}, err
	}

	url := fmt.Sprintf("http://%s:%d/ui", loopback, plan.Ports.API)

	_ = s.Steps.Open(url)

	s.report(Event{
		Step: StepReady, Phase: PhaseDone, URL: url,
		Passphrase: passphrase, GatewayPassword: gatewayPassword,
	})

	return Result{
		Plan: plan, Passphrase: passphrase, URL: url,
		GatewayPassword: gatewayPassword,
	}, nil
}

// prepareMinIO generates its root credentials and makes the bucket.
//
// A bucket in a single-drive MinIO is a directory, so creating one needs no
// client — the same reason initdb could replace createdb. `mc` is a separate
// binary and this avoids downloading it for one mkdir.
func (s *Setup) prepareMinIO() error {
	if err := os.MkdirAll(filepath.Join(s.Settings.MinIOPath, s.Settings.ObjectsBucket), 0o700); err != nil {
		return err
	}

	if _, taken := s.Credentials["OPENARITY_OBJECTS_ACCESS_KEY"]; taken {
		return nil
	}

	user, err := newPassphrase()
	if err != nil {
		return err
	}
	password, err := newPassphrase()
	if err != nil {
		return err
	}

	if s.Credentials == nil {
		s.Credentials = map[string]string{}
	}
	s.Credentials["OPENARITY_OBJECTS_ACCESS_KEY"] = user
	s.Credentials["OPENARITY_OBJECTS_SECRET_KEY"] = password
	return nil
}

// prepareGateway makes sure OmniRoute has a dashboard password.
//
// Its image defaults one to CHANGEME and logs a warning nobody reads, so a
// gateway installed without being asked would come up with a password every
// reader of its documentation knows. If nobody chose one, one is generated and
// returned so it can be shown once — the same bargain as the sign-in
// passphrase.
//
// LiteLLM has no dashboard and needs none of this.
func (s *Setup) prepareGateway() (string, error) {
	if s.Settings.ModelBackend != "omniroute" {
		return "", nil
	}
	if s.Credentials["OPENARITY_GATEWAY_PASSWORD"] != "" {
		return "", nil
	}

	password, err := newPassphrase()
	if err != nil {
		return "", err
	}
	if s.Credentials == nil {
		s.Credentials = map[string]string{}
	}
	s.Credentials["OPENARITY_GATEWAY_PASSWORD"] = password
	return password, nil
}

func ensureSecret(layout Layout) error {
	if exists(layout.Secret) {
		return nil
	}

	password, err := newPassphrase()
	if err != nil {
		return err
	}
	return os.WriteFile(layout.Secret, []byte(password), 0o600)
}

func (s *Setup) configureDex(ctx context.Context, plan Plan) (string, error) {
	if exists(filepath.Join(s.Layout.Dex, "config.yaml")) {
		return "", nil
	}

	passphrase, err := newPassphrase()
	if err != nil {
		return "", err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(passphrase), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("stack: hashing the passphrase: %w", err)
	}

	if err := s.Steps.WriteDex(ctx, plan, string(hash)); err != nil {
		return "", err
	}
	return passphrase, nil
}

const (
	alphabet         = "abcdefghijkmnpqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ23456789"
	passphraseLength = 20
)

var alphabetLen = big.NewInt(int64(len(alphabet)))

func newPassphrase() (string, error) {
	out := make([]byte, passphraseLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", fmt.Errorf("stack: generating a passphrase: %w", err)
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pickPorts(ctx context.Context, withMinIO, withGateway bool) (Ports, error) {
	taken := map[int]bool{}

	pick := func(preferred int) (int, error) {
		for range 10 {
			port, err := PickPort(ctx, preferred)
			if err != nil {
				return 0, err
			}
			if !taken[port] {
				taken[port] = true
				return port, nil
			}
			preferred = 0
		}
		return 0, errors.New("stack: could not find enough distinct free ports")
	}

	var (
		p   Ports
		err error
	)
	if p.API, err = pick(DefaultAPIPort); err != nil {
		return Ports{}, err
	}
	if p.Webhook, err = pick(DefaultWebhookPort); err != nil {
		return Ports{}, err
	}
	if p.Dex, err = pick(DefaultDexPort); err != nil {
		return Ports{}, err
	}
	if p.Postgres, err = pick(DefaultPostgresPort); err != nil {
		return Ports{}, err
	}
	if withMinIO {
		if p.MinIO, err = pick(DefaultMinIOPort); err != nil {
			return Ports{}, err
		}
	}
	if withGateway {
		if p.Gateway, err = pick(DefaultGatewayPort); err != nil {
			return Ports{}, err
		}
	}
	return p, nil
}
