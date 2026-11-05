package gitops

import (
	"strings"
	"testing"
)

// The funnel-door bp-agenity HelmRelease must NEVER stall (Refs #4111 #6163
// #4277) — the twin of TestOrgTenantBPAgenity_NeverStalls on the BSS door.
//
// Readiness waits on the Sovereign owner's Anthropic credential, which the
// chart's init container blocks on by design (#6163). Every other funnel app
// carries `retries: 3`; for agenity that buys ~1h, after which Flux marks the
// release Stalled=True/RetriesExceeded and a credential supplied later brings
// the Pod up but leaves the HelmRelease Failed until a human resets it
// (measured on hw307 ns acmewalk307: Stalled 56 days, 11 806 init restarts).
//
// Asserted structurally: the `retries:` line that FOLLOWS each of the
// `install:` / `upgrade:` keys, comments stripped — not a bare substring, so
// the template's own prose cannot satisfy it.
func TestAgenityHR_NeverStalls(t *testing.T) {
	out := generateHelmReleaseApp("agenity", helmReleaseAppOpts{
		slug: "acme", parentDomain: "omani.trade", chartVersion: "0.5.34",
		sharedRealmIssuer: "https://auth.hw307.omani.works/realms/sovereign",
	})
	if !strings.Contains(out, "chart: bp-agenity") {
		t.Fatal("control render is not an agenity HR — assertions below would be vacuous")
	}
	got := retriesByLeg(out)
	for _, leg := range []string{"install", "upgrade"} {
		v, ok := got[leg]
		if !ok {
			t.Errorf("spec.%s carries no remediation.retries — helm-controller defaults to 0 and the release stalls after one failed attempt", leg)
			continue
		}
		if v != "-1" {
			t.Errorf("spec.%s.remediation.retries = %s, want -1 (retry forever: the only blocker is an operator-supplied credential)", leg, v)
		}
	}
	// CONTROL — the other funnel apps keep their bounded retries; agenity is
	// the exception BECAUSE of the credential wait, not a new default.
	other := retriesByLeg(generateHelmReleaseApp("openclaw", helmReleaseAppOpts{
		slug: "acme", parentDomain: "omani.trade", chartVersion: "0.2.19",
	}))
	if other["install"] != "3" || other["upgrade"] != "3" {
		t.Errorf("openclaw retries changed (%v) — the never-stall posture is agenity-only", other)
	}
}

// retriesByLeg maps "install"/"upgrade" → the retries value declared under
// that key, reading only real (non-comment) lines.
func retriesByLeg(doc string) map[string]string {
	out := map[string]string{}
	leg := ""
	for _, ln := range strings.Split(doc, "\n") {
		tr := strings.TrimSpace(ln)
		if tr == "" || strings.HasPrefix(tr, "#") {
			continue
		}
		switch {
		case tr == "install:":
			leg = "install"
		case tr == "upgrade:":
			leg = "upgrade"
		case tr == "values:" || tr == "chart:":
			leg = ""
		case strings.HasPrefix(tr, "retries:") && leg != "":
			out[leg] = strings.TrimSpace(strings.TrimPrefix(tr, "retries:"))
		}
	}
	return out
}
