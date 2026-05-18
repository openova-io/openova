// sandbox_auth.go — real handlers for sandbox.auth.* (Keycloak realm +
// OIDC client management).
//
// Scope (architecture.md §3 + claude-code-byos.md §4): an agent shipping
// an app from inside a Sandbox usually needs a federated identity
// surface for that app's users — login, redirect, refresh, logout.
// Rather than bolt-on per-app Keycloak admin work for the operator,
// Sandbox exposes a thin slice of the Keycloak Admin REST API gated to
// the Sandbox's OWN realm:
//
//   - sandbox.auth.provisionRealm {realm_name}           → POST /admin/realms
//   - sandbox.auth.listClients                           → GET /admin/realms/<r>/clients
//   - sandbox.auth.registerClient {client_id, redirect_uris}
//                                                        → POST /admin/realms/<r>/clients
//
// All three call Keycloak using the sandbox-controller-injected admin
// bearer (env.KeycloakAdminToken) against env.KeycloakAdminURL — we do
// NOT re-do client_credentials here because the controller already
// minted a short-lived bearer with the right scopes (realm-admin /
// manage-realm); this MCP server is a thin wrapper.
//
// Two scope guardrails differ from sandbox.db.* (which only touches the
// Sandbox's own k8s namespace):
//
//   1. listClients + registerClient always target the Sandbox's OWN
//      realm — derived deterministically from env.OrgID +
//      env.SandboxID via sandboxRealmName(). The agent CANNOT pass a
//      `realm` argument to redirect the call elsewhere.
//
//   2. provisionRealm accepts a free-form `realm_name` (an app may need
//      multiple realms, e.g. one per app rather than one per Sandbox),
//      but the rendered realm is tagged with the
//      `openova.io/sandbox-id` attribute so listClients / a future
//      sandbox.auth.listRealms can filter to Sandbox-managed realms only.
//
// Auth: same shape as sandbox.db.* — claims.OrgID must match env.OrgID,
// RequiredCapability = "sandbox.auth", and every call requires
// env.KeycloakAdminURL + env.KeycloakAdminToken to be set or the handler
// returns a clear "controller didn't wire keycloak admin" error.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// kcRealmNameRE constrains the realm-name argument the agent supplies to
// provisionRealm. Keycloak itself accepts a wider range, but a strict
// surface here keeps the rendered realm URLs (`/realms/<name>/.well-known/...`)
// predictable AND prevents an agent from supplying e.g. `..` paths.
var kcRealmNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)

// kcClientIDRE matches the human-readable Keycloak clientId. Same
// rationale as kcRealmNameRE — strict enough to keep the rendered
// `/admin/realms/<r>/clients?clientId=<id>` URL well-formed.
var kcClientIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,127}$`)

// kcSandboxAttribute marks every realm sandbox.auth.provisionRealm
// creates. Surfaced in the JSON response so the agent (or a sibling
// observer) can correlate a realm back to a Sandbox without keeping a
// separate ledger.
const (
	kcManagedByAttribute = "openova.io/managed-by"
	kcManagedByValue     = "openova-sandbox-mcp"
	kcSandboxIDAttribute = "openova.io/sandbox-id"
)

// kcHTTPTimeout caps every Admin REST call. Keycloak inside the cluster
// is fast (<200ms p99); anything slower is a degraded backend and the
// agent wants a fast error, not a 60s hang.
const kcHTTPTimeout = 15 * time.Second

// --- helpers -----------------------------------------------------------

// requireKeycloak is the canonical gate every sandbox.auth.* handler
// runs first: the controller must have wired BOTH KEYCLOAK_ADMIN_URL
// and KEYCLOAK_ADMIN_TOKEN. If either is empty the tool surfaces a
// well-typed error rather than letting an unauthenticated GET leak
// past as a confusing 401 from Keycloak.
func requireKeycloak(env *Env) error {
	if env == nil {
		return errors.New("sandbox.auth: server env missing on context")
	}
	if strings.TrimSpace(env.KeycloakAdminURL) == "" {
		return errors.New("sandbox.auth: KEYCLOAK_ADMIN_URL not set (sandbox-controller didn't wire keycloak admin)")
	}
	if strings.TrimSpace(env.KeycloakAdminToken) == "" {
		return errors.New("sandbox.auth: KEYCLOAK_ADMIN_TOKEN not set (sandbox-controller didn't mount admin Secret)")
	}
	return nil
}

// sandboxRealmName returns the deterministic realm name for the
// Sandbox bound to env. Format: `sandbox-<org>-<id>` (lowercased,
// dashes-only). This is the realm that listClients + registerClient
// target. provisionRealm can create OTHER realms, but the Sandbox's
// "own" realm is reserved for the agent's primary auth surface.
func sandboxRealmName(env *Env) string {
	org := strings.ToLower(strings.TrimSpace(env.OrgID))
	id := strings.ToLower(strings.TrimSpace(env.SandboxID))
	if org == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("sandbox-%s-%s", org, id)
}

// kcHTTPClient returns the per-process HTTP client used for every
// Keycloak Admin call. Centralised so the timeout + the Authorization
// header are applied uniformly.
func kcHTTPClient() *http.Client {
	return &http.Client{Timeout: kcHTTPTimeout}
}

// kcDo runs a JSON HTTP request against Keycloak with the env's admin
// bearer. Returns the response body bytes + status; the caller decodes.
// Body is closed before return.
func kcDo(ctx context.Context, env *Env, method, urlStr string, payload any) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal payload: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+env.KeycloakAdminToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := kcHTTPClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("keycloak %s %s: %w", method, urlStr, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out, resp.StatusCode, nil
}

// --- sandbox.auth.provisionRealm ---------------------------------------

type sandboxAuthProvisionRealmArgs struct {
	// RealmName — the new realm to create. Must match kcRealmNameRE.
	// Recommended pattern: `<sandbox-id>-<app-slug>` so a single
	// Sandbox can host multiple per-app realms without name collisions
	// across the Keycloak instance.
	RealmName string `json:"realm_name"`

	// DisplayName — optional human-readable label shown in the
	// Keycloak admin UI. Defaults to RealmName.
	DisplayName string `json:"display_name,omitempty"`
}

func sandboxAuthProvisionRealm(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxAuthProvisionRealmArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.auth.provisionRealm: invalid arguments: %w", err)
	}
	args.RealmName = strings.TrimSpace(args.RealmName)
	if !kcRealmNameRE.MatchString(args.RealmName) {
		return nil, fmt.Errorf("sandbox.auth.provisionRealm: `realm_name` must match %s", kcRealmNameRE.String())
	}
	env := EnvFrom(ctx)
	if err := requireKeycloak(env); err != nil {
		return nil, err
	}
	display := args.DisplayName
	if display == "" {
		display = args.RealmName
	}
	payload := map[string]any{
		"realm":       args.RealmName,
		"displayName": display,
		"enabled":     true,
		"attributes": map[string]string{
			kcManagedByAttribute: kcManagedByValue,
			kcSandboxIDAttribute: env.SandboxID,
		},
	}
	urlStr := strings.TrimRight(env.KeycloakAdminURL, "/") + "/admin/realms"
	body, status, err := kcDo(ctx, env, http.MethodPost, urlStr, payload)
	if err != nil {
		return nil, fmt.Errorf("sandbox.auth.provisionRealm: %w", err)
	}
	switch status {
	case http.StatusCreated:
		return map[string]any{
			"status":     "Created",
			"realm":      args.RealmName,
			"issuer_url": strings.TrimRight(env.KeycloakAdminURL, "/") + "/realms/" + args.RealmName,
			"managed_by": kcManagedByValue,
			"sandbox_id": env.SandboxID,
			"note":       "Per-Sandbox realm created. Use sandbox.auth.registerClient to add OIDC clients.",
		}, nil
	case http.StatusConflict:
		// Idempotent: realm already exists. Return the same shape so
		// agents can call provisionRealm safely on every session.
		return map[string]any{
			"status":     "AlreadyExists",
			"realm":      args.RealmName,
			"issuer_url": strings.TrimRight(env.KeycloakAdminURL, "/") + "/realms/" + args.RealmName,
			"note":       "Realm already exists (idempotent no-op).",
		}, nil
	default:
		return nil, fmt.Errorf("sandbox.auth.provisionRealm: keycloak POST /admin/realms returned %d: %s", status, body)
	}
}

func schemaSandboxAuthProvisionRealm() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"realm_name"},
		"properties": map[string]any{
			"realm_name": map[string]any{
				"type":        "string",
				"description": "Keycloak realm name. Lowercase alnum + hyphen, 3-63 chars, leading letter.",
				"pattern":     kcRealmNameRE.String(),
			},
			"display_name": map[string]any{
				"type":        "string",
				"description": "Optional human-readable label shown in the Keycloak admin UI. Defaults to realm_name.",
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.auth.listClients ------------------------------------------

// kcOIDCClient is a slim subset of Keycloak's ClientRepresentation —
// only the fields the agent actually consumes (id, clientId,
// redirectUris, enabled, publicClient, protocol). Avoids leaking the
// full schema + any sensitive `secret` field via this read endpoint.
type kcOIDCClient struct {
	ID           string   `json:"id"`
	ClientID     string   `json:"clientId"`
	Enabled      bool     `json:"enabled"`
	PublicClient bool     `json:"publicClient"`
	Protocol     string   `json:"protocol,omitempty"`
	RedirectURIs []string `json:"redirectUris,omitempty"`
	WebOrigins   []string `json:"webOrigins,omitempty"`
	Name         string   `json:"name,omitempty"`
	Description  string   `json:"description,omitempty"`
}

func sandboxAuthListClients(ctx context.Context, _ json.RawMessage) (any, error) {
	env := EnvFrom(ctx)
	if err := requireKeycloak(env); err != nil {
		return nil, err
	}
	realm := sandboxRealmName(env)
	if realm == "" {
		return nil, errors.New("sandbox.auth.listClients: env missing OrgID/SandboxID (cannot derive Sandbox realm)")
	}
	urlStr := fmt.Sprintf("%s/admin/realms/%s/clients?first=0&max=1000",
		strings.TrimRight(env.KeycloakAdminURL, "/"), url.PathEscape(realm))
	body, status, err := kcDo(ctx, env, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("sandbox.auth.listClients: %w", err)
	}
	if status == http.StatusNotFound {
		// Realm doesn't exist yet — surface a friendly empty list so
		// the agent's first call (before provisionRealm) doesn't 502.
		return map[string]any{
			"realm": realm,
			"count": 0,
			"items": []any{},
			"note":  "Sandbox realm does not exist yet. Call sandbox.auth.provisionRealm first.",
		}, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("sandbox.auth.listClients: keycloak GET clients returned %d: %s", status, body)
	}
	var clients []kcOIDCClient
	if err := json.Unmarshal(body, &clients); err != nil {
		return nil, fmt.Errorf("sandbox.auth.listClients: decode response: %w", err)
	}
	return map[string]any{
		"realm": realm,
		"count": len(clients),
		"items": clients,
	}, nil
}

// --- sandbox.auth.registerClient ---------------------------------------

type sandboxAuthRegisterClientArgs struct {
	// ClientID — the human-readable identifier the agent picks
	// (`acme-app`, `dashboard`). Must match kcClientIDRE.
	ClientID string `json:"client_id"`

	// RedirectURIs — at least one redirect URL the OAuth flow may
	// land on. Globs accepted by Keycloak (`https://app.example/*`).
	RedirectURIs []string `json:"redirect_uris"`

	// PublicClient — true for SPAs (PKCE flow, no secret); false for
	// confidential clients. Defaults to false.
	PublicClient bool `json:"public_client,omitempty"`

	// Name — optional Keycloak admin-UI label.
	Name string `json:"name,omitempty"`
}

func sandboxAuthRegisterClient(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxAuthRegisterClientArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.auth.registerClient: invalid arguments: %w", err)
	}
	args.ClientID = strings.TrimSpace(args.ClientID)
	if !kcClientIDRE.MatchString(args.ClientID) {
		return nil, fmt.Errorf("sandbox.auth.registerClient: `client_id` must match %s", kcClientIDRE.String())
	}
	if len(args.RedirectURIs) == 0 {
		return nil, errors.New("sandbox.auth.registerClient: `redirect_uris` must have at least one entry")
	}
	for _, u := range args.RedirectURIs {
		if strings.TrimSpace(u) == "" {
			return nil, errors.New("sandbox.auth.registerClient: empty entry in `redirect_uris`")
		}
	}
	env := EnvFrom(ctx)
	if err := requireKeycloak(env); err != nil {
		return nil, err
	}
	realm := sandboxRealmName(env)
	if realm == "" {
		return nil, errors.New("sandbox.auth.registerClient: env missing OrgID/SandboxID (cannot derive Sandbox realm)")
	}
	payload := map[string]any{
		"clientId":            args.ClientID,
		"enabled":             true,
		"publicClient":        args.PublicClient,
		"protocol":            "openid-connect",
		"redirectUris":        args.RedirectURIs,
		"standardFlowEnabled": true,
		"webOrigins":          []string{"+"},
		"attributes": map[string]string{
			kcManagedByAttribute: kcManagedByValue,
			kcSandboxIDAttribute: env.SandboxID,
		},
	}
	if args.Name != "" {
		payload["name"] = args.Name
	}
	urlStr := fmt.Sprintf("%s/admin/realms/%s/clients",
		strings.TrimRight(env.KeycloakAdminURL, "/"), url.PathEscape(realm))
	body, status, err := kcDo(ctx, env, http.MethodPost, urlStr, payload)
	if err != nil {
		return nil, fmt.Errorf("sandbox.auth.registerClient: %w", err)
	}
	switch status {
	case http.StatusCreated:
		return map[string]any{
			"status":        "Created",
			"realm":         realm,
			"client_id":     args.ClientID,
			"public_client": args.PublicClient,
			"redirect_uris": args.RedirectURIs,
			"issuer_url":    strings.TrimRight(env.KeycloakAdminURL, "/") + "/realms/" + realm,
			"note":          "Client registered. For confidential clients (public_client=false), fetch the secret via Keycloak admin UI or sandbox.auth.* future tool.",
		}, nil
	case http.StatusConflict:
		return map[string]any{
			"status":    "AlreadyExists",
			"realm":     realm,
			"client_id": args.ClientID,
			"note":      "Client with this clientId already exists in the Sandbox realm (idempotent no-op).",
		}, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("sandbox.auth.registerClient: realm %q does not exist (call sandbox.auth.provisionRealm first)", realm)
	default:
		return nil, fmt.Errorf("sandbox.auth.registerClient: keycloak POST clients returned %d: %s", status, body)
	}
}

func schemaSandboxAuthRegisterClient() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"client_id", "redirect_uris"},
		"properties": map[string]any{
			"client_id": map[string]any{
				"type":        "string",
				"description": "Human-readable Keycloak clientId (e.g. `acme-app`). 2-128 chars, alnum + `._-`.",
				"pattern":     kcClientIDRE.String(),
			},
			"redirect_uris": map[string]any{
				"type":        "array",
				"description": "Permitted redirect URLs after OAuth login (Keycloak accepts globs like `https://app.example/*`).",
				"items":       map[string]any{"type": "string"},
				"minItems":    1,
			},
			"public_client": map[string]any{
				"type":        "boolean",
				"description": "True for SPAs (PKCE flow, no client_secret). Default false (confidential client).",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Optional Keycloak admin-UI display label.",
			},
		},
		"additionalProperties": false,
	}
}
