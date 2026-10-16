package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
)

const maxZipBytes = skill.MaxSkillBytes + 1<<20

func ParseZipURL(s string) (string, error) {
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "" || u.Opaque != "":
		return "", errors.New("a zip source is an https:// address")
	case u.User != nil:
		return "", errors.New("a zip source may not carry a user or password; the address is stored and shown")
	case u.RawQuery != "" || u.ForceQuery:
		return "", errors.New("a zip source may not carry a query string; the address is stored and shown")
	case u.Fragment != "":
		return "", errors.New("a zip source may not carry a #fragment")
	}
	return u.String(), nil
}

type Zips struct {
	Client *http.Client
}

func (z Zips) Fetch(ctx context.Context, address string) (string, []skill.Entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", nil, err
	}

	res, err := z.Client.Do(req)
	if err != nil {
		return "", nil, unreachable(err)
	}
	defer func() { _ = res.Body.Close() }()

	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return "", nil, fmt.Errorf("%w: nothing is at %s", ErrNotFound, address)
	case res.StatusCode != http.StatusOK:
		return "", nil, fmt.Errorf("%w: %s answered %d", ErrUnavailable, req.URL.Host, res.StatusCode)
	}

	tooBig := fmt.Errorf("the zip is over %d MiB", maxZipBytes>>20)
	if res.ContentLength > maxZipBytes {
		return "", nil, tooBig
	}
	data, err := io.ReadAll(&capped{r: upstream{res.Body}, left: maxZipBytes, err: tooBig})
	if err != nil {
		return "", nil, err
	}

	entries, err := skill.ReadZip(data)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), entries, nil
}
