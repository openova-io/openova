package openova

import (
	"context"
	"encoding/json"
	"math/big"
	"sort"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// In-place vertical scaling (#6867).
//
// A pod's requests can change WITHOUT the pod being recreated — that is what
// Kubernetes in-place vertical scaling does, and what a Vertical Pod
// Autoscaler uses on a cluster that has it. The pod keeps its UID, so the
// collector keeps one tracked resource, and before this change every
// informer update simply overwrote the tracked requests: the whole hour was
// billed at whichever size happened to be in the map when the hour was
// emitted. The cloud collector never had this bug because an ECS resize is a
// window.Transition and the hour splits at it. These tests pin that the
// platform collector now does the same, through the same window helpers.

// billed sums the quantity of one SKU across the emitted records, exactly.
func billed(recs []store.UsageRecord, sku string) *big.Rat {
	t := new(big.Rat)
	for _, r := range recs {
		if r.SKU != sku {
			continue
		}
		q, ok := new(big.Rat).SetString(string(r.Quantity))
		if !ok {
			continue
		}
		t.Add(t, q)
	}
	return t
}

// moneyAt is quantity × rate at the 6 decimals every money column carries.
func moneyAt(qty *big.Rat, rate string) string {
	r, _ := new(big.Rat).SetString(rate)
	return new(big.Rat).Mul(qty, r).FloatString(6)
}

func sortedRecords(recs []store.UsageRecord) []store.UsageRecord {
	out := append([]store.UsageRecord(nil), recs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ResourceID != out[j].ResourceID {
			return out[i].ResourceID < out[j].ResourceID
		}
		if out[i].SKU != out[j].SKU {
			return out[i].SKU < out[j].SKU
		}
		return out[i].WindowStart.Before(out[j].WindowStart)
	})
	return out
}

// TestPodResizedInPlaceBillsEachHalfOfTheHourAtItsOwnSize: a pod whose
// requests double at 30 minutes past the hour bills half the hour at each
// size — and the money agrees to the last digit, which is the only assertion
// that would have caught the old behaviour (it billed the whole hour at one
// of the two sizes, and which one depended on when the emit ran).
func TestPodResizedInPlaceBillsEachHalfOfTheHourAtItsOwnSize(t *testing.T) {
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	hour := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	now := hour
	c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	c.ObserveNamespace(orgNamespace("acme"))

	// 10:00 — the pod starts at 500m / 1Gi.
	c.ObservePod(testPod("acme", "web-0", "pod-uid-1", hour, "500m", "1Gi"))

	// 10:30 — the SAME pod (same UID, no restart) is resized in place to
	// 1 CPU / 2Gi. This is the update the old code overwrote in place.
	now = hour.Add(30 * time.Minute)
	c.ObservePod(testPod("acme", "web-0", "pod-uid-1", hour, "1", "2Gi"))

	// 11:00 — the hour is emitted.
	now = hour.Add(time.Hour)
	ctx := context.Background()
	if _, err := c.EmitOrg(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	src := repo.sourcesOf(cust.ID)[0]
	recs := repo.usageRecords(src.ID)

	// Two slices per SKU: [10:00, 10:30) at the old size, [10:30, 11:00) at
	// the new one.
	byWindow := map[string]store.UsageRecord{}
	for _, r := range recs {
		byWindow[r.SKU+"|"+r.WindowStart.UTC().Format("15:04")+"-"+r.WindowEnd.UTC().Format("15:04")] = r
	}
	for key, want := range map[string]string{
		"k8s.vcpu|10:00-10:30":   "0.250000", // 0.5 vCPU × 0.5 h
		"k8s.vcpu|10:30-11:00":   "0.500000", // 1.0 vCPU × 0.5 h
		"k8s.mem_gb|10:00-10:30": "0.500000", // 1 GiB × 0.5 h
		"k8s.mem_gb|10:30-11:00": "1.000000", // 2 GiB × 0.5 h
	} {
		got, ok := byWindow[key]
		if !ok {
			t.Fatalf("no record for %s; got %d records: %v", key, len(recs), byWindow)
		}
		if string(got.Quantity) != want {
			t.Fatalf("%s quantity = %s, want %s", key, got.Quantity, want)
		}
	}

	// Money: at 0.030000 per vCPU-hour the hour costs 0.022500, not the
	// 0.030000 the old code billed when it observed the resize before the
	// emit, and not the 0.015000 it billed when it did not.
	if got := moneyAt(billed(recs, SKUVCPU), "0.030000"); got != "0.022500" {
		t.Fatalf("vCPU cost for the hour = %s, want 0.022500", got)
	}
	if got := moneyAt(billed(recs, SKUMem), "0.004000"); got != "0.006000" {
		t.Fatalf("memory cost for the hour = %s, want 0.006000", got)
	}
}

// TestPodRecreatedByEvictionStillBillsAsTwoLifecycles: eviction-based
// autoscaling recreates the pod under a NEW UID. That path was already
// correct, and it must stay correct — the two pods are two resources, each
// billed for the part of the hour it existed, and neither carries the
// other's size.
func TestPodRecreatedByEvictionStillBillsAsTwoLifecycles(t *testing.T) {
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	hour := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	now := hour
	c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	c.ObserveNamespace(orgNamespace("acme"))
	c.ObservePod(testPod("acme", "web-0", "pod-uid-1", hour, "500m", "1Gi"))

	// 10:30 — evicted and recreated at the bigger size under a new UID.
	now = hour.Add(30 * time.Minute)
	c.ObservePodDeleted(testPod("acme", "web-0", "pod-uid-1", hour, "500m", "1Gi"))
	c.ObservePod(testPod("acme", "web-0", "pod-uid-2", now, "1", "2Gi"))

	now = hour.Add(time.Hour)
	if _, err := c.EmitOrg(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	src := repo.sourcesOf(cust.ID)[0]
	recs := repo.usageRecords(src.ID)

	perResource := map[string]*big.Rat{}
	for _, r := range recs {
		if r.SKU != SKUVCPU {
			continue
		}
		q, _ := new(big.Rat).SetString(string(r.Quantity))
		if perResource[r.ResourceID] == nil {
			perResource[r.ResourceID] = new(big.Rat)
		}
		perResource[r.ResourceID].Add(perResource[r.ResourceID], q)
	}
	if len(perResource) != 2 {
		t.Fatalf("vCPU billed against %d resources, want 2 (one lifecycle each): %v", len(perResource), perResource)
	}
	if got := perResource["pod/pod-uid-1"]; got == nil || got.FloatString(6) != "0.250000" {
		t.Fatalf("evicted pod vCPU = %v, want 0.250000", got)
	}
	if got := perResource["pod/pod-uid-2"]; got == nil || got.FloatString(6) != "0.500000" {
		t.Fatalf("replacement pod vCPU = %v, want 0.500000", got)
	}
	if got := moneyAt(billed(recs, SKUVCPU), "0.030000"); got != "0.022500" {
		t.Fatalf("vCPU cost for the hour = %s, want 0.022500", got)
	}
}

// TestUnresizedWorkloadEmitsByteIdenticalRecords: the transition list must
// change NOTHING for a resource that never resizes. The comparison is
// against the very code path that ran before it existed — an empty
// transition list, which platformSKUs falls back to the last observed size
// for — and it is byte-for-byte over the serialised records, not a spot
// check of one quantity.
func TestUnresizedWorkloadEmitsByteIdenticalRecords(t *testing.T) {
	build := func(withTransitions bool) []byte {
		repo := newFakeRepo()
		cust := repo.addActiveCustomer("acme")
		now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
		c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
		c.ObserveNamespace(orgNamespace("acme"))
		podCreated := time.Date(2026, 9, 10, 10, 45, 0, 0, time.UTC)
		pvcCreated := time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC)
		// Four observations of the SAME sizes: an informer update that
		// changes nothing billable must record no transition.
		for i := 0; i < 4; i++ {
			c.ObservePod(testPod("acme", "web-0", "pod-uid-1", podCreated, "500m", "1Gi"))
			c.ObservePVC(testPVC("acme", "data-0", "pvc-uid-1", pvcCreated, "10G"))
		}
		c.mu.Lock()
		for _, tr := range c.res {
			if len(tr.Transitions) != 1 {
				c.mu.Unlock()
				t.Fatalf("%s/%s accumulated %d transitions without ever resizing", tr.Kind, tr.Name, len(tr.Transitions))
			}
			if !withTransitions {
				tr.Transitions = nil
			}
		}
		c.mu.Unlock()
		if _, err := c.EmitOrg(context.Background(), "acme"); err != nil {
			t.Fatal(err)
		}
		src := repo.sourcesOf(cust.ID)[0]
		b, err := json.Marshal(sortedRecords(repo.usageRecords(src.ID)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	with, without := build(true), build(false)
	if string(with) != string(without) {
		t.Fatalf("a workload that never resizes changed shape:\n with transitions: %s\n without:          %s", with, without)
	}
	if len(with) == 0 || string(with) == "[]" || string(with) == "null" {
		t.Fatal("the comparison ran on no records at all")
	}
}

// TestResizeSplitUsesTheSharedWindowMath: the split is the cloud collector's
// window math, not a second implementation. A size token is a transition
// flavour, so the same helper that bills an ECS resize half at each flavour
// bills the pod — and a token round-trips exactly.
func TestResizeSplitUsesTheSharedWindowMath(t *testing.T) {
	tr := &trackedResource{Kind: "pod", VCPU: 0.5, MemGiB: 1.5}
	got, ok := parseShape(shapeOf(tr))
	if !ok || got.vcpu != 0.5 || got.mem != 1.5 {
		t.Fatalf("shape round-trip = %+v ok=%v", got, ok)
	}
	pvc := &trackedResource{Kind: "pvc", PVCGB: 10}
	if got, ok := parseShape(shapeOf(pvc)); !ok || got.pvc != 10 {
		t.Fatalf("pvc shape round-trip = %+v ok=%v", got, ok)
	}
	// An unreadable or absent token falls back to the last observed size,
	// so a resource tracked before sizes were recorded still bills.
	if _, ok := parseShape(""); ok {
		t.Fatal("an empty token must not decode")
	}
	lines := platformSKUs(tr, "not a shape")
	if len(lines) != 2 || lines[0].factor != 0.5 || lines[1].factor != 1.5 {
		t.Fatalf("fallback lines = %+v", lines)
	}
}
