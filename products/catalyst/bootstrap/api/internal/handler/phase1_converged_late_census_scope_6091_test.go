// phase1_converged_late_census_scope_6091_test.go — #6091.
//
// WHAT WAS WRONG. censusHelmReleases listed `Namespace("")` — every namespace
// on the cluster — while Phase-1 itself watches only `Namespace(FluxNamespace)`
// (helmwatch.go:1396 and :2682). The census that decides whether a `failed`
// record may be rescued therefore counted a DIFFERENT population from the one
// Result.ComponentStates was derived from, and per-Org tenant application
// HelmReleases rode into both halves of the decision:
//
//   - into `total`, where the ratio gate is `ready*10 < total*9`. Measured
//     read-only on hw293 (dep a0077ba47e3720e5) minutes apart with no platform
//     change: 76/84 = 0.9048, then 72/78 = 0.9231 — pure tenant churn. At
//     0.9048 the rescue was ONE non-Ready tenant release from declining
//     (75/84 = 0.8929). The flux-system population read 63/67 = 0.9403.
//
//   - into `readyIDs`, which is keyed by NAME with no namespace
//     discrimination, so a tenant release could satisfy the #6082
//     per-component recovery proof for a platform component sharing its id.
//     `newapi` is exactly such an id on hw293.
//
// WHY THIS TEST EXISTS AT ALL. Every other test of the rescue seams
// `censusHelmReleases` out wholesale (phase1_converged_late_test.go:96,
// phase1_converged_late_failed_recovered_6082_test.go:80 and its siblings), so
// the real lister — and specifically its namespace — was the one part of the
// census nothing exercised. The suite was fully green with the bug in it.
//
// It asserts on the OBSERVED counts from a seeded fake apiserver, not on a
// constant: a test that read `helmwatch.FluxNamespace` back out of the source
// would pass against a census that named the constant and then ignored it.
package handler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// hr builds one HelmRelease with the given Ready condition status. A status of
// "" means the object carries no conditions at all — the shape a freshly
// created, not-yet-reconciled release has.
func hr(ns, name, ready string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
	}}
	if ready != "" {
		_ = unstructured.SetNestedSlice(o.Object, []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready},
		}, "status", "conditions")
	}
	return o
}

func censusFakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	gvk := schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind("HelmReleaseList"), &unstructured.UnstructuredList{})
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{helmReleaseGVR: "HelmReleaseList"}, objs...)
}

// TestCensusHelmReleases_CountsOnlyFluxNamespace6091 is the hw293 shape with
// the numbers scaled down: a platform population that has converged, plus
// tenant releases whose failures have nothing to do with it.
//
// The seed reproduces the hw293 disagreement in miniature. Scoped to
// flux-system it reads 9 Ready of 10 = 0.90 and the rescue PROCEEDS — the one
// non-Ready platform entry is `bp-velero`, a by-design hcloud no-op on a Huawei
// prov, exactly the kind of entry the 0.90 tolerance was sized for. Censused
// cluster-wide the same instant it reads 10 of 14 = 0.714 and the rescue
// DECLINES, purely on customer application state.
func TestCensusHelmReleases_CountsOnlyFluxNamespace6091(t *testing.T) {
	dyn := censusFakeClient(
		// The platform population Phase-1 watched.
		hr(helmwatch.FluxNamespace, "bp-cilium", "True"),
		hr(helmwatch.FluxNamespace, "bp-keycloak", "True"),
		hr(helmwatch.FluxNamespace, "bp-self-sovereign-cutover", "True"),
		hr(helmwatch.FluxNamespace, "bp-newapi", "True"),
		hr(helmwatch.FluxNamespace, "bp-flux", "True"),
		hr(helmwatch.FluxNamespace, "bp-gitea", "True"),
		hr(helmwatch.FluxNamespace, "bp-harbor", "True"),
		hr(helmwatch.FluxNamespace, "bp-openbao", "True"),
		hr(helmwatch.FluxNamespace, "bp-cnpg", "True"),
		hr(helmwatch.FluxNamespace, "bp-velero", "False"), // by-design hcloud no-op on a Huawei prov
		// Tenant application releases. Two failed on the plan-quota ceiling
		// ("Helm install failed ... context deadline exceeded"); one is mid-install.
		hr("hw293walkone", "uat174-wp-rtz-a", "False"),
		hr("hw293walkone", "uat224-openclaw-mgmt-a", ""),
		hr("g7doora", "bp-wordpress-tenant", "False"),
		hr("hw293walktwo", "uatm4-agenity-rtz-a", "True"),
	)

	ready, total, readyIDs, err := censusHelmReleasesWithClient(context.Background(), dyn)
	if err != nil {
		t.Fatalf("census: %v", err)
	}

	if total != 10 {
		t.Errorf("total = %d, want 10 (the flux-system population only). "+
			"A total of 14 means the census is still listing every namespace and "+
			"tenant application releases are in the ratio denominator — #6091.", total)
	}
	if ready != 9 {
		t.Errorf("ready = %d, want 9", ready)
	}

	// THE CONTROL that makes the scoping matter rather than merely differ: the
	// SAME seeded cluster, censused across all namespaces the way #6091 did,
	// reaches the OPPOSITE verdict under runConvergedLateRescue's own ratio
	// gate. If both populations agreed, this test would be pinning a
	// distinction without a consequence.
	wideList, err := dyn.Resource(helmReleaseGVR).Namespace("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("cluster-wide control list: %v", err)
	}
	wideTotal := len(wideList.Items)
	wideReady := 0
	for i := range wideList.Items {
		conds, _, _ := unstructured.NestedSlice(wideList.Items[i].Object, "status", "conditions")
		for _, c := range conds {
			if cm, ok := c.(map[string]interface{}); ok && cm["type"] == "Ready" && cm["status"] == "True" {
				wideReady++
				break
			}
		}
	}
	scopedDeclines := ready*10 < total*9
	wideDeclines := wideReady*10 < wideTotal*9
	if scopedDeclines {
		t.Errorf("scoped census %d/%d DECLINES the rescue — the platform population "+
			"converged and must not read as unconverged", ready, total)
	}
	if !wideDeclines {
		t.Errorf("cluster-wide census %d/%d does not decline, so this seed does not "+
			"reproduce #6091 and the scoped assertion above proves nothing",
			wideReady, wideTotal)
	}

	// readyIDs must not carry tenant-derived ids: they are what
	// notRecoveredFailedComponents consults as positive recovery evidence.
	for _, id := range []string{"uatm4-agenity-rtz-a", "uat174-wp-rtz-a", "uat224-openclaw-mgmt-a", "wordpress-tenant"} {
		if readyIDs[id] {
			t.Errorf("readyIDs carries tenant-derived id %q — a per-Org release can "+
				"reach the #6082 per-component recovery proof", id)
		}
	}
	for _, id := range []string{"cilium", "keycloak", "self-sovereign-cutover", "newapi"} {
		if !readyIDs[id] {
			t.Errorf("readyIDs is missing platform id %q — the census stopped seeing "+
				"the population the recovery proof needs", id)
		}
	}
	if readyIDs["velero"] {
		t.Errorf("readyIDs carries %q, whose release is Ready=False — membership must "+
			"stay POSITIVE evidence only", "velero")
	}
}

// TestCensusHelmReleases_TenantReleaseCannotForgeRecovery6091 is defect 2 on
// its own, with the id collision that exists for real.
//
// `newapi` is simultaneously a platform ComponentStates key and a
// tenant-installable Blueprint. Here the PLATFORM flux-system/bp-newapi is
// still failing and a tenant Org's bp-newapi is healthy. A namespace-blind
// census reports newapi as recovered on the strength of the tenant's copy,
// which is precisely the evidence notRecoveredFailedComponents treats as proof
// that the component whose failure latched the record has healed.
func TestCensusHelmReleases_TenantReleaseCannotForgeRecovery6091(t *testing.T) {
	dyn := censusFakeClient(
		hr(helmwatch.FluxNamespace, "bp-newapi", "False"), // the platform component: still failed
		hr("someorg", "bp-newapi", "True"),                // a customer's own install: healthy
	)

	_, _, readyIDs, err := censusHelmReleasesWithClient(context.Background(), dyn)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if readyIDs["newapi"] {
		t.Errorf("readyIDs[\"newapi\"] is true while the PLATFORM flux-system/bp-newapi " +
			"is Ready=False — a tenant release in another namespace forged the " +
			"per-component recovery proof (#6091)")
	}

	// The control, in the same call: with the platform release healthy the id
	// MUST appear, so the assertion above is not passing merely because the
	// census stopped reporting anything.
	dyn = censusFakeClient(hr(helmwatch.FluxNamespace, "bp-newapi", "True"))
	_, _, readyIDs, err = censusHelmReleasesWithClient(context.Background(), dyn)
	if err != nil {
		t.Fatalf("census (control): %v", err)
	}
	if !readyIDs["newapi"] {
		t.Fatalf("control failed: a Ready platform bp-newapi did not register, so the " +
			"negative assertion above proves nothing")
	}
}
