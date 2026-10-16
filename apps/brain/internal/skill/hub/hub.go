package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
)

var (
	ErrInvalid     = errors.New("not a skill source")
	ErrUnavailable = errors.New("the source could not be reached")
)

type Import struct {
	Kind    string
	Ref     string
	SHA     string
	Entries []skill.Entry
}

type Hub struct {
	GitHub GitHub
	Zips   Zips
	Token  func(context.Context) (string, error)
}

func (h Hub) Fetch(ctx context.Context, source string) (Import, error) {
	switch {
	case strings.HasPrefix(source, "github:"):
		return h.fetchGitHub(ctx, source)
	case strings.HasPrefix(strings.ToLower(source), "https://"):
		return h.fetchZip(ctx, source)
	default:
		return Import{}, fmt.Errorf("%w: a source is github:owner/repo/path@ref, or the https:// address of a zip", ErrInvalid)
	}
}

func (h Hub) fetchGitHub(ctx context.Context, source string) (Import, error) {
	src, err := ParseGitHub(source)
	if err != nil {
		return Import{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	g := h.GitHub
	if h.Token != nil {
		if g.Token, err = h.Token(ctx); err != nil {
			return Import{}, fmt.Errorf("%w: reading the GitHub token: %w", ErrUnavailable, err)
		}
	}

	sha, entries, err := g.Fetch(ctx, src)
	if err != nil {
		return Import{}, err
	}
	return Import{Kind: "github", Ref: src.String(), SHA: sha, Entries: entries}, nil
}

func (h Hub) fetchZip(ctx context.Context, source string) (Import, error) {
	address, err := ParseZipURL(source)
	if err != nil {
		return Import{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	sha, entries, err := h.Zips.Fetch(ctx, address)
	if err != nil {
		return Import{}, err
	}
	return Import{Kind: "url", Ref: address, SHA: sha, Entries: entries}, nil
}

type upstream struct{ r io.Reader }

func (u upstream) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return n, err
}

func unreachable(err error) error {
	if errors.Is(err, ErrRefused) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}
