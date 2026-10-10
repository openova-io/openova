// plan_quota_overage_mode_test.go — the overage mode (founder model,
// 2026-10-10), proven on the rendered objects.
//
// A package is a prepaid commitment whose headline is the ResourceQuota LIMIT.
// spec.commerce.overageMode chooses what happens above it:
//
//   - capped (default, also empty): limits = headline — the pre-overage
//     render, with the mode stated as a comment line and an annotation;
//   - grow: limits = the grow ceiling (default the XL headline, clamped to
//     [plan headline, XL headline]); requests unchanged; overheads unchanged;
//     no storage cap in either mode.
package gitops

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

func renderOverage(t *testing.T, plan, mode, cpu, mem string) map[string][]byte {
	t.Helper()
	out, err := Render(Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org", PlanSlug: plan,
		SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*",
		OverageMode: mode, GrowCeilingCPU: cpu, GrowCeilingMemory: mem})
	if err != nil {
		t.Fatalf("Render(plan=%q mode=%q): %v", plan, mode, err)
	}
	return out
}

type rqDoc struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Hard map[string]string `json:"hard"`
	} `json:"spec"`
}

type lrDoc struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Limits []struct {
			DefaultRequest map[string]string `json:"defaultRequest"`
			Default        map[string]string `json:"default"`
		} `json:"limits"`
	} `json:"spec"`
}

func parseRQ(t *testing.T, out map[string][]byte) rqDoc {
	t.Helper()
	var d rqDoc
	b, ok := out["vcluster/resourcequota.yaml"]
	if !ok {
		t.Fatal("no vcluster/resourcequota.yaml rendered")
	}
	if err := yaml.Unmarshal(b, &d); err != nil {
		t.Fatalf("resourcequota.yaml: %v", err)
	}
	return d
}

func parseLR(t *testing.T, out map[string][]byte) lrDoc {
	t.Helper()
	var d lrDoc
	if err := yaml.Unmarshal(out["vcluster/limitrange.yaml"], &d); err != nil {
		t.Fatalf("limitrange.yaml: %v", err)
	}
	return d
}

// The capped render is the pre-overage render. testdata/capped_pre_overage
// holds the ResourceQuota + LimitRange this renderer produced immediately
// before the mode existed; the capped render must equal it once the two lines
// the mode adds (the comment line and the annotation) are removed — so the
// hard cap, the defaults and every other annotation are byte-identical. Empty
// and "capped" (and an unknown mode) render byte-identically to each other
// across EVERY file.
func TestRender_CappedIsThePreOverageRender(t *testing.T) {
	t.Parallel()
	added := map[string]bool{
		"#   overage mode: capped":              true,
		`    openova.io/overage-mode: "capped"`: true,
	}
	strip := func(b []byte) []byte {
		var keep []string
		for _, l := range strings.Split(string(b), "\n") {
			if !added[l] {
				keep = append(keep, l)
			}
		}
		return []byte(strings.Join(keep, "\n"))
	}
	for _, plan := range []string{"s", "m", "l", "xl", "flexi"} {
		empty := renderOverage(t, plan, "", "", "")
		for _, mode := range []string{OverageModeCapped, "CAPPED-typo"} {
			other := renderOverage(t, plan, mode, "4", "8Gi")
			if len(other) != len(empty) {
				t.Fatalf("plan %s mode %q: %d files, empty mode %d", plan, mode, len(other), len(empty))
			}
			for k, v := range empty {
				if !bytes.Equal(v, other[k]) {
					t.Errorf("plan %s: %s differs between mode \"\" and %q", plan, k, mode)
				}
			}
		}
		for _, f := range []string{"limitrange", "resourcequota"} {
			golden := filepath.Join("testdata", "capped_pre_overage", plan+"-"+f+".yaml")
			want, err := os.ReadFile(golden)
			got, rendered := empty["vcluster/"+f+".yaml"]
			if os.IsNotExist(err) {
				if rendered {
					t.Errorf("plan %s renders %s but had none before the overage mode", plan, f)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan == "flexi" {
				// Burstable: no quota, so no mode — literally unchanged.
				if !bytes.Equal(got, want) {
					t.Errorf("plan flexi %s changed:\n%s", f, got)
				}
				continue
			}
			if !bytes.Contains(got, []byte(`openova.io/overage-mode: "capped"`)) {
				t.Errorf("plan %s %s lacks openova.io/overage-mode: \"capped\"", plan, f)
			}
			if bytes.Contains(got, []byte("grow-ceiling")) {
				t.Errorf("plan %s capped %s carries a grow ceiling", plan, f)
			}
			if s := strip(got); !bytes.Equal(s, want) {
				t.Errorf("plan %s capped %s is not the pre-overage render (minus the mode lines):\n--- got\n%s\n--- want\n%s", plan, f, s, want)
			}
		}
	}
}

// Plan M, grow, default ceiling: limits = the XL headline + the same overheads
// capped M adds; requests identical to capped M. Every figure is derived from
// the table and the overhead values, then cross-checked against the literal.
func TestRender_PlanM_GrowDefaultCeilingIsTheXLHeadline(t *testing.T) {
	t.Parallel()
	q, xl := planQuota("m"), planQuotaTable[growCeilingPlan]
	cp, ps := vclusterControlPlaneOverhead, platformStackOverheadFor(q)
	sum := func(plan string, over resource.Quantity) string {
		s := mustQ(t, "m", plan)
		s.Add(over)
		return s.String()
	}
	grow := parseRQ(t, renderOverage(t, "m", OverageModeGrow, "", ""))
	capped := parseRQ(t, renderOverage(t, "m", OverageModeCapped, "", ""))
	for _, c := range []struct{ key, want, literal string }{
		{"limits.cpu", sum(xl.CPULimit, cpPlus(cp.LimitsCPU, ps.LimitsCPU)), "14050m"},
		{"limits.memory", sum(xl.MemLimit, cpPlus(cp.LimitsMemory, ps.LimitsMemory)), "24746Mi"},
	} {
		if got := grow.Spec.Hard[c.key]; got != c.want || got != c.literal {
			t.Errorf("grow m %s = %s, want %s (literal %s: 8 + 1500m + 4550m / 16Gi + 1194Mi + 7168Mi)", c.key, got, c.want, c.literal)
		}
	}
	for _, k := range []string{"requests.cpu", "requests.memory"} {
		if grow.Spec.Hard[k] != capped.Spec.Hard[k] {
			t.Errorf("grow m %s = %s, want capped's %s (the guaranteed share does not move)", k, grow.Spec.Hard[k], capped.Spec.Hard[k])
		}
	}
	if len(grow.Spec.Hard) != 4 {
		t.Errorf("grow m hard = %v, want exactly the four cpu/memory keys (no storage cap in either mode)", grow.Spec.Hard)
	}
	for k, want := range map[string]string{
		"openova.io/overage-mode": "grow",
		"openova.io/grow-ceiling": "cpu=8 memory=16Gi",
		"openova.io/plan-cap":     "cpu=2 memory=4Gi", // the headline the customer bought
	} {
		if got := grow.Metadata.Annotations[k]; got != want {
			t.Errorf("grow m ResourceQuota annotation %s = %q, want %q", k, got, want)
		}
	}
}

// Plan M, grow, a chosen ceiling of 4 vCPU / 8Gi (the L shape).
func TestRender_PlanM_GrowChosenCeiling(t *testing.T) {
	t.Parallel()
	q := planQuota("m")
	cp, ps := vclusterControlPlaneOverhead, platformStackOverheadFor(q)
	sum := func(plan string, over resource.Quantity) string {
		s := mustQ(t, "m", plan)
		s.Add(over)
		return s.String()
	}
	out := renderOverage(t, "m", OverageModeGrow, "4", "8Gi")
	rq := parseRQ(t, out)
	if got, want := rq.Spec.Hard["limits.cpu"], sum("4", cpPlus(cp.LimitsCPU, ps.LimitsCPU)); got != want || got != "10050m" {
		t.Errorf("limits.cpu = %s, want %s (10050m)", got, want)
	}
	if got, want := rq.Spec.Hard["limits.memory"], sum("8Gi", cpPlus(cp.LimitsMemory, ps.LimitsMemory)); got != want || got != "16554Mi" {
		t.Errorf("limits.memory = %s, want %s (16554Mi)", got, want)
	}
	if got := rq.Metadata.Annotations["openova.io/grow-ceiling"]; got != "cpu=4 memory=8Gi" {
		t.Errorf("grow-ceiling annotation = %q", got)
	}
	if !bytes.Contains(out["vcluster/resourcequota.yaml"], []byte("#   grow ceiling (limits, in place of the headline): cpu=4 memory=8Gi")) {
		t.Errorf("ResourceQuota comment block does not state the ceiling:\n%s", out["vcluster/resourcequota.yaml"])
	}
	// The LimitRange carries the mode + ceiling, but its per-container
	// defaults stay HEADLINE-derived (M: 42m→250m / 171Mi→512Mi).
	lr := parseLR(t, out)
	capLR := parseLR(t, renderOverage(t, "m", "", "", ""))
	if lr.Metadata.Annotations["openova.io/overage-mode"] != "grow" || lr.Metadata.Annotations["openova.io/grow-ceiling"] != "cpu=4 memory=8Gi" {
		t.Errorf("LimitRange annotations = %v", lr.Metadata.Annotations)
	}
	if len(lr.Spec.Limits) != 1 || len(capLR.Spec.Limits) != 1 {
		t.Fatal("want one Container limit")
	}
	for _, k := range []string{"cpu", "memory"} {
		if lr.Spec.Limits[0].Default[k] != capLR.Spec.Limits[0].Default[k] ||
			lr.Spec.Limits[0].DefaultRequest[k] != capLR.Spec.Limits[0].DefaultRequest[k] {
			t.Errorf("grow moved the LimitRange %s defaults: %+v vs capped %+v", k, lr.Spec.Limits[0], capLR.Spec.Limits[0])
		}
	}
}

// The clamps: below the plan headline → the headline; above the XL headline
// → the XL headline; empty / unparseable / non-positive → the XL headline.
// Each resource clamps on its own.
func TestQuotaLimitsFor_Clamps(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, plan, mode, cpu, mem string
		wantCPU, wantMem           string
		wantGrow                   bool
	}{
		{"capped is the headline", "m", OverageModeCapped, "8", "16Gi", "2", "4Gi", false},
		{"empty mode is capped", "l", "", "8", "16Gi", "4", "8Gi", false},
		{"grow default", "s", OverageModeGrow, "", "", "8", "16Gi", true},
		{"grow chosen", "m", OverageModeGrow, "3", "6Gi", "3", "6Gi", true},
		{"below headline raises", "l", OverageModeGrow, "2", "4Gi", "4", "8Gi", true},
		{"above XL lowers", "m", OverageModeGrow, "16", "64Gi", "8", "16Gi", true},
		{"per resource", "m", OverageModeGrow, "1", "32Gi", "2", "16Gi", true},
		{"fractional vcpu", "m", OverageModeGrow, "2.5", "5120Mi", "2500m", "5Gi", true},
		{"unparseable / zero default", "m", OverageModeGrow, "lots", "0", "8", "16Gi", true},
		{"xl grows to twice xl by default", "xl", OverageModeGrow, "", "", "16", "32Gi", true},
		{"xl chosen", "xl", OverageModeGrow, "12", "24Gi", "12", "24Gi", true},
		{"xl above twice xl lowers", "xl", OverageModeGrow, "64", "128Gi", "16", "32Gi", true},
		{"flexi has no limits term", "flexi", OverageModeGrow, "4", "8Gi", "", "", false},
	} {
		cpu, mem, grow := QuotaLimitsFor(c.plan, c.mode, c.cpu, c.mem)
		if cpu != c.wantCPU || mem != c.wantMem || grow != c.wantGrow {
			t.Errorf("%s: QuotaLimitsFor(%s,%s,%s,%s) = (%s,%s,%v), want (%s,%s,%v)",
				c.name, c.plan, c.mode, c.cpu, c.mem, cpu, mem, grow, c.wantCPU, c.wantMem, c.wantGrow)
		}
	}
}

// The clamps on the rendered object, against the capped renders: a ceiling
// below M's headline renders M's capped limits; a ceiling above XL renders the
// XL-ceiling limits of the default.
func TestRender_GrowCeilingClampsOnTheRenderedQuota(t *testing.T) {
	t.Parallel()
	capped := parseRQ(t, renderOverage(t, "m", OverageModeCapped, "", ""))
	low := parseRQ(t, renderOverage(t, "m", OverageModeGrow, "1", "1Gi"))
	def := parseRQ(t, renderOverage(t, "m", OverageModeGrow, "", ""))
	high := parseRQ(t, renderOverage(t, "m", OverageModeGrow, "64", "512Gi"))
	for _, k := range []string{"limits.cpu", "limits.memory", "requests.cpu", "requests.memory"} {
		if low.Spec.Hard[k] != capped.Spec.Hard[k] {
			t.Errorf("below-headline ceiling: %s = %s, want capped's %s", k, low.Spec.Hard[k], capped.Spec.Hard[k])
		}
		if high.Spec.Hard[k] != def.Spec.Hard[k] {
			t.Errorf("above-XL ceiling: %s = %s, want the default ceiling's %s", k, high.Spec.Hard[k], def.Spec.Hard[k])
		}
	}
	if got := low.Metadata.Annotations["openova.io/grow-ceiling"]; got != "cpu=2 memory=4Gi" {
		t.Errorf("below-headline grow-ceiling annotation = %q, want the clamped cpu=2 memory=4Gi", got)
	}
}

// Flexi is Burstable: grow changes nothing — still no ResourceQuota, and the
// LimitRange is byte-identical to the capped one (no mode annotation).
func TestRender_FlexiGrowStillHasNoQuota(t *testing.T) {
	t.Parallel()
	grow := renderOverage(t, "flexi", OverageModeGrow, "4", "8Gi")
	capped := renderOverage(t, "flexi", "", "", "")
	if _, ok := grow["vcluster/resourcequota.yaml"]; ok {
		t.Fatal("flexi + grow rendered a ResourceQuota")
	}
	if PlanRendersResourceQuota("flexi") {
		t.Fatal("PlanRendersResourceQuota(flexi) = true; the overage mode must not change it")
	}
	for k, v := range capped {
		if !bytes.Equal(v, grow[k]) {
			t.Errorf("flexi %s differs between capped and grow", k)
		}
	}
}
