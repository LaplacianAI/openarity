package gateway

import (
	"net/http"
	"strings"
	"testing"
)

// A provider is the whole of what an adapter is, so these tests are about the
// one thing the seam itself decides: which adapter a request reaches. Everything
// about parsing belongs to the conformance kit, not here.

type stubProvider struct {
	name   string
	routes []Route
	keys   []string
}

func (s stubProvider) Name() string { return s.name }

func (s stubProvider) Routes() []Route {
	if s.routes == nil {
		return []Route{{Method: http.MethodPost}}
	}
	return s.routes
}

func (s stubProvider) Keys() []string {
	if s.keys == nil {
		return []string{KeySigning}
	}
	return s.keys
}

func (s stubProvider) Verify(WebhookRequest, Credentials) error { return nil }

func (s stubProvider) Parse(WebhookRequest) (Result, error) { return Result{}, nil }

// A key an adapter did not declare reads as empty rather than panicking, and
// an empty secret must never verify. Every Verify therefore has to refuse ""
// explicitly — hmac.Equal("", "") is true, so an absent secret would otherwise
// become a forgery oracle.
func TestAnUndeclaredCredentialReadsAsEmpty(t *testing.T) {
	t.Parallel()

	creds := Credentials{KeySigning: "s3cr3t"}

	if got := creds.Get(KeySigning); got != "s3cr3t" {
		t.Errorf("Get(%q) = %q, want the secret", KeySigning, got)
	}
	if got := creds.Get(KeyVerifyToken); got != "" {
		t.Errorf("Get(%q) = %q, want empty", KeyVerifyToken, got)
	}

	var absent Credentials
	if got := absent.Get(KeySigning); got != "" {
		t.Errorf("Get on nil Credentials = %q, want empty", got)
	}
}

func TestRegistryFindsAProviderByName(t *testing.T) {
	t.Parallel()

	reg, err := NewRegistry(stubProvider{name: "slack"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	if _, ok := reg.Get("slack"); !ok {
		t.Error("a registered provider was not found")
	}
	if _, ok := reg.Get("discord"); ok {
		t.Error("an unregistered provider was found")
	}
}

// The name is the channels.provider column and a path segment, so it matches
// byte for byte. Folding case here would make "Slack" and "slack" two rows that
// route to one adapter, and only one of them would be the one anybody wrote.
func TestAProviderNameIsMatchedExactly(t *testing.T) {
	t.Parallel()

	reg, err := NewRegistry(stubProvider{name: "slack"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	if _, ok := reg.Get("Slack"); ok {
		t.Error("Slack resolved to the adapter registered as slack")
	}
}

// Two providers claiming one name means a channel row silently routes to
// whichever won the map write. Refusing at construction makes it a boot
// failure instead, which is the only moment anyone can act on it.
func TestRegistryRefusesDuplicateNames(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry(stubProvider{name: "slack"}, stubProvider{name: "slack"})
	if err == nil {
		t.Fatal("NewRegistry accepted two providers named slack")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("the error does not name the provider: %v", err)
	}
}

// An empty name would mount hooks at /hooks//{channel} and match a channel row
// nobody can write, so the adapter would be unreachable rather than wrong.
func TestRegistryRefusesAnEmptyName(t *testing.T) {
	t.Parallel()

	if _, err := NewRegistry(stubProvider{name: ""}); err == nil {
		t.Fatal("NewRegistry accepted a provider with no name")
	}
}

// A provider with no routes is registered, resolvable, and never called: the
// mux has nothing to mount for it. That is the silent kind of broken.
func TestRegistryRefusesAProviderWithNoRoutes(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry(stubProvider{name: "slack", routes: []Route{}})
	if err == nil {
		t.Fatal("NewRegistry accepted a provider that declares no routes")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("the error does not name the provider: %v", err)
	}
}

// An adapter that asks for no secret has nothing to verify a signature
// against, so its Verify can only be returning nil. That is an open webhook
// endpoint, and it is the one mistake here nobody notices from the outside.
func TestRegistryRefusesAProviderWithNoSecretKeys(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry(stubProvider{name: "slack", keys: []string{}})
	if err == nil {
		t.Fatal("NewRegistry accepted a provider that declares no secret keys")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("the error does not name the provider: %v", err)
	}
}

// A brain with no adapters compiled in is a normal deployment, not an error —
// the API listener is the whole product until a channel is configured.
func TestARegistryWithNoProvidersIsValid(t *testing.T) {
	t.Parallel()

	reg, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry with no providers: %v", err)
	}
	if _, ok := reg.Get("slack"); ok {
		t.Error("an empty registry resolved a name")
	}
}

func TestAProviderMayDeclareSeveralRoutes(t *testing.T) {
	t.Parallel()

	meta := stubProvider{name: "meta", routes: []Route{
		{Method: http.MethodGet},
		{Method: http.MethodPost},
	}}

	reg, err := NewRegistry(meta)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	got, ok := reg.Get("meta")
	if !ok {
		t.Fatal("meta was not registered")
	}
	if len(got.Routes()) != 2 {
		t.Errorf("Routes() = %v, want both the verification GET and the event POST", got.Routes())
	}
}

// A suffix is pasted onto "/hooks/{name}/{channel_id}". Without the slash,
// "events" mounts /hooks/slack/{channel_id}events — a path that is not what
// anyone configured and that no test of the adapter would reveal.
func TestRegistryRefusesASuffixWithNoLeadingSlash(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry(stubProvider{name: "slack", routes: []Route{
		{Method: http.MethodPost, Suffix: "events"},
	}})
	if err == nil {
		t.Fatal("NewRegistry accepted a suffix that does not start with /")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("the error does not name the provider: %v", err)
	}
}

// A trailing slash is a second path for the same route, and only one of them
// is the one written on the provider's configuration page.
func TestRegistryRefusesASuffixWithATrailingSlash(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry(stubProvider{name: "slack", routes: []Route{
		{Method: http.MethodPost, Suffix: "/events/"},
	}})
	if err == nil {
		t.Fatal("NewRegistry accepted a suffix ending in /")
	}
}

// An empty method makes the pattern " /hooks/slack/{channel_id}", which
// http.ServeMux rejects with a panic about pattern syntax — a long way from
// the adapter that wrote it.
func TestRegistryRefusesARouteWithNoMethod(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry(stubProvider{name: "slack", routes: []Route{{Method: ""}}})
	if err == nil {
		t.Fatal("NewRegistry accepted a route with no method")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("the error does not name the provider: %v", err)
	}
}

// Two identical routes panic the mux at boot with a message about a duplicate
// pattern. Catching it here says which adapter, which is the part that is hard
// to work out from the other message.
func TestRegistryRefusesTwoIdenticalRoutes(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry(stubProvider{name: "slack", routes: []Route{
		{Method: http.MethodPost, Suffix: "/events"},
		{Method: http.MethodPost, Suffix: "/events"},
	}})
	if err == nil {
		t.Fatal("NewRegistry accepted the same route twice")
	}
}

// The same suffix under two methods is how a provider serves a verification
// GET and an event POST on one URL, so it must stay legal.
func TestOneSuffixMayHaveTwoMethods(t *testing.T) {
	t.Parallel()

	_, err := NewRegistry(stubProvider{name: "slack", routes: []Route{
		{Method: http.MethodGet, Suffix: "/events"},
		{Method: http.MethodPost, Suffix: "/events"},
	}})
	if err != nil {
		t.Errorf("NewRegistry refused a GET and a POST on one suffix: %v", err)
	}
}

// Most of what arrives at a webhook is not a message — a reaction, an edit, a
// bot joining a room. The zero Result is how an adapter says "nothing to do",
// so it has to be a valid answer rather than something the handler treats as
// a parse failure.
func TestTheZeroResultIsNothingToDo(t *testing.T) {
	t.Parallel()

	var r Result

	if len(r.Messages) != 0 {
		t.Errorf("Messages = %v, want none", r.Messages)
	}
	if r.Ack != nil {
		t.Errorf("Ack = %q, want none", r.Ack)
	}
}
