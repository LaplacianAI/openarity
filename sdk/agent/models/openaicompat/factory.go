package openaicompat

import (
	"errors"
	"sync"

	"github.com/openai/openai-go/v3/option"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func Factory(opts ...option.RequestOption) agent.ClientFactory {
	// Keyed by the Endpoint itself. It is two strings, so it is comparable and
	// Go will use it as a map key directly — which beats concatenating the URL
	// and the credential into one, both because there is no delimiter to get
	// wrong and because hashing a secret to avoid holding it here would be
	// theatre: the client built from it holds the same string.
	//
	// The credential has to be part of the key. Two teams on one gateway have
	// one URL and different virtual keys, and keying on the URL alone would
	// have them evict each other's client on every alternating call.
	var (
		mu      sync.Mutex
		clients = map[agent.Endpoint]*Client{}
	)

	return func(e agent.Endpoint) (agent.ModelClient, error) {
		if e.BaseURL == "" {
			return nil, errors.New("the endpoint has no base URL")
		}

		mu.Lock()
		defer mu.Unlock()

		if c, ok := clients[e]; ok {
			return c, nil
		}
		c := New(e.BaseURL, e.APIKey, opts...)
		clients[e] = c
		return c, nil
	}
}
