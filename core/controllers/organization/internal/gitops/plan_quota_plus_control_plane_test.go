// plan_quota_plus_control_plane_test.go — the host-namespace ResourceQuota is
// the purchased plan PLUS the vCluster control-plane overhead (#6902 follow-up,
// Refs #4292 #6867).
//
// Three properties, each with its own vacuity guard:
//
//  1. PIN — vclusterControlPlaneOverhead equals what the RENDERED vcluster
//     HelmRelease actually asks for. The test parses vcluster.yaml, walks every
//     `resources` block under spec.values, refuses any block it cannot classify
//     (a new sidecar must be accounted for explicitly), and recomputes the
//     overhead with the ResourceQuota pod-usage rule. It also pins the derived
//     figures by value, because those figures are restated in the canonical
//     docs and a silent move here would strand them.
//  2. TABLE — for EVERY hard-capped slug in planQuotaTable the rendered hard
//     cap equals plan + overhead exactly, per resource, and is strictly above
//     the plan (so an overhead that collapses to zero is caught, not merely a
//     wrong non-zero one). Flexi stays quota-less.
//  3. LIMITRANGE — the per-container defaults are plan-only and unchanged by
//     the overhead.
package gitops

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// wantControlPlaneOverhead is the derived figure, pinned. It is restated in
// docs/SYSTEM-DESIGN.md and products/chargeback/DESIGN.md; if the render moves,
// this fails first and names the doc lines that need to move with it.
var wantControlPlaneOverhead = map[string]string{
	"requests.cpu":    "520m",   // syncer 500m + coredns 20m
	"requests.memory": "1088Mi", // syncer 1Gi + coredns 64Mi
	"limits.cpu":      "1500m",  // syncer 500m + coredns 1000m
	"limits.memory":   "1194Mi", // syncer 1Gi + coredns 170Mi
	"storage":         "5Gi",    // data-vcluster-0 volume claim
}

func renderPlan(t *testing.T, plan string) map[string][]byte {
	t.Helper()
	out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org", PlanSlug: plan,
		SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"})
	if err != nil {
		t.Fatalf("Render(plan=%q): %v", plan, err)
	}
	return out
}

func mustQ(t *testing.T, what, s string) resource.Quantity {
	t.Helper()
	q, err := resource.ParseQuantity(s)
	if err != nil {
		t.Fatalf("%s: %q is not a resource quantity: %v", what, s, err)
	}
	return q
}

// collectResourceBlocks walks the parsed HelmRelease values and records every
// map under a `resources` key that carries requests or limits, by dotted path.
func collectResourceBlocks(node any, path string, into map[string]map[string]any) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	for k, v := range m {
		p := k
		if path != "" {
			p = path + "." + k
		}
		if k == "resources" {
			if rm, ok := v.(map[string]any); ok {
				_, hasReq := rm["requests"]
				_, hasLim := rm["limits"]
				if hasReq || hasLim {
					into[p] = rm
				}
			}
		}
		collectResourceBlocks(v, p, into)
	}
}

// TestVClusterControlPlaneOverhead_PinnedToRenderedValues is property 1.
func TestVClusterControlPlaneOverhead_PinnedToRenderedValues(t *testing.T) {
	t.Parallel()
	out := renderPlan(t, "m")
	var hr struct {
		Spec struct {
			Values map[string]any `json:"values"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(out["vcluster/vcluster.yaml"], &hr); err != nil {
		t.Fatalf("vcluster.yaml did not parse: %v", err)
	}
	if len(hr.Spec.Values) == 0 {
		t.Fatal("vacuity: vcluster.yaml has no spec.values — nothing to derive the overhead from")
	}

	blocks := map[string]map[string]any{}
	collectResourceBlocks(hr.Spec.Values, "", blocks)

	// Every resources block the render carries must be classified below. An
	// unknown block is a container the quota will charge that the overhead
	// does not account for — exactly the drift this test exists to catch.
	const (
		syncerPath  = "controlPlane.statefulSet.resources"
		distroPath  = "controlPlane.distro.k8s.resources"
		corednsPath = "controlPlane.coredns.deployment.resources"
	)
	known := map[string]string{
		syncerPath:  "vcluster-0 app container (syncer)",
		distroPath:  "vcluster-0 init container (k8s distro)",
		corednsPath: "coredns pod, synced into the host namespace",
	}
	var paths []string
	for p := range blocks {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) < len(known) {
		t.Fatalf("vacuity: only %d resources blocks rendered (%v) — the overhead derivation would be incomplete; want at least %d", len(paths), paths, len(known))
	}
	for _, p := range paths {
		if _, ok := known[p]; !ok {
			t.Errorf("unclassified resources block at spec.values.%s — a container the ResourceQuota charges but vclusterControlPlaneOverhead does not account for; classify it in controlPlaneOverheadOf and here", p)
		}
	}
	for p, role := range known {
		if _, ok := blocks[p]; !ok {
			t.Fatalf("expected resources block %s (%s) is not rendered — the template no longer interpolates it and the overhead is derived from nothing", p, role)
		}
	}

	get := func(path, kind, res string) resource.Quantity {
		t.Helper()
		k, ok := blocks[path][kind].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s missing", path, kind)
		}
		v, ok := k[res]
		if !ok {
			t.Fatalf("%s.%s.%s missing", path, kind, res)
		}
		return mustQ(t, path+"."+kind+"."+res, fmt.Sprint(v))
	}
	maxQ := func(a, b resource.Quantity) resource.Quantity {
		if b.Cmp(a) > 0 {
			return b
		}
		return a
	}
	// The ResourceQuota pod-usage rule: vcluster-0 = max(app container, init
	// container) per resource; coredns is its own pod and adds in full.
	derive := func(kind, res string) resource.Quantity {
		vc0 := maxQ(get(syncerPath, kind, res), get(distroPath, kind, res))
		vc0.Add(get(corednsPath, kind, res))
		return vc0
	}
	got := map[string]resource.Quantity{
		"requests.cpu":    vclusterControlPlaneOverhead.RequestsCPU,
		"requests.memory": vclusterControlPlaneOverhead.RequestsMemory,
		"limits.cpu":      vclusterControlPlaneOverhead.LimitsCPU,
		"limits.memory":   vclusterControlPlaneOverhead.LimitsMemory,
		"storage":         vclusterControlPlaneOverhead.Storage,
	}
	derived := map[string]resource.Quantity{
		"requests.cpu":    derive("requests", "cpu"),
		"requests.memory": derive("requests", "memory"),
		"limits.cpu":      derive("limits", "cpu"),
		"limits.memory":   derive("limits", "memory"),
	}
	// Storage: the backing-store volume claim.
	cp, _ := hr.Spec.Values["controlPlane"].(map[string]any)
	ss, _ := cp["statefulSet"].(map[string]any)
	pers, _ := ss["persistence"].(map[string]any)
	vcl, _ := pers["volumeClaim"].(map[string]any)
	size, ok := vcl["size"]
	if !ok {
		t.Fatal("controlPlane.statefulSet.persistence.volumeClaim.size not rendered — the storage overhead is derived from nothing")
	}
	derived["storage"] = mustQ(t, "volumeClaim.size", fmt.Sprint(size))

	for res, d := range derived {
		g := got[res]
		if g.IsZero() {
			t.Errorf("vacuity: vclusterControlPlaneOverhead %s is zero — the quota would be the bare plan again", res)
		}
		if g.Cmp(d) != 0 {
			t.Errorf("vclusterControlPlaneOverhead %s = %s but the rendered HelmRelease derives %s — the constant and the render drifted apart", res, g.String(), d.String())
		}
		want := mustQ(t, "wantControlPlaneOverhead."+res, wantControlPlaneOverhead[res])
		if d.Cmp(want) != 0 {
			t.Errorf("derived control-plane overhead %s = %s, pinned figure is %s — the render moved; update wantControlPlaneOverhead AND the restated figures in docs/SYSTEM-DESIGN.md and products/chargeback/DESIGN.md", res, d.String(), want.String())
		}
	}

	// The operator-facing annotation must carry the same figures, or the live
	// object explains a split that is not the one enforced.
	rq := string(renderPlan(t, "s")["vcluster/resourcequota.yaml"])
	for res, v := range wantControlPlaneOverhead {
		if !strings.Contains(rq, v) {
			t.Errorf("resourcequota.yaml does not carry the %s overhead figure %q anywhere (annotation or comment):\n%s", res, v, rq)
		}
	}
	if !strings.Contains(rq, "openova.io/vcluster-control-plane-overhead: "+fmt.Sprintf("%q", vclusterControlPlaneOverhead.String())) {
		t.Errorf("resourcequota.yaml annotation openova.io/vcluster-control-plane-overhead does not equal vclusterControlPlaneOverhead.String() = %q:\n%s", vclusterControlPlaneOverhead.String(), rq)
	}
}

// TestControlPlaneOverheadOf_PodUsageRule proves the arithmetic on a synthetic
// shape where each branch of the rule is load-bearing: the init container
// outweighs the app container on CPU (init wins), the app container outweighs
// it on memory (app wins), and coredns adds in full on both.
func TestControlPlaneOverheadOf_PodUsageRule(t *testing.T) {
	t.Parallel()
	o := controlPlaneOverheadOf(vclusterControlPlaneShape{
		Syncer:     containerShape{RequestsCPU: "100m", RequestsMemory: "1Gi", LimitsCPU: "200m", LimitsMemory: "2Gi"},
		Distro:     containerShape{RequestsCPU: "300m", RequestsMemory: "256Mi", LimitsCPU: "300m", LimitsMemory: "256Mi"},
		CoreDNS:    containerShape{RequestsCPU: "50m", RequestsMemory: "64Mi", LimitsCPU: "1", LimitsMemory: "170Mi"},
		VolumeSize: "7Gi",
	})
	want := map[string]string{
		"requests.cpu":    "350m",   // max(100m, 300m) + 50m
		"requests.memory": "1088Mi", // max(1Gi, 256Mi) + 64Mi
		"limits.cpu":      "1300m",  // max(200m, 300m) + 1
		"limits.memory":   "2218Mi", // max(2Gi, 256Mi) + 170Mi
		"storage":         "7Gi",
	}
	got := map[string]resource.Quantity{
		"requests.cpu": o.RequestsCPU, "requests.memory": o.RequestsMemory,
		"limits.cpu": o.LimitsCPU, "limits.memory": o.LimitsMemory, "storage": o.Storage,
	}
	for res, w := range want {
		if g := got[res]; g.Cmp(mustQ(t, res, w)) != 0 {
			t.Errorf("%s = %s, want %s", res, g.String(), w)
		}
	}
	// Millicore / byte views the report and docs quote.
	if vclusterControlPlaneOverhead.RequestsCPU.MilliValue() != 520 {
		t.Errorf("requests.cpu = %dm, want 520m", vclusterControlPlaneOverhead.RequestsCPU.MilliValue())
	}
	if vclusterControlPlaneOverhead.RequestsMemory.Value() != 1088*1024*1024 {
		t.Errorf("requests.memory = %d bytes, want %d (1088Mi)", vclusterControlPlaneOverhead.RequestsMemory.Value(), 1088*1024*1024)
	}
}

// TestRender_ResourceQuotaIsPlanPlusControlPlaneOverhead is property 2, driven
// off planQuotaTable itself so a new plan is covered the moment it is added.
func TestRender_ResourceQuotaIsPlanPlusControlPlaneOverhead(t *testing.T) {
	t.Parallel()
	if len(planQuotaTable) == 0 {
		t.Fatal("vacuity: planQuotaTable is empty — the per-plan loop would assert nothing")
	}
	o := vclusterControlPlaneOverhead
	for res, q := range map[string]resource.Quantity{
		"requests.cpu": o.RequestsCPU, "requests.memory": o.RequestsMemory,
		"limits.cpu": o.LimitsCPU, "limits.memory": o.LimitsMemory,
	} {
		if q.IsZero() {
			t.Fatalf("vacuity: overhead %s is zero — plan + overhead would equal the plan and this test could not tell them apart", res)
		}
	}

	// The table's own slugs plus the inputs planQuota resolves to "s": an
	// empty legacy slug, an unknown one, and a case variant.
	slugs := []string{"", "bogus", "S"}
	for slug := range planQuotaTable {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	hardCapped := 0
	for _, slug := range slugs {
		q := planQuota(slug)
		out := renderPlan(t, slug)
		raw, ok := out["vcluster/resourcequota.yaml"]
		if q.Burstable {
			if ok {
				t.Errorf("plan %q is Burstable (Flexi, pay per use) and must render NO ResourceQuota — overhead does not change that", slug)
			}
			continue
		}
		if !ok {
			t.Fatalf("plan %q: missing resourcequota.yaml", slug)
		}
		hardCapped++

		var rq struct {
			Metadata struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Hard map[string]string `json:"hard"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(raw, &rq); err != nil {
			t.Fatalf("plan %q: resourcequota.yaml did not parse: %v\n%s", slug, err, raw)
		}

		for res, tc := range map[string]struct {
			plan     string
			overhead resource.Quantity
		}{
			"requests.cpu":    {q.CPU, o.RequestsCPU},
			"requests.memory": {q.Mem, o.RequestsMemory},
			"limits.cpu":      {q.CPU, o.LimitsCPU},
			"limits.memory":   {q.Mem, o.LimitsMemory},
		} {
			hard, ok := rq.Spec.Hard[res]
			if !ok {
				t.Errorf("plan %q: spec.hard has no %s", slug, res)
				continue
			}
			got := mustQ(t, slug+" hard "+res, hard)
			plan := mustQ(t, slug+" plan "+res, tc.plan)
			want := plan.DeepCopy()
			want.Add(tc.overhead)
			if got.Cmp(want) != 0 {
				t.Errorf("plan %q: hard %s = %s, want plan %s + control plane %s = %s",
					slug, res, got.String(), plan.String(), tc.overhead.String(), want.String())
			}
			// Control: strictly above the plan. A zero overhead would satisfy
			// the equality above only because the vacuity guard already
			// refused it; this makes the property visible per row.
			if got.Cmp(plan) <= 0 {
				t.Errorf("plan %q: hard %s = %s is not strictly above the purchased plan %s — the control plane is eating the customer's plan again",
					slug, res, got.String(), plan.String())
			}
		}
		if len(rq.Spec.Hard) != 4 {
			t.Errorf("plan %q: spec.hard has %d keys %v, want exactly requests/limits × cpu/memory (no storage quota is rendered; the plan has no storage figure)",
				slug, len(rq.Spec.Hard), rq.Spec.Hard)
		}

		// The split is stamped on the object.
		if got, want := rq.Metadata.Annotations["openova.io/plan-cap"], fmt.Sprintf("cpu=%s memory=%s", q.CPU, q.Mem); got != want {
			t.Errorf("plan %q: annotation openova.io/plan-cap = %q, want %q", slug, got, want)
		}
		if got := rq.Metadata.Annotations["openova.io/vcluster-control-plane-overhead"]; got != o.String() {
			t.Errorf("plan %q: annotation openova.io/vcluster-control-plane-overhead = %q, want %q", slug, got, o.String())
		}
		if rq.Metadata.Annotations["openova.io/quota-formula"] == "" {
			t.Errorf("plan %q: annotation openova.io/quota-formula missing", slug)
		}
		if got := rq.Metadata.Labels["openova.io/plan"]; got != slug {
			t.Errorf("plan %q: label openova.io/plan = %q (the raw input slug is what the walker filters by)", slug, got)
		}
	}
	if hardCapped < 4 {
		t.Fatalf("vacuity: only %d hard-capped plans walked — the table should carry at least s/m/l/xl", hardCapped)
	}
}

// TestRender_LimitRangeDefaultsUnchangedByOverhead is property 3: the
// per-container defaultRequest/default stay plan-only (plan / 8 for fixed
// tiers, the small floor for Flexi) and never absorb the control plane.
func TestRender_LimitRangeDefaultsUnchangedByOverhead(t *testing.T) {
	t.Parallel()
	want := map[string]struct{ cpu, mem string }{
		"s": {"250m", "512Mi"}, "m": {"500m", "1Gi"}, "l": {"1", "2Gi"}, "xl": {"2", "4Gi"},
		"flexi": {"100m", "128Mi"},
	}
	if len(want) != len(planQuotaTable) {
		t.Fatalf("vacuity: %d plans pinned here, planQuotaTable has %d — pin the new plan's LimitRange defaults", len(want), len(planQuotaTable))
	}
	for slug, w := range want {
		raw, ok := renderPlan(t, slug)["vcluster/limitrange.yaml"]
		if !ok {
			t.Fatalf("plan %q: missing limitrange.yaml", slug)
		}
		var lr struct {
			Spec struct {
				Limits []struct {
					Type           string            `json:"type"`
					DefaultRequest map[string]string `json:"defaultRequest"`
					Default        map[string]string `json:"default"`
					MaxRatio       map[string]string `json:"maxLimitRequestRatio"`
				} `json:"limits"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(raw, &lr); err != nil {
			t.Fatalf("plan %q: limitrange.yaml did not parse: %v", slug, err)
		}
		if len(lr.Spec.Limits) != 1 || lr.Spec.Limits[0].Type != "Container" {
			t.Fatalf("plan %q: want exactly one Container limit, got %+v", slug, lr.Spec.Limits)
		}
		l := lr.Spec.Limits[0]
		for _, m := range []map[string]string{l.DefaultRequest, l.Default} {
			gotCPU, wantCPU := mustQ(t, slug, m["cpu"]), mustQ(t, slug, w.cpu)
			gotMem, wantMem := mustQ(t, slug, m["memory"]), mustQ(t, slug, w.mem)
			if gotCPU.Cmp(wantCPU) != 0 || gotMem.Cmp(wantMem) != 0 {
				t.Errorf("plan %q: LimitRange defaults = cpu %s / memory %s, want %s / %s (plan-only; the control-plane overhead must not leak in)",
					slug, m["cpu"], m["memory"], w.cpu, w.mem)
			}
		}
		if len(l.MaxRatio) != 0 {
			t.Errorf("plan %q: maxLimitRequestRatio must stay absent (#4758)", slug)
		}
	}
}
