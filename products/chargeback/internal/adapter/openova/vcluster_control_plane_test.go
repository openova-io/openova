package openova

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
)

// The Organization's vCluster control plane runs in the Organization's own host
// namespace (#6902) and used to be metered as if the customer had bought it:
// vcluster-0 (500m / 1Gi), the synced coredns (20m / 64Mi) and the 5Gi
// backing-store PVC all became k8s.* rows on the customer's source. The
// org-controller sizes the namespace quota as plan + that control plane so it
// never eats the plan; the meters must draw the same line.
func TestVClusterControlPlaneIsNotMetered(t *testing.T) {
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	created := now.Add(-90 * time.Minute)
	c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	c.SetOverheadOrg("sov")
	c.ObserveNamespace(orgNamespace("acme"))

	// The control plane, labelled the way the loft-sh chart and the syncer
	// label it live (dashboard_vcluster_empty_5932_test.go carries the
	// measured vcluster-0 label set).
	vc0 := testPod("acme", "vcluster-0", "pod-vc0", created, "500m", "1Gi")
	vc0.Labels = map[string]string{"app": "vcluster", "release": "vcluster", "statefulset.kubernetes.io/pod-name": "vcluster-0"}
	c.ObservePod(vc0)
	dns := testPod("acme", "coredns-7d4b8c9f-x-kube-system-x-vcluster", "pod-dns", created, "20m", "64Mi")
	dns.Labels = map[string]string{"vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "kube-system", "k8s-app": "vcluster-kube-dns"}
	c.ObservePod(dns)
	vc0PVC := testPVC("acme", "data-vcluster-0", "pvc-vc0", created, "5Gi")
	vc0PVC.Labels = map[string]string{"app": "vcluster", "release": "vcluster"}
	c.ObservePVC(vc0PVC)

	// The customer's workloads, mirrored down from the customer's own virtual
	// namespace — metered as before.
	wp := testPod("acme", "wordpress-0-x-acme-x-vcluster", "pod-wp", created, "250m", "512Mi")
	wp.Labels = map[string]string{"vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "acme", "app.kubernetes.io/name": "wordpress"}
	c.ObservePod(wp)
	// Control on the exact-value rule: a customer pod that happens to carry
	// app=vcluster is still a synced customer pod, not the control plane.
	odd := testPod("acme", "vcluster-lookalike-x-acme-x-vcluster", "pod-odd", created, "100m", "128Mi")
	odd.Labels = map[string]string{"app": "vcluster", "vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "acme"}
	c.ObservePod(odd)
	wpPVC := testPVC("acme", "data-wordpress-0-x-acme-x-vcluster", "pvc-wp", created, "10G")
	wpPVC.Labels = map[string]string{"vcluster.loft.sh/managed-by": "vcluster"}
	c.ObservePVC(wpPVC)

	if _, err := c.EmitOrg(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	src := repo.sourcesOf(cust.ID)[0]
	byResource := map[string]bool{}
	var vcpuFullHour float64
	for _, r := range repo.usageRecords(src.ID) {
		byResource[r.ResourceID] = true
		if r.SKU == SKUVCPU && r.WindowStart.Equal(time.Date(2026, 9, 11, 11, 0, 0, 0, time.UTC)) {
			q, err := strconv.ParseFloat(string(r.Quantity), 64)
			if err != nil {
				t.Fatal(err)
			}
			vcpuFullHour += q
		}
	}
	for _, want := range []string{"pod/pod-wp", "pod/pod-odd", "pvc/pvc-wp"} {
		if !byResource[want] {
			t.Errorf("customer resource %s produced no usage — the exclusion is over-reaching", want)
		}
	}
	for _, control := range []string{"pod/pod-vc0", "pod/pod-dns", "pvc/pvc-vc0"} {
		if byResource[control] {
			t.Errorf("vCluster control plane %s was metered on the customer's source — the control plane is overhead the Sovereign pays, not usage the customer bought", control)
		}
	}
	// The 11:00 vCPU hour is the customer's 0.25 + 0.10, not 0.87 with the
	// control plane's 0.52 folded in.
	if vcpuFullHour < 0.349999 || vcpuFullHour > 0.350001 {
		t.Errorf("11:00 k8s.vcpu total = %.6f, want 0.35 (wordpress 0.25 + lookalike 0.10; the control plane's 0.52 must be absent)", vcpuFullHour)
	}
}

// The platform-overhead line is where the control plane DOES belong: the
// Sovereign pays for it, and the line must reconcile back to the cloud total
// (#6850). Both an unlabelled platform namespace and the Sovereign's own
// labelled Organization namespace keep counting vcluster-0.
func TestVClusterControlPlaneCountsOnTheOverheadLine(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	c := &PlatformCollector{Now: func() time.Time { return now }}
	c.SetOverheadOrg("sov")
	c.ObserveNamespace(ns("mgmt", nil))
	c.ObserveNamespace(ns("sov", map[string]string{orgLabel: "sov"}))
	c.ObserveNamespace(orgNamespace("acme"))

	for _, nsName := range []string{"mgmt", "sov", "acme"} {
		p := testPod(nsName, "vcluster-0", "pod-vc0-"+nsName, now.Add(-time.Hour), "500m", "1Gi")
		p.Labels = map[string]string{"app": "vcluster", "release": "vcluster"}
		c.ObservePod(p)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, nsName := range []string{"mgmt", "sov"} {
		if _, tracked := c.res["pod/pod-vc0-"+nsName]; !tracked {
			t.Errorf("%s/vcluster-0 not tracked — the platform-overhead line must carry the Sovereign's own vCluster control planes or it cannot reconcile to the cloud total", nsName)
		}
	}
	if _, tracked := c.res["pod/pod-vc0-acme"]; tracked {
		t.Error("acme/vcluster-0 tracked — a customer Organization's control plane must stay off its meters")
	}
}

// TestIsVClusterControlPlane pins the predicate on the label shapes measured
// live, plus the near-misses that must NOT match.
func TestIsVClusterControlPlane(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		annos  map[string]string
		want   bool
	}{
		{"vcluster-0 (chart labels)", map[string]string{"app": "vcluster", "release": "vcluster"}, nil, true},
		{"data-vcluster-0 PVC (selector labels)", map[string]string{"app": "vcluster", "release": "vcluster"}, nil, true},
		{"synced coredns by label", map[string]string{"vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "kube-system"}, nil, true},
		{"synced coredns by annotation only", map[string]string{"vcluster.loft.sh/managed-by": "vcluster"}, map[string]string{"vcluster.loft.sh/object-namespace": "kube-system"}, true},
		{"synced customer pod", map[string]string{"vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "acme"}, nil, false},
		{"synced customer pod carrying app=vcluster", map[string]string{"app": "vcluster", "vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "acme"}, nil, false},
		{"app=vcluster-operator near-miss", map[string]string{"app": "vcluster-operator"}, nil, false},
		{"plain customer pod", map[string]string{"app.kubernetes.io/name": "wordpress"}, nil, false},
		{"no labels", nil, nil, false},
	}
	for _, tc := range cases {
		if got := isVClusterControlPlane(tc.labels, tc.annos); got != tc.want {
			t.Errorf("%s: isVClusterControlPlane = %v, want %v", tc.name, got, tc.want)
		}
	}
}
