package secrets

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
)

const (
	baoTimeout  = 5 * time.Second
	maxBodySize = 1 << 20
)

type openBao struct {
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
	_ Store  = (*openBao)(nil)
	_ Writer = (*openBao)(nil)
	_ Prober = (*openBao)(nil)
)

func NewOpenBao(addr, roleID, secretID, mount string, hc *http.Client) Store {
	if hc == nil {
		hc = &http.Client{Timeout: baoTimeout}
	}
	return &openBao{
		addr:     strings.TrimRight(addr, "/"),
		roleID:   roleID,
		secretID: secretID,
		mount:    mount,
		hc:       hc,
	}
}

func (v *openBao) dataURL(path string) string {
	return v.addr + "/v1/" + url.PathEscape(v.mount) + "/data/" + path
}

func (v *openBao) metadataURL(path string) string {
	return v.addr + "/v1/" + url.PathEscape(v.mount) + "/metadata/" + path
}

func (v *openBao) authToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.tok != "" && time.Now().Before(v.expires) {
		return v.tok, nil
	}

	tok, lease, err := v.login(ctx)
	if err != nil {
		return "", err
	}
	v.tok, v.expires = tok, time.Now().Add(lease)
	return tok, nil
}

func (v *openBao) login(ctx context.Context) (string, time.Duration, error) {
	payload, err := json.Marshal(map[string]string{
		"role_id":   v.roleID,
		"secret_id": v.secretID,
	})
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	status, body, err := v.do(ctx, http.MethodPost,
		v.addr+"/v1/auth/approle/login", "", payload)
	if err != nil {
		return "", 0, err
	}
	if status != http.StatusOK {
		return "", 0, fmt.Errorf("%w: approle login returned %d", ErrUnavailable, status)
	}

	var doc struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if doc.Auth.ClientToken == "" {
		return "", 0, fmt.Errorf("%w: approle login returned no token", ErrUnavailable)
	}
	return doc.Auth.ClientToken, time.Duration(doc.Auth.LeaseDuration) * time.Second, nil
}

func (v *openBao) do(
	ctx context.Context, method, endpoint, tok string, payload []byte,
) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if tok != "" {
		req.Header.Set("X-Vault-Token", tok)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := v.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return resp.StatusCode, body, nil
}

func (v *openBao) write(ctx context.Context, method, endpoint string, payload []byte) error {
	tok, err := v.authToken(ctx)
	if err != nil {
		return err
	}

	status, _, err := v.do(ctx, method, endpoint, tok, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("%w: write returned %d", ErrUnavailable, status)
	}
	return nil
}

func (v *openBao) Get(ctx context.Context, path, key string) (string, error) {
	tok, err := v.authToken(ctx)
	if err != nil {
		return "", err
	}

	status, body, err := v.do(ctx, http.MethodGet, v.dataURL(path), tok, nil)
	if err != nil {
		return "", err
	}

	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", fmt.Errorf("%w: %s/%s", ErrNotFound, path, key)
	default:
		return "", fmt.Errorf("%w: read returned %d", ErrUnavailable, status)
	}

	var doc struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	value, ok := doc.Data.Data[key]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s", ErrNotFound, path, key)
	}
	return value, nil
}

func (v *openBao) Put(ctx context.Context, path, key, value string) error {
	payload, err := json.Marshal(map[string]map[string]string{
		"data": {key: value},
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return v.write(ctx, http.MethodPost, v.dataURL(path), payload)
}

func (v *openBao) Delete(ctx context.Context, path string) error {
	return v.write(ctx, http.MethodDelete, v.metadataURL(path), nil)
}

func (v *openBao) Ping(ctx context.Context) error {
	status, _, err := v.do(ctx, http.MethodGet, v.addr+"/v1/sys/health", "", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%w: sys/health returned %d", ErrUnavailable, status)
	}
	return nil
}
