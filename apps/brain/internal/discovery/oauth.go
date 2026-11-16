package discovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// The issuer whose refresh tokens come only with access_type=offline and
// prompt=consent; it lists no offline_access to ask for them by.
const googleIssuer = "https://accounts.google.com"

const maxTokenBytes = 1 << 20

var (
	ErrNoAuthorization = errors.New("the server names no authorization server to sign in with")
	ErrNoPKCE          = errors.New("the authorization server does not offer PKCE with S256, so signing in to it is refused")
	// ErrSignInAgain is a grant the authorization server refused: revoked,
	// expired, or a code already used. Only a new sign-in mends it.
	ErrSignInAgain = errors.New("the authorization server refused the sign-in; connect the server again")
)

// AuthServer is where a server is signed in to, as discovery found it.
type AuthServer struct {
	Issuer       string
	AuthorizeURL string
	TokenURL     string
	RegisterURL  string
	RevokeURL    string
	// Resource is the server's canonical URL, named in every authorization
	// and token request so a token is bound to it.
	Resource string
	// Scopes are the challenge's, else the resource's scopes_supported.
	Scopes []string
	// OfflineAccess is whether the authorization server lists
	// offline_access, the scope a refresh token is asked for by.
	OfflineAccess bool
	MetadataDocs  bool
	// IssResponse is whether the authorization response carries iss, which
	// the callback must then check.
	IssResponse bool
	TokenAuth   []string
}

// Client is the OAuth client the brain signs in as. Secret is empty for a
// public client.
type Client struct {
	ID     string
	Secret string
}

// Start is a sign-in begun: the URL to send the person to, and the state and
// PKCE verifier the callback is checked against.
type Start struct {
	URL      string
	State    string
	Verifier string
}

// Tokens are what a code or a refresh token was exchanged for. A Refresh of
// "" means the authorization server kept the one it had.
type Tokens struct {
	Access  string
	Refresh string
	Expiry  time.Time
	Scopes  []string
}

// DiscoverAuth finds the authorization server of an MCP server, as the MCP
// authorization spec lays out: a 401's challenge if the server sends one,
// else the protected resource metadata at its well-known paths, then the
// authorization server's own metadata. Every fetch goes through the
// transport discovery dials with.
func (d *Discoverer) DiscoverAuth(ctx context.Context, serverURL string) (AuthServer, error) {
	client := d.oauthClient()

	challenges, err := d.challenge(ctx, client, serverURL)
	if err != nil {
		return AuthServer{}, err
	}
	prm, err := resourceMetadata(ctx, client, challenges, serverURL)
	if err != nil {
		return AuthServer{}, err
	}

	// accounts.google.com is named with a slash, and its metadata answers
	// only at the root's well-known paths.
	issuer := strings.TrimSuffix(prm.AuthorizationServers[0], "/")
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, client)
	if err != nil {
		return AuthServer{}, fmt.Errorf("reading the authorization server's metadata: %w", err)
	}
	if asm == nil {
		return AuthServer{}, ErrNoAuthorization
	}
	if !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return AuthServer{}, ErrNoPKCE
	}

	scopes := prm.ScopesSupported
	if s := scopeOf(challenges); s != "" {
		scopes = strings.Fields(s)
	}
	return AuthServer{
		Issuer:        issuer,
		AuthorizeURL:  asm.AuthorizationEndpoint,
		TokenURL:      asm.TokenEndpoint,
		RegisterURL:   asm.RegistrationEndpoint,
		RevokeURL:     asm.RevocationEndpoint,
		Resource:      prm.Resource,
		Scopes:        scopes,
		OfflineAccess: slices.Contains(asm.ScopesSupported, "offline_access"),
		MetadataDocs:  asm.ClientIDMetadataDocumentSupported,
		IssResponse:   asm.AuthorizationResponseIssParameterSupported,
		TokenAuth:     asm.TokenEndpointAuthMethodsSupported,
	}, nil
}

func (d *Discoverer) oauthClient() *http.Client {
	return &http.Client{Transport: capped{next: d.transport}, Timeout: d.timeout}
}

// challenge asks the server, unauthenticated, what it wants. Not every
// server that signs in answers 401: Gmail's answers initialize with 200, so
// no challenge is not a server that needs no sign-in.
func (d *Discoverer) challenge(ctx context.Context, client *http.Client, serverURL string) ([]oauthex.Challenge, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25",` +
		`"capabilities":{},"clientInfo":{"name":"openarity-brain","version":"v0.1.0"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("asking the server: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking the server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxTokenBytes))

	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil, nil
	}
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return nil, fmt.Errorf("reading the server's challenge: %w", err)
	}
	return challenges, nil
}

// resourceMetadata is the first protected resource metadata that answers:
// at the challenge's URL, then at the server's path, then at its root. A
// document naming no authorization server ends the search, as the spec has
// it.
func resourceMetadata(
	ctx context.Context, client *http.Client, challenges []oauthex.Challenge, serverURL string,
) (*oauthex.ProtectedResourceMetadata, error) {
	var last error
	for _, c := range metadataCandidates(paramOf(challenges, "resource_metadata"), serverURL) {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, c.url, c.resource, client)
		if err != nil {
			last = err
			continue
		}
		if prm == nil {
			continue
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, ErrNoAuthorization
		}
		return prm, nil
	}
	if last != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoAuthorization, last)
	}
	return nil, ErrNoAuthorization
}

type candidate struct{ url, resource string }

func metadataCandidates(fromChallenge, serverURL string) []candidate {
	var out []candidate
	if fromChallenge != "" {
		out = append(out, candidate{fromChallenge, serverURL})
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return out
	}
	at := *u
	at.RawQuery, at.Fragment = "", ""
	at.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(u.Path, "/")
	out = append(out, candidate{at.String(), serverURL})
	at.Path = "/.well-known/oauth-protected-resource"
	root := *u
	root.Path, root.RawQuery, root.Fragment = "", "", ""
	return append(out, candidate{at.String(), root.String()})
}

func paramOf(challenges []oauthex.Challenge, key string) string {
	for _, c := range challenges {
		if c.Scheme == "bearer" && c.Params[key] != "" {
			return c.Params[key]
		}
	}
	return ""
}

func scopeOf(challenges []oauthex.Challenge) string { return paramOf(challenges, "scope") }

// AuthorizeURL begins a sign-in: a PKCE verifier, a state, and the URL that
// asks for scopes, or the server's own when scopes is empty. A refresh token
// is asked for the way the authorization server takes it.
func AuthorizeURL(as AuthServer, c Client, redirect string, scopes []string) (Start, error) {
	if len(scopes) == 0 {
		scopes = as.Scopes
	}
	state, err := randomState()
	if err != nil {
		return Start{}, err
	}
	verifier := oauth2.GenerateVerifier()

	opts := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", as.Resource),
	}
	switch {
	case as.Issuer == googleIssuer:
		opts = append(opts, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	case as.OfflineAccess && !slices.Contains(scopes, "offline_access"):
		scopes = append(slices.Clone(scopes), "offline_access")
	}
	return Start{URL: config(as, c, redirect, scopes).AuthCodeURL(state, opts...), State: state, Verifier: verifier}, nil
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("making the sign-in's state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func config(as AuthServer, c Client, redirect string, scopes []string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ID,
		ClientSecret: c.Secret,
		Endpoint:     oauth2.Endpoint{AuthURL: as.AuthorizeURL, TokenURL: as.TokenURL, AuthStyle: authStyle(as, c)},
		RedirectURL:  redirect,
		Scopes:       scopes,
	}
}

// authStyle is chosen, never detected: detecting tries one style and then
// the other, which spends an authorization code on the first attempt.
func authStyle(as AuthServer, c Client) oauth2.AuthStyle {
	if c.Secret == "" || (slices.Contains(as.TokenAuth, "client_secret_post") && !slices.Contains(as.TokenAuth, "client_secret_basic")) {
		return oauth2.AuthStyleInParams
	}
	return oauth2.AuthStyleInHeader
}

// Exchange trades the callback's code for tokens, with the verifier only the
// brain holds.
func (d *Discoverer) Exchange(ctx context.Context, as AuthServer, c Client, redirect, code, verifier string) (Tokens, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, d.oauthClient())
	tok, err := config(as, c, redirect, nil).Exchange(ctx, code,
		oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("resource", as.Resource))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			return Tokens{}, ErrSignInAgain
		}
		return Tokens{}, fmt.Errorf("exchanging the code: %w", err)
	}
	scope, _ := tok.Extra("scope").(string)
	return Tokens{Access: tok.AccessToken, Refresh: tok.RefreshToken, Expiry: tok.Expiry, Scopes: strings.Fields(scope)}, nil
}

// Refresh trades a refresh token for a new access token. A form of its own
// rather than x/oauth2's, which cannot name the resource on a refresh, and
// the spec has every token request name it.
func (d *Discoverer) Refresh(ctx context.Context, as AuthServer, c Client, refresh string) (Tokens, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"resource":      {as.Resource},
	}
	style := authStyle(as, c)
	if style == oauth2.AuthStyleInParams {
		form.Set("client_id", c.ID)
		if c.Secret != "" {
			form.Set("client_secret", c.Secret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, as.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, fmt.Errorf("refreshing: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if style == oauth2.AuthStyleInHeader {
		req.SetBasicAuth(url.QueryEscape(c.ID), url.QueryEscape(c.Secret))
	}

	resp, err := d.oauthClient().Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("refreshing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBytes))
	if err != nil {
		return Tokens{}, fmt.Errorf("refreshing: reading the reply: %w", err)
	}

	var body struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int64  `json:"expires_in"`
		Scope     string `json:"scope"`
		Error     string `json:"error"`
	}
	_ = json.NewDecoder(bytes.NewReader(raw)).Decode(&body)
	switch {
	case body.Error == "invalid_grant":
		return Tokens{}, ErrSignInAgain
	case resp.StatusCode != http.StatusOK || body.Access == "":
		// Never the body: it may echo what was sent.
		return Tokens{}, fmt.Errorf("refreshing: the authorization server answered %d %s", resp.StatusCode, body.Error)
	}
	t := Tokens{Access: body.Access, Refresh: body.Refresh, Scopes: strings.Fields(body.Scope)}
	if body.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return t, nil
}
