package store

import (
	"strings"
	"testing"
)

// The metric list and the SQL tuple built from it must always say the same
// thing. They are two renderings of one list, so the only way they can
// disagree is a hand-edit — which is exactly what happened to the single
// SKU literal this replaced, spread across four queries.
func TestMetricSKUListAndSQLTupleAgree(t *testing.T) {
	if len(MetricSKUs) == 0 {
		t.Fatal("no metric SKUs: every sampled measurement would be rated")
	}
	if n := strings.Count(metricSKUsSQL, "'") / 2; n != len(MetricSKUs) {
		t.Fatalf("the SQL tuple lists %d SKUs but MetricSKUs has %d: %s", n, len(MetricSKUs), metricSKUsSQL)
	}
	for _, sku := range MetricSKUs {
		if !strings.Contains(metricSKUsSQL, "'"+sku+"'") {
			t.Fatalf("%q is a metric in Go but a meter in SQL: %s", sku, metricSKUsSQL)
		}
		if !IsMetricSKU(sku) {
			t.Fatalf("IsMetricSKU(%q) = false", sku)
		}
	}
	// The filter is written so it can be prefixed with a table alias
	// ("u." + filter); a leading alias of its own would break that.
	if !strings.HasPrefix(metricSKUFilter, "sku NOT IN ") {
		t.Fatalf("filter = %q, want an un-aliased predicate", metricSKUFilter)
	}
	if costMeterFilter != "u."+metricSKUFilter {
		t.Fatalf("aliased filter = %q", costMeterFilter)
	}
	// The billable traffic meter must NOT be in the list, or it could never
	// be rated once the operator adds a price for it.
	if IsMetricSKU(SKUEIPTraffic) {
		t.Fatalf("%q is excluded from cost: a rate on it would never reach a statement", SKUEIPTraffic)
	}
	if !IsMetricSKU(SKUCPUUtil) || !IsMetricSKU(SKUEIPTrafficObserved) {
		t.Fatal("a sampled measurement is missing from the metric list")
	}
	if IsMetricSKU("ecs.s6.large.2") {
		t.Fatal("a compute meter is treated as a metric")
	}
}

// A shared bandwidth object reaches the explorer as a resource kind of its
// own; without a label it renders as the raw string.
func TestSharedBandwidthKindHasALabel(t *testing.T) {
	if got := KindLabel("bandwidth"); got == "bandwidth" || got == "" {
		t.Fatalf("KindLabel(bandwidth) = %q", got)
	}
}
