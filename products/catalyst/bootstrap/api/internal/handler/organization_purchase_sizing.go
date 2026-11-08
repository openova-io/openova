// organization_purchase_sizing.go — the customer's purchase is sized from the
// plan's overcommit (Refs #7067).
//
// The Organization's plan is a LIMIT budget (the headline the customer pays
// for — plan-quota limits.*) and a REQUEST budget that is that headline over
// the workbook overcommit, CPU ÷ 6 and memory ÷ 3 (plan-quota requests.*, the
// guaranteed share the Sovereign reserves). The org-controller's LimitRange
// sizes every container authored without resources by the same rule
// (core/controllers/organization/internal/gitops/manifests.go
// limitRangeDefaults: request = limit / ratio, rounded up).
//
// The purchase charts the BSS door delivers into the Organization namespace
// on the customer's behalf (bp-wordpress-tenant, bp-stalwart-tenant) were
// authored the other way round — requests at or near their limits — so on
// hw307 acmewalk307 (plan M, request budget 334m) the Stalwart pod alone
// requested 500m: the purchase could never fit the budget it was sold
// against, and in the single shared quota of the time it consumed the
// platform's slack instead (the wedge platform_scope.go describes).
//
// The rule here is the plan's own: every purchase workload renders its limit
// as today and its request as limit / ratio. A customer workload sized this
// way consumes its request budget in exactly the proportion it consumes its
// limit budget, so the request side can never be the one that refuses a
// purchase whose limits fit the headline. The limit side is the package the
// customer bought; TestPurchaseSizing_BundleLimitsAgainstHeadline states, per
// plan, whether the bundle's limits fit it.
package handler

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// purchaseOvercommitCPU / purchaseOvercommitMemory are the plan overcommit
// ratios. They are the org-controller's planOvercommitCPU /
// planOvercommitMemory (a separate Go module, so restated here and pinned to
// that source by TestPurchaseSizing_RatioIsTheOrgControllers).
const (
	purchaseOvercommitCPU    = 6
	purchaseOvercommitMemory = 3
)

// purchaseShape is one container's requests/limits as the templates render them.
type purchaseShape struct {
	RequestsCPU    string
	RequestsMemory string
	LimitsCPU      string
	LimitsMemory   string
}

// purchasedWorkload is one sized container of the customer's purchase.
type purchasedWorkload struct {
	// Key addresses the shape from the templates (index .Purchase "<key>").
	Key string
	// Release is the purchase HelmRelease that renders it.
	Release string
	// ValuesPath is where the shape lands in that release's values.
	ValuesPath []string
	// LimitsCPU / LimitsMemory are the chart's own limits, kept (the request
	// is what moves). The pin test reads each chart default.
	LimitsCPU    string
	LimitsMemory string
}

// purchasedWorkloads — every container the two purchase charts size through
// values. Containers a chart leaves unsized (stalwart-webmail's seed-domain
// init, the WordPress init containers) take the LimitRange defaults, which
// already follow the ratio; the WordPress hook Jobs size themselves inline
// near the ratio (5:1 / 2:1) and run only during a Helm operation.
var purchasedWorkloads = []purchasedWorkload{
	{Key: "stalwart", Release: "bp-stalwart-tenant", ValuesPath: []string{"stalwart", "resources"}, LimitsCPU: "500m", LimitsMemory: "1Gi"},
	{Key: "webmail", Release: "bp-stalwart-tenant", ValuesPath: []string{"webmail", "resources"}, LimitsCPU: "100m", LimitsMemory: "128Mi"},
	{Key: "stalwart-setup", Release: "bp-stalwart-tenant", ValuesPath: []string{"mailboxProvisioner", "setupJob", "resources"}, LimitsCPU: "100m", LimitsMemory: "64Mi"},
	{Key: "wordpress", Release: "bp-wordpress-tenant", ValuesPath: []string{"wordpress", "resources"}, LimitsCPU: "500m", LimitsMemory: "512Mi"},
	// The chart sets no CPU limit on its database; 500m is the figure the
	// live wordpress-db-1 pod ran with on hw307 and is stated so the limit
	// side is explicit rather than a per-plan LimitRange default.
	{Key: "wordpress-db", Release: "bp-wordpress-tenant", ValuesPath: []string{"database", "cluster", "resources"}, LimitsCPU: "500m", LimitsMemory: "512Mi"},
}

// ratioSized is the plan rule: request = limit / ratio, rounded UP to the
// millicore / MiB so limit/request never exceeds the ratio.
func ratioSized(limitCPU, limitMemory string) purchaseShape {
	c := resource.MustParse(limitCPU)
	m := resource.MustParse(limitMemory)
	const mi = 1024 * 1024
	ceil := func(n, d int64) int64 { return (n + d - 1) / d }
	return purchaseShape{
		RequestsCPU:    fmt.Sprintf("%dm", ceil(c.MilliValue(), purchaseOvercommitCPU)),
		RequestsMemory: fmt.Sprintf("%dMi", ceil(ceil(m.Value(), mi), purchaseOvercommitMemory)),
		LimitsCPU:      limitCPU,
		LimitsMemory:   limitMemory,
	}
}

// purchaseShapes is the template input: Key → the ratio-sized shape.
func purchaseShapes() map[string]purchaseShape {
	out := make(map[string]purchaseShape, len(purchasedWorkloads))
	for _, w := range purchasedWorkloads {
		out[w.Key] = ratioSized(w.LimitsCPU, w.LimitsMemory)
	}
	return out
}
