package skill

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

const (
	MaxFileBytes  = 5 << 20
	MaxSkillBytes = 20 << 20
	MaxFiles      = 200

	maxPathBytes = 1024
)

var errNoManifest = errors.New(
	"SKILL.md must be at the top of the skill, or inside one directory named as the skill",
)

type Entry struct {
	Path string
	Data []byte
}

type File struct {
	Path      string
	Data      []byte
	SHA256    [sha256.Size]byte
	MediaType string
}

type Directory struct {
	Manifest Manifest
	Files    []File
}

func Assemble(entries []Entry) (Directory, error) {
	if len(entries) > MaxFiles {
		return Directory{}, fmt.Errorf("a skill holds at most %d files", MaxFiles)
	}

	total := 0
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if err := checkPath(e.Path); err != nil {
			return Directory{}, err
		}
		if seen[e.Path] {
			return Directory{}, fmt.Errorf("%q is sent twice", e.Path)
		}
		seen[e.Path] = true

		if len(e.Data) > MaxFileBytes {
			return Directory{}, fmt.Errorf("%q is over %d MiB", e.Path, MaxFileBytes>>20)
		}
		total += len(e.Data)
		if total > MaxSkillBytes {
			return Directory{}, fmt.Errorf("a skill is at most %d MiB", MaxSkillBytes>>20)
		}
	}

	root, err := rootOf(entries)
	if err != nil {
		return Directory{}, err
	}

	var dir Directory
	for _, e := range entries {
		rel := strings.TrimPrefix(e.Path, root)
		switch {
		case rel == ManifestName:
			m, err := Parse(e.Data)
			if err != nil {
				return Directory{}, err
			}
			dir.Manifest = m

		case path.Base(rel) == ManifestName:
			return Directory{}, fmt.Errorf("%q is a second SKILL.md; send one skill at a time", e.Path)

		default:
			dir.Files = append(dir.Files, File{
				Path:      rel,
				Data:      e.Data,
				SHA256:    sha256.Sum256(e.Data),
				MediaType: objects.Sniff(e.Data),
			})
		}
	}

	if name := strings.TrimSuffix(root, "/"); name != "" && name != dir.Manifest.Name {
		return Directory{}, fmt.Errorf("the directory is named %q but its SKILL.md names the skill %q",
			name, dir.Manifest.Name)
	}

	slices.SortFunc(dir.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return dir, nil
}

func rootOf(entries []Entry) (string, error) {
	if slices.ContainsFunc(entries, func(e Entry) bool { return e.Path == ManifestName }) {
		return "", nil
	}
	if len(entries) == 0 {
		return "", errNoManifest
	}

	top, _, _ := strings.Cut(entries[0].Path, "/")
	for _, e := range entries {
		dir, _, nested := strings.Cut(e.Path, "/")
		if !nested || dir != top {
			return "", errNoManifest
		}
	}

	root := top + "/"
	if !slices.ContainsFunc(entries, func(e Entry) bool { return e.Path == root+ManifestName }) {
		return "", errNoManifest
	}
	return root, nil
}

func checkPath(p string) error {
	switch {
	case !utf8.ValidString(p):
		return fmt.Errorf("the file path %q is not valid UTF-8", p)
	case len(p) > maxPathBytes:
		return fmt.Errorf("a file path is over %d bytes", maxPathBytes)
	case strings.ContainsRune(p, '\\'):
		return fmt.Errorf("%q uses a backslash; separate directories with /", p)
	case strings.ContainsFunc(p, unicode.IsControl):
		return fmt.Errorf("%q contains a control character", p)
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%q is not a relative path inside the skill", p)
		}
	}
	return nil
}
