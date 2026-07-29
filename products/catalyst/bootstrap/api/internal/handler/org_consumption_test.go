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

/* ── #5485 defect A — one-shot Jobs collapse to ONE overhead line ────── */

// TestAggregateConsumption_OneShotJobsCollapseToASingleRow — #5485
// defect A. orgForRow already routed every Job pod to the platform
// bucket, but nothing collapsed them, so the __platform__ Application
// table itemized one row per Job pod:
// `cutover-harbor-prewarm-1785340840`, `cutover-gitea-mirror-…`,
// `cutover-harbor-projects-…`, `openbao-snapshot-save-29755690`,
// `legacy-cert-cleanup`, `cert-nextkey-guard` — contradicting this
// handler's own "single Platform overhead line" contract. They now fold
// into one row, WITHOUT dropping their cost.
func TestAggregateConsumption_OneShotJobsCollapseToASingleRow(t *testing.T) {
	const parent = "hw291.omani.works"
	rows := []podRow{
		// Durable platform workloads — must stay individually itemized.
		{namespace: "catalyst-system", application: "catalyst-api", ownerKind: "ReplicaSet", cpuReq: 500, memReq: 1 << 30},
		{namespace: "kube-system", application: "cilium", ownerKind: "DaemonSet", cpuReq: 100, memReq: 128 << 20},
		// The six live one-shot Job pods from the #5485 report.
		{namespace: "catalyst-system", application: "cutover-harbor-prewarm-1785340840", ownerKind: "Job", cpuReq: 10, memReq: 32 << 20},
		{namespace: "catalyst-system", application: "cutover-gitea-mirror-1785340111", ownerKind: "Job", cpuReq: 10, memReq: 32 << 20},
		{namespace: "catalyst-system", application: "cutover-harbor-projects-1785340222", ownerKind: "Job", cpuReq: 10, memReq: 32 << 20},
		{namespace: "openbao", application: "openbao-snapshot-save-29755690", ownerKind: "Job", cpuReq: 10, memReq: 32 << 20},
		{namespace: "cert-manager", application: "legacy-cert-cleanup", ownerKind: "Job", cpuReq: 10, memReq: 32 << 20},
		{namespace: "cert-manager", application: "cert-nextkey-guard", ownerKind: "Job", cpuReq: 10, memReq: 32 << 20},
	}

	resp := aggregateConsumption(rows, parent, testInfraSet())

	platform := resp.Orgs[len(resp.Orgs)-1]
	if !platform.IsPlatform {
		t.Fatalf("last row must be the platform-overhead rollup, got %#v", resp.Orgs)
	}

	// Not one row per Job pod: exactly ONE collapsed activity line.
	ephemeralRows := 0
	for _, a := range platform.Apps {
		if a.Application == ephemeralApp {
			ephemeralRows++
		}
		for _, jobName := range []string{
			"cutover-harbor-prewarm-1785340840",
			"cutover-gitea-mirror-1785340111",
			"cutover-harbor-projects-1785340222",
			"openbao-snapshot-save-29755690",
			"legacy-cert-cleanup",
			"cert-nextkey-guard",
		} {
			if a.Application == jobName {
				t.Errorf("one-shot Job pod itemized as its own Application row: %q", jobName)
			}
		}
	}
	if ephemeralRows != 1 {
		t.Errorf("expected exactly 1 collapsed %q row, got %d (apps=%#v)", ephemeralApp, ephemeralRows, platform.Apps)
	}

	// The collapse spans namespaces, so the row says so rather than
	// claiming one namespace owns all six Jobs.
	var collapsed *appConsumption
	for i := range platform.Apps {
		if platform.Apps[i].Application == ephemeralApp {
			collapsed = &platform.Apps[i]
		}
	}
	// NOTE: no t.Fatalf below this point — a bail here would skip the
	// vacuity assertions that follow, which is exactly how a
	// drop-everything "fix" sneaks through a green run.
	if collapsed == nil {
		t.Errorf("no collapsed one-shot row at all — apps=%#v", platform.Apps)
	} else {
		if collapsed.Namespace != ephemeralMixedNamespace {
			t.Errorf("collapsed row Namespace = %q, want %q (Jobs span catalyst-system/openbao/cert-manager)",
				collapsed.Namespace, ephemeralMixedNamespace)
		}
		// Cost is COLLAPSED, not discarded: all six Job pods' CPU lands
		// on the one row.
		if collapsed.CPUMilli != 60 {
			t.Errorf("collapsed row CPUMilli = %v, want 60 (six Job pods folded)", collapsed.CPUMilli)
		}
	}
	// The platform total still carries every pod, collapsed or not.
	if platform.CPUMilli != 660 {
		t.Errorf("platform CPUMilli = %v, want 660 (durable 600 + one-shot 60) — the fix must collapse, not drop", platform.CPUMilli)
	}

	/* ── Vacuity control ────────────────────────────────────────────
	   A "fix" that filtered every row out (or emptied Apps) would also
	   produce "no Job pod rows" while destroying showback. Assert the
	   durable platform workloads are STILL itemized, the totals are
	   non-zero, and per-app percents still sum to ~100 within the org. */
	byApp := map[string]float64{}
	for _, a := range platform.Apps {
		byApp[a.Application] = a.CPUMilli
	}
	if byApp["catalyst-api"] != 500 {
		t.Errorf("durable app catalyst-api lost its row/cost: CPUMilli=%v, want 500", byApp["catalyst-api"])
	}
	if byApp["cilium"] != 100 {
		t.Errorf("durable app cilium lost its row/cost: CPUMilli=%v, want 100", byApp["cilium"])
	}
	if len(platform.Apps) != 3 {
		t.Errorf("expected 3 platform app rows (catalyst-api + cilium + one collapsed activity line), got %d: %#v",
			len(platform.Apps), platform.Apps)
	}
	if resp.TotalCostUnits <= 0 || platform.CostUnits <= 0 {
		t.Errorf("showback totals collapsed to zero: total=%v platform=%v", resp.TotalCostUnits, platform.CostUnits)
	}
	var pct float64
	for _, a := range platform.Apps {
		pct += a.Percent
	}
	if math.Abs(pct-100) > 0.5 {
		t.Errorf("per-app percents within the platform org must still sum to ~100, got %v", pct)
	}
}

// TestAggregateConsumption_SingleNamespaceCollapseKeepsRealNamespace —
// when every one-shot Job ran in the SAME namespace the collapsed row
// keeps that namespace instead of the "(various)" marker, so the
// operator still sees where the activity happened.
func TestAggregateConsumption_SingleNamespaceCollapseKeepsRealNamespace(t *testing.T) {
	resp := aggregateConsumption([]podRow{
		{namespace: "catalyst-system", application: "cutover-harbor-prewarm-1785340840", ownerKind: "Job", cpuReq: 10},
		{namespace: "catalyst-system", application: "cutover-gitea-mirror-1785340111", ownerKind: "Job", cpuReq: 20},
	}, "hw291.omani.works", testInfraSet())

	platform := resp.Orgs[len(resp.Orgs)-1]
	if !platform.IsPlatform || len(platform.Apps) != 1 {
		t.Fatalf("expected one collapsed row on the platform org, got %#v", platform)
	}
	if got := platform.Apps[0]; got.Application != ephemeralApp || got.Namespace != "catalyst-system" || got.CPUMilli != 30 {
		t.Errorf("collapsed row = %+v, want {%s catalyst-system 30m}", got, ephemeralApp)
	}
}

// TestAggregateConsumption_TenantAppsNeverCollapse — the collapse is
// scoped to one-shot Job pods. A tenant's durable Applications keep their
// own per-app rows (the DoD 3 "per-app cost attribution" contract).
func TestAggregateConsumption_TenantAppsNeverCollapse(t *testing.T) {
	resp := aggregateConsumption([]podRow{
		{namespace: "acme", application: "blog", org: "acme", ownerKind: "StatefulSet", cpuReq: 200},
		{namespace: "acme", application: "api", org: "acme", ownerKind: "Deployment", cpuReq: 300},
		{namespace: "acme", application: "shop", org: "acme", ownerKind: "Deployment", cpuReq: 100},
	}, "hw291.omani.works", testInfraSet())

	var acme *orgConsumption
	for i := range resp.Orgs {
		if resp.Orgs[i].Org == "acme" {
			acme = &resp.Orgs[i]
		}
	}
	if acme == nil {
		t.Fatal("acme org row missing")
	}
	if len(acme.Apps) != 3 {
		t.Errorf("tenant apps must stay itemized: got %d rows, want 3 (%#v)", len(acme.Apps), acme.Apps)
	}
	for _, a := range acme.Apps {
		if a.Application == ephemeralApp {
			t.Errorf("a tenant's durable app was folded into the one-shot row: %#v", acme.Apps)
		}
	}
}
