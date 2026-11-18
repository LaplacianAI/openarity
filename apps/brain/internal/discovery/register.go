package discovery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// OAuthClientSecretKey is the key a registered client's secret is kept
// under, at teams/<team>/mcp-oauth-client/<secret_id>.
const OAuthClientSecretKey = "client_secret"

// clientName is what an authorization server shows a person on its consent
// screen for a client the brain registered.
const clientName = "openarity"

var (
	// ErrNoRegistration is an authorization server with no registration
	// endpoint: a client is registered with it by hand, and set on the server.
	ErrNoRegistration = errors.New("the server's sign-in does not let apps register themselves: " +
		"register a client with it and set its client id on the server")
	// ErrRegistrationRefused is a registration endpoint that answered with
	// anything but a client. What it said is logged, not shown.
	ErrRegistrationRefused = errors.New("the server's sign-in did not accept openarity's registration")
)

// RegistrationError is a registration refused. Error is the sentence a
// person is shown: the authorization server's own reason when it gave one,
// clipped. Cause is the rest, for the log only, since a raw reply may be a
// page of HTML or echo what was sent.
type RegistrationError struct {
	Reason string
	Cause  error
}

func (e *RegistrationError) Error() string {
	if e.Reason == "" {
		return ErrRegistrationRefused.Error()
	}
	return ErrRegistrationRefused.Error() + ": " + e.Reason
}

func (e *RegistrationError) Unwrap() []error { return []error{ErrRegistrationRefused, e.Cause} }

// Registration is a client an authorization server issued: an id, and a
// secret when it made the client a confidential one, which may expire.
type Registration struct {
	ClientID        string
	ClientSecret    string
	SecretExpiresAt time.Time
}

// Register registers the brain as an OAuth client with a server's
// authorization server, as RFC 7591 lays out, through the transport discovery
// dials with. It asks for a public client, which PKCE protects and which has
// no secret to keep, unless the authorization server lists only methods that
// need one.
func (d *Discoverer) Register(ctx context.Context, as AuthServer, redirect string) (Registration, error) {
	if as.RegisterURL == "" {
		return Registration{}, ErrNoRegistration
	}
	meta := &oauthex.ClientRegistrationMetadata{
		ClientName:              clientName,
		RedirectURIs:            []string{redirect},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: authMethod(as.TokenAuth),
		ApplicationType:         "web",
	}
	resp, err := oauthex.RegisterClient(ctx, as.RegisterURL, meta, d.oauthClient())
	if err != nil {
		var refused *oauthex.ClientRegistrationError
		if errors.As(err, &refused) {
			reason := refused.ErrorDescription
			if reason == "" {
				reason = refused.ErrorCode
			}
			return Registration{}, &RegistrationError{Reason: clip(reason), Cause: err}
		}
		return Registration{}, &RegistrationError{Cause: err}
	}
	return Registration{
		ClientID:        resp.ClientID,
		ClientSecret:    resp.ClientSecret,
		SecretExpiresAt: resp.ClientSecretExpiresAt,
	}, nil
}

// authMethod is how the brain authenticates at the token endpoint. RFC 7591
// defaults an omitted method to client_secret_basic, so none is always said
// out loud. An authorization server that lists no methods is asked for none,
// as every MCP client registers: one that refuses says so in its answer.
func authMethod(supported []string) string {
	switch {
	case len(supported) == 0 || slices.Contains(supported, "none"):
		return "none"
	case slices.Contains(supported, "client_secret_basic"):
		return "client_secret_basic"
	default:
		return "client_secret_post"
	}
}

// clip keeps a refusal's reason to a sentence: it is the authorization
// server's own words, shown to a person, and nothing bounds what it sends.
func clip(s string) string {
	const most = 300
	if r := []rune(s); len(r) > most {
		return string(r[:most]) + "…"
	}
	return s
}

// Clients is where a server's registered client is kept.
type Clients interface {
	GetOAuthClient(ctx context.Context, server uuid.UUID) (db.McpOauthClient, error)
}

// Usable is whether a registration still fits a sign-in about to be made:
// the same authorization server, the same redirect URI, and a secret, if it
// has one, not yet expired.
func Usable(reg db.McpOauthClient, issuer, redirect string, now time.Time) bool {
	return sameIssuer(reg.Issuer, issuer) && reg.RedirectUri == redirect &&
		(reg.SecretExpiresAt == nil || now.Before(*reg.SecretExpiresAt))
}

func sameIssuer(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// RegisteredClient is the client a registration names, its secret read from
// the store when it has one.
func RegisteredClient(ctx context.Context, sec Secrets, reg db.McpOauthClient) (Client, error) {
	c := Client{ID: reg.ClientID}
	if reg.SecretID == nil {
		return c, nil
	}
	v, err := sec.Get(ctx, secrets.Path(reg.TeamID, secrets.KindMCPOAuthClient, *reg.SecretID), OAuthClientSecretKey)
	if err != nil {
		return c, fmt.Errorf("reading the registered client's secret: %w", err)
	}
	c.Secret = v
	return c, nil
}

// registeredOf is a server's registered client, or the empty client when it
// has none: a sign-in with no client is refused by the authorization server,
// which ends it as any refused sign-in is ended.
func registeredOf(ctx context.Context, sec Secrets, clients Clients, row db.McpServer) (Client, error) {
	if clients == nil {
		return Client{}, nil
	}
	reg, err := clients.GetOAuthClient(ctx, row.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, nil
	}
	if err != nil {
		return Client{}, fmt.Errorf("reading the registered client: %w", err)
	}
	return RegisteredClient(ctx, sec, reg)
}
