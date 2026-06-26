package handlers

import (
	"context"
	"testing"

	"github.com/openova-io/openova/core/services/provisioning/gitops"
)

// TestResolveOrgParentDomain_4421 pins the #4421 invariant: the Organization
// CR's pool (which the org-controller's DNS writer + console route key off) is
// the SAME pool the per-Org apps-HTTPRoute renders under (h.AppsParentDomain).
// A per-customer funnel pick (payload parent_domain) must NEVER override it on
// a Sovereign that has an apps pool — otherwise the app host falls through to a
// stale apex wildcard and resolves to a dead IP.
func TestResolveOrgParentDomain_4421(t *testing.T) {
	cases := []struct {
		name        string
		appsPool    string
		payloadPool string
		want        string
	}{
		{
			// THE bug: customer picked omani.rest in the funnel but the
			// Sovereign apps render under omani.homes. The apps pool must win
			// so the per-Org A-record lands in omani.homes (where the app host
			// resolves) instead of omani.rest (where it would never be looked
			// up for the app host).
			name:        "apps pool wins over a diverging payload pick",
			appsPool:    "omani.homes",
			payloadPool: "omani.rest",
			want:        "omani.homes",
		},
		{
			name:        "apps pool wins over omani.trade payload pick",
			appsPool:    "omani.homes",
			payloadPool: "omani.trade",
			want:        "omani.homes",
		},
		{
			name:        "apps pool used when payload is empty",
			appsPool:    "omani.homes",
			payloadPool: "",
			want:        "omani.homes",
		},
		{
			name:        "agreeing pools resolve to that pool",
			appsPool:    "omani.homes",
			payloadPool: "omani.homes",
			want:        "omani.homes",
		},
		{
			// Degenerate Sovereign with no apps pool wired at all — the
			// payload survives as a last-resort fallback.
			name:        "payload fallback when apps pool empty",
			appsPool:    "",
			payloadPool: "omani.rest",
			want:        "omani.rest",
		},
		{
			name:        "both empty resolves to empty (feature disabled)",
			appsPool:    "",
			payloadPool: "",
			want:        "",
		},
		{
			name:        "normalizes case + whitespace",
			appsPool:    "  Omani.Homes  ",
			payloadPool: "omani.rest",
			want:        "omani.homes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOrgParentDomain(tc.appsPool, tc.payloadPool)
			if got != tc.want {
				t.Errorf("resolveOrgParentDomain(%q,%q) = %q; want %q",
					tc.appsPool, tc.payloadPool, got, tc.want)
			}
		})
	}
}

// TestResolveParentDomain_SingleSourceOfTruth_4421 proves the handler's apps
// pool and the gitops apps-HTTPRoute generator pool come from the SAME
// resolver, so they cannot drift: an unset TENANT_PARENT_DOMAIN defaults to
// omani.homes on BOTH sides, and a set value flows through verbatim. This is
// what main.go relies on when it sets
// Handler.AppsParentDomain = gitops.ResolveParentDomain(tenantParentDomain).
func TestResolveParentDomain_SingleSourceOfTruth_4421(t *testing.T) {
	// Empty env: apps generator defaults to omani.homes; the handler, fed the
	// SAME resolved value, must stamp the SAME pool on the Org CR.
	appsPool := gitops.ResolveParentDomain("")
	if appsPool != "omani.homes" {
		t.Fatalf("apps generator default pool = %q; want omani.homes", appsPool)
	}
	if got := resolveOrgParentDomain(appsPool, "omani.rest"); got != appsPool {
		t.Errorf("Org CR pool %q diverged from apps pool %q despite shared resolver", got, appsPool)
	}

	// Explicit env: flows through unchanged on both sides.
	if got := gitops.ResolveParentDomain("omani.trade"); got != "omani.trade" {
		t.Errorf("ResolveParentDomain(omani.trade) = %q; want omani.trade", got)
	}
	if got := resolveOrgParentDomain("omani.trade", "omani.homes"); got != "omani.trade" {
		t.Errorf("Org CR pool = %q; want the apps pool omani.trade", got)
	}
}

// TestCreateOrganizationCR_AppsPoolWins_NoFail_4421 drives the full handler
// path with a diverging payload pick + a configured apps pool: the Org CR must
// still mint (no provision.org_create_failed), confirming the #4421 override is
// silent + non-fatal (it only realigns the pool, it does not reject the Org).
func TestCreateOrganizationCR_AppsPoolWins_NoFail_4421(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:         pub,
		AppsParentDomain: "omani.homes", // Sovereign apps render here
		SovereignFQDN:    "test.omani.works",
	}
	data := tenantCreatedPayload{
		ID:           "tenant-abc",
		Slug:         "acme",
		OwnerEmail:   "owner@example.com",
		PlanID:       "org-pool-basic",
		ParentDomain: "omani.rest", // customer's diverging funnel pick
	}
	// k8s env is scrubbed → expect a non-nil "create Organization" error from
	// the POST, but crucially NOT a validation failure event.
	err := h.createOrganizationCR(context.Background(), data)
	if err == nil {
		t.Fatalf("expected non-nil error (k8s env scrubbed); got nil")
	}
	if pub.byType("provision.org_create_failed") != nil {
		t.Errorf("diverging payload pool should be silently realigned, not fail-published")
	}
}
