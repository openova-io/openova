package handler

import (
	"testing"

	sigyaml "sigs.k8s.io/yaml"
)

// The per-Org bp-agenity HelmRelease must NEVER stall (Refs #4111 #6163 #4277).
//
// Its readiness is gated on an input the platform does not ship — the
// Sovereign owner's Anthropic credential — and the chart's init container
// deliberately fails until that credential lands (#6163). With
// remediation.retries unset, helm-controller gives up after ONE failed
// attempt (`Stalled=True / RetriesExceeded`) and never reconciles the release
// again, so a credential supplied later brings the Pod up but leaves the
// HelmRelease Failed until a human resets it. Measured on hw307, ns
// acmewalk307, 2026-11-05: Stalled for 56 days with the init container
// restarted 11 806 times, ready the whole time.
//
// -1 is Flux's retry-forever sentinel (the vcluster boundary HR uses it for
// the same reason). Asserted on the PARSED YAML, not on a substring, so a
// comment line cannot satisfy it.
func TestOrgTenantBPAgenity_NeverStalls(t *testing.T) {
	gate5425Env(t, "")

	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{Agenity: "0.5.34"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	doc := files["bp-agenity.yaml"]
	if doc == "" {
		t.Fatal("bp-agenity.yaml did not render — nothing to assert on")
	}

	var hr struct {
		Kind string `json:"kind"`
		Spec struct {
			Install struct {
				Timeout     string `json:"timeout"`
				Remediation *struct {
					Retries *int `json:"retries"`
				} `json:"remediation"`
			} `json:"install"`
			Upgrade struct {
				Timeout     string `json:"timeout"`
				Remediation *struct {
					Retries *int `json:"retries"`
				} `json:"remediation"`
			} `json:"upgrade"`
		} `json:"spec"`
	}
	if err := sigyaml.Unmarshal([]byte(doc), &hr); err != nil {
		t.Fatalf("bp-agenity.yaml is not parseable YAML: %v\n---\n%s", err, doc)
	}
	// VACUITY — the document must be the HelmRelease, or the fields below
	// are being asserted on nothing.
	if hr.Kind != "HelmRelease" {
		t.Fatalf("rendered kind = %q, want HelmRelease", hr.Kind)
	}
	for _, leg := range []struct {
		name    string
		timeout string
		rem     *struct {
			Retries *int `json:"retries"`
		}
	}{
		{"install", hr.Spec.Install.Timeout, hr.Spec.Install.Remediation},
		{"upgrade", hr.Spec.Upgrade.Timeout, hr.Spec.Upgrade.Remediation},
	} {
		if leg.rem == nil || leg.rem.Retries == nil {
			t.Errorf("spec.%s.remediation.retries is UNSET — helm-controller defaults to 0 and the release stalls permanently after one failed attempt", leg.name)
			continue
		}
		if *leg.rem.Retries != -1 {
			t.Errorf("spec.%s.remediation.retries = %d, want -1 (retry forever: the only blocker is an operator-supplied credential that can arrive days later)", leg.name, *leg.rem.Retries)
		}
		// CONTROL — the 15m deadline must survive: retries alone would just
		// re-hit a shorter default timeout (#3988 made the same point for the
		// Application fan-out).
		if leg.timeout != "15m" {
			t.Errorf("spec.%s.timeout = %q, want 15m", leg.name, leg.timeout)
		}
	}
}
