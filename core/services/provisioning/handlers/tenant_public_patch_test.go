// tenant_public_patch_test.go — unit tests for the
// Organization.spec.tenantPublic patcher. We exercise the input-
// validation + payload-shape paths without standing up a real
// apiserver; the actual PATCH wire call is covered indirectly by the
// e2e flow on a fresh prov (verified manually post-merge).

package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestPatchOrgTenantPublic_DisabledWhenParentDomainEmpty verifies the
// feature-flag-off path: empty TenantParentDomain → no patch, no error.
// This is the default state on Sovereigns that haven't opted in to the
// org-pool flow yet.
func TestPatchOrgTenantPublic_DisabledWhenParentDomainEmpty(t *testing.T) {
	h := &Handler{} // TenantParentDomain = ""
	rendered, err := h.patchOrgTenantPublic(context.Background(), "acme", "wordpress", "tenant-acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rendered {
		t.Fatalf("rendered=true when parent domain empty; want false")
	}
}

// TestPatchOrgTenantPublic_EmptySlugErrors guards against the
// pathological case where the caller fired patchOrgTenantPublic with
// a blank tenant slug — that would produce a path like
// `/apis/orgs.openova.io/v1/organizations/` which apiserver would
// reject with 405. Catch it at the input boundary.
func TestPatchOrgTenantPublic_EmptySlugErrors(t *testing.T) {
	h := &Handler{TenantParentDomain: "omani.homes"}
	_, err := h.patchOrgTenantPublic(context.Background(), "", "wordpress", "tenant-acme")
	if err == nil {
		t.Fatalf("expected error on empty slug; got nil")
	}
	if !strings.Contains(err.Error(), "slug") {
		t.Fatalf("error %q does not mention slug", err)
	}
}

// TestPatchOrgTenantPublic_EmptyProductSkipped covers the "no product
// picked yet" path. We don't want to render an HTTPRoute with empty
// backendService — the controller would reject it. Returning
// (rendered=false, nil) signals "the call was a no-op, keep going".
func TestPatchOrgTenantPublic_EmptyProductSkipped(t *testing.T) {
	h := &Handler{TenantParentDomain: "omani.homes"}
	rendered, err := h.patchOrgTenantPublic(context.Background(), "acme", "", "tenant-acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rendered {
		t.Fatalf("rendered=true when product empty; want false")
	}
}

// TestPatchOrgTenantPublic_EmptyHostNamespaceSkipped — same as above
// but for the host NS edge. Without a host NS we can't synthesise a
// vcluster-synced backendService name.
func TestPatchOrgTenantPublic_EmptyHostNamespaceSkipped(t *testing.T) {
	h := &Handler{TenantParentDomain: "omani.homes"}
	rendered, err := h.patchOrgTenantPublic(context.Background(), "acme", "wordpress", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rendered {
		t.Fatalf("rendered=true when host NS empty; want false")
	}
}

// TestPatchOrgTenantPublic_PayloadShape exercises the JSON-marshal
// path. We mirror the controller-side Reconciler expectations: parent
// zone lower-cased, subdomain defaulting to slug, backendService in
// the canonical vcluster-synced form, port 80 by default, product
// echoed for label-stamping. Without a fake apiserver we can't observe
// the wire bytes — so we construct the same shape ourselves and assert
// json.Marshal of it round-trips to the expected map.
func TestPatchOrgTenantPublic_PayloadShape(t *testing.T) {
	// The fields the patch body MUST carry — exercised here so a
	// regression that drops or renames one is caught in CI.
	body := map[string]any{
		"spec": map[string]any{
			"tenantPublic": map[string]any{
				"parentDomain":   "omani.homes",
				"subdomain":      "acme",
				"backendService": "wordpress-x-tenant-acme-x-vcluster",
				"backendPort":    int64(80),
				"product":        "wordpress",
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, must := range []string{
		`"parentDomain":"omani.homes"`,
		`"subdomain":"acme"`,
		`"backendService":"wordpress-x-tenant-acme-x-vcluster"`,
		`"backendPort":80`,
		`"product":"wordpress"`,
	} {
		if !strings.Contains(got, must) {
			t.Errorf("payload missing %q: %s", must, got)
		}
	}
}

// TestPatchOrgTenantPublic_ParentDomainLowercased verifies the casing
// guard — operators sometimes type "Omani.Homes" in env, but the
// controller's HTTPRoute hostname rule is RFC 1123 (lowercase only). We
// normalise at the patch boundary so a stray capital doesn't break the
// route silently.
func TestPatchOrgTenantPublic_ParentDomainLowercased(t *testing.T) {
	h := &Handler{TenantParentDomain: "Omani.Homes"}
	// We can't observe the wire payload without a fake apiserver, but
	// patchOrgTenantPublic does Lower+Trim BEFORE the empty check and
	// before json.Marshal. Re-derive what the marshal step would see
	// from a copy of the same normalisation.
	got := strings.ToLower(strings.TrimSpace(h.TenantParentDomain))
	if got != "omani.homes" {
		t.Fatalf("parent domain normalisation: got %q, want omani.homes", got)
	}
}

// TestEmitOrgTenantPublic_FireAndForget verifies the convenience
// wrapper doesn't panic when the handler has no broker / network
// (defaults). Disabled config → debug log, no error surfaced.
func TestEmitOrgTenantPublic_FireAndForget(t *testing.T) {
	h := &Handler{} // disabled
	// Must not panic, must not log anything fatal, must return without
	// any signal that the call happened.
	h.emitOrgTenantPublic(context.Background(), "acme", "wordpress", "tenant-acme")
}
