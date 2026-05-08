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
type KeycloakClient interface {
	// EnsureGroup find-or-creates a group at /<slug> with the given
	// attributes. Returns (uuid, path, error).
	EnsureGroup(ctx context.Context, path string, attrs map[string][]string) (uuid string, kcPath string, realm string, err error)
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
