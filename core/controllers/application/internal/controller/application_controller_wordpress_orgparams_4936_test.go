// Tests for #4936 — per-Org WordPress orgDomain + adminUser are DERIVED
// from the parent Organization context, never typed by the customer.
//
// The console 'New instance' dialog + the marketplace funnel collect
// neither field, so the Blueprint configSchema no longer requires them and
// the Application controller injects them from the Organization CR before
// the configSchema validation + every render path. These unit tests lock in
// the derivation + the "operator override wins / no-op for other blueprints"
// contract.
package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func orgCR(spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata":   map[string]interface{}{"name": "acme"},
		"spec":       spec,
	}}
}

func TestInjectOrgDerivedParameters_DerivesDomainAndOwner(t *testing.T) {
	org := orgCR(map[string]interface{}{
		"slug":        "acme",
		"displayName": "ACME Corp",
		"owners": []interface{}{
			map[string]interface{}{"email": "dev@acme.test", "role": "developer"},
			map[string]interface{}{"email": "owner@acme.test", "role": "owner"},
		},
		"tenantPublic": map[string]interface{}{
			"parentDomain": "omani.homes",
			"subdomain":    "acme",
		},
	})

	got := injectOrgDerivedParameters("bp-wordpress-tenant", org, map[string]interface{}{})

	if got["orgDomain"] != "acme.omani.homes" {
		t.Fatalf("orgDomain = %v, want acme.omani.homes", got["orgDomain"])
	}
	admin, ok := got["adminUser"].(map[string]interface{})
	if !ok {
		t.Fatalf("adminUser not a map: %T", got["adminUser"])
	}
	if admin["email"] != "owner@acme.test" {
		t.Fatalf("adminUser.email = %v, want owner@acme.test (role=owner wins)", admin["email"])
	}
	if admin["displayName"] != "ACME Corp" {
		t.Fatalf("adminUser.displayName = %v, want ACME Corp", admin["displayName"])
	}
}

func TestInjectOrgDerivedParameters_SubdomainFallsBackToSlug(t *testing.T) {
	org := orgCR(map[string]interface{}{
		"slug": "beta",
		"tenantPublic": map[string]interface{}{
			"parentDomain": "omani.rest",
			// subdomain omitted → falls back to slug
		},
	})
	got := injectOrgDerivedParameters("bp-wordpress", org, map[string]interface{}{})
	if got["orgDomain"] != "beta.omani.rest" {
		t.Fatalf("orgDomain = %v, want beta.omani.rest (slug fallback)", got["orgDomain"])
	}
}

func TestInjectOrgDerivedParameters_OperatorOverrideWins(t *testing.T) {
	org := orgCR(map[string]interface{}{
		"slug":        "acme",
		"displayName": "ACME Corp",
		"owners": []interface{}{
			map[string]interface{}{"email": "owner@acme.test", "role": "owner"},
		},
		"tenantPublic": map[string]interface{}{"parentDomain": "omani.homes", "subdomain": "acme"},
	})

	explicit := map[string]interface{}{
		"orgDomain": "byo.example.com",
		"adminUser": map[string]interface{}{"email": "boss@byo.example.com"},
	}
	got := injectOrgDerivedParameters("bp-wordpress-tenant", org, explicit)

	if got["orgDomain"] != "byo.example.com" {
		t.Fatalf("orgDomain = %v, want byo.example.com (explicit wins)", got["orgDomain"])
	}
	admin := got["adminUser"].(map[string]interface{})
	if admin["email"] != "boss@byo.example.com" {
		t.Fatalf("adminUser.email = %v, want boss@byo.example.com (explicit wins)", admin["email"])
	}
	// displayName was absent in the explicit override → filled from Org.
	if admin["displayName"] != "ACME Corp" {
		t.Fatalf("adminUser.displayName = %v, want ACME Corp (absent sub-field filled)", admin["displayName"])
	}
}

func TestInjectOrgDerivedParameters_NoOpForOtherBlueprints(t *testing.T) {
	org := orgCR(map[string]interface{}{
		"slug":         "acme",
		"tenantPublic": map[string]interface{}{"parentDomain": "omani.homes"},
	})
	in := map[string]interface{}{"replicas": int64(2)}
	got := injectOrgDerivedParameters("bp-nextcloud", org, in)
	if _, ok := got["orgDomain"]; ok {
		t.Fatalf("orgDomain injected for a non-WordPress Blueprint: %v", got["orgDomain"])
	}
	if _, ok := got["adminUser"]; ok {
		t.Fatalf("adminUser injected for a non-WordPress Blueprint")
	}
}

func TestInjectOrgDerivedParameters_NoPublicHostnameSkipsOrgDomain(t *testing.T) {
	// tenantPublic absent → no derivable domain → orgDomain left unset (the
	// configSchema no longer requires it, so admission still passes).
	org := orgCR(map[string]interface{}{
		"slug":   "acme",
		"owners": []interface{}{map[string]interface{}{"email": "owner@acme.test", "role": "owner"}},
	})
	got := injectOrgDerivedParameters("bp-wordpress-tenant", org, map[string]interface{}{})
	if _, ok := got["orgDomain"]; ok {
		t.Fatalf("orgDomain should be unset when no public hostname is bound, got %v", got["orgDomain"])
	}
	// adminUser.email still derivable from owners.
	admin := got["adminUser"].(map[string]interface{})
	if admin["email"] != "owner@acme.test" {
		t.Fatalf("adminUser.email = %v, want owner@acme.test", admin["email"])
	}
}

func TestDeriveOrgOwnerEmail_FallsBackToFirstOwner(t *testing.T) {
	// No entry with role=owner → first owner with a non-empty email wins.
	org := orgCR(map[string]interface{}{
		"owners": []interface{}{
			map[string]interface{}{"email": "admin@acme.test", "role": "admin"},
			map[string]interface{}{"email": "dev@acme.test", "role": "developer"},
		},
	})
	if got := deriveOrgOwnerEmail(org); got != "admin@acme.test" {
		t.Fatalf("deriveOrgOwnerEmail = %v, want admin@acme.test (first-owner fallback)", got)
	}
}
