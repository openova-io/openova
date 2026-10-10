// plan_quota_headline_over_guaranteed_6971_test.go — the #6971 overcommit
// model, proven on the rendered objects.
//
// The National Cloud workbook (NC-OO-Pricing.xlsx, 2026-06-28) sells each
// package by a HEADLINE shape and provisions a GUARANTEED share of it: CPU
// is overcommitted 6×, memory 3×. On the Org boundary namespace that is:
//
//   - ResourceQuota limits.*   = headline  + the overheads' limits
//   - ResourceQuota requests.* = guaranteed + the overheads' requests
//   - LimitRange default       = headline / 8 per container
//   - LimitRange defaultRequest = that over the ratio (÷ 6 / ÷ 3)
//
// Plan M is the worked example the brief names: requests.cpu 334m + overhead,
// limits.cpu 2 + overhead, ratio 6 / 3.
package gitops

import (
	"fmt"
	"math"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// TestPlanQuota_RequestIsHeadlineOverOvercommit: for every hard-capped plan
// the request is the headline over the ratio, rounded UP to the millicore /
// MiB, so headline ÷ request is never above the ratio and never more than a
// rounding step below it.
func TestPlanQuota_RequestIsHeadlineOverOvercommit(t *testing.T) {
	t.Parallel()
	hardCapped := 0
	for slug, q := range planQuotaTable {
		if q.Burstable {
			if q.CPULimit != "" || q.CPURequest != "" || q.MemLimit != "" || q.MemRequest != "" {
				t.Errorf("plan %q is Burstable and must carry no figures: %+v", slug, q)
			}
			continue
		}
		hardCapped++
		limCPU, reqCPU := qMilli(t, slug, q.CPULimit), qMilli(t, slug, q.CPURequest)
		limMem, reqMem := qBytes(t, slug, q.MemLimit)/mi, qBytes(t, slug, q.MemRequest)/mi
		if want := int64(math.Ceil(float64(limCPU) / planOvercommitCPU)); reqCPU != want {
			t.Errorf("plan %q: CPURequest = %dm, want ceil(%dm / %d) = %dm", slug, reqCPU, limCPU, planOvercommitCPU, want)
		}
		if want := int64(math.Ceil(float64(limMem) / planOvercommitMemory)); reqMem != want {
			t.Errorf("plan %q: MemRequest = %dMi, want ceil(%dMi / %d) = %dMi", slug, reqMem, limMem, planOvercommitMemory, want)
		}
		// The ratio the live object states must hold, tightly.
		if r := float64(limCPU) / float64(reqCPU); r > planOvercommitCPU || r < planOvercommitCPU-0.1 {
			t.Errorf("plan %q: headline/guaranteed cpu = %.3f, want within (%d-0.1, %d]", slug, r, planOvercommitCPU, planOvercommitCPU)
		}
		if r := float64(limMem) / float64(reqMem); r > planOvercommitMemory || r < planOvercommitMemory-0.1 {
			t.Errorf("plan %q: headline/guaranteed memory = %.3f, want within (%d-0.1, %d]", slug, r, planOvercommitMemory, planOvercommitMemory)
		}
	}
	if hardCapped < 4 {
		t.Fatalf("vacuity: only %d hard-capped plans checked", hardCapped)
	}
}

// TestLimitRangeDefaults_HeadlineOverEightThenOvercommit pins the
// per-container defaults by value and by rule: default = headline / 8,
// defaultRequest = default over the ratio, rounded up.
func TestLimitRangeDefaults_HeadlineOverEightThenOvercommit(t *testing.T) {
	t.Parallel()
	want := map[string]containerShape{
		"s":  {RequestsCPU: "21m", RequestsMemory: "86Mi", LimitsCPU: "125m", LimitsMemory: "256Mi"},
		"m":  {RequestsCPU: "42m", RequestsMemory: "171Mi", LimitsCPU: "250m", LimitsMemory: "512Mi"},
		"l":  {RequestsCPU: "84m", RequestsMemory: "342Mi", LimitsCPU: "500m", LimitsMemory: "1Gi"},
		"xl": {RequestsCPU: "167m", RequestsMemory: "683Mi", LimitsCPU: "1", LimitsMemory: "2Gi"},
	}
	for slug, w := range want {
		q := planQuota(slug)
		got := limitRangeDefaults(q)
		for _, c := range []struct{ name, got, want string }{
			{"defaultRequest cpu", got.RequestsCPU, w.RequestsCPU},
			{"defaultRequest memory", got.RequestsMemory, w.RequestsMemory},
			{"default cpu", got.LimitsCPU, w.LimitsCPU},
			{"default memory", got.LimitsMemory, w.LimitsMemory},
		} {
			if qCmp(t, slug, c.got, c.want) != 0 {
				t.Errorf("plan %q: %s = %s, want %s", slug, c.name, c.got, c.want)
			}
		}
		// By rule, against the table rather than the pins above.
		if l, h := qMilli(t, slug, got.LimitsCPU), qMilli(t, slug, q.CPULimit); l*8 != h {
			t.Errorf("plan %q: default cpu %dm × 8 != headline %dm", slug, l, h)
		}
		if l, h := qBytes(t, slug, got.LimitsMemory), qBytes(t, slug, q.MemLimit); l*8 != h {
			t.Errorf("plan %q: default memory %d × 8 != headline %d", slug, l, h)
		}
		if r := float64(qMilli(t, slug, got.LimitsCPU)) / float64(qMilli(t, slug, got.RequestsCPU)); r > planOvercommitCPU || r < planOvercommitCPU-0.1 {
			t.Errorf("plan %q: default/defaultRequest cpu = %.3f, want within (%d-0.1, %d]", slug, r, planOvercommitCPU, planOvercommitCPU)
		}
		if r := float64(qBytes(t, slug, got.LimitsMemory)) / float64(qBytes(t, slug, got.RequestsMemory)); r > planOvercommitMemory || r < planOvercommitMemory-0.1 {
			t.Errorf("plan %q: default/defaultRequest memory = %.3f, want within (%d-0.1, %d]", slug, r, planOvercommitMemory, planOvercommitMemory)
		}
	}
	// Flexi keeps the small symmetric floor — no headline to derive from.
	if got := limitRangeDefaults(planQuota("flexi")); got != (containerShape{RequestsCPU: "100m", RequestsMemory: "128Mi", LimitsCPU: "100m", LimitsMemory: "128Mi"}) {
		t.Errorf("flexi defaults = %+v, want the 100m / 128Mi floor on both sides", got)
	}
}

// TestRender_PlanM_HeadlineLimitOverGuaranteedRequest renders plan M and reads
// the two objects back the way an operator would: the ResourceQuota carries
// 334m + overhead on requests.cpu and 2 + overhead on limits.cpu (and the
// same split on memory), the LimitRange seeds 42m → 250m / 171Mi → 512Mi, and
// both objects state the 6 / 3 model in their annotations.
func TestRender_PlanM_HeadlineLimitOverGuaranteedRequest(t *testing.T) {
	t.Parallel()
	out := renderPlan(t, "m")
	q := planQuota("m")
	cp := vclusterControlPlaneOverhead
	ps := platformStackOverheadFor(q)

	var rq struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Hard map[string]string `json:"hard"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(out["vcluster/resourcequota.yaml"], &rq); err != nil {
		t.Fatalf("resourcequota.yaml: %v", err)
	}
	sum := func(plan string, over resource.Quantity) string {
		s := mustQ(t, "m", plan)
		s.Add(over)
		return s.String()
	}
	for _, c := range []struct{ key, want, literal string }{
		{"requests.cpu", sum("334m", cpPlus(cp.RequestsCPU, ps.RequestsCPU)), "4694m"},
		{"requests.memory", sum("1366Mi", cpPlus(cp.RequestsMemory, ps.RequestsMemory)), "8518Mi"},
		{"limits.cpu", sum("2", cpPlus(cp.LimitsCPU, ps.LimitsCPU)), "8050m"},
		{"limits.memory", sum("4Gi", cpPlus(cp.LimitsMemory, ps.LimitsMemory)), "12458Mi"},
	} {
		got := rq.Spec.Hard[c.key]
		if got != c.want {
			t.Errorf("plan m %s = %s, want %s (plan term + control plane + platform stack)", c.key, got, c.want)
		}
		if got != c.literal {
			t.Errorf("plan m %s = %s, want the literal %s (334m + 520m + 3840m / 2 + 1500m + 4550m; 1366Mi + 1088Mi + 6064Mi / 4Gi + 1194Mi + 7168Mi)", c.key, got, c.literal)
		}
	}
	// requests < limits on both resources — the overcommit is visible on the cap.
	if r, l := mustQ(t, "m", rq.Spec.Hard["requests.cpu"]), mustQ(t, "m", rq.Spec.Hard["limits.cpu"]); r.Cmp(l) >= 0 {
		t.Errorf("plan m requests.cpu %s is not below limits.cpu %s", r.String(), l.String())
	}
	if r, l := mustQ(t, "m", rq.Spec.Hard["requests.memory"]), mustQ(t, "m", rq.Spec.Hard["limits.memory"]); r.Cmp(l) >= 0 {
		t.Errorf("plan m requests.memory %s is not below limits.memory %s", r.String(), l.String())
	}
	// The plan's own ratio, stripped of the overheads: headline / guaranteed.
	if r := float64(qMilli(t, "m", q.CPULimit)) / float64(qMilli(t, "m", q.CPURequest)); fmt.Sprintf("%.2f", r) != "5.99" {
		t.Errorf("plan m headline/guaranteed cpu = %.3f, want 5.99 (2000m / 334m, the 6× overcommit rounded up to the millicore)", r)
	}
	if r := float64(qBytes(t, "m", q.MemLimit)) / float64(qBytes(t, "m", q.MemRequest)); fmt.Sprintf("%.2f", r) != "3.00" {
		t.Errorf("plan m headline/guaranteed memory = %.3f, want 3.00 (4096Mi / 1366Mi)", r)
	}
	for k, want := range map[string]string{
		"openova.io/plan-cap":               "cpu=2 memory=4Gi",
		"openova.io/plan-guaranteed-cpu":    "334m",
		"openova.io/plan-guaranteed-memory": "1366Mi",
		"openova.io/plan-overcommit-cpu":    "6",
		"openova.io/plan-overcommit-memory": "3",
	} {
		if got := rq.Metadata.Annotations[k]; got != want {
			t.Errorf("ResourceQuota annotation %s = %q, want %q", k, got, want)
		}
	}

	var lr struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Limits []struct {
				DefaultRequest map[string]string `json:"defaultRequest"`
				Default        map[string]string `json:"default"`
				MaxRatio       map[string]string `json:"maxLimitRequestRatio"`
			} `json:"limits"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(out["vcluster/limitrange.yaml"], &lr); err != nil {
		t.Fatalf("limitrange.yaml: %v", err)
	}
	if len(lr.Spec.Limits) != 1 {
		t.Fatalf("want one Container limit, got %d", len(lr.Spec.Limits))
	}
	l := lr.Spec.Limits[0]
	for _, c := range []struct{ name, got, want string }{
		{"defaultRequest.cpu", l.DefaultRequest["cpu"], "42m"},
		{"defaultRequest.memory", l.DefaultRequest["memory"], "171Mi"},
		{"default.cpu", l.Default["cpu"], "250m"},
		{"default.memory", l.Default["memory"], "512Mi"},
	} {
		if qCmp(t, "m", c.got, c.want) != 0 {
			t.Errorf("plan m LimitRange %s = %s, want %s", c.name, c.got, c.want)
		}
	}
	if r := float64(qMilli(t, "m", l.Default["cpu"])) / float64(qMilli(t, "m", l.DefaultRequest["cpu"])); r > 6 || r < 5.9 {
		t.Errorf("plan m LimitRange default/defaultRequest cpu = %.3f, want within (5.9, 6]", r)
	}
	if r := float64(qBytes(t, "m", l.Default["memory"])) / float64(qBytes(t, "m", l.DefaultRequest["memory"])); r > 3 || r < 2.9 {
		t.Errorf("plan m LimitRange default/defaultRequest memory = %.3f, want within (2.9, 3]", r)
	}
	if len(l.MaxRatio) != 0 {
		t.Errorf("plan m LimitRange carries maxLimitRequestRatio %v — it would reject the synced coredns (50:1) and the platform stack's sidecars (20:1); the model is stated as annotations instead (#4758)", l.MaxRatio)
	}
	for k, want := range map[string]string{
		"openova.io/plan-cap":               "cpu=2 memory=4Gi",
		"openova.io/plan-guaranteed-cpu":    "334m",
		"openova.io/plan-guaranteed-memory": "1366Mi",
		"openova.io/plan-overcommit-cpu":    "6",
		"openova.io/plan-overcommit-memory": "3",
	} {
		if got := lr.Metadata.Annotations[k]; got != want {
			t.Errorf("LimitRange annotation %s = %q, want %q", k, got, want)
		}
	}
}

// qMilli / qBytes parse a quantity literal and return it in millicores /
// bytes (resource.Quantity's accessors are pointer methods, so a parsed value
// must be bound first).
func qMilli(t *testing.T, ctx, s string) int64 {
	t.Helper()
	q := mustQ(t, ctx, s)
	return q.MilliValue()
}

func qBytes(t *testing.T, ctx, s string) int64 {
	t.Helper()
	q := mustQ(t, ctx, s)
	return q.Value()
}

// qCmp compares two quantity literals (-1 / 0 / +1).
func qCmp(t *testing.T, ctx, a, b string) int {
	t.Helper()
	qa, qb := mustQ(t, ctx, a), mustQ(t, ctx, b)
	return qa.Cmp(qb)
}

// cpPlus sums two overhead quantities.
func cpPlus(a, b resource.Quantity) resource.Quantity {
	s := a.DeepCopy()
	s.Add(b)
	return s
}
