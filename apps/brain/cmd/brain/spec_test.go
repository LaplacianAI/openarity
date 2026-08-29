package main

import (
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	apispec "github.com/LaplacianAI/openarity/apps/brain/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

// The spec is a hand-written contract, so nothing stops it drifting from the
// service except a test. These are that test: every route described, every
// description served.

// probeRoutes are mounted by internal/server rather than by a Router, because
// they must answer whether or not authentication is configured. They are in
// the spec all the same — a client cares that they exist, not which mux
// registered them.
var probeRoutes = []string{"GET /healthz", "GET /readyz"}

// devOnlyRoutes serve the documentation. They are deliberately absent from the
// spec: they exist only in development, and a description of itself is not
// part of the API's contract.
var devOnlyRoutes = []string{"GET /docs", "GET /openapi.yaml"}

// publicRoutes answer without a token in every environment. The list is
// deliberately hand-written and short: a route joins it by being named here,
// never by a flag someone set on a router.
var publicRoutes = []string{"GET /auth/config"}

type patterned interface {
	Patterns() []string
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// servedRoutes is every "METHOD /path" the brain answers in the given
// environment. The store and authorizer are nil because nothing here serves a
// request — only the routing table is read.
func servedRoutes(t *testing.T, environment config.Environment) []string {
	t.Helper()

	cfg := &config.Config{Environment: environment}

	routes := slices.Clone(probeRoutes)
	for _, r := range newRouters(cfg, discardLogger(), nil, nil, nil, nil, nil) {
		p, ok := r.(patterned)
		if !ok {
			t.Fatalf("%T does not expose Patterns, so it cannot be checked against the spec", r)
		}
		routes = append(routes, p.Patterns()...)
	}

	sort.Strings(routes)
	return routes
}

// specRoutes is every operation described in api/openapi.yaml, in the same
// "METHOD /path" form the router produces.
func specRoutes(t *testing.T) []string {
	t.Helper()

	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(apispec.Spec, &doc); err != nil {
		t.Fatalf("the embedded spec is not valid YAML: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("the spec describes no paths, so this test would pass vacuously")
	}

	verbs := []string{"get", "post", "put", "patch", "delete"}

	var routes []string
	for path, operations := range doc.Paths {
		for verb := range operations {
			if slices.Contains(verbs, verb) {
				routes = append(routes, strings.ToUpper(verb)+" "+path)
			}
		}
	}

	sort.Strings(routes)
	return routes
}

// The contract, in both directions. An endpoint added without a description
// fails here rather than reaching a client undocumented; a description left
// behind by a deleted route fails here rather than sending someone after an
// endpoint that is gone.
func TestEveryRouteIsInTheSpec(t *testing.T) {
	t.Parallel()

	served := servedRoutes(t, config.EnvironmentProduction)
	described := specRoutes(t)

	for _, route := range served {
		if !slices.Contains(described, route) {
			t.Errorf("%s is served but not in api/openapi.yaml — see the update-api-spec skill", route)
		}
	}
	for _, route := range described {
		if !slices.Contains(served, route) {
			t.Errorf("%s is in api/openapi.yaml but nothing serves it", route)
		}
	}
}

// A guard on the guard: if newRouters ever returns nothing, the comparison
// above still passes when the spec is also empty.
func TestTheContractTestHasSomethingToCompare(t *testing.T) {
	t.Parallel()

	served := servedRoutes(t, config.EnvironmentProduction)
	if len(served) <= len(probeRoutes) {
		t.Fatalf("only the probes are served (%v) — newRouters returned nothing", served)
	}
	if len(specRoutes(t)) < len(served) {
		t.Error("the spec describes fewer operations than are served")
	}
}

// The documentation routes are development-only. Mounting them elsewhere
// publishes a map of every endpoint and its authorisation rules, without a
// token.
func TestDocsAreMountedOnlyInDevelopment(t *testing.T) {
	t.Parallel()

	production := servedRoutes(t, config.EnvironmentProduction)
	for _, route := range devOnlyRoutes {
		if slices.Contains(production, route) {
			t.Errorf("%s is served in production", route)
		}
	}

	development := servedRoutes(t, config.EnvironmentDevelopment)
	for _, route := range devOnlyRoutes {
		if !slices.Contains(development, route) {
			t.Errorf("%s is missing in development", route)
		}
	}
}

// Adding the docs must not change anything else. A router that quietly took
// over "/" would shadow the API and only show up in development.
func TestDocsAddNothingButTheirOwnRoutes(t *testing.T) {
	t.Parallel()

	production := servedRoutes(t, config.EnvironmentProduction)
	development := servedRoutes(t, config.EnvironmentDevelopment)

	if len(development) != len(production)+len(devOnlyRoutes) {
		t.Fatalf("development serves %v, production %v — the difference is not just the docs",
			development, production)
	}
	for _, route := range production {
		if !slices.Contains(development, route) {
			t.Errorf("%s is served in production but not in development", route)
		}
	}
}

// An unknown environment string is not development. A typo in a deployment's
// configuration must fail closed.
func TestOnlyDevelopmentGetsTheDocs(t *testing.T) {
	t.Parallel()

	for _, environment := range []config.Environment{"", "prod", "Development", "dev", config.EnvironmentStaging, config.EnvironmentTest} {
		routes := servedRoutes(t, environment)
		for _, route := range devOnlyRoutes {
			if slices.Contains(routes, route) {
				t.Errorf("environment %q served %s", environment, route)
			}
		}
	}
}

// Every data route stays behind authentication. Public is for documentation
// and for the one endpoint that says how to authenticate; a data route that
// acquires the flag is a silent authentication bypass.
func TestOnlyDeclaredRoutesArePublic(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Environment: config.EnvironmentDevelopment}
	allowed := slices.Concat(devOnlyRoutes, publicRoutes)

	for _, r := range newRouters(cfg, discardLogger(), nil, nil, nil, nil, nil) {
		if !r.Public() {
			continue
		}

		p, ok := r.(patterned)
		if !ok {
			t.Fatalf("%T is public and cannot be inspected", r)
		}
		for _, route := range p.Patterns() {
			if !slices.Contains(allowed, route) {
				t.Errorf("%s is served without authentication", route)
			}
		}
	}
}

// The CLI's first call, before it holds anything. Mounting it in development
// only would mean a staging or production client had no way to discover the
// flow, which is the whole reason the endpoint exists.
func TestAuthConfigIsServedInEveryEnvironment(t *testing.T) {
	t.Parallel()

	for _, environment := range []config.Environment{
		config.EnvironmentDevelopment,
		config.EnvironmentStaging,
		config.EnvironmentProduction,
	} {
		routes := servedRoutes(t, environment)
		for _, route := range publicRoutes {
			if !slices.Contains(routes, route) {
				t.Errorf("environment %q does not serve %s", environment, route)
			}
		}
	}
}

// server.Router is guaranteed by the slice type; Patterns is not. A domain
// package returning something that satisfies only the first would make every
// check in this file skip it silently, so the assertion is here rather than
// inside a t.Fatalf nobody reaches.
func TestEveryRouterExposesItsPatterns(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Environment: config.EnvironmentDevelopment}
	routers := newRouters(cfg, discardLogger(), nil, nil, nil, nil, nil)

	if len(routers) == 0 {
		t.Fatal("newRouters returned nothing")
	}

	for _, r := range routers {
		if _, ok := r.(patterned); !ok {
			t.Errorf("%T does not expose Patterns", r)
		}
	}
}
