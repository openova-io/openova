package gitops

import (
	"strings"
	"testing"
)

// #4297 (keystone of EPIC #4293) — per-Org apps land INSIDE the Org vCluster,
// TIER-AWARE. The funnel's apps-sync Flux Kustomization carries
// spec.kubeConfig.secretRef ONLY for the vcluster tier (paid M+), so the host
// Flux reconciles the apps tree INTO the Org vCluster apiserver. For the host
// tier (free/S) there is NO vcluster — emitting a kubeConfig referencing the
// never-created `vc-vcluster` mirror would StateError forever, so the apps-sync
// omits kubeConfig and the apps reconcile straight into the host `<slug>` ns.

const testBasePath = "clusters/sov/tenants"

func appsSyncFor(t *testing.T, slug, planSlug string) string {
	t.Helper()
	g := NewManifestGenerator(testBasePath)
	out := g.GenerateAllWithAppConfigs(slug, planSlug, []string{"wordpress"}, "pw", nil)
	path := testBasePath + "/" + slug + "/apps-sync.yaml"
	body, ok := out[path]
	if !ok {
		t.Fatalf("apps-sync.yaml missing for slug=%q plan=%q (keys: %v)", slug, planSlug, keys(out))
	}
	return body
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestAppsSync_VclusterTier_HasKubeConfig — a paid (M) tier Org's apps-sync
// Kustomization carries the kubeConfig secretRef so the apps land inside the
// vcluster. targetNamespace stays the org-controller `<slug>` ns.
func TestAppsSync_VclusterTier_HasKubeConfig(t *testing.T) {
	body := appsSyncFor(t, "acme", "m")
	if !strings.Contains(body, "kubeConfig:") {
		t.Errorf("vcluster tier apps-sync MISSING kubeConfig block:\n%s", body)
	}
	if !strings.Contains(body, "name: tenant-acme-kubeconfig") {
		t.Errorf("vcluster tier apps-sync MISSING the kubeconfig mirror secretRef name:\n%s", body)
	}
	if !strings.Contains(body, "key: config") {
		t.Errorf("vcluster tier apps-sync MISSING secretRef key: config:\n%s", body)
	}
	if !strings.Contains(body, "targetNamespace: acme") {
		t.Errorf("vcluster tier apps-sync targetNamespace is not <slug> acme:\n%s", body)
	}
}

// TestAppsSync_HostTier_NoKubeConfig — free/S/"" tier Orgs have no vcluster, so
// the apps-sync carries NO kubeConfig (apps reconcile into the host `<slug>` ns
// directly). The apps tree must still render.
func TestAppsSync_HostTier_NoKubeConfig(t *testing.T) {
	for _, plan := range []string{"s", "free", ""} {
		t.Run("plan="+plan, func(t *testing.T) {
			body := appsSyncFor(t, "acme", plan)
			if strings.Contains(body, "kubeConfig:") {
				t.Errorf("host tier (plan=%q) apps-sync MUST NOT carry kubeConfig — it would StateError on the never-created vc-vcluster mirror:\n%s", plan, body)
			}
			if strings.Contains(body, "tenant-acme-kubeconfig") {
				t.Errorf("host tier (plan=%q) apps-sync MUST NOT reference the kubeconfig mirror:\n%s", plan, body)
			}
			// Still targets the host `<slug>` ns + points at the apps path.
			if !strings.Contains(body, "targetNamespace: acme") {
				t.Errorf("host tier (plan=%q) apps-sync targetNamespace is not <slug> acme:\n%s", plan, body)
			}
			if !strings.Contains(body, "path: ./"+testBasePath+"/acme/apps") {
				t.Errorf("host tier (plan=%q) apps-sync path wrong:\n%s", plan, body)
			}

			// The apps tree itself must still render so apps actually deploy.
			g := NewManifestGenerator(testBasePath)
			out := g.GenerateAllWithAppConfigs("acme", plan, []string{"wordpress"}, "pw", nil)
			if _, ok := out[testBasePath+"/acme/apps/app-wordpress.yaml"]; !ok {
				t.Errorf("host tier (plan=%q) MISSING app-wordpress.yaml (keys: %v)", plan, keys(out))
			}
		})
	}
}

// TestBoundaryIsVcluster_FunnelParity locks the funnel's tier gate in lockstep
// with the org-controller's authoritative boundaryIsVcluster gate (#4292,
// core/controllers/organization/internal/gitops/manifests.go). The two are
// intentional duplicates (Go internal/ package boundary); this table guards
// against silent drift.
func TestBoundaryIsVcluster_FunnelParity(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"s":     false,
		"S":     false,
		"free":  false,
		" s ":   false,
		"m":     true,
		"l":     true,
		"xl":    true,
		"flexi": true,
		"M":     true,
	}
	for plan, want := range cases {
		if got := BoundaryIsVcluster(plan); got != want {
			t.Errorf("BoundaryIsVcluster(%q) = %v, want %v", plan, got, want)
		}
	}
}

// TestCNPGPair_VclusterTier_HRLevelKubeConfig — the active-hot-standby
// bp-cnpg-pair HelmRelease is a HelmRelease CR, so it cannot be redirected into
// the vcluster by the apps-sync Kustomization (no in-cluster helm-controller →
// #3055 StateError). For the vcluster tier it must be a HOST file carrying
// HR-level spec.kubeConfig (the host helm-controller installs INTO the vcluster).
func TestCNPGPair_VclusterTier_HRLevelKubeConfig(t *testing.T) {
	g := NewManifestGenerator(testBasePath)
	out := g.GenerateAllWithAppConfigs("acme", "m",
		[]string{"umami"}, "pw",
		map[string]map[string]any{
			"postgres": {
				"active_hot_standby": true,
				"primary_region":     "hz-fsn-rtz-prod",
				"replica_region":     "hz-hel-rtz-prod",
			},
		},
	)

	// The HR lives as a HOST file (NOT under apps/) so the host helm-controller
	// reconciles it.
	hostHR, ok := out[testBasePath+"/acme/db-cnpg-pair.yaml"]
	if !ok {
		t.Fatalf("vcluster-tier CNPG-pair HR not emitted as a HOST file (keys: %v)", keys(out))
	}
	if _, inApps := out[testBasePath+"/acme/apps/db-cnpg-pair.yaml"]; inApps {
		t.Errorf("CNPG-pair HR must NOT be in the vcluster-redirected apps/ tree (it would StateError)")
	}
	if !strings.Contains(hostHR, "kind: HelmRelease") {
		t.Errorf("host CNPG-pair file missing HelmRelease:\n%s", hostHR)
	}
	if !strings.Contains(hostHR, "kubeConfig:") {
		t.Errorf("vcluster-tier CNPG-pair HR MISSING HR-level kubeConfig:\n%s", hostHR)
	}
	if !strings.Contains(hostHR, "name: tenant-acme-kubeconfig") {
		t.Errorf("vcluster-tier CNPG-pair HR kubeConfig must reference the mirror secret:\n%s", hostHR)
	}
	// HR authored in flux-system so the secretRef (no namespace field) resolves
	// against the co-located mirror.
	if !strings.Contains(hostHR, "namespace: flux-system") {
		t.Errorf("vcluster-tier CNPG-pair HR must be authored in flux-system (mirror ns):\n%s", hostHR)
	}
	// Chart installs into the in-vcluster `<slug>` ns where the app pods live.
	if !strings.Contains(hostHR, "targetNamespace: acme") {
		t.Errorf("CNPG-pair chart targetNamespace must be the Org <slug> ns acme:\n%s", hostHR)
	}

	// The standalone postgres-credentials Secret the app pods read lives INSIDE
	// the vcluster (apps/ tree), NOT on the host.
	secret, ok := out[testBasePath+"/acme/apps/db-cnpg-pair-secret.yaml"]
	if !ok {
		t.Fatalf("CNPG-pair postgres-credentials Secret not emitted into apps/ tree (keys: %v)", keys(out))
	}
	if !strings.Contains(secret, "kind: Secret") || !strings.Contains(secret, "name: postgres-credentials") {
		t.Errorf("apps-tree CNPG secret wrong shape:\n%s", secret)
	}
	if strings.Contains(secret, "kind: HelmRelease") {
		t.Errorf("apps-tree CNPG secret must NOT carry the HelmRelease (that lives on the host):\n%s", secret)
	}
}

// TestCNPGPair_HostTier_NoKubeConfig — for the host tier the CNPG-pair HR has
// no vcluster to target, so it carries NO kubeConfig and is authored in the
// host `<slug>` ns where the chart installs.
func TestCNPGPair_HostTier_NoKubeConfig(t *testing.T) {
	g := NewManifestGenerator(testBasePath)
	out := g.GenerateAllWithAppConfigs("acme", "s",
		[]string{"umami"}, "pw",
		map[string]map[string]any{
			"postgres": {
				"active_hot_standby": true,
				"primary_region":     "hz-fsn-rtz-prod",
				"replica_region":     "hz-hel-rtz-prod",
			},
		},
	)
	hostHR, ok := out[testBasePath+"/acme/db-cnpg-pair.yaml"]
	if !ok {
		t.Fatalf("host-tier CNPG-pair HR not emitted (keys: %v)", keys(out))
	}
	if strings.Contains(hostHR, "kubeConfig:") {
		t.Errorf("host-tier CNPG-pair HR MUST NOT carry kubeConfig (no vcluster):\n%s", hostHR)
	}
	if !strings.Contains(hostHR, "namespace: acme") {
		t.Errorf("host-tier CNPG-pair HR must be authored in the host <slug> ns acme:\n%s", hostHR)
	}
}
