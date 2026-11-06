package switchover

import (
	"context"
	"testing"

	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
)

// hw307 2026-11-06: a planned switchover of dr-cnpg-pair (a data-tier pair:
// no pdmZone, no hostnames, no region IPs — clients follow the CNPG -rw
// service) failed at flip-dns because dns.Synthesize errors on an empty DNS
// seam, right after the old primary had been demoted. flip-dns must skip a
// plan that declares nothing to flip, exactly as drain-http skips without an
// HTTPRoute.
func TestFlipDNS_SkippedForPlanWithoutDNSSeam(t *testing.T) {
	t.Parallel()
	seq, w, _, _, committed := makeStatelessSequencer(t)
	plan := statelessPlan("cnpg-pair")
	plan.PDMZone = ""
	plan.SynthParams = dns.SynthParams{}
	plan.Application = newTestApp(nil)
	plan.HTTPRouteName = ""

	res := seq.Execute(context.Background(), plan)
	if res.Err != nil {
		t.Fatalf("Execute: %v (failed at step %d) — a DB-only pair has no DNS to flip", res.Err, res.FailedAtStep)
	}
	if len(*committed) != 0 {
		t.Fatalf("committed %d DNS records for a plan with no DNS seam", len(*committed))
	}
	st, _ := w.Read(context.Background())
	if st.Holder != "hel" {
		t.Fatalf("lease holder = %q want hel — the switchover must still complete", st.Holder)
	}
}

// A plan WITH a DNS seam still flips DNS (no regression of the stateless/
// HTTP-fronted path).
func TestFlipDNS_StillRunsWhenDNSSeamDeclared(t *testing.T) {
	t.Parallel()
	seq, _, _, _, committed := makeStatelessSequencer(t)
	if res := seq.Execute(context.Background(), statelessPlan("sso-bridge")); res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if len(*committed) == 0 {
		t.Fatal("expected DNS records committed when the plan declares a DNS seam")
	}
}

func TestPlanHasDNSSeam(t *testing.T) {
	cases := []struct {
		name string
		plan SwitchoverPlan
		want bool
	}{
		{"empty", SwitchoverPlan{}, false},
		{"zone", SwitchoverPlan{PDMZone: "z"}, true},
		{"hostnames", SwitchoverPlan{SynthParams: dns.SynthParams{Hostnames: []string{"h"}}}, true},
		{"ips", SwitchoverPlan{SynthParams: dns.SynthParams{RegionToIPs: map[string][]string{"a": {"1.2.3.4"}}}}, true},
	}
	for _, c := range cases {
		if got := planHasDNSSeam(c.plan); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
