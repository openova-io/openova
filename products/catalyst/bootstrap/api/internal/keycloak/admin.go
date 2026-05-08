package keycloak

// admin.go — Keycloak Admin REST API operations beyond the original
// /auth/handover scope (EnsureUser + ImpersonateToken).
//
// Slice D1a of EPIC-0 #1095 lands OIDC client CRUD. organization-controller
// (slice C1) calls these to provision per-Organization OIDC clients in the
// Sovereign realm so an Org's vCluster + Hubble UI + Application UI all
// federate to the same Keycloak realm with their own client secrets.
//
// Idiom mirrors client.go exactly:
//   - serviceAccountToken first (cached at the caller-level on a future slice)
//   - JSON request → 4xx becomes a typed error sentinel where useful
//   - 200/201/204 success cases each parsed explicitly
//   - http.Client supplied at New(); test code injects a fake roundtripper
//
// Endpoints used (per the Keycloak Admin REST API documentation, version
// 24.x — pinned via the Keycloak dependency in platform/keycloak):
//
//   GET    /admin/realms/{realm}/clients?clientId={id}
//   GET    /admin/realms/{realm}/clients/{uuid}
//   POST   /admin/realms/{realm}/clients
//   PUT    /admin/realms/{realm}/clients/{uuid}
//   DELETE /admin/realms/{realm}/clients/{uuid}
//
// Note the Keycloak naming convention: the URL `clientId` query parameter
// (and `ClientID` field on the JSON payload) is the human-readable
// identifier that operators set (e.g. `acme-app`, `hubble-ui`). The path
// segment `{uuid}` is the internal Keycloak primary key, which Keycloak
// generates on POST. Catalyst code refers to the human-readable form as
// `ClientID` and the UUID form as `ID` (matching the JSON field names).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// errClientAlreadyExists is returned by CreateClient when Keycloak responds
// with 409 Conflict because an OIDC client with the same clientId is
// already registered. Callers wanting find-or-create behaviour catch this
// sentinel and re-find via FindClientByClientID.
var errClientAlreadyExists = errors.New("keycloak: oidc client already exists")

// ErrClientNotFound is returned by GetClient when the Keycloak UUID does
// not resolve. Useful for callers building idempotent reconciliation
// loops.
var ErrClientNotFound = errors.New("keycloak: oidc client not found")

// OIDCClient is the slice of fields catalyst-api consumes/sets when
// provisioning per-Org clients. It is intentionally smaller than the full
// upstream ClientRepresentation — only the fields organization-controller
// (slice C1) needs are surfaced. Unmarshal preserves unknown fields by
// matching only declared keys; future fields can be added without breaking
// existing callers.
//
// Per docs/CLAUDE.md §10 the ClientSecret field is NEVER persisted to
// disk by catalyst-api or returned in HTTP responses to the operator —
// it lives only in memory long enough to be written into a SealedSecret
// (organization-controller's job, not this layer).
type OIDCClient struct {
	// ID is the Keycloak-internal UUID. Empty before CreateClient, populated
	// after by GetClient / FindClientByClientID.
	ID string `json:"id,omitempty"`

	// ClientID is the human-readable identifier the operator picks
	// (e.g. "acme-app", "hubble-ui").
	ClientID string `json:"clientId"`

	// Name is the display name shown in the Keycloak admin UI.
	Name string `json:"name,omitempty"`

	// Description is free-form prose for the Keycloak admin UI.
	Description string `json:"description,omitempty"`

	// Enabled gates whether tokens can be issued for this client.
	Enabled bool `json:"enabled"`

	// PublicClient = true for SPAs (PKCE flow, no secret); false for
	// confidential clients (server-to-server, has secret).
	PublicClient bool `json:"publicClient"`

	// ClientAuthenticatorType is "client-secret" by default for
	// confidential clients.
	ClientAuthenticatorType string `json:"clientAuthenticatorType,omitempty"`

	// Secret is the OIDC client_secret. Populated by Keycloak on
	// CreateClient (when PublicClient=false). NEVER persisted to disk —
	// caller writes it into a SealedSecret.
	Secret string `json:"secret,omitempty"`

	// Protocol is "openid-connect" for OIDC. Other Keycloak protocols
	// (saml) are out of scope here.
	Protocol string `json:"protocol,omitempty"`

	// RootURL prefixes RedirectUris when they start with /; equivalent to
	// the Keycloak admin UI's Root URL field.
	RootURL string `json:"rootUrl,omitempty"`

	// RedirectURIs lists permitted redirect targets after login. Glob
	// patterns are accepted by Keycloak (e.g. "https://acme.example/*").
	RedirectURIs []string `json:"redirectUris,omitempty"`

	// WebOrigins lists permitted CORS origins. Use ["+"] to mirror
	// RedirectURIs, or ["*"] to allow any origin.
	WebOrigins []string `json:"webOrigins,omitempty"`

	// StandardFlowEnabled = true enables Authorization Code Flow.
	StandardFlowEnabled bool `json:"standardFlowEnabled"`

	// DirectAccessGrantsEnabled = true enables Resource Owner Password
	// Credentials grant (use sparingly — only for kubectl-style CLIs).
	DirectAccessGrantsEnabled bool `json:"directAccessGrantsEnabled"`

	// ServiceAccountsEnabled = true gives the client a Keycloak
	// ServiceAccount so it can call the Admin REST API itself
	// (used by catalyst-api-server in the Sovereign realm).
	ServiceAccountsEnabled bool `json:"serviceAccountsEnabled"`

	// FrontchannelLogout = true enables OIDC front-channel logout.
	FrontchannelLogout bool `json:"frontchannelLogout"`

	// Attributes is a map of Keycloak client attributes. Common entries:
	// "post.logout.redirect.uris", "pkce.code.challenge.method",
	// "use.refresh.tokens".
	Attributes map[string]string `json:"attributes,omitempty"`
}

// FindClientByClientID looks up an OIDC client by its human-readable
// clientId (e.g. "acme-app"). Returns the empty struct + nil error if no
// such client exists — callers wanting strict "must exist" semantics
// should check the returned ID for emptiness.
//
// The Keycloak Admin API's GET /clients?clientId=X returns a list (not a
// single object) because clientId is unique only WITHIN a realm and an
// older API version allowed duplicates; the modern realm enforces
// uniqueness at admission so the list is at most 1 element.
func (c *Client) FindClientByClientID(ctx context.Context, clientID string) (OIDCClient, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return OIDCClient{}, fmt.Errorf("keycloak.FindClientByClientID: service account token: %w", err)
	}
	return c.findClientByClientID(ctx, saToken, clientID)
}

func (c *Client) findClientByClientID(ctx context.Context, saToken, clientID string) (OIDCClient, error) {
	u := fmt.Sprintf("%s/admin/realms/%s/clients?clientId=%s",
		c.addr, c.realm, url.QueryEscape(clientID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return OIDCClient{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return OIDCClient{}, fmt.Errorf("keycloak: GET clients: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return OIDCClient{}, fmt.Errorf("keycloak: GET clients %d: %s", resp.StatusCode, body)
	}

	var clients []OIDCClient
	if err := json.Unmarshal(body, &clients); err != nil {
		return OIDCClient{}, fmt.Errorf("keycloak: decode clients: %w", err)
	}
	if len(clients) == 0 {
		return OIDCClient{}, nil
	}
	return clients[0], nil
}

// GetClient looks up a Keycloak OIDC client by its internal UUID. Returns
// ErrClientNotFound on 404 so callers can branch on absence vs other
// errors.
func (c *Client) GetClient(ctx context.Context, uuid string) (OIDCClient, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return OIDCClient{}, fmt.Errorf("keycloak.GetClient: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/clients/%s", c.addr, c.realm, url.PathEscape(uuid))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return OIDCClient{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return OIDCClient{}, fmt.Errorf("keycloak: GET client: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return OIDCClient{}, ErrClientNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return OIDCClient{}, fmt.Errorf("keycloak: GET client %d: %s", resp.StatusCode, body)
	}

	var oc OIDCClient
	if err := json.Unmarshal(body, &oc); err != nil {
		return OIDCClient{}, fmt.Errorf("keycloak: decode client: %w", err)
	}
	return oc, nil
}

// ListClients returns the realm's full OIDC client list. Used by the
// access-matrix UI (EPIC-3 #1098) to enumerate per-Org clients. The
// upstream API is paginated via `first` + `max`; for now we request the
// first 1000 entries which suffices for any realistic Sovereign realm
// (an organization-per-thousand bound).
func (c *Client) ListClients(ctx context.Context) ([]OIDCClient, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.ListClients: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/clients?first=0&max=1000", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET clients: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET clients %d: %s", resp.StatusCode, body)
	}

	var clients []OIDCClient
	if err := json.Unmarshal(body, &clients); err != nil {
		return nil, fmt.Errorf("keycloak: decode clients: %w", err)
	}
	return clients, nil
}

// CreateClient registers a new OIDC client in the realm. On success
// returns the Keycloak-assigned UUID. If the clientId is already taken
// the call returns errClientAlreadyExists — the EnsureClient wrapper
// re-finds in that case for idempotent reconciliation.
//
// Keycloak's POST /clients returns 201 with an empty body and the new
// UUID in the Location header (last URL path segment).
func (c *Client) CreateClient(ctx context.Context, oc OIDCClient) (string, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return "", fmt.Errorf("keycloak.CreateClient: service account token: %w", err)
	}
	return c.createClient(ctx, saToken, oc)
}

func (c *Client) createClient(ctx context.Context, saToken string, oc OIDCClient) (string, error) {
	// Default protocol to openid-connect if the caller left it empty —
	// the Keycloak Admin API rejects empty protocol with 400.
	if oc.Protocol == "" {
		oc.Protocol = "openid-connect"
	}

	body, err := json.Marshal(oc)
	if err != nil {
		return "", err
	}

	u := fmt.Sprintf("%s/admin/realms/%s/clients", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: POST client: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated:
		// Keycloak returns the new UUID via Location header — last path
		// segment. Parse defensively in case Keycloak ever changes the
		// shape.
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", errors.New("keycloak: POST client 201 without Location header")
		}
		// Extract the UUID from .../clients/{uuid}
		if idx := lastSegment(loc); idx != "" {
			return idx, nil
		}
		return "", fmt.Errorf("keycloak: POST client 201 Location parse failed: %s", loc)
	case http.StatusConflict:
		return "", errClientAlreadyExists
	default:
		return "", fmt.Errorf("keycloak: POST client %d: %s", resp.StatusCode, respBody)
	}
}

// UpdateClient replaces the realm's client with the given representation.
// The .ID field on oc must be set to the Keycloak UUID. Keycloak's PUT
// /clients/{uuid} requires the FULL client representation (it's a replace
// not a patch) — callers should call GetClient first, mutate, then
// UpdateClient.
//
// On success returns 204 No Content. On 404 returns ErrClientNotFound.
func (c *Client) UpdateClient(ctx context.Context, oc OIDCClient) error {
	if oc.ID == "" {
		return errors.New("keycloak.UpdateClient: oc.ID (Keycloak UUID) is required")
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.UpdateClient: service account token: %w", err)
	}

	body, err := json.Marshal(oc)
	if err != nil {
		return err
	}

	u := fmt.Sprintf("%s/admin/realms/%s/clients/%s",
		c.addr, c.realm, url.PathEscape(oc.ID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT client: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrClientNotFound
	default:
		return fmt.Errorf("keycloak: PUT client %d: %s", resp.StatusCode, respBody)
	}
}

// DeleteClient removes an OIDC client by its Keycloak UUID. Returns nil
// on 204, ErrClientNotFound on 404 so callers can treat absent-as-success.
func (c *Client) DeleteClient(ctx context.Context, uuid string) error {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.DeleteClient: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/clients/%s", c.addr, c.realm, url.PathEscape(uuid))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: DELETE client: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrClientNotFound
	default:
		return fmt.Errorf("keycloak: DELETE client %d: %s", resp.StatusCode, respBody)
	}
}

// EnsureClient is the find-or-create shorthand organization-controller
// (slice C1) calls per Organization. If a client with the given clientId
// exists the existing UUID is returned; otherwise CreateClient is called
// and the new UUID is returned. The 409-race path re-finds idempotently.
//
// Returns (uuid, secret, error). The secret is non-empty only on the
// CREATE path — Keycloak does not echo the secret on subsequent reads,
// it must be fetched separately via /clients/{uuid}/client-secret. That
// fetcher lives in slice D1c (group-attr/secret operations) so this slice
// returns "" for the secret on the find path.
func (c *Client) EnsureClient(ctx context.Context, oc OIDCClient) (uuid string, secret string, err error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return "", "", fmt.Errorf("keycloak.EnsureClient: service account token: %w", err)
	}

	existing, err := c.findClientByClientID(ctx, saToken, oc.ClientID)
	if err != nil {
		return "", "", fmt.Errorf("keycloak.EnsureClient: find: %w", err)
	}
	if existing.ID != "" {
		return existing.ID, "", nil
	}

	uuid, err = c.createClient(ctx, saToken, oc)
	if errors.Is(err, errClientAlreadyExists) {
		// 409 race — re-find by clientId and return the now-existing UUID.
		existing, err = c.findClientByClientID(ctx, saToken, oc.ClientID)
		if err != nil {
			return "", "", fmt.Errorf("keycloak.EnsureClient: re-find after 409: %w", err)
		}
		if existing.ID == "" {
			return "", "", errors.New("keycloak.EnsureClient: 409 conflict but find returned empty")
		}
		return existing.ID, "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("keycloak.EnsureClient: create: %w", err)
	}
	// On the CREATE path the caller-supplied oc.Secret is what Keycloak
	// stored. Echo it back so the caller can write it into a SealedSecret.
	return uuid, oc.Secret, nil
}

// lastSegment returns the final path segment of a URL. Keycloak's
// POST /clients returns the new UUID as the last segment of the
// Location header; this helper extracts it.
func lastSegment(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			if i == len(s)-1 {
				continue
			}
			return s[i+1:]
		}
	}
	return s
}
