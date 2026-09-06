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
	StartStack func(context.Context, Plan) error
	Open       func(url string) error
}

type Setup struct {
	Layout   Layout
	Finder   Finder
	Steps    Steps
	Versions Versions
}

type Result struct {
	Plan       Plan
	Passphrase string
	URL        string
}

var required = []string{"postgres", "dex", "brain"}

func (s *Setup) Run(ctx context.Context) (Result, error) {
	if s.Layout.Installed() {
		return Result{}, fmt.Errorf("%w: %s", ErrInstalled, s.Layout.State)
	}

	plan := Plan{
		Layout:   s.Layout,
		Versions: s.Versions,
		Arch:     runtime.GOARCH,
		Binaries: map[string]string{},
	}

	for _, name := range required {
		path, err := s.Finder.Find(name)
		if err != nil {
			return Result{}, err
		}
		plan.Binaries[name] = path
	}

	if err := s.Layout.Create(); err != nil {
		return Result{}, err
	}

	if err := ensureSecret(s.Layout); err != nil {
		return Result{}, err
	}

	ports, err := pickPorts(ctx)
	if err != nil {
		return Result{}, err
	}
	plan.Ports = ports

	if !exists(filepath.Join(s.Layout.Data, "PG_VERSION")) {
		if err := s.Steps.InitDB(ctx, plan); err != nil {
			return Result{}, err
		}
	}

	if err := s.Steps.CreateDBs(ctx, plan); err != nil {
		return Result{}, err
	}
	if err := s.Steps.Migrate(ctx, plan); err != nil {
		return Result{}, err
	}

	passphrase, err := s.configureDex(ctx, plan)
	if err != nil {
		return Result{}, err
	}

	if err := s.Steps.StartStack(ctx, plan); err != nil {
		return Result{}, err
	}

	if err := SaveState(s.Layout.State, State{
		Root:     s.Layout.Root,
		Versions: plan.Versions,
		Ports:    plan.Ports,
		Arch:     plan.Arch,
		Binaries: plan.Binaries,
	}); err != nil {
		return Result{}, err
	}

	url := fmt.Sprintf("http://%s:%d/ui", loopback, plan.Ports.API)

	_ = s.Steps.Open(url)

	return Result{Plan: plan, Passphrase: passphrase, URL: url}, nil
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

func pickPorts(ctx context.Context) (Ports, error) {
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
		return 0, errors.New("stack: could not find four distinct free ports")
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
	return p, nil
}
