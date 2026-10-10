package openova

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The LIMIT meters (DESIGN.md §22.11): for an Organization on a sized
// package the collector writes the pods' CPU and memory LIMITS beside the
// request meters, on the same hour slices — a container with no limit
// counted at its request — and for a flexi Organization it writes none.
func TestPlatformCollectorLimitMeters(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	pod := testPod("acme", "web-0", "pod-1", time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC), "500m", "1Gi")
	pod.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")}
	// A sidecar with requests only: its limit is its request.
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "side", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}})

	for _, c := range []struct {
		plan       string
		wantLimits bool
	}{{"m", true}, {"flexi", false}} {
		repo := newFakeRepo()
		cust := repo.addPlanCustomer("acme", c.plan, time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC))
		pc := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
		pc.ObserveNamespace(orgNamespace("acme"))
		pc.ObservePod(pod)
		if _, err := pc.EmitOrg(context.Background(), "acme"); err != nil {
			t.Fatal(err)
		}
		m := recordMap(repo.usageRecords(repo.sourcesOf(cust.ID)[0].ID))
		if r := m[SKUVCPU+"|11:00"]; string(r.Quantity) != "0.750000" {
			t.Fatalf("%s: request vcpu = %v, want 0.75", c.plan, r)
		}
		lv, hasV := m[store.SKUVCPULimit+"|11:00"]
		lm, hasM := m[store.SKUMemLimit+"|11:00"]
		if !c.wantLimits {
			if hasV || hasM {
				t.Fatalf("flexi carries limit meters: %v %v", lv, lm)
			}
			continue
		}
		// 2 + 0.25 vCPU and 4 + 0.5 GiB for the hour.
		if string(lv.Quantity) != "2.250000" || lv.Unit != store.UnitVCPU || string(lm.Quantity) != "4.500000" || lm.Unit != store.UnitMem {
			t.Fatalf("limit meters = %+v / %+v, want 2.25 vcpu-hour and 4.5 gib-hour", lv, lm)
		}
	}
}

// The size token carries the limits only where they differ from the
// requests, so a pod sized requests == limits keeps the token it had.
func TestShapeTokenCarriesLimitsOnlyWhenTheyDiffer(t *testing.T) {
	same := &trackedResource{Kind: "pod", VCPU: 1, MemGiB: 2, LimitVCPU: 1, LimitMemGiB: 2}
	if got := shapeOf(same); got != "cpu=1,mem=2" {
		t.Fatalf("token = %q", got)
	}
	diff := &trackedResource{Kind: "pod", VCPU: 0.5, MemGiB: 1, LimitVCPU: 2, LimitMemGiB: 4}
	tok := shapeOf(diff)
	if tok != "cpu=0.5,mem=1,lcpu=2,lmem=4" {
		t.Fatalf("token = %q", tok)
	}
	lines := platformSKUs(&trackedResource{Kind: "pod"}, tok, true)
	if len(lines) != 4 || lines[2].factor != 2 || lines[3].factor != 4 {
		t.Fatalf("lines from %q = %+v", tok, lines)
	}
	// An older token with no limits bills the limits at the requests.
	lines = platformSKUs(&trackedResource{Kind: "pod"}, "cpu=0.5,mem=1", true)
	if lines[2].factor != 0.5 || lines[3].factor != 1 {
		t.Fatalf("lines from a request-only token = %+v", lines)
	}
}
