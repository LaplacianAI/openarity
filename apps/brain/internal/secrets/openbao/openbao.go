package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

const (
	baoTimeout  = 5 * time.Second
	maxBodySize = 1 << 20
	renewSkew   = 10 * time.Second
)

type store struct {
	addr     string
	roleID   string
	secretID string
	mount    string
	hc       *http.Client

	mu      sync.Mutex
	tok     string
	expires time.Time
}

var (
	_ secrets.Store  = (*store)(nil)
	_ secrets.Writer = (*store)(nil)
	_ secrets.Prober = (*store)(nil)
)

func New(addr, roleID, secretID, mount string, hc *http.Client) secrets.Store {
	if hc == nil {
		hc = &http.Client{Timeout: baoTimeout}
	}
	return &store{
		addr:     strings.TrimRight(addr, "/"),
		roleID:   roleID,
		secretID: secretID,
		mount:    mount,
		hc:       hc,
	}
}

func (s *store) dataURL(path string) string {
	return s.addr + "/v1/" + url.PathEscape(s.mount) + "/data/" + path
}

func (s *store) metadataURL(path string) string {
	return s.addr + "/v1/" + url.PathEscape(s.mount) + "/metadata/" + path
}

func (s *store) renew(ctx context.Context, current string) (time.Duration, error) {
	status, body, err := s.do(ctx, http.MethodPost,
		s.addr+"/v1/auth/token/renew-self", current, []byte(`{}`))
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("%w: renew-self returned %d", secrets.ErrUnavailable, status)
	}

	var doc struct {
		Auth struct {
			LeaseDuration int `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return 0, fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}
	return time.Duration(doc.Auth.LeaseDuration) * time.Second, nil
}

func (s *store) authToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	switch {
	case s.tok == "":
		// No session yet.
	case now.Before(s.expires.Add(-renewSkew)):
		return s.tok, nil
	default:
		if lease, err := s.renew(ctx, s.tok); err == nil {
			s.expires = now.Add(lease)
			return s.tok, nil
		}
	}

	tok, lease, err := s.login(ctx)
	if err != nil {
		return "", err
	}
	s.tok, s.expires = tok, now.Add(lease)
	return tok, nil
}

func (s *store) login(ctx context.Context) (string, time.Duration, error) {
	payload, err := json.Marshal(map[string]string{
		"role_id":   s.roleID,
		"secret_id": s.secretID,
	})
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}

	status, body, err := s.do(ctx, http.MethodPost,
		s.addr+"/v1/auth/approle/login", "", payload)
	if err != nil {
		return "", 0, err
	}
	if status != http.StatusOK {
		return "", 0, fmt.Errorf("%w: approle login returned %d", secrets.ErrUnavailable, status)
	}

	var doc struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", 0, fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}
	if doc.Auth.ClientToken == "" {
		return "", 0, fmt.Errorf("%w: approle login returned no token", secrets.ErrUnavailable)
	}
	return doc.Auth.ClientToken, time.Duration(doc.Auth.LeaseDuration) * time.Second, nil
}

func (s *store) do(
	ctx context.Context, method, endpoint, tok string, payload []byte,
) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}
	if tok != "" {
		req.Header.Set("X-Vault-Token", tok)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}
	return resp.StatusCode, body, nil
}

func (s *store) write(ctx context.Context, method, endpoint string, payload []byte) error {
	tok, err := s.authToken(ctx)
	if err != nil {
		return err
	}

	status, _, err := s.do(ctx, method, endpoint, tok, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("%w: write returned %d", secrets.ErrUnavailable, status)
	}
	return nil
}

func (s *store) Get(ctx context.Context, path, key string) (string, error) {
	tok, err := s.authToken(ctx)
	if err != nil {
		return "", err
	}

	status, body, err := s.do(ctx, http.MethodGet, s.dataURL(path), tok, nil)
	if err != nil {
		return "", err
	}

	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", fmt.Errorf("%w: %s/%s", secrets.ErrNotFound, path, key)
	default:
		return "", fmt.Errorf("%w: read returned %d", secrets.ErrUnavailable, status)
	}

	var doc struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}

	value, ok := doc.Data.Data[key]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s", secrets.ErrNotFound, path, key)
	}
	return value, nil
}

func (s *store) Put(ctx context.Context, path, key, value string) error {
	payload, err := json.Marshal(map[string]map[string]string{
		"data": {key: value},
	})
	if err != nil {
		return fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}
	return s.write(ctx, http.MethodPost, s.dataURL(path), payload)
}

func (s *store) Create(ctx context.Context, path, key, value string) error {
	payload, err := json.Marshal(map[string]any{
		"data":    map[string]string{key: value},
		"options": map[string]int{"cas": 0},
	})
	if err != nil {
		return fmt.Errorf("%w: %w", secrets.ErrUnavailable, err)
	}

	tok, err := s.authToken(ctx)
	if err != nil {
		return err
	}

	status, body, err := s.do(ctx, http.MethodPost, s.dataURL(path), tok, payload)
	if err != nil {
		return err
	}

	switch status {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusBadRequest:
		if bytes.Contains(body, []byte("check-and-set parameter did not match")) {
			return fmt.Errorf("%w: %s", secrets.ErrExists, path)
		}
	default:
		return fmt.Errorf("%w: create returned %d", secrets.ErrUnavailable, status)
	}

	return nil
}

func (s *store) Delete(ctx context.Context, path string) error {
	return s.write(ctx, http.MethodDelete, s.metadataURL(path), nil)
}

func (s *store) Ping(ctx context.Context) error {
	status, _, err := s.do(ctx, http.MethodGet, s.addr+"/v1/sys/health", "", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%w: sys/health returned %d", secrets.ErrUnavailable, status)
	}
	return nil
}
