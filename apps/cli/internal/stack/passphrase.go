package stack

import (
	"fmt"
	"os"
	"regexp"

	"golang.org/x/crypto/bcrypt"

	"github.com/LaplacianAI/openarity/apps/cli/internal/atomicfile"
)

// Resetting the sign-in.
//
// The passphrase is generated during setup, shown once, and stored only as a
// bcrypt hash in dex's configuration — so it cannot be recovered, and that is
// deliberate. What was not deliberate is the only answer we had for somebody
// who lost it: delete the install directory and set everything up again,
// which throws away the database, the identity and every connection along
// with the password.
//
// This replaces the hash and leaves the rest alone.

// The line dex holds the hash on. Matched rather than parsed as YAML because
// the file is written by this tool, from a template, and round-tripping it
// through a YAML library would reorder and reformat somebody's configuration
// for no reason. The template is in the command package; this is the one
// field it may rewrite.
// No trailing \s*$: \s matches a newline and is greedy, so the match swallowed
// the line ending and the replacement wrote the file back one line shorter.
// Caught by a test that compared every other line rather than only looking
// for the new hash.
var dexHash = regexp.MustCompile(`(?m)^(\s*hash:\s*)"[^"]*"`)

// NewPassphrase is one the person did not choose, which is the point: it is
// twenty characters from an alphabet with no lookalikes in it.
func NewPassphrase() (string, error) { return newPassphrase() }

// ResetPassphrase writes a new hash into dex's configuration and returns the
// passphrase it hashed. A blank one is generated.
//
// dex reads its configuration at startup and nowhere else, so this takes
// effect when dex is restarted — which is why the command that calls it says
// so rather than leaving somebody to find out at the sign-in page.
func ResetPassphrase(path, chosen string) (string, error) {
	current, err := os.ReadFile(path) //nolint:gosec // a path from Layout
	if err != nil {
		return "", fmt.Errorf("stack: reading %s: %w", path, err)
	}

	passphrase := chosen
	if passphrase == "" {
		if passphrase, err = newPassphrase(); err != nil {
			return "", err
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(passphrase), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("stack: hashing the passphrase: %w", err)
	}

	if !dexHash.Match(current) {
		return "", fmt.Errorf("stack: %s has no password hash to replace — it is not a configuration this wrote", path)
	}

	// ReplaceAll with a literal replacement: a bcrypt hash contains $ and
	// slashes, and $1 in a replacement template means something. Expand-free
	// substitution is the only safe form here.
	updated := dexHash.ReplaceAllFunc(current, func(line []byte) []byte {
		prefix := dexHash.FindSubmatch(line)[1]
		return append(append([]byte{}, prefix...), []byte(`"`+string(hash)+`"`)...)
	})

	if err := atomicfile.Write(path, updated); err != nil {
		return "", err
	}
	return passphrase, nil
}
