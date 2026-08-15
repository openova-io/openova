package handler

// applications_placement_workload_identity_6344_test.go — UAT row 60 (#3375),
// the hw298 reproduction.
//
// WHAT WAS MEASURED. On hw298 (dep 2540d866403f1f7c), owner session, one
// binary, one pass, four Ready Applications answered the SAME endpoint:
//
//	shared-pg      ns shared-data  active-hot-standby → 2 targets, Primary + Standby, clusters set, derivedFromRuntime true
//	spine-gitea    ns catalyst     active-hot-standby → 1 target,  Standby, cluster "", unresolvedPrimary true
//	spine-keycloak ns catalyst     active-hot-standby → 1 target,  Standby, cluster "", unresolvedPrimary true
//	spine-openbao  ns catalyst     active-passive     → 1 target,  Standby, cluster "", unresolvedPrimary true
//
// `shared-pg` is the CONTROL: the projection works, so the fix cannot be "make
// the projection work". The three that collapse differ in one property only —
// their Pods do not carry their name. `shared-pg` owns a CNPG Cluster called
// `shared-pg`; `spine-gitea` is an Application CR in ns `catalyst` adopting
// HelmRelease `flux-system/bp-gitea`, whose Pods are `instance=gitea` in ns
// `gitea`. Occupancy matched none of them, no Primary was produced, and the
// Continuum augmentation's lone `cluster: ""` Standby became the whole answer.
//
// The three cases below are exactly that discriminator, plus the invariant the
// fix must NOT trade away.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// identityFixturePod — a Pod carrying the identity a HELM CHART stamps, in the
// namespace the chart installs into. `instance` is the Helm release name, which
// for the failing shapes is NOT the Application's name.
func identityFixturePod(ns, name, instance, region, cnpgRole string) *unstructured.Unstructured {
	lbls := map[string]any{
		"app.kubernetes.io/instance": instance,
		"app.kubernetes.io/name":     instance,
	}
	if region != "" {
		lbls["openova.io/region"] = region
	}
	if cnpgRole != "" {
		lbls["openova.io/cnpg-role"] = cnpgRole
		lbls["cnpg.io/cluster"] = instance
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace":         ns,
			"name":              name,
			"creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"resourceVersion":   "1",
			"labels":            lbls,
		},
		"spec": map[string]any{
			"containers": []any{map[string]any{"name": "main", "image": "ghcr.io/x:1"}},
		},
		"status": map[string]any{
			"phase":      "Running",
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}}
}

// spineApplicationFixtureCR — the shape post_handover_spine_apps.go actually
// applies: named `spine-<chart>` in ns `catalyst`, spec.bootstrap true, and the
// ADOPTION pointer at the bootstrap HelmRelease. It carries NO releaseName and
// NO targetNamespace of its own — the workload identity lives one hop away.
func spineApplicationFixtureCR(name, hrName, hrNS string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetName(name)
	u.SetNamespace("catalyst")
	// The label is spelled out rather than taken from the const it mirrors:
	// this fixture stands in for what a LIVE cluster carries, and a fixture
	// that reads the same const the code reads cannot notice the const moving.
	u.SetLabels(map[string]string{"catalyst.openova.io/adopts-helmrelease": hrName})
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"bootstrap": true,
		"helmRelease": map[string]any{
			"name":      hrName,
			"namespace": hrNS,
		},
	}, "spec")
	return u
}

// helmReleaseFixture — the bootstrap-kit HR shape
// (clusters/_template/bootstrap-kit/10-gitea.yaml): `bp-<chart>` in flux-system
// declaring the release name and the namespace the workload installs into.
func helmReleaseFixture(name, ns, releaseName, targetNS string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	u.SetKind("HelmRelease")
	u.SetName(name)
	u.SetNamespace(ns)
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"releaseName":     releaseName,
		"targetNamespace": targetNS,
	}, "spec")
	return u
}

// identityFixtureClients builds a fake dynamic client that knows the FOUR kinds
// this join reads: Pods (occupancy), Namespaces + Nodes (the region join), and
// the Application / HelmRelease CRs that carry the authoritative identity link.
func identityFixtureClients(objs ...runtime.Object) (*dynamicfake.FakeDynamicClient, *kfake.Clientset) {
	scheme := runtime.NewScheme()
	kinds := []schema.GroupVersionKind{
		{Version: "v1", Kind: "Pod"}, {Version: "v1", Kind: "PodList"},
		{Version: "v1", Kind: "Namespace"}, {Version: "v1", Kind: "NamespaceList"},
		{Version: "v1", Kind: "Node"}, {Version: "v1", Kind: "NodeList"},
		{Group: "apps.openova.io", Version: "v1", Kind: "Application"},
		{Group: "apps.openova.io", Version: "v1", Kind: "ApplicationList"},
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"},
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList"},
	}
	for _, gvk := range kinds {
		if len(gvk.Kind) > 4 && gvk.Kind[len(gvk.Kind)-4:] == "List" {
			scheme.AddKnownTypeWithName(gvk, &unstructured.UnstructuredList{})
			continue
		}
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	}
	listKinds := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                                          "PodList",
		{Version: "v1", Resource: "namespaces"}:                                    "NamespaceList",
		{Version: "v1", Resource: "nodes"}:                                         "NodeList",
		{Group: "apps.openova.io", Version: "v1", Resource: "applications"}:        "ApplicationList",
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}: "HelmReleaseList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...), kfake.NewSimpleClientset()
}

// newIdentityPlacementHandler wires a two-region k8scache (pods + the CR kinds
// the identity link is read from) AND a sovereign dynamic client seeded with
// the Continuum CRs, so augmentWithContinuumStandby — the ONLY term that can
// emit a `cluster: ""` Standby — is genuinely reachable. Without it the pre-fix
// symptom would degrade to an empty target list and the test would be pinning
// something the walk never saw.
func newIdentityPlacementHandler(
	t *testing.T,
	depID, regionA, regionB string,
	objsA, objsB []runtime.Object,
	continuumCRs []runtime.Object,
) *Handler {
	t.Helper()

	clusterA := depID
	clusterB := depID + "-" + regionB

	reg := k8scache.NewRegistry()
	for _, k := range []k8scache.Kind{
		{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true},
		{Name: "namespace", GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}},
		{Name: "node", GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}},
		{Name: "application", GVR: schema.GroupVersionResource{Group: "apps.openova.io", Version: "v1", Resource: "applications"}, Namespaced: true},
		{Name: "helmrelease", GVR: schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, Namespaced: true},
	} {
		if err := reg.Add(k); err != nil {
			t.Fatalf("registry add %s: %v", k.Name, err)
		}
	}

	dynA, coreA := identityFixtureClients(objsA...)
	dynB, coreB := identityFixtureClients(objsB...)

	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   quietHandlerLogger(),
		Registry: reg,
		Clusters: []k8scache.ClusterRef{
			{ID: clusterA, DynamicClient: dynA, CoreClient: coreA},
			{ID: clusterB, DynamicClient: dynB, CoreClient: coreB},
		},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)

	// Wait for BOTH clusters' pod informers AND cluster A's CR informers.
	// Asserting on counts rather than sleeping keeps a slow machine from
	// turning "not synced yet" into "the fix does not work".
	deadline := time.Now().Add(3 * time.Second)
	wantA, wantB := countKind(objsA, "Pod"), countKind(objsB, "Pod")
	wantApps, wantHRs := countKind(objsA, "Application"), countKind(objsA, "HelmRelease")
	for time.Now().Before(deadline) {
		a, _, _ := f.List(clusterA, "pod", labels.Everything())
		b, _, _ := f.List(clusterB, "pod", labels.Everything())
		apps, _, _ := f.List(clusterA, "application", labels.Everything())
		hrs, _, _ := f.List(clusterA, "helmrelease", labels.Everything())
		if len(a) >= wantA && len(b) >= wantB && len(apps) >= wantApps && len(hrs) >= wantHRs {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "")
	factory, _ := fakeContinuumDynamicFactory(continuumCRs...)
	h.dynamicFactory = factory

	kubeconfig := filepath.Join(t.TempDir(), depID+".yaml")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	h.deployments.Store(depID, &Deployment{
		ID:     depID,
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN: "t99.omani.works",
			Regions: []provisioner.RegionSpec{
				{CloudRegion: regionA},
				{CloudRegion: regionB},
			},
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "t99.omani.works",
			KubeconfigPath: kubeconfig,
		},
	})
	return h
}

func countKind(objs []runtime.Object, kind string) int {
	n := 0
	for _, o := range objs {
		if u, ok := o.(*unstructured.Unstructured); ok && u.GetKind() == kind {
			n++
		}
	}
	return n
}

// TestPlacementWorkloadIdentity_Row60_6344 — the hw298 discriminator, run as
// one table so the CONTROL and the DEFECT are answered by the same code path
// in the same run. A fix that only moves the failing rows by breaking the
// control fails here too.
func TestPlacementWorkloadIdentity_Row60_6344(t *testing.T) {
	const (
		regionA = "me-east-215-a"
		regionB = "me-east-215-b"
	)

	cases := []struct {
		name    string
		depID   string
		appName string
		queryNS string
		objsA   []runtime.Object
		objsB   []runtime.Object
		crs     []runtime.Object

		wantTargets      int
		wantPrimary      bool
		wantStandby      bool
		wantDerived      bool
		wantUnresolvedPr bool
		wantPrimaryRegn  string
		wantStandbyRegn  string
		wantPattern      bpv1.Pattern
		why              string
	}{
		{
			// THE CONTROL — hw298 row 1. The app name coincides with the
			// identity its own Pods carry, which is the ONLY reason this row
			// was green. It must stay green: resolution widens, never narrows.
			name:    "shared-pg shape: pod labels equal the app name — still resolves both legs",
			depID:   "dep-6344-sharedpg",
			appName: "shared-pg",
			queryNS: "shared-data",
			objsA: []runtime.Object{
				identityFixturePod("shared-data", "shared-pg-1", "shared-pg", regionA, cnpgRolePrimary),
				helmReleaseFixture("bp-postgres-shared", "flux-system", "shared-pg", "shared-data"),
			},
			objsB: []runtime.Object{
				identityFixturePod("shared-data", "shared-pg-r-1", "shared-pg", regionB, cnpgRoleReplica),
			},
			wantTargets:     2,
			wantPrimary:     true,
			wantStandby:     true,
			wantDerived:     true,
			wantPrimaryRegn: regionA,
			wantStandbyRegn: regionB,
			wantPattern:     bpv1.PatternActiveHotStandby,
			why:             "the control proves the projection works; if this row moves, the fix broke what already worked",
		},
		{
			// THE DEFECT — hw298 rows 2-4. Identical backing state, identical
			// binary; the app name simply does not appear on its own Pods.
			//
			// PRE-FIX this returns exactly what the walk recorded: ONE target,
			// role Standby, cluster "", unresolvedPrimary true,
			// derivedFromRuntime false — which the Topology tab draws as
			// `Pattern: not reported`, one card, Switchover disabled.
			name:    "spine-gitea shape: pods carry the RELEASE identity, not the app name",
			depID:   "dep-6344-spinegitea",
			appName: "spine-gitea",
			queryNS: "catalyst",
			objsA: []runtime.Object{
				identityFixturePod("gitea", "gitea-75d9f486fb-g8hsr", "gitea", regionA, ""),
				spineApplicationFixtureCR("spine-gitea", "bp-gitea", "flux-system"),
				helmReleaseFixture("bp-gitea", "flux-system", "gitea", "gitea"),
			},
			objsB: []runtime.Object{},
			crs: []runtime.Object{
				newContinuumUnstructured("dr-spine-gitea", "catalyst", "spine-gitea", regionA, []string{regionB}),
			},
			wantTargets:     2,
			wantPrimary:     true,
			wantStandby:     true,
			wantDerived:     true,
			wantPrimaryRegn: regionA,
			wantStandbyRegn: regionB,
			wantPattern:     bpv1.PatternActiveHotStandby,
			why:             "row 60's clause: region-a primary + region-b replica, not one card",
		},
		{
			// THE INVARIANT the fix must not trade away (#6268 / #6271).
			// Same authoritative link, same Continuum declaration — and NO
			// Pods anywhere. The declaration must NOT be promoted into an
			// observation: no Primary may be invented, and the half-pair must
			// still refuse to claim derivedFromRuntime.
			name:    "genuinely absent workload: declaration must not become an observed Primary",
			depID:   "dep-6344-absent",
			appName: "spine-keycloak",
			queryNS: "catalyst",
			objsA: []runtime.Object{
				spineApplicationFixtureCR("spine-keycloak", "bp-keycloak", "flux-system"),
				helmReleaseFixture("bp-keycloak", "flux-system", "keycloak", "keycloak"),
			},
			objsB: []runtime.Object{},
			crs: []runtime.Object{
				newContinuumUnstructured("dr-spine-keycloak", "catalyst", "spine-keycloak", regionA, []string{regionB}),
			},
			wantTargets:      1,
			wantPrimary:      false,
			wantStandby:      true,
			wantDerived:      false,
			wantUnresolvedPr: true,
			wantStandbyRegn:  regionB,
			why:              "no pods observed anywhere — the honest answer is an unresolved half-pair, never a fabricated Primary",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIdentityPlacementHandler(t, tc.depID, regionA, regionB, tc.objsA, tc.objsB, tc.crs)
			resp := callPlacementNS(t, h, tc.depID, tc.appName, tc.queryNS)

			if len(resp.Targets) != tc.wantTargets {
				t.Fatalf("#6344 %s: got %d target(s) want %d — %s (targets=%+v, derivedFromRuntime=%v, unresolvedPrimary=%v)",
					tc.appName, len(resp.Targets), tc.wantTargets, tc.why, resp.Targets, resp.DerivedFromRuntime, resp.UnresolvedPrimary)
			}
			if got := hasPrimaryTarget(resp.Targets); got != tc.wantPrimary {
				t.Fatalf("#6344 %s: hasPrimary=%v want %v — %s (targets=%+v)",
					tc.appName, got, tc.wantPrimary, tc.why, resp.Targets)
			}
			if got := hasStandbyTarget(resp.Targets); got != tc.wantStandby {
				t.Fatalf("#6344 %s: hasStandby=%v want %v (targets=%+v)", tc.appName, got, tc.wantStandby, resp.Targets)
			}
			if resp.DerivedFromRuntime != tc.wantDerived {
				t.Fatalf("#6344 %s: derivedFromRuntime=%v want %v (targets=%+v)",
					tc.appName, resp.DerivedFromRuntime, tc.wantDerived, resp.Targets)
			}
			if resp.UnresolvedPrimary != tc.wantUnresolvedPr {
				t.Fatalf("#6344 %s: unresolvedPrimary=%v want %v — a Standby with no Primary renders identically to an honest single-region app (targets=%+v)",
					tc.appName, resp.UnresolvedPrimary, tc.wantUnresolvedPr, resp.Targets)
			}

			primary := targetByRole(resp.Targets, bpv1.DataRolePrimary)
			standby := targetByRole(resp.Targets, bpv1.DataRoleStandby)
			if tc.wantPrimaryRegn != "" {
				if primary == nil || primary.Region != tc.wantPrimaryRegn {
					t.Fatalf("#6344 %s: Primary region %+v want %q", tc.appName, primary, tc.wantPrimaryRegn)
				}
				// An empty `cluster` on the Primary was one of the two live
				// symptoms; an observed leg always names the cluster it ran on.
				if primary.Cluster == "" {
					t.Fatalf("#6344 %s: Primary carries an EMPTY cluster (%+v) — that is the augmentation's synthetic leg, not an observation", tc.appName, *primary)
				}
			}
			if tc.wantStandbyRegn != "" {
				if standby == nil || standby.Region != tc.wantStandbyRegn {
					t.Fatalf("#6344 %s: Standby region %+v want %q", tc.appName, standby, tc.wantStandbyRegn)
				}
			}
			if tc.wantPattern != "" {
				if got := bpv1.DerivePattern(resp.Targets, bpv1.CapabilityPrimaryStandby); got != tc.wantPattern {
					t.Fatalf("#6344 %s: pattern %q want %q — the tab showed `Pattern: not reported` (targets=%+v)",
						tc.appName, got, tc.wantPattern, resp.Targets)
				}
			}
		})
	}
}
