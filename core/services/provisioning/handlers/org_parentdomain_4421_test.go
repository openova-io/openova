package handlers

import (
	"context"
	"testing"

	"github.com/openova-io/openova/core/services/provisioning/gitops"
)

// TestResolveOrgParentDomain_4999 pins the #4999 behavior (superseding the #4421
// band-aid): the Organization CR's pool HONORS the customer's funnel pick when
// the Sovereign SERVES that pool zone, and only falls back to the apps pool when
// the pick is empty or not served. The apps-HTTPRoute generator follows the SAME
// resolved zone (consumer.go scoped clone), so the #4421 invariant (per-Org
// A-record zone == app-host zone) still holds — both simply move to the honored
// TLD together instead of collapsing onto the single apps pool.
func TestResolveOrgParentDomain_4999(t *testing.T) {
	cases := []struct {
		name        string
		appsPool    string
		poolDomains []string
		payloadPool string
		want        string
	}{
		{
			// THE #4999 core: customer picked omani.rest (a served pool zone) in
			// the funnel; apps pool is omani.homes → HONOR the pick. The Org CR,
			// the console DNS/TLS/route, AND the apps generator all follow it.
			name:        "served payload pick is honored",
			appsPool:    "omani.homes",
			payloadPool: "omani.rest",
			want:        "omani.rest",
		},
		{
			name:        "served omani.trade pick is honored",
			appsPool:    "omani.homes",
			payloadPool: "omani.trade",
			want:        "omani.trade",
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
			// The #4421 dead-IP guard: a pick the Sovereign does NOT serve falls
			// back to the apps pool so we never provision under an unserved zone.
			name:        "unserved payload pick falls back to apps pool",
			appsPool:    "omani.homes",
			payloadPool: "not-a-pool.example",
			want:        "omani.homes",
		},
		{
			// An explicit served-set that excludes the pick → fall back.
			name:        "pick outside configured pool set falls back",
			appsPool:    "omani.homes",
			poolDomains: []string{"omani.homes"},
			payloadPool: "omani.rest",
			want:        "omani.homes",
		},
		{
			// Degenerate Sovereign with no apps pool wired at all — the payload
			// survives as a last-resort fallback.
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
			name:        "normalizes case + whitespace + leading dot (served pick)",
			appsPool:    "omani.homes",
			payloadPool: "  .Omani.Rest  ",
			want:        "omani.rest",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOrgParentDomain(tc.appsPool, tc.poolDomains, tc.payloadPool)
			if got != tc.want {
				t.Errorf("resolveOrgParentDomain(%q,%v,%q) = %q; want %q",
					tc.appsPool, tc.poolDomains, tc.payloadPool, got, tc.want)
			}
		})
	}
}

// TestResolveParentDomain_ConsoleAppsLockstep_4999 proves the two zones stay in
// lockstep under the honored pick: whatever resolveOrgParentDomain stamps on the
// Org CR, the apps generator renders under (consumer.go feeds the SAME resolved
// zone to a scoped generator clone). A served pick moves BOTH; an unset env still
// defaults both to omani.homes.
func TestResolveParentDomain_ConsoleAppsLockstep_4999(t *testing.T) {
	// Empty env: apps generator defaults to omani.homes; an empty pick stamps the
	// SAME pool on the Org CR.
	appsPool := gitops.ResolveParentDomain("")
	if appsPool != "omani.homes" {
		t.Fatalf("apps generator default pool = %q; want omani.homes", appsPool)
	}
	if got := resolveOrgParentDomain(appsPool, nil, ""); got != appsPool {
		t.Errorf("empty pick: Org CR pool %q diverged from apps pool %q", got, appsPool)
	}

	// A served pick is honored — the Org CR zone AND (via the scoped clone) the
	// app-host zone both become omani.rest, so they stay aligned on the pick.
	if got := resolveOrgParentDomain(appsPool, nil, "omani.rest"); got != "omani.rest" {
		t.Errorf("served pick: Org CR pool = %q; want the honored omani.rest", got)
	}

	// Explicit apps env flows through unchanged when the pick is empty.
	if got := gitops.ResolveParentDomain("omani.trade"); got != "omani.trade" {
		t.Errorf("ResolveParentDomain(omani.trade) = %q; want omani.trade", got)
	}
	if got := resolveOrgParentDomain("omani.trade", nil, ""); got != "omani.trade" {
		t.Errorf("empty pick: Org CR pool = %q; want the apps pool omani.trade", got)
	}
}

// TestCreateOrganizationCR_HonorsServedPick_NoFail_4999 drives the full handler
// path with a served payload pick + a configured apps pool: the Org CR must
// still mint (no provision.org_create_failed), confirming honoring the pick is
// silent + non-fatal.
func TestCreateOrganizationCR_HonorsServedPick_NoFail_4999(t *testing.T) {
	clearK8sEnv(t)
	pub := &recordingPublisher{}
	h := &Handler{
		Producer:         pub,
		AppsParentDomain: "omani.homes", // Sovereign primary apps pool
		SovereignFQDN:    "test.omani.works",
		// PoolDomains nil → the canonical four .omani.X served zones.
	}
	data := tenantCreatedPayload{
		ID:           "tenant-abc",
		Slug:         "acme",
		OwnerEmail:   "owner@example.com",
		PlanID:       "org-pool-basic",
		ParentDomain: "omani.rest", // customer's served funnel pick — now honored
	}
	// k8s env is scrubbed → expect a non-nil "create Organization" error from the
	// POST, but crucially NOT a validation failure event.
	err := h.createOrganizationCR(context.Background(), data)
	if err == nil {
		t.Fatalf("expected non-nil error (k8s env scrubbed); got nil")
	}
	if pub.byType("provision.org_create_failed") != nil {
		t.Errorf("a served payload pick should be silently honored, not fail-published")
	}
}
