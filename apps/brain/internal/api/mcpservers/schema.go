package mcpservers

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

type server struct {
	ID                 uuid.UUID          `json:"id"`
	TeamID             uuid.UUID          `json:"team_id"`
	Name               string             `json:"name"`
	URL                *string            `json:"url,omitempty"`
	Command            []string           `json:"command,omitempty"`
	Env                map[string]string  `json:"env"`
	Vars               map[string]string  `json:"vars,omitempty"`
	Egress             []sandbox.HostPort `json:"egress,omitempty"`
	AuthSecretRef      *string            `json:"auth_secret_ref,omitempty"`
	AuthKind           string             `json:"auth_kind"`
	OAuthClientID      *string            `json:"oauth_client_id,omitempty"`
	OAuthClientSecret  *string            `json:"oauth_client_secret_ref,omitempty"`
	OAuthScopes        []string           `json:"oauth_scopes,omitempty"`
	Connection         *connection        `json:"connection"`
	Registration       *registration      `json:"oauth_registration"`
	Bare               bool               `json:"bare"`
	TrustHints         bool               `json:"trust_hints"`
	DiscoveredAt       *time.Time         `json:"discovered_at"`
	LastDiscoveryError *string            `json:"last_discovery_error"`
	LastDiscoveryAt    *time.Time         `json:"last_discovery_at"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

// trustRequest is whether to believe a server's own hints about its tools.
type trustRequest struct {
	Trust *bool `json:"trust"`
}

type Request struct {
	Name          string            `json:"name"`
	URL           *string           `json:"url"`
	Command       []string          `json:"command"`
	Env           map[string]string `json:"env"`
	AuthSecretRef *string           `json:"auth_secret_ref"`
	Bare          bool              `json:"bare"`

	// Vars and Egress are for a command server, run in the team's sandbox:
	// plain settings shown as they are, beside env's references, and the
	// only places its room may connect to.
	Vars   map[string]string  `json:"vars"`
	Egress []sandbox.HostPort `json:"egress"`

	// AuthKind is static, the default, or oauth: signed in to on the
	// dashboard with the client named here, or, with none named, one the
	// brain registers itself at the first Connect.
	AuthKind             string   `json:"auth_kind"`
	OAuthClientID        *string  `json:"oauth_client_id"`
	OAuthClientSecretRef *string  `json:"oauth_client_secret_ref"`
	OAuthScopes          []string `json:"oauth_scopes"`
}

// connection is a server's sign-in, as far as it may be shown: never a token.
type connection struct {
	Status          string     `json:"status"`
	Reason          *string    `json:"reason"`
	ConnectedBy     *uuid.UUID `json:"connected_by"`
	ConnectedAt     time.Time  `json:"connected_at"`
	ScopesGranted   []string   `json:"scopes_granted"`
	AccessExpiresAt *time.Time `json:"access_expires_at"`
}

// registration is the client the brain registered for a server with no
// client of its own: its id, which the authorization server shows, and when.
type registration struct {
	ClientID     string    `json:"client_id"`
	RegisteredAt time.Time `json:"registered_at"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolList struct {
	Items []tool `json:"items"`
}

type serverCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

// secretRequest is an MCP secret's keys and values, written as one secret.
type secretRequest struct {
	Values map[string]string `json:"values"`
}
