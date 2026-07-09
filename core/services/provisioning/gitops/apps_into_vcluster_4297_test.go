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

// TestAppsSync_SourceRepo_DefaultsToHistoricalDefault — #4761. With no
// AppsSyncSourceRepo wiring the funnel-door `tenant-<slug>-apps` Kustomization's
// sourceRef must resolve to the post-#4798 default GitRepository name
// (openova-org-tenants) so every existing Sovereign renders byte-unchanged.
func TestAppsSync_SourceRepo_DefaultsToHistoricalDefault(t *testing.T) {
	for _, plan := range []string{"m", "s", ""} {
		t.Run("plan="+plan, func(t *testing.T) {
			body := appsSyncFor(t, "acme", plan)
			if !strings.Contains(body, "name: "+appsSyncSourceRepoDefault) {
				t.Errorf("empty AppsSyncSourceRepo must render the historical default sourceRef name %q:\n%s", appsSyncSourceRepoDefault, body)
			}
			// The literal default must equal the post-#4798 name (no silent drift).
			if appsSyncSourceRepoDefault != "openova-org-tenants" {
				t.Errorf("appsSyncSourceRepoDefault drifted from the post-#4798 funnel-door repo name: got %q", appsSyncSourceRepoDefault)
			}
		})
	}
}

// TestAppsSync_SourceRepo_Configurable — #4761. A Sovereign whose funnel-door
// apps GitRepository CR is named differently (per-bootstrap state) can point the
// sourceRef at it via CATALYST_APPS_SYNC_SOURCE_REPO (ManifestGenerator
// .AppsSyncSourceRepo) with no code change. Non-default config → non-default
// output; the historical default must NOT leak through.
func TestAppsSync_SourceRepo_Configurable(t *testing.T) {
	for _, plan := range []string{"m", "s"} {
		t.Run("plan="+plan, func(t *testing.T) {
			g := NewManifestGenerator(testBasePath)
			g.AppsSyncSourceRepo = "sovereign-org-tenants"
			out := g.GenerateAllWithAppConfigs("acme", plan, []string{"wordpress"}, "pw", nil)
			body := out[testBasePath+"/acme/apps-sync.yaml"]
			if !strings.Contains(body, "name: sovereign-org-tenants") {
				t.Errorf("apps-sync sourceRef must honour the overridden AppsSyncSourceRepo:\n%s", body)
			}
			// The default must not appear as the live sourceRef name when overridden.
			if strings.Contains(body, "name: "+appsSyncSourceRepoDefault) {
				t.Errorf("apps-sync sourceRef must not fall back to the default %q when AppsSyncSourceRepo is set:\n%s", appsSyncSourceRepoDefault, body)
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

// TestCNPGPair_PrimaryStaysHostSide_BothTiers is the #4293 BLOCKER-1 lock. The
// bp-cnpg-pair chart ships ONLY postgresql.cnpg.io/v1 Cluster CRs — the operator
// + CRD are cluster-singletons that live on the HOST (slot 16). A vcluster has
// neither the CRD nor a watching operator, so a primary Cluster `helm install`ed
// INTO the vcluster (the keystone's old HR-level kubeConfig shape) fails
// `no matches for kind "Cluster"` and the paid M+ active-hot-standby HA path
// WEDGES on every fresh prov. The PRIMARY side must therefore land on region A's
// HOST `<slug>` ns — with NO vcluster kubeConfig — for BOTH tiers. The in-vcluster
// app pods reach the DB via the synced `postgres` Service + the apps-tree
// credentials Secret.
func TestCNPGPair_PrimaryStaysHostSide_BothTiers(t *testing.T) {
	for _, plan := range []string{"m", "s"} {
		t.Run("plan="+plan, func(t *testing.T) {
			g := NewManifestGenerator(testBasePath)
			out := g.GenerateAllWithAppConfigs("acme", plan,
				[]string{"umami"}, "pw",
				map[string]map[string]any{
					"postgres": {
						"active_hot_standby": true,
						"primary_region":     "hz-fsn-rtz-prod",
						"replica_region":     "hz-hel-rtz-prod",
					},
				},
			)

			// The PRIMARY HR lives as a HOST file (NOT under apps/) so the host
			// helm-controller reconciles it where the cnpg operator + CRD live.
			hostHR, ok := out[testBasePath+"/acme/db-cnpg-pair-primary.yaml"]
			if !ok {
				t.Fatalf("CNPG-pair PRIMARY HR not emitted as a HOST file (keys: %v)", keys(out))
			}
			// Neither side may land in the vcluster-redirected apps/ tree (StateError).
			if _, inApps := out[testBasePath+"/acme/apps/db-cnpg-pair-primary.yaml"]; inApps {
				t.Errorf("CNPG-pair PRIMARY HR must NOT be in the vcluster-redirected apps/ tree (it would StateError)")
			}
			if _, inApps := out[testBasePath+"/acme/apps/db-cnpg-pair-replica.yaml"]; inApps {
				t.Errorf("CNPG-pair REPLICA HR must NOT be in the vcluster-redirected apps/ tree (it would StateError)")
			}
			if !strings.Contains(hostHR, "kind: HelmRelease") {
				t.Errorf("host CNPG-pair PRIMARY file missing HelmRelease:\n%s", hostHR)
			}
			if !strings.Contains(hostHR, "side: primary") {
				t.Errorf("PRIMARY HR must set cnpgPair.side: primary:\n%s", hostHR)
			}
			// BLOCKER-1: the PRIMARY HR must NOT carry an HR-level kubeConfig — the
			// Cluster CR has to reconcile on the HOST where the operator+CRD live.
			if strings.Contains(hostHR, "kubeConfig:") {
				t.Errorf("CNPG-pair PRIMARY HR (plan=%q) MUST NOT carry an HR-level kubeConfig — installing the Cluster CR into the vcluster fails on the missing postgresql.cnpg.io CRD (#4293 BLOCKER-1):\n%s", plan, hostHR)
			}
			if strings.Contains(hostHR, "tenant-acme-kubeconfig") {
				t.Errorf("CNPG-pair PRIMARY HR (plan=%q) MUST NOT reference the vcluster kubeconfig mirror — that routes the primary Cluster INTO the vcluster (the BLOCKER-1 bug):\n%s", plan, hostHR)
			}
			// With no kubeConfig the HR is authored in the host `<slug>` ns (NOT
			// flux-system, which is only for the kubeConfig-carrying replica/mirror).
			if !strings.Contains(hostHR, "namespace: acme") {
				t.Errorf("host-side CNPG-pair PRIMARY HR must be authored in the host `<slug>` ns acme (plan=%q):\n%s", plan, hostHR)
			}
			if strings.Contains(hostHR, "namespace: flux-system") {
				t.Errorf("host-side CNPG-pair PRIMARY HR must NOT live in flux-system (no kubeConfig secretRef to co-locate) (plan=%q):\n%s", plan, hostHR)
			}
			// Chart installs into the host `<slug>` ns where the operator reconciles
			// the Cluster + the synced Service the in-vcluster app pods dial.
			if !strings.Contains(hostHR, "targetNamespace: acme") {
				t.Errorf("CNPG-pair PRIMARY chart targetNamespace must be the host `<slug>` ns acme (plan=%q):\n%s", plan, hostHR)
			}

			// The standalone postgres-credentials Secret the app pods read lives
			// INSIDE the vcluster (apps/ tree), NOT on the host.
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
		})
	}
}

// TestCNPGPair_XRegion_ReplicaTargetsRegionB is the #4282/#4275 fix lock — the
// CROSS-REGION standby placement. The replica HR must target REGION B's
// host-cluster kubeconfig (NOT region A / the vcluster mirror), set
// cnpgPair.side: replica, and be authored in flux-system next to that mirror.
// Without this the standby CNPG Cluster lands in region A, its region-B node-
// affinity matches 0/N nodes, the *-pgbasebackup pod hangs Pending forever, and
// the region-kill pillar has no standby to fail over to (live demo Org,
// 2026-06-25). Verified for BOTH tiers — the standby always crosses regions.
func TestCNPGPair_XRegion_ReplicaTargetsRegionB(t *testing.T) {
	for _, plan := range []string{"m", "s"} {
		t.Run("plan="+plan, func(t *testing.T) {
			g := NewManifestGenerator(testBasePath)
			out := g.GenerateAllWithAppConfigs("acme", plan,
				[]string{"umami"}, "pw",
				map[string]map[string]any{
					"postgres": {
						"active_hot_standby": true,
						"primary_region":     "hz-fsn-rtz-prod",
						"replica_region":     "hz-hel-rtz-prod",
					},
				},
			)
			replicaHR, ok := out[testBasePath+"/acme/db-cnpg-pair-replica.yaml"]
			if !ok {
				t.Fatalf("CNPG-pair REPLICA HR not emitted as a HOST file (keys: %v)", keys(out))
			}
			if !strings.Contains(replicaHR, "side: replica") {
				t.Errorf("REPLICA HR must set cnpgPair.side: replica so the chart renders the standby Cluster ONLY:\n%s", replicaHR)
			}
			// THE FIX: the replica installs THROUGH region-B's host kubeconfig —
			// not the region-A vcluster mirror — so the standby Cluster lands in
			// region B where its node-affinity matches.
			if !strings.Contains(replicaHR, "kubeConfig:") {
				t.Errorf("REPLICA HR MUST carry an HR-level kubeConfig (region B is a separate cluster):\n%s", replicaHR)
			}
			if !strings.Contains(replicaHR, "name: "+replicaRegionKubeSecretDefault) {
				t.Errorf("REPLICA HR kubeConfig must reference the region-B kubeconfig secret %q (NOT the region-A vcluster mirror):\n%s", replicaRegionKubeSecretDefault, replicaHR)
			}
			if strings.Contains(replicaHR, "name: tenant-acme-kubeconfig") {
				t.Errorf("REPLICA HR must NOT target the region-A vcluster mirror — that lands the standby in region A (the #4282 bug):\n%s", replicaHR)
			}
			if !strings.Contains(replicaHR, "namespace: flux-system") {
				t.Errorf("REPLICA HR must be authored in flux-system next to the region-B mirror:\n%s", replicaHR)
			}
			// Both HRs carry the full region pair (validateRegions + the replica's
			// externalClusters source need both regions present).
			if !strings.Contains(replicaHR, "region: hz-fsn-rtz-prod") || !strings.Contains(replicaHR, "region: hz-hel-rtz-prod") {
				t.Errorf("REPLICA HR must carry BOTH primary + replica regions in values:\n%s", replicaHR)
			}
			// Distinct HR/release names so primary + replica (both in flux-system)
			// never collide.
			if !strings.Contains(replicaHR, "name: bp-cnpg-pair-replica") {
				t.Errorf("REPLICA HR must use a side-suffixed name to avoid colliding with the primary HR in flux-system:\n%s", replicaHR)
			}
			if !strings.Contains(replicaHR, "releaseName: cnpg-pair-replica") {
				t.Errorf("REPLICA HR must use a side-suffixed releaseName:\n%s", replicaHR)
			}
		})
	}
}

// TestCNPGPair_XRegion_ReplicaSecretConfigurable — the region-B kubeconfig
// secret name is overridable via ManifestGenerator.ReplicaRegionKubeSecret
// (wired from CATALYST_REPLICA_REGION_KUBECONFIG_SECRET) so a Sovereign whose
// bootstrap mirrors region B under a different name can point the replica HR
// at it without a code change.
func TestCNPGPair_XRegion_ReplicaSecretConfigurable(t *testing.T) {
	g := NewManifestGenerator(testBasePath)
	g.ReplicaRegionKubeSecret = "my-region-b-kubeconfig"
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
	replicaHR := out[testBasePath+"/acme/db-cnpg-pair-replica.yaml"]
	if !strings.Contains(replicaHR, "name: my-region-b-kubeconfig") {
		t.Errorf("REPLICA HR must honour the overridden region-B kubeconfig secret name:\n%s", replicaHR)
	}
	// The secretRef must point at the override, not the default (the default
	// name still appears in the explanatory header comment — assert on the live
	// `name:` secretRef line, not the whole document).
	if strings.Contains(replicaHR, "name: "+replicaRegionKubeSecretDefault) {
		t.Errorf("REPLICA HR secretRef should not fall back to the default when an override is set:\n%s", replicaHR)
	}
}

// TestCNPGPair_HostTier_PrimaryNoKubeConfig — for the host tier the PRIMARY side
// has no vcluster to target, so it carries NO kubeConfig and is authored in the
// host `<slug>` ns where the chart installs (region A). The REPLICA side still
// crosses to region B (asserted in TestCNPGPair_XRegion_ReplicaTargetsRegionB).
func TestCNPGPair_HostTier_PrimaryNoKubeConfig(t *testing.T) {
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
	primaryHR, ok := out[testBasePath+"/acme/db-cnpg-pair-primary.yaml"]
	if !ok {
		t.Fatalf("host-tier CNPG-pair PRIMARY HR not emitted (keys: %v)", keys(out))
	}
	if strings.Contains(primaryHR, "kubeConfig:") {
		t.Errorf("host-tier CNPG-pair PRIMARY HR MUST NOT carry kubeConfig (no vcluster):\n%s", primaryHR)
	}
	if !strings.Contains(primaryHR, "namespace: acme") {
		t.Errorf("host-tier CNPG-pair PRIMARY HR must be authored in the host <slug> ns acme:\n%s", primaryHR)
	}
}
