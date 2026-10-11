package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	maxResponseBytes = 64 << 20
	maxErrorBytes    = 2 << 10
)

type Embedder struct {
	url    string
	model  string
	dims   int
	key    func(context.Context) (string, error)
	client *http.Client
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []embedding `json:"data"`
}

type embedding struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type gatewayError struct {
	Error struct {
		Code          string `json:"code"`
		Message       string `json:"message"`
		CorrelationID string `json:"correlation_id"`
	} `json:"error"`
}

func NewEmbedder(baseURL, model string, dims int, key func(context.Context) (string, error), client *http.Client) *Embedder {
	return &Embedder{
		url:   strings.TrimRight(baseURL, "/") + "/embeddings",
		model: model, dims: dims, key: key, client: client,
	}
}

func (e *Embedder) Model() string { return e.model }

func (e *Embedder) Dimensions() int { return e.dims }

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	if e.key != nil {
		key, err := e.key(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the embedding key: %w", err)
		}
		if key == "" {
			return nil, errors.New("the embedding key is empty in the secret store")
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding with %s: %w", e.model, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding with %s: the model router answered %d: %s",
			e.model, resp.StatusCode, gatewayMessage(resp.Body))
	}

	var out embedResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("embedding with %s: the reply is not the JSON it should be: %w", e.model, err)
	}

	return e.order(out.Data, len(texts))
}

func (e *Embedder) order(data []embedding, n int) ([][]float32, error) {
	if len(data) != n {
		return nil, fmt.Errorf("embedding with %s: %d vectors came back for %d texts", e.model, len(data), n)
	}

	out := make([][]float32, n)
	for _, d := range data {
		switch {
		case d.Index < 0 || d.Index >= n:
			return nil, fmt.Errorf("embedding with %s: a vector is numbered %d, outside the %d texts sent", e.model, d.Index, n)
		case out[d.Index] != nil:
			return nil, fmt.Errorf("embedding with %s: two vectors are numbered %d", e.model, d.Index)
		case len(d.Embedding) != e.dims:
			return nil, fmt.Errorf("%s returned a vector of %d dimensions, and EMBEDDING_DIMENSIONS is %d",
				e.model, len(d.Embedding), e.dims)
		}
		out[d.Index] = d.Embedding
	}

	return out, nil
}

func gatewayMessage(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, maxErrorBytes))

	var ge gatewayError
	if json.Unmarshal(raw, &ge) == nil && ge.Error.Message != "" {
		msg := ge.Error.Message
		if ge.Error.Code != "" {
			msg += " (" + ge.Error.Code + ")"
		}
		if ge.Error.CorrelationID != "" {
			msg += ", correlation " + ge.Error.CorrelationID
		}
		return msg
	}

	return strings.Join(strings.Fields(string(raw)), " ")
}
