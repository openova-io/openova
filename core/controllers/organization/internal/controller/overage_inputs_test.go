// overage_inputs_test.go — spec.commerce.overageMode / growCeiling reach the
// renderer (founder model, 2026-10-10): the mapping helper by value, and the
// whole path through Reconcile to the ResourceQuota committed to Gitea.
package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"k8s.io/apimachinery/pkg/types"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

func TestOverageInputs_MapsSpecCommerce(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name                 string
		in                   *orgapi.OrganizationCommerce
		mode, cpu, mem, disk string
	}{
		{"absent commerce is capped", nil, "capped", "", "", ""},
		{"no mode is capped", &orgapi.OrganizationCommerce{PackageSKU: "plan.m"}, "capped", "", "", ""},
		{"capped ignores a ceiling", &orgapi.OrganizationCommerce{OverageMode: "capped",
			GrowCeiling: &orgapi.OrganizationGrowCeiling{VCPU: 4, MemoryGB: 8}}, "capped", "4", "8Gi", ""},
		{"grow without ceiling", &orgapi.OrganizationCommerce{OverageMode: "grow"}, "grow", "", "", ""},
		{"grow integral", &orgapi.OrganizationCommerce{OverageMode: "grow",
			GrowCeiling: &orgapi.OrganizationGrowCeiling{VCPU: 4, MemoryGB: 8, DiskGB: 120, BandwidthMbps: 500}}, "grow", "4", "8Gi", "120Gi"},
		{"grow fractional", &orgapi.OrganizationCommerce{OverageMode: "grow",
			GrowCeiling: &orgapi.OrganizationGrowCeiling{VCPU: 2.5, MemoryGB: 6.5, DiskGB: 60.5}}, "grow", "2.5", "6656Mi", "61952Mi"},
		{"grow MiB rounding", &orgapi.OrganizationCommerce{OverageMode: "grow",
			GrowCeiling: &orgapi.OrganizationGrowCeiling{MemoryGB: 4.0004}}, "grow", "", "4096Mi", ""},
		{"grow zero / negative = default", &orgapi.OrganizationCommerce{OverageMode: "grow",
			GrowCeiling: &orgapi.OrganizationGrowCeiling{VCPU: 0, MemoryGB: -2, DiskGB: -1}}, "grow", "", "", ""},
	} {
		mode, cpu, mem, disk := overageInputs(c.in)
		// A capped mode passes its ceiling through; the renderer ignores it.
		if mode != c.mode || cpu != c.cpu || mem != c.mem || disk != c.disk {
			t.Errorf("%s: overageInputs = (%q,%q,%q,%q), want (%q,%q,%q,%q)", c.name, mode, cpu, mem, disk, c.mode, c.cpu, c.mem, c.disk)
		}
	}
}

// overageStop reads spec.overageStoppedUntil against the clock: in force only
// for a parseable instant after now, and it says how long until it lifts.
func TestOverageStop_InForceOnlyBeforeTheInstant(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, raw, want string
		after           time.Duration
	}{
		{"absent", "", "", 0},
		{"in force", "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z", 11*24*time.Hour + 12*time.Hour},
		{"offset normalised to UTC", "2026-11-01T04:00:00+04:00", "2026-11-01T00:00:00Z", 11*24*time.Hour + 12*time.Hour},
		{"past lifts", "2026-10-01T00:00:00Z", "", 0},
		{"the instant itself lifts", "2026-10-20T12:00:00Z", "", 0},
		{"unparseable never stops", "next month", "", 0},
	} {
		until, after := overageStop(orgapi.OrganizationSpec{OverageStoppedUntil: c.raw}, now)
		if until != c.want || after != c.after {
			t.Errorf("%s: overageStop(%q) = (%q, %v), want (%q, %v)", c.name, c.raw, until, after, c.want, c.after)
		}
	}
}

// The whole path: an Organization CR with spec.commerce grow 4 / 8 on plan M
// commits a ResourceQuota whose limits are the ceiling + overheads, and one
// without spec.commerce commits the capped quota.
func TestReconcile_SpecCommerceGrowSizesTheCommittedQuota(t *testing.T) {
	t.Parallel()
	const rqKey = "acme/catalyst-tenant/vcluster/resourcequota.yaml"
	render := func(c *orgapi.OrganizationCommerce) string {
		org := sampleOrg() // plan m
		org.Spec.Commerce = c
		r, gs, _ := makeReconciler(t, org)
		if _, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "acme"},
		}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		f, ok := gs.files[rqKey]
		if !ok {
			t.Fatalf("controller did not commit %s", rqKey)
		}
		return string(f.content)
	}
	capped := render(nil)
	grow := render(&orgapi.OrganizationCommerce{PackageSKU: "plan.m", OverageMode: "grow",
		GrowCeiling: &orgapi.OrganizationGrowCeiling{VCPU: 4, MemoryGB: 8}, SpendLimitMonth: "25.000"})
	for _, want := range []string{`openova.io/overage-mode: "capped"`, `limits.cpu: "8050m"`, `limits.memory: "12458Mi"`, `requests.cpu: "4694m"`} {
		if !strings.Contains(capped, want) {
			t.Errorf("capped quota missing %s:\n%s", want, capped)
		}
	}
	for _, want := range []string{`openova.io/overage-mode: "grow"`, `openova.io/grow-ceiling: "cpu=4 memory=8Gi"`,
		`limits.cpu: "10050m"`, `limits.memory: "16554Mi"`, `requests.cpu: "4694m"`, `requests.memory: "8518Mi"`} {
		if !strings.Contains(grow, want) {
			t.Errorf("grow quota missing %s:\n%s", want, grow)
		}
	}
}
