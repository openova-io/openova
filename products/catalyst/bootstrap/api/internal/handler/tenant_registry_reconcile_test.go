package handler

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgCR builds a minimal Organization CR unstructured with the fields the
// registry reconcile reads (slug, tenantPublic.{parentDomain,subdomain}, and
// the openova.io/tenant-id label).
func orgCR(slug, parentDomain, subdomain, tenantID string) *unstructured.Unstructured {
	spec := map[string]any{"slug": slug}
	if parentDomain != "" {
		tp := map[string]any{"parentDomain": parentDomain}
		if subdomain != "" {
			tp["subdomain"] = subdomain
		}
		spec["tenantPublic"] = tp
	}
	meta := map[string]any{"name": slug}
	if tenantID != "" {
		meta["labels"] = map[string]any{"openova.io/tenant-id": tenantID}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata":   meta,
		"spec":       spec,
	}}
}

// TestTenantRegistrationFromOrgCR covers the pure mapping: a pool-parent Org CR
// produces a tenant_kind=org registration whose Host/realm/namespace mirror the
// BSS-pipeline shape; a single-domain (no parentDomain) Org and an invalid-slug
// Org produce no registration.
func TestTenantRegistrationFromOrgCR(t *testing.T) {
	t.Run("pool-parent Org → tenant_kind=org registration", func(t *testing.T) {
		reg, ok := tenantRegistrationFromOrgCR(orgCR("acme", "omani.works", "", "tnt-123"), "omantel.biz")
		if !ok {
			t.Fatal("expected a registration for a pool-parent Org")
		}
		if reg.Host != "console.acme.omani.works" {
			t.Errorf("host: want console.acme.omani.works got %q", reg.Host)
		}
		if reg.TenantKind != store.TenantKindOrg {
			t.Errorf("kind: want org got %q", reg.TenantKind)
		}
		if reg.TenantID != "tnt-123" {
			t.Errorf("tenant_id: want tnt-123 got %q", reg.TenantID)
		}
		if reg.KeycloakClientID != "catalyst-ui" {
			t.Errorf("client_id: want catalyst-ui got %q", reg.KeycloakClientID)
		}
		if reg.OrgKeycloakRealmName != "org-acme" {
			t.Errorf("realm: want org-acme got %q", reg.OrgKeycloakRealmName)
		}
		if reg.KeycloakRealmURL != "https://keycloak.acme.omani.works/realms/org-acme" {
			t.Errorf("realm_url: got %q", reg.KeycloakRealmURL)
		}
		if reg.OrganizationNamespace != "acme" {
			t.Errorf("namespace: want acme got %q", reg.OrganizationNamespace)
		}
	})

	t.Run("explicit subdomain overrides slug in host", func(t *testing.T) {
		reg, ok := tenantRegistrationFromOrgCR(orgCR("acme", "omani.works", "shop", "tnt-1"), "omantel.biz")
		if !ok {
			t.Fatal("expected a registration")
		}
		if reg.Host != "console.shop.omani.works" {
			t.Errorf("host: want console.shop.omani.works got %q", reg.Host)
		}
	})

	t.Run("missing tenant-id label falls back to slug", func(t *testing.T) {
		reg, ok := tenantRegistrationFromOrgCR(orgCR("acme", "omani.works", "", ""), "omantel.biz")
		if !ok {
			t.Fatal("expected a registration")
		}
		if reg.TenantID != "acme" {
			t.Errorf("tenant_id fallback: want acme got %q", reg.TenantID)
		}
	})

	t.Run("single-domain Org (no parentDomain) → no registration", func(t *testing.T) {
		if _, ok := tenantRegistrationFromOrgCR(orgCR("acme", "", "", "tnt-1"), "omantel.biz"); ok {
			t.Error("expected no registration for an Org without a pool parentDomain")
		}
	})

	t.Run("invalid slug → no registration", func(t *testing.T) {
		// Leading digit fails orgSlugRE (mirrors the BSS-door guard).
		if _, ok := tenantRegistrationFromOrgCR(orgCR("1bad", "omani.works", "", "t"), "omantel.biz"); ok {
			t.Error("expected no registration for an invalid slug")
		}
	})
}

// newRegistryReconcileHandler wires a Handler with an empty tenant registry +
// org-tenant OTECHFQDN + a fake dynamic client seeded with the supplied Org CRs.
func newRegistryReconcileHandler(t *testing.T, orgs ...*unstructured.Unstructured) (*Handler, *store.TenantRegistry) {
	t.Helper()
	dir := t.TempDir()
	registry, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("tenant registry: %v", err)
	}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetTenantRegistry(registry)
	h.SetOrganizationDeps(OrganizationDeps{OTECHFQDN: "omantel.biz"})

	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		organizationGVR(): "OrganizationList",
	}
	seed := make([]runtime.Object, 0, len(orgs))
	for _, o := range orgs {
		seed = append(seed, o)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, seed...)
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: nil, dyn: dyn}, nil
	})
	return h, registry
}

// TestReconcileTenantRegistryOnce_RegistersFunnelOrg is the #4179 criterion-4d
// binary DoD at the unit level: a marketplace-funnel Org CR (pool parentDomain,
// no BSS-pipeline run) gets its console host registered as tenant_kind=org —
// the exact row /auth/org-handover → resolveOrgScope needs to ACCEPT the
// session. A single-domain Org in the same list is correctly skipped.
func TestReconcileTenantRegistryOnce_RegistersFunnelOrg(t *testing.T) {
	h, registry := newRegistryReconcileHandler(t,
		orgCR("funnelorg", "omani.works", "", "tnt-funnel"),
		orgCR("legacyorg", "", "", "tnt-legacy"), // no pool parent → skipped
	)

	h.reconcileTenantRegistryOnce(context.Background())

	got, ok := registry.Get("console.funnelorg.omani.works")
	if !ok {
		t.Fatal("funnel Org console host was not registered")
	}
	if got.TenantKind != store.TenantKindOrg {
		t.Errorf("kind: want org got %q", got.TenantKind)
	}
	if got.TenantID != "tnt-funnel" {
		t.Errorf("tenant_id: want tnt-funnel got %q", got.TenantID)
	}
	// resolveOrgScope keys off the host + tenant_kind=org; assert the single-
	// domain Org produced no console.legacyorg.* row.
	for _, r := range registry.List() {
		if r.OrganizationNamespace == "legacyorg" {
			t.Errorf("single-domain Org should not be registered: %+v", r)
		}
	}
}

// TestReconcileTenantRegistryOnce_Idempotent asserts a second pass over the same
// Org CRs is a no-op (the row is unchanged), so the steady-state ticker never
// churns the flat file.
func TestReconcileTenantRegistryOnce_Idempotent(t *testing.T) {
	h, registry := newRegistryReconcileHandler(t,
		orgCR("acme", "omani.works", "", "tnt-1"),
	)
	h.reconcileTenantRegistryOnce(context.Background())
	first, ok := registry.Get("console.acme.omani.works")
	if !ok {
		t.Fatal("first pass did not register the Org")
	}
	h.reconcileTenantRegistryOnce(context.Background())
	second, ok := registry.Get("console.acme.omani.works")
	if !ok {
		t.Fatal("second pass dropped the Org")
	}
	if !tenantRegistrationEqual(first, second) {
		t.Errorf("idempotency: row changed across passes\nfirst=%+v\nsecond=%+v", first, second)
	}
}

// TestEnsureTenantRegisteredForHost is the #3376 terminal-step race fix at the
// unit level: a fresh funnel Org's row is NOT yet in the registry (the periodic
// reconcile tick has not fired), but its Organization CR exists. An on-demand
// sync for the exact console host must resolve + register it immediately so the
// very next resolveOrgScope ACCEPTS the org-handover.
func TestEnsureTenantRegisteredForHost(t *testing.T) {
	t.Run("on-demand registers the matching host", func(t *testing.T) {
		h, registry := newRegistryReconcileHandler(t,
			orgCR("funnelorg", "omani.works", "", "tnt-funnel"),
			orgCR("other", "omani.rest", "", "tnt-other"),
		)
		// Registry starts empty (no periodic tick has run).
		if n := len(registry.List()); n != 0 {
			t.Fatalf("registry should start empty, has %d", n)
		}

		if !h.ensureTenantRegisteredForHost(context.Background(), "console.funnelorg.omani.works") {
			t.Fatal("ensureTenantRegisteredForHost should have registered the funnel Org host")
		}
		got, ok := registry.Get("console.funnelorg.omani.works")
		if !ok {
			t.Fatal("funnel Org host not in registry after on-demand sync")
		}
		if got.TenantKind != store.TenantKindOrg || got.TenantID != "tnt-funnel" {
			t.Errorf("unexpected row: %+v", got)
		}
		// Only the requested host is written — the unrelated Org is not pulled in.
		if _, ok := registry.Get("console.other.omani.rest"); ok {
			t.Error("on-demand sync must only register the requested host, not every Org")
		}
	})

	t.Run("already-registered host is a fast no-op true", func(t *testing.T) {
		h, registry := newRegistryReconcileHandler(t,
			orgCR("acme", "omani.works", "", "tnt-1"),
		)
		if err := registry.Put(store.TenantRegistration{
			Host: "console.acme.omani.works", TenantID: "tnt-1", TenantKind: store.TenantKindOrg,
		}); err != nil {
			t.Fatalf("seed put: %v", err)
		}
		if !h.ensureTenantRegisteredForHost(context.Background(), "console.acme.omani.works") {
			t.Fatal("already-registered host should return true")
		}
	})

	t.Run("no matching Org CR → false (caller refuses)", func(t *testing.T) {
		h, _ := newRegistryReconcileHandler(t,
			orgCR("acme", "omani.works", "", "tnt-1"),
		)
		if h.ensureTenantRegisteredForHost(context.Background(), "console.ghost.omani.works") {
			t.Fatal("a host with no Organization CR must NOT register")
		}
	})

	t.Run("no dynamic client → false, no panic", func(t *testing.T) {
		dir := t.TempDir()
		registry, err := store.NewTenantRegistry(dir)
		if err != nil {
			t.Fatalf("tenant registry: %v", err)
		}
		h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		h.SetTenantRegistry(registry)
		h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
			return &sovereignDeps{core: nil, dyn: nil}, nil
		})
		if h.ensureTenantRegisteredForHost(context.Background(), "console.acme.omani.works") {
			t.Fatal("out-of-cluster ensure must return false")
		}
	})
}

// TestReconcileTenantRegistryOnce_NoDynamicClientIsNoop confirms the out-of-
// cluster / CI degrade path: no dynamic client → no crash, registry untouched.
func TestReconcileTenantRegistryOnce_NoDynamicClientIsNoop(t *testing.T) {
	dir := t.TempDir()
	registry, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("tenant registry: %v", err)
	}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetTenantRegistry(registry)
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: nil, dyn: nil}, nil
	})
	// Must not panic; registry stays empty.
	h.reconcileTenantRegistryOnce(context.Background())
	if n := len(registry.List()); n != 0 {
		t.Errorf("registry should be empty, has %d rows", n)
	}
}
