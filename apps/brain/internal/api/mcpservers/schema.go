package mcpservers

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type server struct {
	ID                 uuid.UUID         `json:"id"`
	TeamID             uuid.UUID         `json:"team_id"`
	Name               string            `json:"name"`
	URL                *string           `json:"url,omitempty"`
	Command            []string          `json:"command,omitempty"`
	Env                map[string]string `json:"env"`
	AuthSecretRef      *string           `json:"auth_secret_ref,omitempty"`
	AuthKind           string            `json:"auth_kind"`
	OAuthClientID      *string           `json:"oauth_client_id,omitempty"`
	OAuthClientSecret  *string           `json:"oauth_client_secret_ref,omitempty"`
	OAuthScopes        []string          `json:"oauth_scopes,omitempty"`
	Connection         *connection       `json:"connection"`
	Bare               bool              `json:"bare"`
	DiscoveredAt       *time.Time        `json:"discovered_at"`
	LastDiscoveryError *string           `json:"last_discovery_error"`
	LastDiscoveryAt    *time.Time        `json:"last_discovery_at"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type Request struct {
	Name          string            `json:"name"`
	URL           *string           `json:"url"`
	Command       []string          `json:"command"`
	Env           map[string]string `json:"env"`
	AuthSecretRef *string           `json:"auth_secret_ref"`
	Bare          bool              `json:"bare"`

	// AuthKind is static, the default, or oauth: signed in to on the
	// dashboard with the client named here.
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
