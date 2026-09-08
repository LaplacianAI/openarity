package stack

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/atomicfile"
)

// State is what setup decided, written once and read by every command after
// it. YAML because the CLI already depends on it; a second serialisation
// format for one file would be a dependency bought for nothing.
//
// No credential appears here. The passphrase is printed once and never
// stored; dex holds only its bcrypt hash, in its own file.
type State struct {
	Root     string   `yaml:"root"`
	Versions Versions `yaml:"versions"`
	Ports    Ports    `yaml:"ports"`

	// The architecture actually installed, which is not always this machine's:
	// there are no Postgres binaries for windows/arm64, so an ARM Windows
	// install runs the amd64 build under emulation. Recording it means
	// `oa status` can say so rather than implying the install is native.
	Arch string `yaml:"arch"`

	Binaries map[string]string `yaml:"binaries"`

	Settings Settings `yaml:"settings"`
}

type Versions struct {
	Postgres string `yaml:"postgres"`
	Dex      string `yaml:"dex"`
	Brain    string `yaml:"brain"`
}

type Ports struct {
	API      int `yaml:"api"`
	Webhook  int `yaml:"webhook"`
	Dex      int `yaml:"dex"`
	Postgres int `yaml:"postgres"`
	MinIO    int `yaml:"minio,omitempty"`
	Gateway  int `yaml:"gateway,omitempty"`
}

// ErrInstalled is returned when a state file already exists. Setup treats it
// as "you want start, not setup" rather than as a failure.
var ErrInstalled = errors.New("stack: already installed")

// SaveState refuses to overwrite. An install is a Postgres cluster with data
// in it, and a second `oa setup` that silently re-ran initdb over the top
// would destroy it — so the guard is here, at the write, rather than in the
// command that happens to call it.
func SaveState(path string, s State) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrInstalled, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeState(path, s)
}

// UpdateState overwrites deliberately, for `oa upgrade` — which changes the
// recorded versions of an install that already exists. A separate function
// rather than a boolean argument: `SaveState(path, s, true)` at a call site
// says nothing about what the true means.
func UpdateState(path string, s State) error { return writeState(path, s) }

func writeState(path string, s State) error {
	out, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, out)
}

func LoadState(path string) (State, error) {
	raw, err := atomicfile.Read(path)
	if err != nil {
		return State{}, err
	}

	var s State
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("stack: reading %s: %w", path, err)
	}
	return s, nil
}
