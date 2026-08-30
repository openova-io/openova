package openova

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func orgNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{orgLabel: name},
	}}
}

func testPod(ns, name, uid string, created time.Time, cpu, mem string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         ns,
			Name:              name,
			UID:               types.UID(uid),
			CreationTimestamp: metav1.Time{Time: created},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(mem),
			}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func testPVC(ns, name, uid string, created time.Time, size string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         ns,
			Name:              name,
			UID:               types.UID(uid),
			CreationTimestamp: metav1.Time{Time: created},
		},
		Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
		}},
	}
}

func recordMap(recs []store.UsageRecord) map[string]store.UsageRecord {
	out := map[string]store.UsageRecord{}
	for _, r := range recs {
		out[r.SKU+"|"+r.WindowStart.UTC().Format("15:04")] = r
	}
	return out
}

// TestPlatformCollectorHourSlices: pods and PVCs turn into k8s.vcpu /
// k8s.mem_gb / k8s.pvc_gb records sliced exactly like the huawei collector
// slices hours: bounded by creation, never crossing a UTC hour boundary,
// quantity = requests × hours.
func TestPlatformCollectorHourSlices(t *testing.T) {
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	c.ObserveNamespace(orgNamespace("acme"))
	c.ObservePod(testPod("acme", "web-0", "pod-uid-1", time.Date(2026, 8, 31, 10, 45, 0, 0, time.UTC), "500m", "1Gi"))
	c.ObservePVC(testPVC("acme", "data-0", "pvc-uid-1", time.Date(2026, 8, 31, 9, 30, 0, 0, time.UTC), "10G"))

	ctx := context.Background()
	n, err := c.EmitOrg(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	srcs := repo.sourcesOf(cust.ID)
	if len(srcs) != 1 || srcs[0].Kind != SourceKindOrg || srcs[0].Status != "verified" {
		t.Fatalf("auto-created platform source = %+v", srcs)
	}
	src := srcs[0]
	if src.LastCollectedAt == nil || !src.LastCollectedAt.Equal(now) {
		t.Fatalf("last_collected_at = %v, want %v", src.LastCollectedAt, now)
	}
	recs := repo.usageRecords(src.ID)
	if n != 7 || len(recs) != 7 {
		t.Fatalf("records = %d (upserted %d), want 7: pod 2 slices × 2 SKUs + pvc 3 slices", len(recs), n)
	}
	m := recordMap(recs)
	want := map[string]string{
		SKUVCPU + "|10:45": "0.125000", // 0.25 h × 0.5 vcpu
		SKUVCPU + "|11:00": "0.500000", // 1 h × 0.5 vcpu
		SKUMem + "|10:45":  "0.250000", // 0.25 h × 1 GiB
		SKUMem + "|11:00":  "1.000000",
		SKUPVC + "|09:30":  "5.000000", // 0.5 h × 10 GB
		SKUPVC + "|10:00":  "10.000000",
		SKUPVC + "|11:00":  "10.000000",
	}
	for k, q := range want {
		r, ok := m[k]
		if !ok {
			t.Fatalf("record %s absent; have %v", k, keysOf(m))
		}
		if string(r.Quantity) != q {
			t.Fatalf("%s quantity = %s, want %s", k, r.Quantity, q)
		}
		if r.CustomerID != cust.ID || r.SourceID != src.ID {
			t.Fatalf("%s attribution = %s/%s", k, r.CustomerID, r.SourceID)
		}
	}
	for _, r := range recs {
		switch r.SKU {
		case SKUVCPU:
			if r.Unit != UnitVCPU {
				t.Fatalf("unit = %s", r.Unit)
			}
		case SKUMem:
			if r.Unit != UnitMem {
				t.Fatalf("unit = %s", r.Unit)
			}
		case SKUPVC:
			if r.Unit != UnitPVC {
				t.Fatalf("unit = %s", r.Unit)
			}
		}
	}
}

func keysOf(m map[string]store.UsageRecord) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPlatformCollectorDeletionClosesTheWindow: a deleted pod bills to its
// deletion instant and not an hour longer; the next pass emits from the
// previous collection stamp (event-driven correction, D3a).
func TestPlatformCollectorDeletionClosesTheWindow(t *testing.T) {
	repo := newFakeRepo()
	repo.addActiveCustomer("acme")
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	c.ObserveNamespace(orgNamespace("acme"))
	pod := testPod("acme", "web-0", "pod-uid-1", time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC), "1", "1Gi")
	c.ObservePod(pod)
	ctx := context.Background()
	if _, err := c.EmitOrg(ctx, "acme"); err != nil {
		t.Fatal(err)
	}

	// The pod dies at 12:30; the collector hears about it and re-emits.
	now = time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	deleted := pod.DeepCopy()
	deleted.DeletionTimestamp = &metav1.Time{Time: time.Date(2026, 8, 31, 12, 30, 0, 0, time.UTC)}
	c.ObservePodDeleted(deleted)
	if _, err := c.EmitOrg(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	cust, _ := repo.customerBySlug("acme")
	src := repo.sourcesOf(cust.ID)[0]
	m := recordMap(repo.usageRecords(src.ID))
	r, ok := m[SKUVCPU+"|12:00"]
	if !ok {
		t.Fatalf("final slice missing; have %v", keysOf(m))
	}
	if string(r.Quantity) != "0.500000" {
		t.Fatalf("final slice quantity = %s, want 0.500000 (30 min × 1 vcpu)", r.Quantity)
	}
	if !r.WindowEnd.Equal(time.Date(2026, 8, 31, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("final slice window_end = %v, want the deletion instant", r.WindowEnd)
	}
	if _, leak := m[SKUVCPU+"|12:30"]; leak {
		t.Fatal("billing continued past the deletion")
	}
}

// TestPlatformCollectorInformerWiring: the real informer path against the
// fake clientset — namespace label discovery, pod replay, emission.
func TestPlatformCollectorInformerWiring(t *testing.T) {
	created := time.Now().UTC().Add(-90 * time.Minute).Truncate(time.Second)
	client := k8sfake.NewSimpleClientset(
		orgNamespace("acme"),
		testPod("acme", "web-0", "pod-uid-1", created, "250m", "512Mi"),
		testPVC("acme", "data-0", "pvc-uid-1", created, "5G"),
	)
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	c := &PlatformCollector{Client: client, Repo: repo, Metrics: metrics.New(), Debounce: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitFor(t, 5*time.Second, "usage emitted through the informer path", func() bool {
		srcs := repo.sourcesOf(cust.ID)
		if len(srcs) == 0 {
			return false
		}
		return len(repo.usageRecords(srcs[0].ID)) > 0
	})
	srcs := repo.sourcesOf(cust.ID)
	skus := map[string]bool{}
	for _, r := range repo.usageRecords(srcs[0].ID) {
		skus[r.SKU] = true
	}
	for _, want := range []string{SKUVCPU, SKUMem, SKUPVC} {
		if !skus[want] {
			t.Fatalf("sku %s absent; have %v", want, skus)
		}
	}
}
