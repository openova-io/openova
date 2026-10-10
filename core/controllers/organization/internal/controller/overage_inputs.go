package controller

import (
	"fmt"
	"math"
	"strconv"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// overageInputs maps the Organization's spec.commerce overage fields onto the
// renderer's Inputs (founder model, 2026-10-10): the overage mode, and the
// grow ceiling as Kubernetes quantities.
//
//   - absent spec.commerce, or a block without overageMode → "capped" (the
//     ResourceQuota limits stay the headline);
//   - growCeiling.vcpu → strconv.FormatFloat(v, 'f', -1, 64) ("8", "2.5");
//   - growCeiling.memoryGB → "<n>Gi" when integral, else whole MiB
//     "<round(memoryGB*1024)>Mi" (GB here is the package's GB, i.e. GiB —
//     the same unit the headline "4 GB" = 4Gi uses);
//   - a zero / negative / absent value → "" (the renderer's default ceiling,
//     the XL headline). The renderer clamps whatever arrives into
//     [plan headline, XL headline], so a bad value can never widen the quota
//     past XL or tighten it below the package.
//
// diskGB, bandwidthMbps and spendLimitMonth are BSS's: the ResourceQuota
// renders no storage or bandwidth cap, and this controller does not read them.
func overageInputs(c *orgapi.OrganizationCommerce) (mode, ceilCPU, ceilMem string) {
	if c == nil || c.OverageMode == "" {
		return gitops.OverageModeCapped, "", ""
	}
	mode = c.OverageMode
	if g := c.GrowCeiling; g != nil {
		if g.VCPU > 0 && !math.IsInf(g.VCPU, 0) {
			ceilCPU = strconv.FormatFloat(g.VCPU, 'f', -1, 64)
		}
		if g.MemoryGB > 0 && !math.IsInf(g.MemoryGB, 0) {
			if g.MemoryGB == math.Trunc(g.MemoryGB) {
				ceilMem = fmt.Sprintf("%dGi", int64(g.MemoryGB))
			} else {
				ceilMem = fmt.Sprintf("%dMi", int64(math.Round(g.MemoryGB*1024)))
			}
		}
	}
	return mode, ceilCPU, ceilMem
}
