package keycloak

// admin_secrets.go — Per-OIDC-client client-secret read + rotation.
//
// Slice D1c of EPIC-0 #1095. organization-controller (slice C1) calls
// these when it provisions a new OIDC client for a per-Org integration:
// the freshly-generated secret is fetched from Keycloak, written into
// a SealedSecret on the cluster, and never persisted in catalyst-api's
// own state.
//
// Per docs/CLAUDE.md §10 the secret is in memory only long enough to
// be handed to the SealedSecret writer.
//
// Endpoints used:
//   GET  /admin/realms/{realm}/clients/{uuid}/client-secret
//   POST /admin/realms/{realm}/clients/{uuid}/client-secret  (regenerate)

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ClientSecret carries the secret value Keycloak emits on read or
// rotation. The Type field is constant ("secret") for OIDC clients
// in client-secret authenticator mode but is preserved here so the
// access-matrix UI can detect any future authenticator types.
type ClientSecret struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value"`
}

// GetClientSecret fetches the current client_secret of an OIDC client
// (by Keycloak UUID). Returns ErrClientNotFound on 404.
//
// The returned ClientSecret.Value is plaintext — caller MUST write it
// to a SealedSecret immediately and never log it.
func (c *Client) GetClientSecret(ctx context.Context, uuid string) (ClientSecret, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return ClientSecret{}, fmt.Errorf("keycloak.GetClientSecret: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/clients/%s/client-secret",
		c.addr, c.realm, url.PathEscape(uuid))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ClientSecret{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return ClientSecret{}, fmt.Errorf("keycloak: GET client-secret: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ClientSecret{}, ErrClientNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return ClientSecret{}, fmt.Errorf("keycloak: GET client-secret %d: %s", resp.StatusCode, body)
	}
	var s ClientSecret
	if err := json.Unmarshal(body, &s); err != nil {
		return ClientSecret{}, fmt.Errorf("keycloak: decode client-secret: %w", err)
	}
	return s, nil
}

// RotateClientSecret asks Keycloak to generate a new client_secret for
// the OIDC client (by Keycloak UUID). The new secret is returned in
// the response body; the previous secret is invalidated immediately
// (no overlap window — callers must write the new value to the
// SealedSecret + restart consumers atomically).
//
// Used by the SecretPolicy reconciler (post-Phase-0) per
// docs/EPICS-1-6-unified-design.md §3.2.6.
//
// Returns ErrClientNotFound on 404.
func (c *Client) RotateClientSecret(ctx context.Context, uuid string) (ClientSecret, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return ClientSecret{}, fmt.Errorf("keycloak.RotateClientSecret: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/clients/%s/client-secret",
		c.addr, c.realm, url.PathEscape(uuid))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return ClientSecret{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return ClientSecret{}, fmt.Errorf("keycloak: POST client-secret: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ClientSecret{}, ErrClientNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return ClientSecret{}, fmt.Errorf("keycloak: POST client-secret %d: %s", resp.StatusCode, body)
	}
	var s ClientSecret
	if err := json.Unmarshal(body, &s); err != nil {
		return ClientSecret{}, fmt.Errorf("keycloak: decode rotated secret: %w", err)
	}
	return s, nil
}
