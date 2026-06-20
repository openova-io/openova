// org_consumption_test.go — the B3 showback aggregation (issue #3378
// DoD 3 + §5), made per-Organization-honest by #3687 fold #3677.
//
// Locks the corrected contract:
//   - attribution keys on the real `openova.io/organization` namespace
//     label carried on podRow.org (NOT a hardcoded 100%-to-parent),
//   - control-plane / infra namespaces and one-shot Job-owned pods roll
//     into a single synthetic "Platform overhead" row (isPlatform=true),
//     never inside a tenant's app list,
//   - a real per-Org namespace immediately yields its own org row with
//     no per-name special-casing (the generality proof §7c).
package handler

import (
	"math"
	"testing"
)

// testInfraSet is the deterministic infra-exclusion set the unit tests
// drive (no env dependency). Mirrors the production default floor for the
// namespaces the tests exercise.
func testInfraSet() map[string]struct{} {
	return infraNamespaceSet("")
}

func TestAggregateConsumption_PlatformOverheadRollup(t *testing.T) {
	const parent = "hw150.omantel.biz"
	rows := []podRow{
		// catalyst-api in the control-plane namespace — platform overhead.
		{namespace: "catalyst", application: "catalyst-api", ownerKind: "ReplicaSet", cpuReq: 500, memReq: 1 << 30},
		// grafana in the (infra) monitoring namespace — platform overhead.
		{namespace: "monitoring", application: "grafana", ownerKind: "StatefulSet", cpuReq: 250, memReq: 512 << 20, storageLim: 10 << 30},
		// a one-shot scan Job pod in an otherwise-tenant namespace — must
		// NEVER attribute to the tenant; rolls into platform overhead.
		{namespace: "acme", application: "scan-vulnerabilityreport-xyz", org: "acme", ownerKind: "Job", cpuReq: 50, memReq: 64 << 20},
	}

	resp := aggregateConsumption(rows, parent, testInfraSet())

	// The parent estate row is always present and first.
	if len(resp.Orgs) == 0 || !resp.Orgs[0].IsParent {
		t.Fatalf("first row must be the parent estate, got %#v", resp.Orgs)
	}

	// There is exactly one platform-overhead row, flagged isPlatform, and
	// it is ordered last.
	last := resp.Orgs[len(resp.Orgs)-1]
	if !last.IsPlatform {
		t.Fatalf("platform-overhead row must be last, got %#v", resp.Orgs)
	}
	if last.Org != platformOrg {
		t.Errorf("platform row org = %q, want %q", last.Org, platformOrg)
	}

	// The Job pod must NOT appear under a tenant `acme` row. In fact no
	// `acme` tenant row should exist at all (its only pod was a Job).
	for _, oc := range resp.Orgs {
		if oc.Org == "acme" {
			t.Fatalf("a one-shot Job pod created a phantom tenant row %q", oc.Org)
		}
		for _, a := range oc.Apps {
			if a.Application == "scan-vulnerabilityreport-xyz" && !oc.IsPlatform {
				t.Errorf("scan Job attributed to %q (isPlatform=%v) — must be platform overhead", oc.Org, oc.IsPlatform)
			}
		}
	}

	// All three pods' cost landed in platform overhead (none in a tenant).
	if last.CPUMilli != 800 {
		t.Errorf("platform CPUMilli = %v, want 800 (all three pods)", last.CPUMilli)
	}
}

func TestAggregateConsumption_RealSecondOrgViaLabelJoinKey(t *testing.T) {
	const parent = "hw150.omantel.biz"
	rows := []podRow{
		// acme tenant workload — namespace carries the org label.
		{namespace: "acme", application: "blog", org: "acme", ownerKind: "StatefulSet", cpuReq: 200, memReq: 256 << 20},
		// beta tenant workload — a second labelled org.
		{namespace: "beta", application: "shop", org: "beta", ownerKind: "Deployment", cpuReq: 300, memReq: 512 << 20},
		// platform pod.
		{namespace: "kube-system", application: "cilium", ownerKind: "DaemonSet", cpuReq: 100, memReq: 128 << 20},
	}

	resp := aggregateConsumption(rows, parent, testInfraSet())

	var acme, beta *orgConsumption
	for i := range resp.Orgs {
		switch resp.Orgs[i].Org {
		case "acme":
			acme = &resp.Orgs[i]
		case "beta":
			beta = &resp.Orgs[i]
		}
	}
	if acme == nil || beta == nil {
		t.Fatalf("both labelled orgs must materialize as rows; got %#v", resp.Orgs)
	}
	if acme.IsParent || acme.IsPlatform || beta.IsParent || beta.IsPlatform {
		t.Errorf("tenant rows must be neither parent nor platform")
	}
	// Each tenant carries ONLY its own app.
	if len(acme.Apps) != 1 || acme.Apps[0].Application != "blog" {
		t.Errorf("acme must carry only blog, got %#v", acme.Apps)
	}
	if len(beta.Apps) != 1 || beta.Apps[0].Application != "shop" {
		t.Errorf("beta must carry only shop, got %#v", beta.Apps)
	}
	// cilium (kube-system) is platform overhead, not a tenant.
	for _, oc := range resp.Orgs {
		for _, a := range oc.Apps {
			if a.Application == "cilium" && !oc.IsPlatform {
				t.Errorf("cilium attributed to %q — must be platform overhead", oc.Org)
			}
		}
	}
}

func TestAggregateConsumption_PerAppPercentSumsTo100WithinOrg(t *testing.T) {
	const parent = "hw150.omantel.biz"
	rows := []podRow{
		{namespace: "acme", application: "blog", org: "acme", ownerKind: "StatefulSet", cpuReq: 200, memReq: 256 << 20},
		{namespace: "acme", application: "api", org: "acme", ownerKind: "Deployment", cpuReq: 300, memReq: 256 << 20},
		// a second blog pod (same app/ns) — must fold into one app row.
		{namespace: "acme", application: "blog", org: "acme", ownerKind: "StatefulSet", cpuReq: 200, memReq: 256 << 20},
	}

	resp := aggregateConsumption(rows, parent, testInfraSet())

	var acme *orgConsumption
	for i := range resp.Orgs {
		if resp.Orgs[i].Org == "acme" {
			acme = &resp.Orgs[i]
		}
	}
	if acme == nil {
		t.Fatal("acme org row missing")
	}
	if len(acme.Apps) != 2 {
		t.Fatalf("expected 2 app rows (blog + api), got %d", len(acme.Apps))
	}
	var blog *appConsumption
	for i := range acme.Apps {
		if acme.Apps[i].Application == "blog" {
			blog = &acme.Apps[i]
		}
	}
	if blog == nil {
		t.Fatal("blog app row missing")
	}
	if blog.CPUMilli != 400 {
		t.Errorf("blog CPUMilli: got %v want 400 (two pods folded)", blog.CPUMilli)
	}
	var pct float64
	for _, a := range acme.Apps {
		pct += a.Percent
	}
	if math.Abs(pct-100) > 0.5 {
		t.Errorf("per-app percents within an org should sum to ~100, got %v", pct)
	}
}

func TestAggregateConsumption_EmptyEstateNeverBlank(t *testing.T) {
	// Zero rows ⇒ still exactly one parent row (the §5 never-blank rule).
	resp := aggregateConsumption(nil, "sovereign", testInfraSet())
	if len(resp.Orgs) != 1 {
		t.Fatalf("empty estate must still render the parent row, got %d orgs", len(resp.Orgs))
	}
	if !resp.Orgs[0].IsParent {
		t.Errorf("the lone row must be the parent")
	}
	if resp.Orgs[0].Apps == nil {
		t.Errorf("apps must be a non-nil slice so the page renders []")
	}
}

func TestOrgForRow_KeysOnLabelNotName(t *testing.T) {
	infra := testInfraSet()
	cases := []struct {
		name string
		row  podRow
		want string
	}{
		{"labelled tenant", podRow{namespace: "acme", org: "acme", ownerKind: "Deployment"}, "acme"},
		{"infra namespace", podRow{namespace: "kube-system", org: "", ownerKind: "DaemonSet"}, platformOrg},
		{"job-owned in tenant ns", podRow{namespace: "acme", org: "acme", ownerKind: "Job"}, platformOrg},
		{"unlabelled host pod", podRow{namespace: "some-host-ns", org: "", ownerKind: "Deployment"}, platformOrg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orgForRow(tc.row, infra); got != tc.want {
				t.Errorf("orgForRow(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestInfraNamespaceSet_EnvOverrideExtendsFloor(t *testing.T) {
	set := infraNamespaceSet("my-infra, another-infra ")
	for _, ns := range []string{"kube-system", "flux-system", "my-infra", "another-infra"} {
		if _, ok := set[ns]; !ok {
			t.Errorf("infra set missing %q (floor + env override)", ns)
		}
	}
	if _, ok := set["acme"]; ok {
		t.Errorf("a real tenant namespace must NOT be in the infra set")
	}
}
