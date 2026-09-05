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

// Plan is what setup decided, handed to every step so none of them has to
// work it out again.
type Plan struct {
	Layout   Layout
	Ports    Ports
	Versions Versions
	Arch     string

	// Resolved absolute paths, keyed by binary name. Populated before any
	// step runs, so a missing binary is reported before there is a cluster
	// on disk to clean up.
	Binaries map[string]string
}

// Steps are the parts that touch the world. They are fields rather than
// method calls so the ordering, the resumption and what reaches disk can be
// tested without a Postgres — those are this file's concern; whether initdb
// works is Postgres's.
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

// Result is what the command prints. Passphrase is empty when setup resumed
// over an install that already had dex configured — the existing sign-in is
// still valid, and regenerating it would silently invalidate the one the
// person wrote down.
type Result struct {
	Plan       Plan
	Passphrase string
	URL        string
}

// The binaries every install needs. Resolved together, up front.
var required = []string{"postgres", "dex", "brain"}

// Run performs the install, and is safe to run again after it fails.
//
// A laptop sleeps and wifi drops, so an interrupted setup is the common case
// rather than the exception. Every step is either idempotent or guarded by
// looking at what is actually on disk — never by a progress file, which can
// disagree with the disk and does so precisely when it matters.
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

	// Before anything is created. Finding out at step four that dex is
	// missing leaves a person with a cluster to clean up before they can
	// retry.
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

	ports, err := pickPorts(ctx)
	if err != nil {
		return Result{}, err
	}
	plan.Ports = ports

	// initdb only when there is no cluster. Running it over one destroys it,
	// and PG_VERSION is how Postgres itself decides a directory is a cluster.
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

	// Last, so the state file existing means a finished install and nothing
	// else. Every guard elsewhere reads it that way.
	if err := SaveState(s.Layout.State, State{
		Root:     s.Layout.Root,
		Versions: plan.Versions,
		Ports:    plan.Ports,
		Arch:     plan.Arch,
	}); err != nil {
		return Result{}, err
	}

	url := fmt.Sprintf("http://%s:%d/ui", loopback, plan.Ports.API)

	// Not fatal. A headless machine has no browser, and failing an otherwise
	// finished install over that would be absurd — the address is printed
	// either way.
	_ = s.Steps.Open(url)

	return Result{Plan: plan, Passphrase: passphrase, URL: url}, nil
}

// configureDex generates the sign-in and hands dex only a hash of it.
//
// It is skipped when dex is already configured, which is what makes a resumed
// setup safe: regenerating here would invalidate the passphrase the person
// wrote down during the attempt that failed, and nothing would say so.
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
	// The alphabet leaves out the characters people misread in a terminal
	// font: 0 and O, 1 and l and I. A passphrase is read off a screen and
	// typed into a browser, so an ambiguous character costs a failed login
	// nobody can explain.
	alphabet = "abcdefghijkmnpqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ23456789"

	// 20 characters of a 55-character alphabet is about 115 bits, which is
	// far more than a local sign-in needs and costs nothing to carry.
	passphraseLength = 20
)

// Precomputed because it is the modulus for every character drawn.
var alphabetLen = big.NewInt(int64(len(alphabet)))

func newPassphrase() (string, error) {
	out := make([]byte, passphraseLength)
	for i := range out {
		// crypto/rand.Int rather than a modulo of a random byte: 256 is not
		// a multiple of 55, so the naive version makes the first 36 letters
		// of the alphabet measurably likelier than the rest. Int rejects
		// out-of-range draws instead.
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

// pickPorts chooses four distinct ports, preferring the defaults.
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
			// PickPort binds to zero to find a free port and closes it
			// again, so two calls can return the same one. Asking without a
			// preference is what breaks the tie.
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
