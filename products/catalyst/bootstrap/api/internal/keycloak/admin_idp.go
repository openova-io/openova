package keycloak

// admin_idp.go — Keycloak Identity Provider CRUD (slice F1 of EPIC-3
// #1098). organization-controller (slice C1+F2) calls these to wire
// per-Organization Azure AD / Okta / generic-OIDC federation into the
// per-Sovereign Keycloak realm: each Org with `spec.identity.federationProvider`
// non-empty gets its own IdP instance + claim mappers, aliased by the
// Org's slug so two Orgs federating to the same upstream IdP stay
// isolated.
//
// The Catalyst convention is to refer to the IdP by its `Alias` (a
// human-readable + unique-within-realm string, e.g. "azure-sso-acme")
// — Keycloak's REST API uses the alias as the path segment for every
// IdP-instance write, so no `Alias` ↔ UUID translation is needed (unlike
// the OIDC client surface in admin.go).
//
// Endpoints used (Keycloak Admin REST API, version 24.x):
//
//   IdP instances:
//   GET    /admin/realms/{realm}/identity-provider/instances
//   GET    /admin/realms/{realm}/identity-provider/instances/{alias}
//   POST   /admin/realms/{realm}/identity-provider/instances
//   PUT    /admin/realms/{realm}/identity-provider/instances/{alias}
//   DELETE /admin/realms/{realm}/identity-provider/instances/{alias}
//
//   IdP mappers (claim mappers):
//   GET    /admin/realms/{realm}/identity-provider/instances/{alias}/mappers
//   POST   /admin/realms/{realm}/identity-provider/instances/{alias}/mappers
//   PUT    /admin/realms/{realm}/identity-provider/instances/{alias}/mappers/{mapperId}
//
// Idempotency anchor (per the T2 seam-map entry / 01-canonical-seams.md):
// every "Ensure" method does a GET-by-alias (or list-by-name for mappers)
// and short-circuits when the desired representation is already on
// the realm. Drift is detected by a deep-equal of the slice of fields
// catalyst writes (Config map + ProviderID + Enabled + alias-bound
// metadata); if drift is observed, a single PUT replaces the full
// representation. Re-runs on a steady-state realm produce 0 writes.
//
// Per docs/INVIOLABLE-PRINCIPLES.md §5 the OIDC client_secret value
// (carried in IdentityProvider.Config["clientSecret"]) NEVER lives on
// disk inside catalyst-api — it lives in memory long enough to be
// PUT/POSTed onto Keycloak, then the in-memory copy is dropped. The
// caller (organization-controller F2) is responsible for sourcing it
// from a K8s Secret (clientSecretRef) and never echoing it back.

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

// ErrIdentityProviderNotFound is returned by GetIdentityProvider /
// DeleteIdentityProvider when the alias does not resolve. Mirrors
// ErrClientNotFound + ErrRoleNotFound so reconciliation loops can
// branch on absence-as-success.
var ErrIdentityProviderNotFound = errors.New("keycloak: identity provider not found")

// errIdentityProviderAlreadyExists is the internal sentinel for the
// 409 path on POST /identity-provider/instances. EnsureIdentityProvider
// catches it and re-finds via GetIdentityProvider.
var errIdentityProviderAlreadyExists = errors.New("keycloak: identity provider already exists")

// IdentityProvider matches the slice of Keycloak's IdentityProviderRepresentation
// catalyst-api consumes / writes. Per slice F1 brief: `Alias` is the
// per-Org-bound human-readable identifier (e.g. "azure-sso-acme"),
// `ProviderID` is the Keycloak provider type ("oidc", "saml",
// "microsoft", "google" — Catalyst's MVP uses "oidc" for Azure SSO and
// generic OIDC, and Keycloak's built-in "microsoft" for the
// Azure-AD-specific consent UX).
//
// The `Config` map carries the OIDC-protocol-specific settings: clientId,
// clientSecret, authorizationUrl, tokenUrl, jwksUrl, issuer, defaultScope,
// validateSignature ("true"|"false" as a string per Keycloak's quirk
// of all-string Config values).
type IdentityProvider struct {
	// Alias is the per-realm-unique IdP identifier. Used as the URL
	// path segment for every IdP-instance write. Catalyst convention
	// is `<provider>-<org-slug>` (e.g. "azure-sso-acme").
	Alias string `json:"alias"`

	// DisplayName is the human-readable name shown in the Keycloak
	// login page's "Or sign in with" button list.
	DisplayName string `json:"displayName,omitempty"`

	// ProviderID is the Keycloak provider type. Catalyst's MVP ships
	// "oidc" (generic) — Azure-AD-specific UX could later switch to
	// "microsoft" for the Microsoft-account login button styling.
	ProviderID string `json:"providerId"`

	// Enabled gates whether the IdP appears on the realm login page.
	Enabled bool `json:"enabled"`

	// StoreToken=true tells Keycloak to retain the IdP's access_token
	// for the federated user — only useful if catalyst-api wants to
	// call the upstream IdP API on the user's behalf later (rare).
	StoreToken bool `json:"storeToken,omitempty"`

	// AddReadTokenRoleOnCreate=true adds the `broker.read-token`
	// realm-management role to newly federated users so they can
	// retrieve the stored token. Only meaningful when StoreToken=true.
	AddReadTokenRoleOnCreate bool `json:"addReadTokenRoleOnCreate,omitempty"`

	// LinkOnly=true lets existing realm users link their account to
	// the IdP without ever creating a new user via this IdP. Catalyst
	// keeps this `false` for B2B Azure AD federation (the whole point
	// is auto-creating users on first login).
	LinkOnly bool `json:"linkOnly,omitempty"`

	// FirstBrokerLoginFlowAlias selects which Keycloak authentication
	// flow runs the first time a federated user logs in. "first broker
	// login" is the Keycloak default; a future slice may ship a
	// catalyst-customized flow that sets `realm_access.roles` from a
	// claim mapper before the first profile-review step.
	FirstBrokerLoginFlowAlias string `json:"firstBrokerLoginFlowAlias,omitempty"`

	// Config is the protocol-specific config map. Per Keycloak's API
	// quirk EVERY value is a string (booleans are "true"/"false",
	// integers stringified). Common OIDC keys: clientId, clientSecret,
	// authorizationUrl, tokenUrl, jwksUrl, issuer, defaultScope,
	// validateSignature, useJwksUrl, prompt, syncMode.
	Config map[string]string `json:"config"`
}

// IdentityProviderMapper matches the slice of Keycloak's
// IdentityProviderMapperRepresentation catalyst-api uses to map upstream
// IdP claims (oid/upn/groups for Azure AD; sub/email/groups for generic
// OIDC) to Catalyst's internal claim shape (`openova.io/external-id`,
// email, `openova.io/groups`) so the existing UserAccess matching works
// unchanged regardless of which upstream IdP fed the user in.
//
// `IdentityProviderMapper` (the field, NOT the struct) is the mapper
// type — Keycloak ships several built-ins:
//   - `oidc-user-attribute-idp-mapper` — copy a claim into a Keycloak user attribute
//   - `oidc-username-idp-mapper`       — derive username from a claim
//   - `oidc-role-idp-mapper`           — assign a realm role on first-login
//   - `oidc-hardcoded-attribute-idp-mapper` — set a fixed attribute regardless
type IdentityProviderMapper struct {
	// ID is the Keycloak-internal UUID. Empty before POST; populated
	// after by the GET /mappers list call.
	ID string `json:"id,omitempty"`

	// Name is the per-IdP-unique mapper name. Catalyst convention is
	// `<source-claim>-to-<dest-attr>` (e.g. "oid-to-external-id").
	Name string `json:"name"`

	// IdentityProviderAlias links the mapper to its parent IdP. Set
	// by the caller; Keycloak validates it matches the URL path
	// segment.
	IdentityProviderAlias string `json:"identityProviderAlias"`

	// IdentityProviderMapper is the mapper TYPE (e.g.
	// "oidc-user-attribute-idp-mapper"). NOT the alias — the field
	// name is unfortunate but matches the upstream JSON shape.
	IdentityProviderMapper string `json:"identityProviderMapper"`

	// Config is the mapper-type-specific config map. For
	// `oidc-user-attribute-idp-mapper`: `claim` (source) +
	// `user.attribute` (destination) + `syncMode`
	// ("INHERIT"|"IMPORT"|"FORCE"|"LEGACY").
	Config map[string]string `json:"config"`
}

// ListIdentityProviders returns every IdP configured on the realm. Used
// by the access-matrix UI (EPIC-3 #1098) to enumerate per-Org federations
// + by EnsureIdentityProvider's pre-flight (the per-alias GET is one
// round-trip cheaper than a list, but list is required for the
// access-matrix view).
func (c *Client) ListIdentityProviders(ctx context.Context) ([]IdentityProvider, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.ListIdentityProviders: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET identity-provider/instances: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET identity-provider/instances %d: %s", resp.StatusCode, body)
	}
	var idps []IdentityProvider
	if err := json.Unmarshal(body, &idps); err != nil {
		return nil, fmt.Errorf("keycloak: decode identity-provider/instances: %w", err)
	}
	return idps, nil
}

// GetIdentityProvider looks up an IdP by alias. Returns
// ErrIdentityProviderNotFound on 404 so reconciliation loops can branch
// on absence-as-success.
func (c *Client) GetIdentityProvider(ctx context.Context, alias string) (IdentityProvider, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return IdentityProvider{}, fmt.Errorf("keycloak.GetIdentityProvider: service account token: %w", err)
	}
	return c.getIdentityProvider(ctx, saToken, alias)
}

func (c *Client) getIdentityProvider(ctx context.Context, saToken, alias string) (IdentityProvider, error) {
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s",
		c.addr, c.realm, url.PathEscape(alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return IdentityProvider{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return IdentityProvider{}, fmt.Errorf("keycloak: GET identity-provider/instance: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return IdentityProvider{}, ErrIdentityProviderNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return IdentityProvider{}, fmt.Errorf("keycloak: GET identity-provider/instance %d: %s", resp.StatusCode, body)
	}
	var idp IdentityProvider
	if err := json.Unmarshal(body, &idp); err != nil {
		return IdentityProvider{}, fmt.Errorf("keycloak: decode identity-provider/instance: %w", err)
	}
	return idp, nil
}

// CreateIdentityProvider POSTs a new IdP instance. On 409 returns
// errIdentityProviderAlreadyExists so EnsureIdentityProvider can re-find.
//
// Keycloak's POST /identity-provider/instances returns 201 No Content
// (no Location header — unlike clients) because the alias IS the
// stable identifier (provided by the caller).
func (c *Client) CreateIdentityProvider(ctx context.Context, idp IdentityProvider) error {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.CreateIdentityProvider: service account token: %w", err)
	}
	return c.createIdentityProvider(ctx, saToken, idp)
}

func (c *Client) createIdentityProvider(ctx context.Context, saToken string, idp IdentityProvider) error {
	body, err := json.Marshal(idp)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: POST identity-provider/instance: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent:
		return nil
	case http.StatusConflict:
		return errIdentityProviderAlreadyExists
	default:
		return fmt.Errorf("keycloak: POST identity-provider/instance %d: %s", resp.StatusCode, respBody)
	}
}

// UpdateIdentityProvider replaces the full IdP representation by alias.
// The PUT semantics are full-replace (not patch), so callers must pass
// the COMPLETE desired state — typically by GET-ing first, mutating the
// drifted fields, and PUT-ing the result.
//
// On 404 returns ErrIdentityProviderNotFound.
func (c *Client) UpdateIdentityProvider(ctx context.Context, idp IdentityProvider) error {
	if idp.Alias == "" {
		return errors.New("keycloak.UpdateIdentityProvider: idp.Alias is required")
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.UpdateIdentityProvider: service account token: %w", err)
	}
	body, err := json.Marshal(idp)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s",
		c.addr, c.realm, url.PathEscape(idp.Alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT identity-provider/instance: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrIdentityProviderNotFound
	default:
		return fmt.Errorf("keycloak: PUT identity-provider/instance %d: %s", resp.StatusCode, respBody)
	}
}

// DeleteIdentityProvider removes the IdP by alias. Returns
// ErrIdentityProviderNotFound on 404 so reconciliation loops can treat
// absent-as-success (e.g. Org dropped federation; mapper-stripped IdPs
// still want a clean slate).
func (c *Client) DeleteIdentityProvider(ctx context.Context, alias string) error {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.DeleteIdentityProvider: service account token: %w", err)
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s",
		c.addr, c.realm, url.PathEscape(alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: DELETE identity-provider/instance: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrIdentityProviderNotFound
	default:
		return fmt.Errorf("keycloak: DELETE identity-provider/instance %d: %s", resp.StatusCode, respBody)
	}
}

// EnsureIdentityProvider is the find-or-create + drift-correct shorthand
// organization-controller (slice F2) calls per Organization with
// non-empty `spec.identity.federationProvider`. Re-runs on a
// steady-state IdP make 0 writes (one GET, deep-equal short-circuit).
//
// Drift detection compares the slice of fields catalyst writes:
//   - DisplayName, ProviderID, Enabled, LinkOnly, StoreToken,
//     AddReadTokenRoleOnCreate, FirstBrokerLoginFlowAlias
//   - Config map (string-equal on every key in the desired set;
//     unknown server-side keys are NOT considered drift to avoid
//     fighting Keycloak defaults like `pkceEnabled` that the admin
//     UI may add)
//
// On the 409 race path the alias was created by a sibling caller
// between our GET and POST — treat as already-exists and re-GET to
// verify the representation matches.
func (c *Client) EnsureIdentityProvider(ctx context.Context, idp IdentityProvider) error {
	if idp.Alias == "" {
		return errors.New("keycloak.EnsureIdentityProvider: idp.Alias is required")
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.EnsureIdentityProvider: service account token: %w", err)
	}

	existing, err := c.getIdentityProvider(ctx, saToken, idp.Alias)
	if err != nil && !errors.Is(err, ErrIdentityProviderNotFound) {
		return fmt.Errorf("keycloak.EnsureIdentityProvider: get: %w", err)
	}
	if err == nil {
		if !idpDrift(existing, idp) {
			// Steady state — idempotent no-op.
			return nil
		}
		// Drift — replace the full representation.
		if err := c.updateIdentityProvider(ctx, saToken, idp); err != nil {
			return fmt.Errorf("keycloak.EnsureIdentityProvider: update on drift: %w", err)
		}
		return nil
	}

	// 404 → create.
	if err := c.createIdentityProvider(ctx, saToken, idp); err != nil {
		if errors.Is(err, errIdentityProviderAlreadyExists) {
			// 409 race — fall through to re-GET-then-update-if-drift.
			existing, gerr := c.getIdentityProvider(ctx, saToken, idp.Alias)
			if gerr != nil {
				return fmt.Errorf("keycloak.EnsureIdentityProvider: re-find after 409: %w", gerr)
			}
			if !idpDrift(existing, idp) {
				return nil
			}
			if uerr := c.updateIdentityProvider(ctx, saToken, idp); uerr != nil {
				return fmt.Errorf("keycloak.EnsureIdentityProvider: update after 409: %w", uerr)
			}
			return nil
		}
		return fmt.Errorf("keycloak.EnsureIdentityProvider: create: %w", err)
	}
	return nil
}

// updateIdentityProvider is the unexported PUT helper used by
// EnsureIdentityProvider — saves a redundant SA-token round-trip.
func (c *Client) updateIdentityProvider(ctx context.Context, saToken string, idp IdentityProvider) error {
	body, err := json.Marshal(idp)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s",
		c.addr, c.realm, url.PathEscape(idp.Alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT identity-provider/instance: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrIdentityProviderNotFound
	default:
		return fmt.Errorf("keycloak: PUT identity-provider/instance %d: %s", resp.StatusCode, respBody)
	}
}

// idpDrift returns true iff the existing (server) IdP differs from the
// desired representation in any catalyst-tracked field. Compares only
// the fields catalyst writes; unknown server-side Config keys are NOT
// drift (Keycloak adds defaults like `pkceEnabled`, `prompt` that are
// fine to leave alone).
func idpDrift(existing, desired IdentityProvider) bool {
	if existing.DisplayName != desired.DisplayName {
		return true
	}
	if existing.ProviderID != desired.ProviderID {
		return true
	}
	if existing.Enabled != desired.Enabled {
		return true
	}
	if existing.LinkOnly != desired.LinkOnly {
		return true
	}
	if existing.StoreToken != desired.StoreToken {
		return true
	}
	if existing.AddReadTokenRoleOnCreate != desired.AddReadTokenRoleOnCreate {
		return true
	}
	if existing.FirstBrokerLoginFlowAlias != desired.FirstBrokerLoginFlowAlias {
		return true
	}
	// Config drift: every desired key must be present on existing with
	// the same value. We do NOT flag unknown server-added keys (Keycloak
	// defaults — pkceEnabled, prompt, etc.) as drift to avoid fighting
	// the admin UI on each reconcile.
	for k, dv := range desired.Config {
		if ev, ok := existing.Config[k]; !ok || ev != dv {
			return true
		}
	}
	return false
}

// ── IdP Mappers ─────────────────────────────────────────────────────

// ListIdentityProviderMappers returns every claim mapper attached to the
// IdP. Used by EnsureIdentityProviderMapper for the find-by-name pass
// + by the access-matrix UI to render the federation summary.
func (c *Client) ListIdentityProviderMappers(ctx context.Context, alias string) ([]IdentityProviderMapper, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.ListIdentityProviderMappers: service account token: %w", err)
	}
	return c.listIdentityProviderMappers(ctx, saToken, alias)
}

func (c *Client) listIdentityProviderMappers(ctx context.Context, saToken, alias string) ([]IdentityProviderMapper, error) {
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers",
		c.addr, c.realm, url.PathEscape(alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET identity-provider/instance/mappers: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrIdentityProviderNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET identity-provider/instance/mappers %d: %s", resp.StatusCode, body)
	}
	var mappers []IdentityProviderMapper
	if err := json.Unmarshal(body, &mappers); err != nil {
		return nil, fmt.Errorf("keycloak: decode identity-provider/instance/mappers: %w", err)
	}
	return mappers, nil
}

// EnsureIdentityProviderMapper is find-or-create + drift-correct on the
// per-IdP claim-mapper list. Idempotency anchor: list once, find by
// `Name`, POST if absent / PUT if drifted / no-op if equal.
//
// The mapper's parent IdP is taken from `mapper.IdentityProviderAlias`
// (caller-set). The supplied `alias` argument selects the URL path
// segment — the two MUST match (Keycloak validates this on PUT).
func (c *Client) EnsureIdentityProviderMapper(ctx context.Context, alias string, mapper IdentityProviderMapper) error {
	if mapper.Name == "" {
		return errors.New("keycloak.EnsureIdentityProviderMapper: mapper.Name is required")
	}
	if mapper.IdentityProviderAlias == "" {
		mapper.IdentityProviderAlias = alias
	}
	if mapper.IdentityProviderAlias != alias {
		return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: mapper.IdentityProviderAlias=%q ≠ url alias=%q",
			mapper.IdentityProviderAlias, alias)
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: service account token: %w", err)
	}

	existing, err := c.listIdentityProviderMappers(ctx, saToken, alias)
	if err != nil {
		return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: list: %w", err)
	}
	for _, m := range existing {
		if m.Name == mapper.Name {
			if !mapperDrift(m, mapper) {
				// Steady state — no-op.
				return nil
			}
			// Drift — PUT to update.
			mapper.ID = m.ID
			if err := c.updateIdentityProviderMapper(ctx, saToken, alias, mapper); err != nil {
				return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: update on drift: %w", err)
			}
			return nil
		}
	}

	// Not present — POST.
	if err := c.createIdentityProviderMapper(ctx, saToken, alias, mapper); err != nil {
		return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: create: %w", err)
	}
	return nil
}

func (c *Client) createIdentityProviderMapper(ctx context.Context, saToken, alias string, mapper IdentityProviderMapper) error {
	body, err := json.Marshal(mapper)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers",
		c.addr, c.realm, url.PathEscape(alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: POST identity-provider/instance/mapper: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return fmt.Errorf("keycloak: POST identity-provider/instance/mapper %d: %s", resp.StatusCode, respBody)
	}
}

func (c *Client) updateIdentityProviderMapper(ctx context.Context, saToken, alias string, mapper IdentityProviderMapper) error {
	if mapper.ID == "" {
		return errors.New("updateIdentityProviderMapper: mapper.ID is required for PUT")
	}
	body, err := json.Marshal(mapper)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers/%s",
		c.addr, c.realm, url.PathEscape(alias), url.PathEscape(mapper.ID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT identity-provider/instance/mapper: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return fmt.Errorf("keycloak: PUT identity-provider/instance/mapper %d: %s", resp.StatusCode, respBody)
	}
}

// mapperDrift returns true iff the existing mapper's mapped fields
// differ from the desired representation. The mapper TYPE field
// (IdentityProviderMapper) is part of the comparison — a type-change
// (e.g. attribute-importer → role-importer) IS drift requiring a PUT.
func mapperDrift(existing, desired IdentityProviderMapper) bool {
	if existing.IdentityProviderMapper != desired.IdentityProviderMapper {
		return true
	}
	if existing.IdentityProviderAlias != desired.IdentityProviderAlias {
		return true
	}
	if len(existing.Config) != len(desired.Config) {
		return true
	}
	for k, dv := range desired.Config {
		if ev, ok := existing.Config[k]; !ok || ev != dv {
			return true
		}
	}
	return false
}
