package gitops

import (
	"strings"
	"testing"
)

// #6352 — the Pillar-4 emitter must render, must claim the slug, and must put
// the SOVEREIGN host in issuerHost (never the Org zone — that is #6314).
func TestAgenityHR_6352(t *testing.T) {
	if !isHelmReleaseApp("agenity") {
		t.Fatal("agenity is not registered in helmReleaseAppSlugs")
	}
	opt := helmReleaseAppOpts{
		slug:              "chepherd",
		parentDomain:      "omani.rest",
		sharedRealmIssuer: "https://auth.hw298.omantel.biz/realms/sovereign",
		chartVersion:      "0.5.28",
	}
	out := generateHelmReleaseApp("agenity", opt)
	if out == "" {
		t.Fatal("generateHelmReleaseApp returned EMPTY for agenity")
	}
	for _, want := range []string{
		"chart: bp-agenity",
		`version: "0.5.28"`,
		"issuerHost: hw298.omantel.biz",
		"sovereignFqdn: chepherd.omani.rest",
		"- agenity.chepherd.omani.rest",
		"clientId: agenity-chepherd",
		"name: cilium-gateway-console",
		"allowGatewayEntity: true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered HR missing %q\n---\n%s", want, out)
		}
	}
	// CONTROL 1 — the Org zone must NEVER be the issuer host (#6314 regression).
	if strings.Contains(out, "issuerHost: chepherd.omani.rest") {
		t.Error("issuerHost is the ORG zone — this is the #6314 envoy-404 bug")
	}
	// CONTROL 2 — no dependsOn (would wedge on a never-rendered bp-keycloak).
	// Assert on the YAML KEY, not the substring: the template's own header
	// comment explains why there is no dependsOn, and a naive Contains matched
	// that prose on the first run. Strip comment lines, then look for the key.
	if yamlHasKey(out, "dependsOn") {
		t.Error("agenity HR carries a dependsOn — wedges in DependencyNotReady")
	}
	// CONTROL 3 — with no shared realm issuer the key is OMITTED, not malformed.
	bare := generateHelmReleaseApp("agenity", helmReleaseAppOpts{slug: "a", parentDomain: "b.c"})
	if strings.Contains(bare, "issuerHost:") {
		t.Errorf("issuerHost stamped without a derivable Sovereign host:\n%s", bare)
	}
	// VACUITY — the positive assertion above must be able to fail.
	if !strings.Contains(bare, "chart: bp-agenity") {
		t.Error("control render is not an agenity HR at all — assertions vacuous")
	}
}


// yamlHasKey reports whether a real (non-comment) line declares the key.
func yamlHasKey(doc, key string) bool {
	for _, ln := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, key+":") || strings.HasPrefix(t, "- "+key+":") {
			return true
		}
	}
	return false
}
