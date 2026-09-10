package gitops

import (
	"strings"
	"testing"
)

// #4297 (keystone of EPIC #4293) — per-Org apps land INSIDE the Org vCluster.
// The funnel's apps-sync Flux Kustomization carries spec.kubeConfig.secretRef
// for EVERY Organization (#4292, founder 2026-09-10: one SME customer = one
// vCluster), so the host Flux reconciles the apps tree INTO the Org vCluster
// apiserver on every plan. The former free/S/"" host-namespace arm (omit
// kubeConfig, reconcile straight into the host `<slug>` ns) no longer exists;
// every_plan_vcluster_test.go walks every slug through that invariant.

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

// TestAppsSync_PlanM_HasKubeConfig — a plan-m Org's apps-sync Kustomization
// carries the kubeConfig secretRef so the apps land inside the vcluster.
// targetNamespace stays the org-controller `<slug>` ns. (Every plan renders
// this shape now; TestAppsSync_SmallPlans_SameShapeAsPaid pins s/free/"".)
func TestAppsSync_PlanM_HasKubeConfig(t *testing.T) {
	body := appsSyncFor(t, "acme", "m")
	if !strings.Contains(body, "kubeConfig:") {
		t.Errorf("plan m apps-sync MISSING kubeConfig block:\n%s", body)
	}
	if !strings.Contains(body, "name: tenant-acme-kubeconfig") {
		t.Errorf("plan m apps-sync MISSING the kubeconfig mirror secretRef name:\n%s", body)
	}
	if !strings.Contains(body, "key: config") {
		t.Errorf("plan m apps-sync MISSING secretRef key: config:\n%s", body)
	}
	if !strings.Contains(body, "targetNamespace: acme") {
		t.Errorf("plan m apps-sync targetNamespace is not <slug> acme:\n%s", body)
	}
}

// TestAppsSync_SmallPlans_SameShapeAsPaid — s/free/"" Orgs are vCluster-backed
// exactly like m (#4292), so their apps-sync carries the SAME kubeConfig
// mirror, targets the same `<slug>` ns and the same apps path, and the apps
// tree still renders. This is the inverse of the former host-namespace
// assertion for these plans.
func TestAppsSync_SmallPlans_SameShapeAsPaid(t *testing.T) {
	paid := appsSyncFor(t, "acme", "m")
	for _, plan := range []string{"s", "free", ""} {
		t.Run("plan="+plan, func(t *testing.T) {
			body := appsSyncFor(t, "acme", plan)
			if !strings.Contains(body, "kubeConfig:") {
				t.Errorf("plan=%q apps-sync MUST carry kubeConfig — every Organization is vCluster-backed (#4292):\n%s", plan, body)
			}
			if !strings.Contains(body, "name: tenant-acme-kubeconfig") {
				t.Errorf("plan=%q apps-sync MUST reference the kubeconfig mirror:\n%s", plan, body)
			}
			if !strings.Contains(body, "targetNamespace: acme") {
				t.Errorf("plan=%q apps-sync targetNamespace is not <slug> acme:\n%s", plan, body)
			}
			if !strings.Contains(body, "path: ./"+testBasePath+"/acme/apps") {
				t.Errorf("plan=%q apps-sync path wrong:\n%s", plan, body)
			}
			// The plan is not an input to the boundary: byte-identical to m.
			if body != paid {
				t.Errorf("plan=%q apps-sync differs from plan m — the plan must not select the boundary:\n--- m ---\n%s\n--- %s ---\n%s", plan, paid, plan, body)
			}

			// The apps tree itself must still render so apps actually deploy.
			g := NewManifestGenerator(testBasePath)
			out := g.GenerateAllWithAppConfigs("acme", plan, []string{"wordpress"}, "pw", nil)
			if _, ok := out[testBasePath+"/acme/apps/app-wordpress.yaml"]; !ok {
				t.Errorf("plan=%q MISSING app-wordpress.yaml (keys: %v)", plan, keys(out))
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

// TestCNPGPair_PrimaryStaysHostSide_EveryPlan is the #4293 finding-1 lock. The
// bp-cnpg-pair chart ships ONLY postgresql.cnpg.io/v1 Cluster CRs — the operator
// + CRD are cluster-singletons that live on the HOST (slot 16). A vcluster has
// neither the CRD nor a watching operator, so a primary Cluster `helm install`ed
// INTO the vcluster (the keystone's old HR-level kubeConfig shape) fails
// `no matches for kind "Cluster"` and the paid M+ active-hot-standby HA path
// WEDGES on every fresh prov. The PRIMARY side must therefore land on region A's
// HOST `<slug>` ns — with NO vcluster kubeConfig — for every plan; m and s are
// both driven through so the host-side exception to "everything reconciles
// into the vCluster" is pinned on a paid and a small plan alike. The
// in-vcluster app pods reach the DB via the synced `postgres` Service + the
// apps-tree credentials Secret.
func TestCNPGPair_PrimaryStaysHostSide_EveryPlan(t *testing.T) {
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
// 2026-06-25). Verified for plans m and s — the standby always crosses regions.
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

// TestCNPGPair_PlanS_PrimaryNoKubeConfig — on plan s (vCluster-backed like every
// plan, #4292) the PRIMARY side still carries NO kubeConfig and is authored in
// the host `<slug>` ns where the chart installs (region A): the CNPG operator +
// CRD are host singletons. The REPLICA side still crosses to region B (asserted
// in TestCNPGPair_XRegion_ReplicaTargetsRegionB).
func TestCNPGPair_PlanS_PrimaryNoKubeConfig(t *testing.T) {
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
		t.Fatalf("plan-s CNPG-pair PRIMARY HR not emitted (keys: %v)", keys(out))
	}
	if strings.Contains(primaryHR, "kubeConfig:") {
		t.Errorf("plan-s CNPG-pair PRIMARY HR MUST NOT carry kubeConfig (the CNPG primary is host-side for every plan):\n%s", primaryHR)
	}
	if !strings.Contains(primaryHR, "namespace: acme") {
		t.Errorf("plan-s CNPG-pair PRIMARY HR must be authored in the host <slug> ns acme:\n%s", primaryHR)
	}
}
