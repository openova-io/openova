// Package keycloak — admin_proxy.go: per-Sovereign Keycloak admin
// operations the catalyst-api proxy surfaces (qa-loop iter-1 Fix #104,
// brief `iter1-prov8-prefetch-fix-authors.md`).
//
// Every operation here pre-authenticates the catalyst-api's own service
// account against `/realms/{realm}/protocol/openid-connect/token` and
// reuses the token for the Admin API call against Keycloak's
// in-cluster service (`https://keycloak.keycloak.svc.cluster.local:8443`).
// The QA matrix calls these via the catalyst-api proxy
// (`/api/v1/sovereigns/{id}/keycloak/admin/...`) and NEVER reaches
// Keycloak directly — see `internal/handler/keycloak_proxy.go` for the
// HTTP surface + tier-admin authentication gate.
//
// Per docs/INVIOLABLE-PRINCIPLES.md §4: the Sovereign realm's admin
// password / direct-access grants stay in-cluster; the proxy authenticates
// the operator with their tier-admin claim, then catalyst-api uses its
// own SA credential to perform the privileged Keycloak Admin API call.
// The operator never sees the SA secret.
package keycloak

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// PasswordGrantTokenResponse mirrors the slice of the
// /realms/{realm}/protocol/openid-connect/token response shape the QA
// matrix asserts on (TC-176): the access_token + refresh_token + expiry
// + the realm_access.roles list embedded inside the access_token JWT
// payload. The handler returns the raw upstream JSON bytes verbatim so
// the matrix can grep for `access_token`, `refresh_token`, role names,
// and `invalid_grant` without us re-shaping the wire contract.
type PasswordGrantTokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope,omitempty"`
}

// PasswordGrantToken performs a Resource-Owner-Password-Credentials
// (ROPC, RFC 6749 §4.3) token mint against the Sovereign realm for the
// QA matrix (TC-176: assert that qa-user1's freshly-minted token
// contains realm_access.roles=[catalyst-developer, catalyst-viewer]).
//
// Auth model: the proxy handler authenticates the CALLER as tier-admin
// (sovereign-admin gate). This method exchanges a USERNAME+PASSWORD
// supplied by the caller — typically a QA-fixture user (`qa-user1`)
// pre-provisioned in the realm — for an access_token via the
// `direct_access_grants_enabled=true` OIDC client (e.g. `kubectl-oidc`
// or `qa-token-mint`). The catalyst-api SA secret is NOT used here;
// password grant is a public-facing OIDC flow gated solely by the
// realm's password policy + the OIDC client's directAccessGrantsEnabled
// flag.
//
// Returns the raw response body bytes (so the matcher can assert on
// claim names lifted directly out of the JWT) plus the parsed envelope
// for the typed callers.
//
// On non-2xx the function returns the upstream error body bytes — the
// matrix asserts on `invalid_grant` literal text from a wrong-password
// path so we MUST surface it verbatim instead of re-wrapping.
func (c *Client) PasswordGrantToken(ctx context.Context, oidcClientID, username, password string) ([]byte, int, error) {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.addr, c.realm)

	data := url.Values{}
	data.Set("grant_type", "password")
	// `client_id` here is the OIDC client the realm trusts to issue
	// password-grant tokens — typically a public client with
	// `directAccessGrantsEnabled=true`. The catalyst-api SA is NOT
	// used (its grant is `client_credentials`, not `password`).
	data.Set("client_id", oidcClientID)
	data.Set("username", username)
	data.Set("password", password)
	data.Set("scope", "openid")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("keycloak.PasswordGrantToken: POST token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

// ListClientServiceAccountRealmRoles proxies
// GET /admin/realms/{realm}/clients/{clientUuid}/service-account-user/role-mappings/realm
// (TC-190 — assert catalyst-api SA holds realm-management roles
// `manage-realm`, `view-realm`, `view-clients`).
//
// `oidcClientUUID` is the Keycloak UUID of the OIDC client whose
// service-account user we're querying. Resolve via FindClientByClientID
// when only the human-readable clientId is known (e.g. "catalyst-api").
//
// Returns the upstream JSON body verbatim so callers can assert on
// role names that are present in the role-name strings inside the
// payload without re-shaping it.
func (c *Client) ListClientServiceAccountRealmRoles(ctx context.Context, oidcClientUUID string) ([]byte, int, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("keycloak.ListClientServiceAccountRealmRoles: service account token: %w", err)
	}
	u := fmt.Sprintf("%s/admin/realms/%s/clients/%s/service-account-user/role-mappings/realm",
		c.addr, c.realm, url.PathEscape(oidcClientUUID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("keycloak: GET service-account role-mappings: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

// CreateIdentityProviderMapper is the public POST-only entrypoint for
// the proxy (TC-161). Unlike EnsureIdentityProviderMapper, this method
// does NOT pre-list — it just POSTs and lets Keycloak return 409 if the
// mapper already exists, surfacing that to the caller via the standard
// error path. Idempotency is the caller's concern (the proxy handler
// surfaces any non-success status from Keycloak verbatim).
//
// `mapper.IdentityProviderAlias` MUST equal `alias` (Keycloak validates
// this on POST); the function fills it in if empty and rejects a
// mismatch up-front to keep the error message intelligible.
func (c *Client) CreateIdentityProviderMapper(ctx context.Context, alias string, mapper IdentityProviderMapper) error {
	if mapper.Name == "" {
		return fmt.Errorf("keycloak.CreateIdentityProviderMapper: mapper.Name is required")
	}
	if mapper.IdentityProviderAlias == "" {
		mapper.IdentityProviderAlias = alias
	}
	if mapper.IdentityProviderAlias != alias {
		return fmt.Errorf("keycloak.CreateIdentityProviderMapper: mapper.IdentityProviderAlias=%q ≠ url alias=%q",
			mapper.IdentityProviderAlias, alias)
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.CreateIdentityProviderMapper: service account token: %w", err)
	}
	return c.createIdentityProviderMapper(ctx, saToken, alias, mapper)
}

// MarshalRealmRolesJSON renders []RealmRole back to JSON for the
// proxy's verbatim-response path (TC-124 / TC-125). Used by the
// handlers when surfacing the upstream KC body shape AS-IS without
// re-wrapping in an `items` envelope (the matrix asserts directly on
// the role-name string presence in the body).
func MarshalRealmRolesJSON(roles []RealmRole) ([]byte, error) {
	return json.Marshal(roles)
}

// MarshalIdentityProvidersJSON — same contract as
// MarshalRealmRolesJSON but for TC-159's []IdentityProvider response.
func MarshalIdentityProvidersJSON(idps []IdentityProvider) ([]byte, error) {
	return json.Marshal(idps)
}

// MarshalIdentityProviderJSON — single IdP envelope for TC-160's
// POST /identity-provider/instances response. After the create POST we
// re-fetch via GetIdentityProvider so the response carries the canonical
// representation Keycloak persisted (the alias the matrix asserts on).
func MarshalIdentityProviderJSON(idp IdentityProvider) ([]byte, error) {
	return json.Marshal(idp)
}

// MarshalOIDCClientsJSON — renders []OIDCClient for TC-285 (GET
// /admin/realms/{realm}/clients?clientId=netbird).
func MarshalOIDCClientsJSON(clients []OIDCClient) ([]byte, error) {
	return json.Marshal(clients)
}
