package openova

import (
	"context"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
)

func labelled[T interface {
	*corev1.Pod | *corev1.PersistentVolumeClaim
}](obj T, labels map[string]string) T {
	switch o := any(obj).(type) {
	case *corev1.Pod:
		o.Labels = labels
	case *corev1.PersistentVolumeClaim:
		o.Labels = labels
	}
	return obj
}

// The per-Organization platform stack — bp-keycloak (plus its postgresql),
// bp-newapi (plus its CNPG postgresql), bp-openclaw, bp-agenity (plus its
// oidc-gate) — is installed for EVERY Organization, and the org-controller
// sizes the namespace ResourceQuota as plan + control plane + this stack
// (core/controllers/organization/internal/gitops/manifests.go platformStack).
// It is overhead the Sovereign delivers, not usage the customer bought, so the
// meters must draw the same line the quota does. Measured on hw307 (Acme Walk,
// plan S, 2026-09-10): keycloak 1 CPU / 2Gi, its postgresql 500m / 512Mi,
// agenity 1005m / 2064Mi and the oidc-gate 50m / 64Mi were all k8s.* rows on
// the customer's source.
func TestPlatformStackIsNotMetered(t *testing.T) {
	repo := newFakeRepo()
	cust := repo.addActiveCustomer("acme")
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	created := now.Add(-90 * time.Minute)
	c := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return now }}
	c.SetOverheadOrg("sov")
	c.ObserveNamespace(orgNamespace("acme"))

	// The stack, labelled the way the charts label it live.
	stack := []*corev1.Pod{
		labelled(testPod("acme", "bp-keycloak-0", "pod-kc", created, "1", "2Gi"),
			map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "keycloak"}),
		labelled(testPod("acme", "bp-keycloak-postgresql-0", "pod-kcpg", created, "500m", "512Mi"),
			map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "postgresql"}),
		labelled(testPod("acme", "bp-newapi-6867df99bd-k2x9p", "pod-na", created, "535m", "352Mi"),
			map[string]string{"app.kubernetes.io/instance": "bp-newapi", "app.kubernetes.io/name": "bp-newapi"}),
		// The CNPG operator labels newapi's database pod with the Cluster name
		// the chart fixes (<fullname>-newapi-pg) and no Helm label.
		labelled(testPod("acme", "bp-newapi-newapi-pg-1", "pod-napg", created, "500m", "512Mi"),
			map[string]string{"cnpg.io/cluster": "bp-newapi-newapi-pg", "cnpg.io/podRole": "instance"}),
		labelled(testPod("acme", "bp-openclaw-7f9c4d-x8s2q", "pod-oc", created, "250m", "512Mi"),
			map[string]string{"app.kubernetes.io/instance": "bp-openclaw", "app.kubernetes.io/name": "bp-openclaw"}),
		labelled(testPod("acme", "bp-agenity-0", "pod-ag", created, "1005m", "2064Mi"),
			map[string]string{"app.kubernetes.io/instance": "bp-agenity", "app.kubernetes.io/name": "bp-agenity"}),
		// The bp-agenity chart's own oidc-gate Deployment: name label only.
		labelled(testPod("acme", "oidc-gate-agenity-acme-5d8f7-q2m4v", "pod-gate", created, "50m", "64Mi"),
			map[string]string{"app.kubernetes.io/name": "bp-oidc-gate", "catalyst.openova.io/component": "oidc-gate-agenity-acme"}),
		// The funnel door installs the same stack INTO the vCluster under
		// releaseName agenity; the syncer mirrors the pod to the host.
		labelled(testPod("acme", "agenity-0-x-acme-x-vcluster", "pod-ag-funnel", created, "1005m", "2064Mi"),
			map[string]string{"app.kubernetes.io/instance": "agenity", "app.kubernetes.io/name": "bp-agenity",
				"vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "acme"}),
	}
	for _, p := range stack {
		c.ObservePod(p)
	}
	// Controls: the customer's own workloads stay metered — the purchased
	// WordPress synced from the vCluster, the purchased Stalwart the BSS door
	// installs host-side, a customer's own CNPG database, and a near-miss on
	// the exact-value release rule.
	customer := []*corev1.Pod{
		labelled(testPod("acme", "wordpress-0-x-acme-x-vcluster", "pod-wp", created, "250m", "512Mi"),
			map[string]string{"app.kubernetes.io/instance": "wordpress", "app.kubernetes.io/name": "wordpress",
				"vcluster.loft.sh/managed-by": "vcluster", "vcluster.loft.sh/namespace": "acme"}),
		labelled(testPod("acme", "bp-stalwart-tenant-0", "pod-mail", created, "200m", "256Mi"),
			map[string]string{"app.kubernetes.io/instance": "bp-stalwart-tenant", "app.kubernetes.io/name": "stalwart"}),
		labelled(testPod("acme", "shop-pg-1", "pod-shoppg", created, "100m", "128Mi"),
			map[string]string{"cnpg.io/cluster": "shop-pg", "cnpg.io/podRole": "instance"}),
		labelled(testPod("acme", "bp-keycloak-lookalike-0", "pod-odd", created, "50m", "64Mi"),
			map[string]string{"app.kubernetes.io/instance": "bp-keycloak-lookalike", "app.kubernetes.io/name": "keycloak"}),
	}
	for _, p := range customer {
		c.ObservePod(p)
	}
	// PVCs: the stack's labelled claims are excluded, the customer's metered.
	c.ObservePVC(labelled(testPVC("acme", "data-bp-keycloak-postgresql-0", "pvc-kcpg", created, "8Gi"),
		map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "postgresql"}))
	c.ObservePVC(labelled(testPVC("acme", "bp-newapi-newapi-pg-1", "pvc-napg", created, "5Gi"),
		map[string]string{"cnpg.io/cluster": "bp-newapi-newapi-pg", "cnpg.io/pvcRole": "PG_DATA"}))
	c.ObservePVC(labelled(testPVC("acme", "data-wordpress-0-x-acme-x-vcluster", "pvc-wp", created, "10G"),
		map[string]string{"vcluster.loft.sh/managed-by": "vcluster"}))

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
	if len(byResource) == 0 {
		t.Fatal("vacuity: no usage records at all — the exclusion swallowed the customer too")
	}
	for _, want := range []string{"pod/pod-wp", "pod/pod-mail", "pod/pod-shoppg", "pod/pod-odd", "pvc/pvc-wp"} {
		if !byResource[want] {
			t.Errorf("customer resource %s produced no usage — the exclusion is over-reaching", want)
		}
	}
	for _, control := range []string{"pod/pod-kc", "pod/pod-kcpg", "pod/pod-na", "pod/pod-napg", "pod/pod-oc", "pod/pod-ag", "pod/pod-gate", "pod/pod-ag-funnel", "pvc/pvc-kcpg", "pvc/pvc-napg"} {
		if byResource[control] {
			t.Errorf("platform-stack resource %s was metered on the customer's source — the stack is overhead the Sovereign delivers, not usage the customer bought", control)
		}
	}
	// The 11:00 vCPU hour is the customer's 0.25 + 0.20 + 0.10 + 0.05, not
	// 5.445 with the stack's 4.845 folded in.
	if vcpuFullHour < 0.599999 || vcpuFullHour > 0.600001 {
		t.Errorf("11:00 k8s.vcpu total = %.6f, want 0.60 (wordpress 0.25 + stalwart 0.20 + shop-pg 0.10 + lookalike 0.05; the stack's 4.845 must be absent)", vcpuFullHour)
	}
}

// The platform-overhead line is where the stack DOES belong: the Sovereign's
// own namespaces keep their keycloak / newapi / agenity pods so that line still
// reconciles back to the cloud total (#6850). Both an unlabelled platform
// namespace and the Sovereign's own labelled Organization keep counting them; a
// customer Organization does not.
func TestPlatformStackCountsOnTheOverheadLine(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	c := &PlatformCollector{Now: func() time.Time { return now }}
	c.SetOverheadOrg("sov")
	c.ObserveNamespace(ns("keycloak", nil))
	c.ObserveNamespace(ns("sov", map[string]string{orgLabel: "sov"}))
	c.ObserveNamespace(orgNamespace("acme"))

	for _, nsName := range []string{"keycloak", "sov", "acme"} {
		c.ObservePod(labelled(testPod(nsName, "bp-keycloak-0", "pod-kc-"+nsName, now.Add(-time.Hour), "1", "2Gi"),
			map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "keycloak"}))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, nsName := range []string{"keycloak", "sov"} {
		if _, tracked := c.res["pod/pod-kc-"+nsName]; !tracked {
			t.Errorf("%s/bp-keycloak-0 not tracked — the platform-overhead line must carry the Sovereign's own platform pods or it cannot reconcile to the cloud total", nsName)
		}
	}
	if _, tracked := c.res["pod/pod-kc-acme"]; tracked {
		t.Error("acme/bp-keycloak-0 tracked — a customer Organization's platform stack must stay off its meters")
	}
}

// TestIsPlatformStack pins the predicate on the label shapes the charts stamp,
// plus the near-misses that must NOT match.
func TestIsPlatformStack(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"bp-keycloak keycloak pod", map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "keycloak"}, true},
		{"bp-keycloak postgresql pod", map[string]string{"app.kubernetes.io/instance": "bp-keycloak", "app.kubernetes.io/name": "postgresql"}, true},
		{"bp-newapi (BSS door release)", map[string]string{"app.kubernetes.io/instance": "bp-newapi", "app.kubernetes.io/name": "bp-newapi"}, true},
		{"newapi (funnel door release)", map[string]string{"app.kubernetes.io/instance": "newapi", "app.kubernetes.io/name": "bp-newapi"}, true},
		{"bp-openclaw controller", map[string]string{"app.kubernetes.io/instance": "bp-openclaw", "app.kubernetes.io/name": "bp-openclaw"}, true},
		{"openclaw (funnel door release)", map[string]string{"app.kubernetes.io/instance": "openclaw", "app.kubernetes.io/name": "bp-openclaw"}, true},
		{"bp-agenity", map[string]string{"app.kubernetes.io/instance": "bp-agenity", "app.kubernetes.io/name": "bp-agenity"}, true},
		{"oidc-gate (name label only)", map[string]string{"app.kubernetes.io/name": "bp-oidc-gate", "catalyst.openova.io/component": "oidc-gate-agenity-acme"}, true},
		{"newapi CNPG pod (BSS door fullname)", map[string]string{"cnpg.io/cluster": "bp-newapi-newapi-pg", "cnpg.io/podRole": "instance"}, true},
		{"newapi CNPG pod (funnel door fullname)", map[string]string{"cnpg.io/cluster": "newapi-bp-newapi-newapi-pg"}, true},
		{"purchased wordpress (synced)", map[string]string{"app.kubernetes.io/instance": "wordpress", "app.kubernetes.io/name": "wordpress", "vcluster.loft.sh/managed-by": "vcluster"}, false},
		{"purchased stalwart (BSS door)", map[string]string{"app.kubernetes.io/instance": "bp-stalwart-tenant", "app.kubernetes.io/name": "stalwart"}, false},
		{"customer CNPG cluster", map[string]string{"cnpg.io/cluster": "shop-pg"}, false},
		{"release near-miss (prefix)", map[string]string{"app.kubernetes.io/instance": "bp-keycloak-lookalike"}, false},
		{"customer keycloak under another release", map[string]string{"app.kubernetes.io/instance": "sso", "app.kubernetes.io/name": "keycloak"}, false},
		{"name near-miss", map[string]string{"app.kubernetes.io/name": "oidc-gate"}, false},
		{"vcluster control plane (different predicate)", map[string]string{"app": "vcluster", "release": "vcluster"}, false},
		{"no labels", nil, false},
	}
	for _, tc := range cases {
		if got := isPlatformStack(tc.labels); got != tc.want {
			t.Errorf("%s: isPlatformStack = %v, want %v", tc.name, got, tc.want)
		}
	}
}
