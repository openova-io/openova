// keycloak.go — minimal Keycloak Admin API client for the
// organization-controller. Mirrors the canonical seam in
// products/catalyst/bootstrap/api/internal/keycloak/{client,admin_groups}.go
// (slices D1a/D1c) with one operation: EnsureGroup.
//
// We do NOT take a Go-module dependency on the catalyst-api package
// because:
//
//   1. catalyst-api is on go 1.26 (toolchain auto-resolved); this binary
//      is on go 1.23 to align with the production controller-runtime
//      base toolchain.
//   2. Cross-module imports between products/ and core/ violate the
//      module-isolation pattern the rest of core/ follows.
//   3. The seam map (01-canonical-seams.md) lists the catalyst-api
//      keycloak package as an extension target — but only for callers
//      INSIDE catalyst-api. Out-of-process callers re-implement the
//      narrow surface they need, like organization-controller does
//      here. This is the same idiom used in core/cmd/cert-manager-
//      dynadot-webhook/ which doesn't import catalyst-api either.
//
// If a third controller needs the same surface, the C5 (useraccess)
// implementer can extract this file to core/pkg/keycloak-client/
// without changing the public API.

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// KeycloakClient is the surface organization-controller depends on. The
// interface form keeps the reconciler unit-testable with a fake — see
// controller_test.go.
//
// Slice F2 (#1098) added the IdentityProvider methods so the reconciler
// can wire per-Org Azure-SSO / Okta / generic-OIDC federation into the
// per-Sovereign Keycloak realm. The IdP surface mirrors the F1
// admin_idp.go shape in catalyst-api but on a narrower types-only
// boundary so this controller does NOT take a Go-module dependency on
// catalyst-api (per the file-doc justification above + the Group C
// pattern).
type KeycloakClient interface {
	// EnsureGroup find-or-creates a group at /<slug> with the given
	// attributes. Returns (uuid, path, error).
	EnsureGroup(ctx context.Context, path string, attrs map[string][]string) (uuid string, kcPath string, realm string, err error)

	// EnsureRealm find-or-creates a per-Org realm named after the Org
	// slug (#3084 Part 2). Returns the realm name (== slug on success).
	// Idempotent: a pre-existing realm short-circuits the create. The
	// realm is created `enabled: true` with displayName set to the Org
	// slug; per-Org IdP federation + app clients land on later
	// reconciles (the sso-bridge reconciler / EnsureIdentityProvider
	// path), not here — this method only guarantees the realm EXISTS so
	// downstream wiring has a target.
	EnsureRealm(ctx context.Context, slug string) (realmName string, err error)

	// DeleteRealm removes the per-Org realm by name (== slug). The
	// reconciler calls this from the finalizer teardown when the
	// Organization is being deleted. Absent-as-success is the contract
	// (a 404 returns nil), mirroring DeleteIdentityProvider.
	DeleteRealm(ctx context.Context, slug string) error

	// EnsureIdentityProvider find-or-creates a realm IdP, replacing
	// the representation when drift is detected. Idempotent on
	// re-runs of equal desired state.
	EnsureIdentityProvider(ctx context.Context, idp KCIdentityProvider) error

	// EnsureIdentityProviderMapper find-or-creates a claim mapper on
	// the named IdP. Idempotent on re-runs of equal desired state.
	EnsureIdentityProviderMapper(ctx context.Context, alias string, mapper KCIdentityProviderMapper) error

	// DeleteIdentityProvider removes the realm IdP by alias. The
	// reconciler calls this best-effort when the Org drops its
	// federation config — absent-as-success is the contract.
	DeleteIdentityProvider(ctx context.Context, alias string) error
}

// KCIdentityProvider mirrors the F1 catalyst-api IdentityProvider type
// (products/catalyst/bootstrap/api/internal/keycloak/admin_idp.go).
// The duplication is intentional — see file-level doc above.
type KCIdentityProvider struct {
	Alias                     string
	DisplayName               string
	ProviderID                string
	Enabled                   bool
	StoreToken                bool
	AddReadTokenRoleOnCreate  bool
	LinkOnly                  bool
	FirstBrokerLoginFlowAlias string
	Config                    map[string]string
}

// KCIdentityProviderMapper mirrors the F1 catalyst-api
// IdentityProviderMapper type.
type KCIdentityProviderMapper struct {
	ID                     string
	Name                   string
	IdentityProviderAlias  string
	IdentityProviderMapper string
	Config                 map[string]string
}

// LiveKeycloak is the production KeycloakClient — wraps the Keycloak
// Admin REST API.
type LiveKeycloak struct {
	addr  string
	realm string
	saID  string
	saSec string
	http  *http.Client
}

// NewLiveKeycloak constructs a LiveKeycloak.
func NewLiveKeycloak(addr, realm, saClientID, saClientSecret string) *LiveKeycloak {
	return &LiveKeycloak{
		addr:  strings.TrimRight(addr, "/"),
		realm: realm,
		saID:  saClientID,
		saSec: saClientSecret,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// errGroupAlreadyExists is the internal sentinel for the EnsureGroup
// 409 race path.
var errGroupAlreadyExists = errors.New("keycloak: group already exists")

// kcGroup is the slice of GroupRepresentation fields we care about.
type kcGroup struct {
	ID         string              `json:"id,omitempty"`
	Name       string              `json:"name"`
	Path       string              `json:"path,omitempty"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

// serviceAccountToken issues a fresh client-credentials token. We do
// NOT cache here — the controller reconciler is low-frequency
// (per-Org events) and a fresh token per reconcile is well under
// Keycloak's rate-limit budget. Slice D1c's higher-volume callers in
// catalyst-api cache the token.
func (k *LiveKeycloak) serviceAccountToken(ctx context.Context) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", k.saID)
	form.Set("client_secret", k.saSec)

	u := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", k.addr, k.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: SA token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak: SA token %d: %s", resp.StatusCode, body)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return "", fmt.Errorf("keycloak: decode SA token: %w", err)
	}
	if t.AccessToken == "" {
		return "", errors.New("keycloak: SA token response missing access_token")
	}
	return t.AccessToken, nil
}

// EnsureGroup find-or-creates a top-level group at the given path
// (e.g. "/acme") with the supplied attributes.
func (k *LiveKeycloak) EnsureGroup(ctx context.Context, path string, attrs map[string][]string) (string, string, string, error) {
	if path == "" {
		return "", "", "", errors.New("keycloak.EnsureGroup: empty path")
	}
	if path[0] != '/' {
		path = "/" + path
	}
	leaf := path
	if i := strings.LastIndex(path, "/"); i >= 0 && i < len(path)-1 {
		leaf = path[i+1:]
	}
	tok, err := k.serviceAccountToken(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("keycloak.EnsureGroup: %w", err)
	}

	existing, err := k.findGroupByPath(ctx, tok, path)
	if err != nil {
		return "", "", "", fmt.Errorf("keycloak.EnsureGroup: find: %w", err)
	}
	if existing.ID != "" {
		// Ensure attributes match; if not, PUT.
		if !attrsEqual(existing.Attributes, attrs) && attrs != nil {
			existing.Attributes = attrs
			if err := k.updateGroup(ctx, tok, existing); err != nil {
				return "", "", "", fmt.Errorf("keycloak.EnsureGroup: update: %w", err)
			}
		}
		return existing.ID, existing.Path, k.realm, nil
	}

	uuid, err := k.createGroup(ctx, tok, kcGroup{Name: leaf, Attributes: attrs})
	if errors.Is(err, errGroupAlreadyExists) {
		again, ferr := k.findGroupByPath(ctx, tok, path)
		if ferr != nil {
			return "", "", "", fmt.Errorf("keycloak.EnsureGroup: re-find after 409: %w", ferr)
		}
		if again.ID == "" {
			return "", "", "", errors.New("keycloak.EnsureGroup: 409 but path-resolve empty")
		}
		return again.ID, again.Path, k.realm, nil
	}
	if err != nil {
		return "", "", "", fmt.Errorf("keycloak.EnsureGroup: create: %w", err)
	}
	// Re-fetch to populate Path.
	created, ferr := k.findGroupByPath(ctx, tok, path)
	if ferr != nil || created.ID == "" {
		return uuid, path, k.realm, nil
	}
	return created.ID, created.Path, k.realm, nil
}

// ── Per-Org realm CRUD (#3084 Part 2) ────────────────────────────────
//
// EnsureRealm mirrors EnsureGroup's idempotency exactly: GET the realm,
// short-circuit if it exists, otherwise POST and treat a 409 as
// already-exists (re-GET to confirm). The realm name IS the Org slug —
// there is no separate UUID handle the way groups have, so the return is
// just the slug on success.

// errRealmAlreadyExists is the internal sentinel for the EnsureRealm
// 409 race path (mirrors errGroupAlreadyExists).
var errRealmAlreadyExists = errors.New("keycloak: realm already exists")

// kcRealm is the slice of RealmRepresentation fields we set on create.
// Keycloak ignores unknown keys on import, so this minimal shape is
// sufficient to materialize the realm; per-Org IdP + clients are layered
// on later by other reconcile paths.
type kcRealm struct {
	Realm       string `json:"realm"`
	DisplayName string `json:"displayName,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// EnsureRealm find-or-creates the per-Org realm named after the slug.
func (k *LiveKeycloak) EnsureRealm(ctx context.Context, slug string) (string, error) {
	if slug == "" {
		return "", errors.New("keycloak.EnsureRealm: empty slug")
	}
	tok, err := k.serviceAccountToken(ctx)
	if err != nil {
		return "", fmt.Errorf("keycloak.EnsureRealm: %w", err)
	}

	exists, err := k.findRealm(ctx, tok, slug)
	if err != nil {
		return "", fmt.Errorf("keycloak.EnsureRealm: find: %w", err)
	}
	if exists {
		return slug, nil
	}

	cerr := k.createRealm(ctx, tok, kcRealm{Realm: slug, DisplayName: slug, Enabled: true})
	if errors.Is(cerr, errRealmAlreadyExists) {
		again, ferr := k.findRealm(ctx, tok, slug)
		if ferr != nil {
			return "", fmt.Errorf("keycloak.EnsureRealm: re-find after 409: %w", ferr)
		}
		if !again {
			return "", errors.New("keycloak.EnsureRealm: 409 but realm-resolve empty")
		}
		return slug, nil
	}
	if cerr != nil {
		return "", fmt.Errorf("keycloak.EnsureRealm: create: %w", cerr)
	}
	return slug, nil
}

// DeleteRealm removes the per-Org realm by name. Returns nil on 204, also
// nil on 404 (absent-as-success — finalizer teardown contract, mirrors
// DeleteIdentityProvider).
func (k *LiveKeycloak) DeleteRealm(ctx context.Context, slug string) error {
	if slug == "" {
		return errors.New("keycloak.DeleteRealm: empty slug")
	}
	tok, err := k.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.DeleteRealm: %w", err)
	}
	u := fmt.Sprintf("%s/admin/realms/%s", k.addr, url.PathEscape(slug))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: DELETE realm: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("keycloak: DELETE realm %d: %s", resp.StatusCode, respBody)
	}
}

// findRealm reports whether the named realm exists. A 200 → true, a 404
// → false; anything else is an error. The realm-info endpoint
// (/admin/realms/{realm}) is the canonical existence probe.
func (k *LiveKeycloak) findRealm(ctx context.Context, tok, slug string) (bool, error) {
	u := fmt.Sprintf("%s/admin/realms/%s", k.addr, url.PathEscape(slug))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := k.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("keycloak: GET realm: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("keycloak: GET realm %d: %s", resp.StatusCode, body)
	}
}

// createRealm POSTs a new realm. Returns errRealmAlreadyExists on 409 so
// the EnsureRealm caller can re-resolve (mirrors createGroup).
func (k *LiveKeycloak) createRealm(ctx context.Context, tok string, realm kcRealm) error {
	body, err := json.Marshal(realm)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms", k.addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: POST realm: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusConflict:
		return errRealmAlreadyExists
	default:
		return fmt.Errorf("keycloak: POST realm %d: %s", resp.StatusCode, respBody)
	}
}

func (k *LiveKeycloak) findGroupByPath(ctx context.Context, tok, path string) (kcGroup, error) {
	if path == "" {
		return kcGroup{}, errors.New("findGroupByPath: empty")
	}
	if path[0] != '/' {
		path = "/" + path
	}
	u := fmt.Sprintf("%s/admin/realms/%s/group-by-path/%s",
		k.addr, k.realm, url.PathEscape(path[1:]))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return kcGroup{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := k.http.Do(req)
	if err != nil {
		return kcGroup{}, fmt.Errorf("keycloak: GET group-by-path: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var g kcGroup
		if err := json.Unmarshal(body, &g); err != nil {
			return kcGroup{}, fmt.Errorf("keycloak: decode group-by-path: %w", err)
		}
		return g, nil
	case http.StatusNotFound:
		return kcGroup{}, nil
	default:
		return kcGroup{}, fmt.Errorf("keycloak: GET group-by-path %d: %s", resp.StatusCode, body)
	}
}

func (k *LiveKeycloak) createGroup(ctx context.Context, tok string, g kcGroup) (string, error) {
	body, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/groups", k.addr, k.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: POST group: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated:
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", errors.New("keycloak: POST group 201 without Location header")
		}
		// Extract last path segment as the Keycloak UUID.
		seg := loc
		if idx := strings.LastIndex(loc, "/"); idx >= 0 && idx < len(loc)-1 {
			seg = loc[idx+1:]
		}
		return seg, nil
	case http.StatusConflict:
		return "", errGroupAlreadyExists
	default:
		return "", fmt.Errorf("keycloak: POST group %d: %s", resp.StatusCode, respBody)
	}
}

func (k *LiveKeycloak) updateGroup(ctx context.Context, tok string, g kcGroup) error {
	if g.ID == "" {
		return errors.New("updateGroup: empty ID")
	}
	body, err := json.Marshal(g)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/groups/%s",
		k.addr, k.realm, url.PathEscape(g.ID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT group: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return fmt.Errorf("keycloak: PUT group %d: %s", resp.StatusCode, respBody)
	}
}

func attrsEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || len(va) != len(vb) {
			return false
		}
		for i := range va {
			if va[i] != vb[i] {
				return false
			}
		}
	}
	return true
}

// ── Identity Provider CRUD (slice F2 wire-up) ────────────────────────
//
// The shape mirrors the F1 admin_idp.go pattern in catalyst-api: every
// Ensure does a GET-or-list pre-flight and short-circuits on byte-equal
// desired state, otherwise POST or PUT exactly once.

// errIdentityProviderAlreadyExists is the 409 sentinel for the
// EnsureIdentityProvider race path.
var errIdentityProviderAlreadyExists = errors.New("keycloak: identity provider already exists")

// errIdentityProviderNotFound is the 404 sentinel.
var errIdentityProviderNotFound = errors.New("keycloak: identity provider not found")

// EnsureIdentityProvider find-or-creates the realm IdP, PUT-replacing
// when drift is observed. Re-runs on steady state make 0 writes.
func (k *LiveKeycloak) EnsureIdentityProvider(ctx context.Context, idp KCIdentityProvider) error {
	if idp.Alias == "" {
		return errors.New("keycloak.EnsureIdentityProvider: idp.Alias is required")
	}
	tok, err := k.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.EnsureIdentityProvider: %w", err)
	}

	existing, err := k.getIdentityProvider(ctx, tok, idp.Alias)
	if err != nil && !errors.Is(err, errIdentityProviderNotFound) {
		return fmt.Errorf("keycloak.EnsureIdentityProvider: get: %w", err)
	}
	if err == nil {
		if !idpDrift(existing, idp) {
			return nil
		}
		if err := k.putIdentityProvider(ctx, tok, idp); err != nil {
			return fmt.Errorf("keycloak.EnsureIdentityProvider: update on drift: %w", err)
		}
		return nil
	}

	// 404 → create. 409 race → re-find and update if drift.
	if cerr := k.postIdentityProvider(ctx, tok, idp); cerr != nil {
		if errors.Is(cerr, errIdentityProviderAlreadyExists) {
			again, gerr := k.getIdentityProvider(ctx, tok, idp.Alias)
			if gerr != nil {
				return fmt.Errorf("keycloak.EnsureIdentityProvider: re-find after 409: %w", gerr)
			}
			if !idpDrift(again, idp) {
				return nil
			}
			if uerr := k.putIdentityProvider(ctx, tok, idp); uerr != nil {
				return fmt.Errorf("keycloak.EnsureIdentityProvider: update after 409: %w", uerr)
			}
			return nil
		}
		return fmt.Errorf("keycloak.EnsureIdentityProvider: create: %w", cerr)
	}
	return nil
}

// DeleteIdentityProvider removes the realm IdP by alias. Returns nil on
// 204, also nil on 404 (treat absent-as-success — F2 finalizer contract).
func (k *LiveKeycloak) DeleteIdentityProvider(ctx context.Context, alias string) error {
	tok, err := k.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.DeleteIdentityProvider: %w", err)
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s",
		k.addr, k.realm, url.PathEscape(alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: DELETE identity-provider/instance: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("keycloak: DELETE identity-provider/instance %d: %s", resp.StatusCode, respBody)
	}
}

// EnsureIdentityProviderMapper find-or-creates the claim mapper, PUT-
// replacing on drift. Idempotent.
func (k *LiveKeycloak) EnsureIdentityProviderMapper(ctx context.Context, alias string, mapper KCIdentityProviderMapper) error {
	if mapper.Name == "" {
		return errors.New("keycloak.EnsureIdentityProviderMapper: mapper.Name is required")
	}
	if mapper.IdentityProviderAlias == "" {
		mapper.IdentityProviderAlias = alias
	}
	if mapper.IdentityProviderAlias != alias {
		return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: alias mismatch %q ≠ %q",
			mapper.IdentityProviderAlias, alias)
	}
	tok, err := k.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: %w", err)
	}
	existing, err := k.listIdentityProviderMappers(ctx, tok, alias)
	if err != nil {
		return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: list: %w", err)
	}
	for _, m := range existing {
		if m.Name == mapper.Name {
			if !mapperDrift(m, mapper) {
				return nil
			}
			mapper.ID = m.ID
			if err := k.putIdentityProviderMapper(ctx, tok, alias, mapper); err != nil {
				return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: update on drift: %w", err)
			}
			return nil
		}
	}
	if err := k.postIdentityProviderMapper(ctx, tok, alias, mapper); err != nil {
		return fmt.Errorf("keycloak.EnsureIdentityProviderMapper: create: %w", err)
	}
	return nil
}

func (k *LiveKeycloak) getIdentityProvider(ctx context.Context, tok, alias string) (KCIdentityProvider, error) {
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s",
		k.addr, k.realm, url.PathEscape(alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return KCIdentityProvider{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := k.http.Do(req)
	if err != nil {
		return KCIdentityProvider{}, fmt.Errorf("keycloak: GET idp: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return KCIdentityProvider{}, errIdentityProviderNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return KCIdentityProvider{}, fmt.Errorf("keycloak: GET idp %d: %s", resp.StatusCode, body)
	}
	var idp KCIdentityProvider
	if err := json.Unmarshal(body, &idp); err != nil {
		return KCIdentityProvider{}, fmt.Errorf("keycloak: decode idp: %w", err)
	}
	return idp, nil
}

func (k *LiveKeycloak) postIdentityProvider(ctx context.Context, tok string, idp KCIdentityProvider) error {
	body, err := json.Marshal(idp)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances", k.addr, k.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: POST idp: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent:
		return nil
	case http.StatusConflict:
		return errIdentityProviderAlreadyExists
	default:
		return fmt.Errorf("keycloak: POST idp %d: %s", resp.StatusCode, respBody)
	}
}

func (k *LiveKeycloak) putIdentityProvider(ctx context.Context, tok string, idp KCIdentityProvider) error {
	body, err := json.Marshal(idp)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s",
		k.addr, k.realm, url.PathEscape(idp.Alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT idp: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return fmt.Errorf("keycloak: PUT idp %d: %s", resp.StatusCode, respBody)
	}
}

func (k *LiveKeycloak) listIdentityProviderMappers(ctx context.Context, tok, alias string) ([]KCIdentityProviderMapper, error) {
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers",
		k.addr, k.realm, url.PathEscape(alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET mappers: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, errIdentityProviderNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET mappers %d: %s", resp.StatusCode, body)
	}
	var ms []KCIdentityProviderMapper
	if err := json.Unmarshal(body, &ms); err != nil {
		return nil, fmt.Errorf("keycloak: decode mappers: %w", err)
	}
	return ms, nil
}

func (k *LiveKeycloak) postIdentityProviderMapper(ctx context.Context, tok, alias string, mapper KCIdentityProviderMapper) error {
	body, err := json.Marshal(mapper)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers",
		k.addr, k.realm, url.PathEscape(alias))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: POST mapper: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return fmt.Errorf("keycloak: POST mapper %d: %s", resp.StatusCode, respBody)
	}
}

func (k *LiveKeycloak) putIdentityProviderMapper(ctx context.Context, tok, alias string, mapper KCIdentityProviderMapper) error {
	if mapper.ID == "" {
		return errors.New("putIdentityProviderMapper: mapper.ID required for PUT")
	}
	body, err := json.Marshal(mapper)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/identity-provider/instances/%s/mappers/%s",
		k.addr, k.realm, url.PathEscape(alias), url.PathEscape(mapper.ID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT mapper: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return fmt.Errorf("keycloak: PUT mapper %d: %s", resp.StatusCode, respBody)
	}
}

// idpDrift compares the slice of IdP fields the controller writes.
// Mirrors the F1 admin_idp.go drift function — unknown server-side
// Config keys (Keycloak defaults like `pkceEnabled`) are NOT drift.
func idpDrift(existing, desired KCIdentityProvider) bool {
	if existing.DisplayName != desired.DisplayName ||
		existing.ProviderID != desired.ProviderID ||
		existing.Enabled != desired.Enabled ||
		existing.LinkOnly != desired.LinkOnly ||
		existing.StoreToken != desired.StoreToken ||
		existing.AddReadTokenRoleOnCreate != desired.AddReadTokenRoleOnCreate ||
		existing.FirstBrokerLoginFlowAlias != desired.FirstBrokerLoginFlowAlias {
		return true
	}
	for k, dv := range desired.Config {
		if ev, ok := existing.Config[k]; !ok || ev != dv {
			return true
		}
	}
	return false
}

// mapperDrift compares mapper fields the controller writes.
func mapperDrift(existing, desired KCIdentityProviderMapper) bool {
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
