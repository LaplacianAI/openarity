package openaicompat

import (
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Reuse is the whole reason the factory exists. A fresh client per run throws
// away the connection pool and pays a TLS handshake on every model call —
// invisible in a test, expensive under load.
func TestOneEndpointGetsOneClient(t *testing.T) {
	t.Parallel()

	build := Factory()
	e := agent.Endpoint{BaseURL: "http://litellm:4000/v1", APIKey: "sk-team-a"}

	first, err := build(e)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	second, err := build(e)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if first != second {
		t.Error("the same endpoint produced two clients, so connections are not being reused")
	}
}

func TestDifferentGatewaysGetDifferentClients(t *testing.T) {
	t.Parallel()

	build := Factory()
	a, err := build(agent.Endpoint{BaseURL: "http://litellm:4000/v1", APIKey: "sk-x"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, err := build(agent.Endpoint{BaseURL: "http://omniroute:20128/v1", APIKey: "sk-x"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if a == b {
		t.Error("two gateways shared a client, so one team's traffic would go to the other's endpoint")
	}
}

// A rotated key must produce a new client. Reusing the cached one would keep
// presenting the credential that was just revoked, and the failure would look
// like the rotation never happened.
func TestARotatedKeyGetsANewClient(t *testing.T) {
	t.Parallel()

	build := Factory()
	url := "http://litellm:4000/v1"

	before, err := build(agent.Endpoint{BaseURL: url, APIKey: "sk-old"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	after, err := build(agent.Endpoint{BaseURL: url, APIKey: "sk-new"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if before == after {
		t.Error("a rotated key reused the client holding the old one")
	}
}

// openai-go falls back to api.openai.com with no base URL. A self-hosted
// deployment that lost its setting would ship a team's prompts to a third party
// instead of failing, which is why this is refused rather than defaulted.
func TestAnEndpointWithNoURLIsRefused(t *testing.T) {
	t.Parallel()

	_, err := Factory()(agent.Endpoint{APIKey: "sk-x"})
	if err == nil {
		t.Fatal("an endpoint with no base URL was accepted")
	}
	if !strings.Contains(err.Error(), "base URL") {
		t.Errorf("err = %v, want it to say what is missing", err)
	}
}

// Two teams on one gateway have one URL and different virtual keys. Keying on
// the URL alone would have them evict each other's client on every alternating
// call — a cache that makes things slower than having none.
func TestTwoCredentialsOnOneGatewayGetTwoClients(t *testing.T) {
	t.Parallel()

	build := Factory()
	url := "http://litellm:4000/v1"

	a, err := build(agent.Endpoint{BaseURL: url, APIKey: "sk-team-a"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, err := build(agent.Endpoint{BaseURL: url, APIKey: "sk-team-b"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a == b {
		t.Fatal("two teams shared a client, so one would present the other's credential")
	}

	// And neither evicted the other.
	again, err := build(agent.Endpoint{BaseURL: url, APIKey: "sk-team-a"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if again != a {
		t.Error("the second team's client evicted the first, so every call rebuilds")
	}
}

// One factory serves every run in the process. Under -race this catches the map
// losing its lock.
func TestTheFactoryIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	build := Factory()
	e := agent.Endpoint{BaseURL: "http://litellm:4000/v1", APIKey: "sk-team-a"}

	const callers = 50
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got []agent.ModelClient
	)

	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := build(e)
			if err != nil {
				t.Errorf("build: %v", err)
				return
			}
			mu.Lock()
			got = append(got, c)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(got) != callers {
		t.Fatalf("%d callers got a client, want %d", len(got), callers)
	}
	for _, c := range got {
		if c != got[0] {
			t.Fatal("concurrent callers were handed different clients for one endpoint")
		}
	}
}

// The factory satisfies the type the Runner takes. Without this the mismatch
// only shows up wherever somebody first wires the two together.
func TestFactorySatisfiesClientFactory(t *testing.T) {
	t.Parallel()

	var _ agent.ClientFactory = Factory()
}
