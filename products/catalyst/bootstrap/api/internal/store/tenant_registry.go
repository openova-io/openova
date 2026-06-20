// Package store — tenant_registry.go: flat-file persistence for the
// host → tenant lookup table that backs the public
// `GET /api/v1/tenant/discover` endpoint (issue #802).
//
// Per [Q-mine-1] of #795 the same Sovereign Console SPA bundle serves
// both otech-admin and Organization-admin views. Tenant context is discovered
// from `window.location.host` against this registry — NOT from
// path/subdomain parsing — so a BYO domain such as `console.acme.com`
// (CNAME → otech ingress) resolves the same way as a free-subdomain
// `console.acme.<otech-fqdn>`.
//
// On-disk shape: <dir>/tenant-registry.json — a single JSON object
// keyed by host. One file (not file-per-tenant) because the registry
// is read on every login bootstrap and a directory walk would amplify
// every cold start. The whole table fits in a few KB even with
// thousands of Organizations.
//
// Why flat-file (not Postgres): per
// docs/INVIOLABLE-PRINCIPLES.md #3 the catalyst-api adds zero
// external storage dependencies; the same rationale that justifies
// flat-file deployment persistence (see store.go header) applies here.
// ADR-0003 §3.4 specifies a Postgres `user_provision_state` table
// for the unified-rbac service; until that service is split out
// into its own deployable unit, the catalyst-api's flat-file store
// is the canonical seam.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// TenantKind is the discriminant the SPA uses to mount otech-tier vs
// Organization-tier route trees from the same bundle.
type TenantKind string

const (
	TenantKindOTECH TenantKind = "otech"
	TenantKindOrg   TenantKind = "sme"
)

// TenantRegistration is the wire + on-disk shape returned by
// `GET /api/v1/tenant/discover?host=...` for a known host.
//
// The fields are precisely what the SPA needs to bootstrap OIDC against
// the right Keycloak realm — no more, no less. KeycloakRealmURL is the
// full issuer URL the OIDC client uses (e.g.
// https://kc.<otech-fqdn>/realms/org-acme); KeycloakClientID is the
// public OIDC client id. The TenantID is opaque to the SPA but echoed
// on every authenticated API call so the backend can scope queries.
type TenantRegistration struct {
	Host             string     `json:"host"`
	TenantID         string     `json:"tenant_id"`
	KeycloakRealmURL string     `json:"keycloak_realm_url"`
	KeycloakClientID string     `json:"keycloak_client_id"`
	TenantKind       TenantKind `json:"tenant_kind"`
	// OrganizationNamespace — for tenant_kind=sme this is the Kubernetes
	// namespace in the OTECH cluster where ADR-0003 step 3 applies the
	// `newapi-key-{user-uuid}` Secret. Empty for tenant_kind=otech.
	OrganizationNamespace string `json:"sme_tenant_namespace,omitempty"`
	// OrgKeycloakAdminURL — admin-API base for the Organization vcluster
	// Keycloak realm (used by ADR-0003 step 1). Different from
	// KeycloakRealmURL: that's the *issuer* (browser-facing), this is
	// the in-cluster admin endpoint (back-end facing). Empty for
	// tenant_kind=otech.
	OrgKeycloakAdminURL string `json:"sme_keycloak_admin_url,omitempty"`
	// OrgKeycloakRealmName — the realm name to target in the admin
	// API path /admin/realms/{realm}/users. Empty for tenant_kind=otech.
	OrgKeycloakRealmName string `json:"org_keycloak_realm_name,omitempty"`
}

// tenantRegistryFile is the JSON file basename. It lives next to the
// per-deployment files in the store directory; the leading dash sorts
// it alongside other top-level metadata files (when more arrive).
const tenantRegistryFile = "-tenant-registry.json"

// TenantRegistry is the directory-backed registry implementation.
//
// Concurrency: every public method takes the mutex before touching the
// in-memory map or filesystem. Reads are O(1) on the in-memory map; a
// single write rewrites the whole file via temp-file + rename, which
// is fine at the cardinality this table targets (thousands, not
// millions).
type TenantRegistry struct {
	dir   string
	mu    sync.RWMutex
	hosts map[string]TenantRegistration
}

// NewTenantRegistry returns a registry rooted at dir. If
// <dir>/-tenant-registry.json exists it is loaded; if it does not, the
// registry starts empty and the file is materialised on the first Put.
// dir must already exist (the catalyst-api's deployment store creates
// it at startup; this constructor reuses the same directory).
func NewTenantRegistry(dir string) (*TenantRegistry, error) {
	if dir == "" {
		return nil, errors.New("tenant registry: directory is required")
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("tenant registry: stat %q: %w", dir, err)
	}
	r := &TenantRegistry{dir: dir, hosts: make(map[string]TenantRegistration)}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *TenantRegistry) load() error {
	path := filepath.Join(r.dir, tenantRegistryFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // empty registry on first start
	}
	if err != nil {
		return fmt.Errorf("tenant registry: read %q: %w", path, err)
	}
	var on diskShape
	if err := json.Unmarshal(data, &on); err != nil {
		return fmt.Errorf("tenant registry: decode %q: %w", path, err)
	}
	r.mu.Lock()
	for _, t := range on.Tenants {
		r.hosts[strings.ToLower(t.Host)] = t
	}
	r.mu.Unlock()
	return nil
}

// diskShape is the JSON-on-disk wrapper. Embedding the slice in an
// object (not a top-level array) leaves room for a future schema
// version field without needing a migration.
type diskShape struct {
	Version int                  `json:"version"`
	Tenants []TenantRegistration `json:"tenants"`
}

// Get looks up a host. Hosts are normalised to lowercase before
// comparison so `Console.Acme.Com` and `console.acme.com` resolve
// identically.
func (r *TenantRegistry) Get(host string) (TenantRegistration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.hosts[strings.ToLower(host)]
	return t, ok
}

// Put upserts a tenant and persists the whole table.
func (r *TenantRegistry) Put(t TenantRegistration) error {
	if strings.TrimSpace(t.Host) == "" {
		return errors.New("tenant registry: host is required")
	}
	if strings.TrimSpace(t.TenantID) == "" {
		return errors.New("tenant registry: tenant_id is required")
	}
	if t.TenantKind != TenantKindOTECH && t.TenantKind != TenantKindOrg {
		return fmt.Errorf("tenant registry: invalid tenant_kind %q", t.TenantKind)
	}
	t.Host = strings.ToLower(strings.TrimSpace(t.Host))
	r.mu.Lock()
	r.hosts[t.Host] = t
	err := r.flushLocked()
	r.mu.Unlock()
	return err
}

// Delete removes a host entry. Idempotent — deleting a missing host
// returns nil.
func (r *TenantRegistry) Delete(host string) error {
	r.mu.Lock()
	delete(r.hosts, strings.ToLower(host))
	err := r.flushLocked()
	r.mu.Unlock()
	return err
}

// List returns every registered tenant sorted by host, primarily for
// admin-tooling read access (and tests).
func (r *TenantRegistry) List() []TenantRegistration {
	r.mu.RLock()
	out := make([]TenantRegistration, 0, len(r.hosts))
	for _, t := range r.hosts {
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func (r *TenantRegistry) flushLocked() error {
	out := diskShape{Version: 1, Tenants: make([]TenantRegistration, 0, len(r.hosts))}
	for _, t := range r.hosts {
		out.Tenants = append(out.Tenants, t)
	}
	sort.Slice(out.Tenants, func(i, j int) bool { return out.Tenants[i].Host < out.Tenants[j].Host })
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("tenant registry: marshal: %w", err)
	}
	path := filepath.Join(r.dir, tenantRegistryFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("tenant registry: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("tenant registry: rename: %w", err)
	}
	return nil
}
