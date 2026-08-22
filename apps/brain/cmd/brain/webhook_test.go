package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
)

func testRegistry(t *testing.T) gateway.Registry {
	t.Helper()

	reg, err := newRegistry()
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}
	return reg
}

// The compiled-in adapters are one greppable list. A provider that is written,
// tested, and never added to it is unreachable, and nothing else says so.
func TestTheRegistryCarriesTheCompiledInAdapters(t *testing.T) {
	t.Parallel()

	if _, ok := testRegistry(t).Get("custom"); !ok {
		t.Error("the custom adapter is not registered, so no channel using it can receive anything")
	}
}

// A router built and never mounted is the failure the whole seam exists to
// avoid: it compiles, no linter complains, and every delivery 404s.
func TestTheGatewayIsMountedOnTheWebhookListener(t *testing.T) {
	t.Parallel()

	routers := newWebhookRouters(discardLogger(), nil, nil, testRegistry(t))
	if len(routers) == 0 {
		t.Fatal("newWebhookRouters returned nothing")
	}

	var served []string
	for _, r := range routers {
		p, ok := r.(patterned)
		if !ok {
			t.Fatalf("%T does not expose Patterns", r)
		}
		served = append(served, p.Patterns()...)
	}

	want := "POST /hooks/custom/{channel_id}"
	if !slices.Contains(served, want) {
		t.Errorf("the webhook listener serves %v, want %s among them", served, want)
	}
}

// That listener has no guard, so a router expecting one would panic at boot.
func TestEveryWebhookRouterIsPublic(t *testing.T) {
	t.Parallel()

	for _, r := range newWebhookRouters(discardLogger(), nil, nil, testRegistry(t)) {
		if !r.Public() {
			t.Errorf("%T is not public", r)
		}
	}
}

// The hook is not an API route. It carries no token, has no rbac.json entry,
// and describing it in openapi.yaml would advertise it to clients that can
// never call it.
func TestTheHookIsNotAnAPIRoute(t *testing.T) {
	t.Parallel()

	for _, route := range servedRoutes(t, config.EnvironmentProduction) {
		if strings.Contains(route, "/hooks/") {
			t.Errorf("%s is served on the API listener", route)
		}
	}
	for _, route := range specRoutes(t) {
		if strings.Contains(route, "/hooks/") {
			t.Errorf("%s is described in api/openapi.yaml", route)
		}
	}
}
