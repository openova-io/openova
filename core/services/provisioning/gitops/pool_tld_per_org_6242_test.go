// pool_tld_per_org_6242_test.go — #6242, the UAT row-95 shape.
//
// Row 95 asserts "two Orgs, two TLDs, two running apps, identical mechanism".
// The funnel resolver has honoured the customer's pool pick per-Org since
// #4999, and the generator is cloned per Org at consumer.go's
// resolveOrgParentDomain seam — but three rendered artifacts still carried a
// LITERAL `omani.rest`, so an Org whose pick was any other pool zone got
// manifests pointing at a zone it does not own:
//
//	gitops.go generateHostIngress   — the rule host AND the TLS SAN
//	gitops.go bookstack DBEnvStyle  — APP_URL
//	apps.go   cal-com / ghost       — NEXTAUTH_URL, NEXT_PUBLIC_WEBAPP_URL, url
//
// WHY THIS TEST RENDERS TWO ORGS AND NOT ONE
// ------------------------------------------
// A single-Org test is vacuous here by construction: pick `omani.rest` as the
// fixture zone and the hardcoded literal produces byte-identical output to the
// fix. The bug is INVISIBLE on a Sovereign serving one pool TLD and appears
// only when a second Org lands on a second one — which is exactly why it
// survived. So the control shares the suspect property: a second Organization,
// same app set, same generator type, same code path, differing ONLY in its pool
// zone. Each Org's render must carry its OWN zone and must not mention the
// other's.
package gitops

import (
	"strings"
	"testing"
)

// poolZoneAppSlugs are the apps that actually embed a public hostname in what
// they render: one per affected role. bookstack exercises the DBEnvStyle
// branch, cal-com and ghost the AppSpec.EnvVars placeholder path, and wordpress
// is the plain Deployment that the host Ingress routes.
var poolZoneAppSlugs = []string{"wordpress", "bookstack", "cal-com", "ghost"}

// renderForZone renders one Organization's full tree under a given pool zone.
func renderForZone(t *testing.T, slug, zone string) map[string]string {
	t.Helper()
	g := &ManifestGenerator{
		BasePath:     "clusters/contabo-mkt/tenants",
		ParentDomain: zone,
	}
	out := g.GenerateAllWithAppConfigs(slug, "m", poolZoneAppSlugs, "pw", nil)
	if len(out) == 0 {
		t.Fatalf("generator produced no manifests for %s under %s", slug, zone)
	}
	return out
}

// TestPoolZoneIsPerOrg_TwoOrgsTwoTLDs is the row-95 guard.
func TestPoolZoneIsPerOrg_TwoOrgsTwoTLDs(t *testing.T) {
	const (
		zoneA = "omani.trade"
		zoneB = "omani.homes"
	)
	orgs := []struct{ slug, zone, otherZone string }{
		{"alpha", zoneA, zoneB},
		{"bravo", zoneB, zoneA},
	}

	for _, org := range orgs {
		files := renderForZone(t, org.slug, org.zone)

		ownHost := org.slug + "." + org.zone
		sawOwnHost := false

		for name, body := range files {
			// No render may name the OTHER Organization's pool zone. This is
			// the assertion the hardcoded literal fails: with `omani.rest`
			// baked in, neither Org names its own zone and both name a third.
			if strings.Contains(body, org.otherZone) {
				t.Errorf("%s/%s names the other Organization's pool zone %q:\n%s",
					org.slug, name, org.otherZone, excerptAround(body, org.otherZone))
			}
			// Nor may any render name a pool zone that is neither Org's — the
			// control that catches a literal surviving at a site this test
			// does not otherwise inspect.
			for _, stray := range []string{"omani.rest", "omani.works"} {
				if stray == org.zone || stray == org.otherZone {
					continue
				}
				if strings.Contains(body, stray) {
					t.Errorf("%s/%s names pool zone %q, which no Organization in this test picked — a hardcoded literal:\n%s",
						org.slug, name, stray, excerptAround(body, stray))
				}
			}
			if strings.Contains(body, ownHost) {
				sawOwnHost = true
			}
		}

		// Assert on the VALUE, not merely the absence of the wrong one: the
		// Org's real host must actually appear, or a render that dropped every
		// hostname would pass the exclusions above.
		if !sawOwnHost {
			t.Errorf("no manifest for %s contains its own host %q — the zone was dropped, not re-pointed",
				org.slug, ownHost)
		}
	}
}

// TestPoolZoneIsPerOrg_HostIngressCarriesTheOrgZone pins the two Ingress sites
// (rule host + TLS SAN) by name, so a regression reports WHICH artifact drifted
// rather than only that something did.
func TestPoolZoneIsPerOrg_HostIngressCarriesTheOrgZone(t *testing.T) {
	const zone = "omani.trade"
	ing := generateHostIngress("charlie", "charlie", zone, []string{"wordpress"})
	if ing == "" {
		t.Fatal("generateHostIngress rendered nothing for a routable app")
	}
	want := "charlie." + zone
	if n := strings.Count(ing, want); n != 2 {
		t.Errorf("host Ingress names %q %d time(s), want 2 (rule host + TLS SAN):\n%s", want, n, ing)
	}
	if strings.Contains(ing, "omani.rest") {
		t.Errorf("host Ingress still carries the hardcoded omani.rest:\n%s", ing)
	}
}

// TestPoolZoneIsPerOrg_EnvPlaceholderSubstituted pins the AppSpec placeholder
// contract itself: PARENTDOMAIN must be substituted, never emitted raw. A
// literal `PARENTDOMAIN` in a rendered URL is a worse failure than the old
// hardcoded zone — it is not even a hostname.
func TestPoolZoneIsPerOrg_EnvPlaceholderSubstituted(t *testing.T) {
	files := renderForZone(t, "delta", "omani.homes")
	for name, body := range files {
		if strings.Contains(body, "PARENTDOMAIN") {
			t.Errorf("%s emits the raw PARENTDOMAIN placeholder:\n%s", name, excerptAround(body, "PARENTDOMAIN"))
		}
		if strings.Contains(body, "TENANT.") {
			t.Errorf("%s emits the raw TENANT placeholder:\n%s", name, excerptAround(body, "TENANT."))
		}
	}
}

// TestAppPublicHost_NoTrailingDotOnAnEmptyZone pins the degenerate case: a
// generator with no pool wired must yield the bare slug, not `slug.`, which
// would render a syntactically invalid hostname into a manifest.
func TestAppPublicHost_NoTrailingDotOnAnEmptyZone(t *testing.T) {
	for _, zone := range []string{"", "   ", "."} {
		if got := appPublicHost("echo", zone); got != "echo" {
			t.Errorf("appPublicHost(%q, %q) = %q, want %q", "echo", zone, got, "echo")
		}
	}
	if got := appPublicHost("echo", " Omani.Trade. "); got != "echo.omani.trade" {
		t.Errorf("appPublicHost did not normalise the zone: got %q", got)
	}
}

// excerptAround returns the line containing needle, for a legible failure.
func excerptAround(body, needle string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
