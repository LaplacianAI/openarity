package openaicompat

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/openai/openai-go/v3/option"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func Factory(opts ...option.RequestOption) agent.ClientFactory {
	var (
		mu      sync.Mutex
		clients = map[string]*Client{}
	)

	return func(e agent.Endpoint) (agent.ModelClient, error) {
		if e.BaseURL == "" {
			return nil, errors.New("the endpoint has no base URL")
		}

		key := cacheKey(e)

		mu.Lock()
		defer mu.Unlock()

		if c, ok := clients[key]; ok {
			return c, nil
		}
		c := New(e.BaseURL, e.APIKey, opts...)
		clients[key] = c
		return c, nil
	}
}

func cacheKey(e agent.Endpoint) string {
	sum := sha256.Sum256([]byte(e.APIKey))
	return e.BaseURL + "\x00" + hex.EncodeToString(sum[:8])
}
