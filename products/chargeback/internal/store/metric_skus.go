package store

// Metrics that are not meters (#6867).
//
// Some records in the ledger are SAMPLED MEASUREMENTS rather than billable
// quantities: the hourly CPU utilisation of an instance, and the outbound
// traffic of an address the cloud bills by its reserved size rather than by
// its traffic. They ride in usage_records because they are per-resource,
// per-hour facts and the recommendation rules read them there — but they are
// never rated, never summed into cost or usage, and never reported as
// "unpriced", or the operator would be told to put a price on a percentage.
//
// The list lives here ONCE. It used to be four hand-written copies of a
// single SKU literal spread across the cost CTE, the rating aggregate, the
// overview aggregate and the boundary-recompute delete; adding a second
// metric to three of the four would have left it billable in the fourth,
// which is the failure mode nothing looks wrong for.
const (
	// SKUCPUUtil is the hourly mean CPU utilisation of an instance, in
	// percent (huawei.SKUCPUUtil writes it; a test pins the two equal).
	SKUCPUUtil = "ecs.cpu_util"
	// SKUEIPTrafficObserved is the hourly outbound traffic, in GB, of an
	// address whose pipe the cloud bills by RESERVED SIZE. The cloud
	// charges nothing for it, so it must never be rated — the billable
	// twin of this measurement, on a traffic-billed address, is the
	// separate SKU eip.traffic_gb.
	SKUEIPTrafficObserved = "eip.traffic_gb.observed"
)

// MetricSKUs is that list as data, for the readers that need it in Go.
var MetricSKUs = []string{SKUCPUUtil, SKUEIPTrafficObserved}

// metricSKUsSQL is the same list as a SQL tuple, built from the SAME
// constants so the two can never say different things.
const metricSKUsSQL = `('` + SKUCPUUtil + `', '` + SKUEIPTrafficObserved + `')`

// metricSKUFilter keeps only the meters, for a query with no table alias.
const metricSKUFilter = `sku NOT IN ` + metricSKUsSQL

// IsMetricSKU reports whether a SKU is a sampled measurement rather than a
// billable meter.
func IsMetricSKU(sku string) bool {
	for _, m := range MetricSKUs {
		if sku == m {
			return true
		}
	}
	return false
}
