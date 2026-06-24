package gitops

import (
	"strings"
	"testing"
)

// Workstream B (#4292 / EPIC #4293) — QoS split on the generated app
// Deployments. Fixed tiers (S/M/L/XL) must render requests==limits so the pod
// is Guaranteed (and so the per-Org LimitRange's maxLimitRequestRatio
// {cpu:1,memory:1} admits it). Flexi alone keeps the historical asymmetric
// shape (requests<limits) → Burstable.

// appDeploymentResources extracts the (reqCPU,reqMem,limCPU,limMem) block from a
// rendered app-*.yaml Deployment body for a given plan slug.
func renderAppDeployment(planSlug string) string {
	g := NewManifestGenerator("clusters/sov/tenants")
	out := g.GenerateAllWithAppConfigs("acme", planSlug, []string{"wordpress"}, "pw", nil)
	for path, body := range out {
		if strings.Contains(path, "app-wordpress.yaml") {
			return body
		}
	}
	return ""
}

func TestQoS_FixedTiersGuaranteed(t *testing.T) {
	// wordpress AppSpec request defaults are 100m / 256Mi (apps.go). A
	// Guaranteed pod renders limits == those requests.
	for _, slug := range []string{"s", "m", "l", "xl"} {
		body := renderAppDeployment(slug)
		if body == "" {
			t.Fatalf("plan %s: no app-wordpress.yaml rendered", slug)
		}
		// Guaranteed: cpu/memory appear in BOTH requests and limits at the same
		// value (the request floor).
		for _, want := range []string{
			"cpu: 100m",
			"memory: 256Mi",
		} {
			if strings.Count(body, want) < 2 {
				t.Errorf("plan %s: expected %q in BOTH requests and limits (Guaranteed), body:\n%s", slug, want, body)
			}
		}
		// Must NOT carry the old asymmetric Burstable ceiling.
		if strings.Contains(body, "cpu: 500m") || strings.Contains(body, "memory: 512Mi") {
			t.Errorf("plan %s: fixed tier must NOT render the Burstable ceiling 500m/512Mi (would be Burstable)\n%s", slug, body)
		}
	}
}

func TestQoS_FlexiBurstable(t *testing.T) {
	body := renderAppDeployment("flexi")
	if body == "" {
		t.Fatalf("flexi: no app-wordpress.yaml rendered")
	}
	// Burstable: the asymmetric ceiling is retained (requests 100m/256Mi <
	// limits 500m/512Mi).
	if !strings.Contains(body, "cpu: 500m") || !strings.Contains(body, "memory: 512Mi") {
		t.Errorf("flexi: expected the Burstable ceiling 500m/512Mi\n%s", body)
	}
	if !strings.Contains(body, "cpu: 100m") || !strings.Contains(body, "memory: 256Mi") {
		t.Errorf("flexi: expected the request floor 100m/256Mi\n%s", body)
	}
}
