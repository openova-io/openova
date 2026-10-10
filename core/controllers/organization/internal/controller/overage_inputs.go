package controller

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// overageInputs maps the Organization's spec.commerce overage fields onto the
// renderer's Inputs (founder model, 2026-10-10): the overage mode, and the
// grow ceiling as Kubernetes quantities.
//
//   - absent spec.commerce, or a block without overageMode → "capped" (the
//     ResourceQuota limits are the headline, the app-storage quota the
//     package disk);
//   - growCeiling.vcpu → strconv.FormatFloat(v, 'f', -1, 64) ("8", "2.5");
//   - growCeiling.memoryGB / diskGB → "<n>Gi" when integral, else whole MiB
//     "<round(GB*1024)>Mi" (GB here is the package's GB, i.e. GiB — the same
//     unit the headline "4 GB" = 4Gi uses);
//   - a zero / negative / absent value → "" (the renderer's default ceiling).
//     The renderer clamps whatever arrives into [plan headline, the plan's
//     grow top], so a bad value can never widen the quota past it or tighten
//     it below the package.
//
// bandwidthMbps, spendLimitMonth and prepaidAmount are BSS's: no bandwidth cap
// renders, and the spend limit reaches this controller only as
// spec.overageStoppedUntil (overageStop).
func overageInputs(c *orgapi.OrganizationCommerce) (mode, ceilCPU, ceilMem, ceilDisk string) {
	if c == nil || c.OverageMode == "" {
		return gitops.OverageModeCapped, "", "", ""
	}
	mode = c.OverageMode
	if g := c.GrowCeiling; g != nil {
		if g.VCPU > 0 && !math.IsInf(g.VCPU, 0) {
			ceilCPU = strconv.FormatFloat(g.VCPU, 'f', -1, 64)
		}
		ceilMem = gibQuantity(g.MemoryGB)
		ceilDisk = gibQuantity(g.DiskGB)
	}
	return mode, ceilCPU, ceilMem, ceilDisk
}

// gibQuantity renders a package GB figure as a Kubernetes quantity: "<n>Gi"
// when integral, else whole MiB; "" for zero, negative or infinite.
func gibQuantity(gb float64) string {
	if gb <= 0 || math.IsInf(gb, 0) || math.IsNaN(gb) {
		return ""
	}
	if gb == math.Trunc(gb) {
		return fmt.Sprintf("%dGi", int64(gb))
	}
	return fmt.Sprintf("%dMi", int64(math.Round(gb*1024)))
}

// overageStop resolves spec.overageStoppedUntil (the BSS spend-limit stop,
// products/chargeback DESIGN.md §22.11) against the clock:
//
//   - in force (a parseable RFC3339 instant after now): the canonical UTC
//     spelling for gitops.Inputs.OverageStoppedUntil, which renders the grow
//     Organization capped, and how long until it lifts — the reconcile
//     requeues at that instant so grow returns at the period start on its
//     own, whether or not BSS has cleared the field by then;
//   - absent, unparseable or already past: "" and 0 — grow as chosen. An
//     unparseable value never stops anything: the stop is a narrowing, and a
//     value the controller cannot read must not narrow the quota forever.
func overageStop(spec orgapi.OrganizationSpec, now time.Time) (until string, after time.Duration) {
	raw := strings.TrimSpace(spec.OverageStoppedUntil)
	if raw == "" {
		return "", 0
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil || !t.After(now) {
		return "", 0
	}
	return t.UTC().Format(time.RFC3339), t.Sub(now)
}
