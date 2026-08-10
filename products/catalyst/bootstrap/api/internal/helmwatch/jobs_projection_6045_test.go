// jobs_projection_6045_test.go — #6045. The /jobs page could not show a
// single per-Organization Application install.
//
// The projection that feeds /jobs listed exactly one namespace —
// Namespace(FluxNamespace) at helmwatch.go:2682, FluxNamespace ==
// "flux-system" at :86 — and then kept only `bp-` prefixed names. A
// per-Org Application install is neither: the application-controller
// authors its HelmRelease in the Application's own namespace (or the
// vCluster host namespace) named after the Application
// (core/controllers/pkg/render/manifests.go, `name: {{ .AppName }}` /
// `namespace: {{ .HRNamespace }}`), and Applications are never `bp-`
// prefixed — `bp-` names a Blueprint, an Application is `<purpose>`.
//
// So the ONLY installs a User can actually cause to fail were
// structurally invisible in EVERY filter state, `failed` included. A
// User watched a real install fail and the page said "No jobs match the
// current filters."
//
// These fixtures encode both halves. The two per-Org Application HRs
// must appear; the per-Org INFRASTRUCTURE HR must not — it is real noise
// the old `bp-` filter was, by accident, protecting the page from, and
// it is the control that proves this fix discriminates rather than
// disables.
package helmwatch

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// newHRWithLabels builds a HelmRelease in an arbitrary namespace with an
// arbitrary label set and either a Ready=True condition or the
// Stalled=True / Ready=False+RetriesExceeded pair — the exact terminal
// shape a Flux install lands on when it has exhausted its retries, and
// the shape that must render as Failed.
func newHRWithLabels(ns, name string, labels map[string]string, stalled bool) *unstructured.Unstructured {
	now := time.Now().UTC().Format(time.RFC3339)
	conds := []any{
		map[string]any{
			"type":               "Ready",
			"status":             string(metav1.ConditionTrue),
			"reason":             "ReconciliationSucceeded",
			"message":            "Helm install succeeded",
			"lastTransitionTime": now,
		},
	}
	if stalled {
		conds = []any{
			map[string]any{
				"type":               "Ready",
				"status":             string(metav1.ConditionFalse),
				"reason":             "RetriesExceeded",
				"message":            "Helm install failed: timed out waiting for the condition",
				"lastTransitionTime": now,
			},
			map[string]any{
				"type":               "Stalled",
				"status":             string(metav1.ConditionTrue),
				"reason":             "RetriesExceeded",
				"message":            "Failed to install after 3 attempt(s)",
				"lastTransitionTime": now,
			},
		}
	}
	meta := map[string]any{"name": name, "namespace": ns}
	if len(labels) > 0 {
		lm := map[string]any{}
		for k, v := range labels {
			lm[k] = v
		}
		meta["labels"] = lm
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   meta,
		"spec":       map[string]any{},
		"status":     map[string]any{"conditions": conds},
	}}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease",
	})
	return u
}

// applicationInstallLabels — what core/controllers/pkg/render/manifests.go
// stamps on the SINGLE-HR host path.
func applicationInstallLabels(org, app string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by":    "application-controller",
		"app.kubernetes.io/name":          app,
		"catalyst.openova.io/application": app,
		ApplicationOrgLabel:               org,
		"catalyst.openova.io/env-type":    "prod",
		"catalyst.openova.io/blueprint":   "bp-podinfo",
		"catalyst.openova.io/region":      "me-east-215",
	}
}

// fanoutInstallLabels — what the TOPOLOGY FAN-OUT path stamps
// (core/controllers/application/internal/render/fanout.go merges
// fanoutOwnerLabels over LabelApp/LabelTopology/LabelCluster/LabelRole).
// Note it does NOT carry `app.kubernetes.io/managed-by` and uses
// `catalyst.openova.io/app`, not `.../application` — the two render paths
// share exactly ONE label, which is why that one is the discriminator.
func fanoutInstallLabels(org, app string) map[string]string {
	return map[string]string{
		ApplicationOrgLabel:            org,
		"catalyst.openova.io/env-type": "prod",
		"catalyst.openova.io/app-uid":  "3f1c-uid",
		"catalyst.openova.io/app":      app,
		"catalyst.openova.io/topology": "active-hot-standby",
		"catalyst.openova.io/cluster":  "hw-me-east-215-b",
		"catalyst.openova.io/role":     "active",
	}
}

// perOrgVClusterLabels — VERBATIM from the vclusterTemplate in
// core/controllers/organization/internal/gitops/manifests.go:236. This is
// the control fixture: a NON-bp HelmRelease living in a per-Org namespace
// that is pure infrastructure. Its own template comment records that it
// goes Stalled=True / RetriesExceeded on a cold Sovereign-Harbor pull, so
// a naive "list every namespace, drop the bp- filter" fix would put a
// Failed `vcluster` row on /jobs for every Organization on the Sovereign.
// It carries `openova.io/organization` — NOT `catalyst.openova.io/...`.
func perOrgVClusterLabels(org string) map[string]string {
	return map[string]string{
		"openova.io/organization":      org,
		"openova.io/vcluster":          org,
		"openova.io/managed-by":        "flux",
		"app.kubernetes.io/managed-by": "flux",
	}
}

func newJobsProjectionFixture() *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList",
	}, &unstructured.UnstructuredList{})
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		// ── bootstrap-kit, must keep working exactly as before ──
		newHRWithLabels(FluxNamespace, "bp-cilium", nil, false),
		newHRWithLabels(FluxNamespace, "bp-openbao", nil, false),
		// ── control: non-bp noise IN flux-system stays excluded ──
		newHRWithLabels(FluxNamespace, "flux-system", nil, false),
		// ── the whole point: per-Org Application installs ──
		newHRWithLabels("acme", "marketing-site", applicationInstallLabels("acme", "marketing-site"), true),
		newHRWithLabels("acme", "orders-db-hw-me-east-215-b", fanoutInstallLabels("acme", "orders-db"), false),
		// ── control: per-Org INFRASTRUCTURE stays excluded ──
		newHRWithLabels("acme", "vcluster", perOrgVClusterLabels("acme"), true),
		// ── control: an Application whose name collides with a
		//    bootstrap-kit component id must not overwrite it ──
		newHRWithLabels("beta", "cilium", applicationInstallLabels("beta", "cilium"), false),
	)
}

func byAppID(snap []ComponentSnapshot) map[string]ComponentSnapshot {
	out := map[string]ComponentSnapshot{}
	for _, cs := range snap {
		out[cs.AppID] = cs
	}
	return out
}

// TestJobsProjection_SurfacesPerOrgApplicationInstall is the decisive
// guard. It asserts on the VALUE — that the row IS the per-Org install,
// in the right namespace, with the Failed status its Stalled condition
// implies — not merely that the returned list is non-empty.
func TestJobsProjection_SurfacesPerOrgApplicationInstall(t *testing.T) {
	snap, err := ListAndSnapshotJobsProjection(context.Background(), newJobsProjectionFixture())
	if err != nil {
		t.Fatalf("ListAndSnapshotJobsProjection: %v", err)
	}

	var got *ComponentSnapshot
	for i := range snap {
		if snap[i].HelmReleaseName == "marketing-site" && snap[i].Namespace == "acme" {
			got = &snap[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("the per-Org Application install acme/marketing-site is ABSENT from the /jobs "+
			"projection — the only installs a User can cause to fail are invisible in every "+
			"filter state, `failed` included (#6045). projection carried %d rows: %v",
			len(snap), appIDs(snap))
	}
	if got.Status != StateFailed {
		t.Errorf("acme/marketing-site carries Stalled=True reason=RetriesExceeded — the exact "+
			"terminal shape that must render as Failed. Status = %q, want %q", got.Status, StateFailed)
	}
	if !got.Stalled {
		t.Errorf("acme/marketing-site must be marked Stalled so the read-side anti-flap downgrade " +
			"(#3916) does not turn a terminal failure back into `installing`")
	}
	if got.AppID == "" {
		t.Errorf("per-Org install has an empty AppID — it cannot be addressed as a /jobs row")
	}
}

// TestJobsProjection_CoversBothRenderPaths — the fan-out render path
// stamps a DIFFERENT label set from the single-HR path. A fix keyed on a
// label only one path emits would silently miss every multi-region
// Application.
func TestJobsProjection_CoversBothRenderPaths(t *testing.T) {
	snap, err := ListAndSnapshotJobsProjection(context.Background(), newJobsProjectionFixture())
	if err != nil {
		t.Fatalf("ListAndSnapshotJobsProjection: %v", err)
	}
	found := false
	for _, cs := range snap {
		if cs.HelmReleaseName == "orders-db-hw-me-east-215-b" && cs.Namespace == "acme" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the TOPOLOGY FAN-OUT Application install acme/orders-db-hw-me-east-215-b is "+
			"absent — fanout.go stamps catalyst.openova.io/app, not .../application, so a fix "+
			"keyed on the single-HR label set misses every multi-region Application. rows: %v",
			appIDs(snap))
	}
}

// TestJobsProjection_ExcludesPerOrgInfrastructure is the CONTROL. Without
// it, a "fix" that simply lists every namespace and deletes the `bp-`
// filter passes both tests above — and floods /jobs with one Failed
// `vcluster` row per Organization.
func TestJobsProjection_ExcludesPerOrgInfrastructure(t *testing.T) {
	snap, err := ListAndSnapshotJobsProjection(context.Background(), newJobsProjectionFixture())
	if err != nil {
		t.Fatalf("ListAndSnapshotJobsProjection: %v", err)
	}
	for _, cs := range snap {
		if cs.HelmReleaseName == "vcluster" {
			t.Fatalf("the per-Org INFRASTRUCTURE HelmRelease acme/vcluster leaked into the /jobs "+
				"projection (AppID=%q). It is not an Application a User installed, and its own "+
				"chart comment records that it goes Stalled=True/RetriesExceeded on a cold "+
				"Harbor pull — so this would put a spurious Failed row on the page for every "+
				"Organization on the Sovereign.", cs.AppID)
		}
		if cs.HelmReleaseName == "flux-system" {
			t.Fatalf("non-bp infrastructure in flux-system leaked into the projection (AppID=%q)", cs.AppID)
		}
	}
}

// TestJobsProjection_PreservesBootstrapKitRows — the bootstrap-kit half
// is load-bearing for Phase-1 convergence and must be byte-identical to
// what it was before the widening.
func TestJobsProjection_PreservesBootstrapKitRows(t *testing.T) {
	dyn := newJobsProjectionFixture()
	widened, err := ListAndSnapshotJobsProjection(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListAndSnapshotJobsProjection: %v", err)
	}
	bootstrap, err := ListAndSnapshotHelmReleases(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListAndSnapshotHelmReleases: %v", err)
	}
	if len(bootstrap) != 2 {
		t.Fatalf("fixture sanity: bootstrap-kit projection should carry exactly bp-cilium + "+
			"bp-openbao, got %v", appIDs(bootstrap))
	}
	byID := byAppID(widened)
	for _, want := range bootstrap {
		got, ok := byID[want.AppID]
		if !ok {
			t.Fatalf("bootstrap-kit row %q disappeared from the widened projection", want.AppID)
		}
		if got.HelmReleaseName != want.HelmReleaseName || got.Namespace != want.Namespace ||
			got.Status != want.Status || got.Stalled != want.Stalled {
			t.Errorf("bootstrap-kit row %q changed shape: %+v -> %+v", want.AppID, want, got)
		}
	}
}

// TestJobsProjection_AppIDDoesNotCollideWithBootstrapKit — an Application
// may legitimately be named `cilium`. If its AppID were the bare name it
// would overwrite the bootstrap-kit component's row in the jobs.Store
// (keyed by AppID), and a User's failing install would silently replace a
// platform component's status — or be replaced by it.
func TestJobsProjection_AppIDDoesNotCollideWithBootstrapKit(t *testing.T) {
	snap, err := ListAndSnapshotJobsProjection(context.Background(), newJobsProjectionFixture())
	if err != nil {
		t.Fatalf("ListAndSnapshotJobsProjection: %v", err)
	}
	seen := map[string]ComponentSnapshot{}
	for _, cs := range snap {
		if prev, dup := seen[cs.AppID]; dup {
			t.Fatalf("AppID %q is used by BOTH %s/%s and %s/%s — one row overwrites the other in "+
				"the jobs.Store", cs.AppID, prev.Namespace, prev.HelmReleaseName,
				cs.Namespace, cs.HelmReleaseName)
		}
		seen[cs.AppID] = cs
	}
	// And specifically: the bootstrap-kit `cilium` row must still be the
	// bootstrap-kit one, not the beta Org's Application.
	bootstrapCilium, ok := seen["cilium"]
	if !ok {
		t.Fatalf("bootstrap-kit AppID `cilium` missing entirely: %v", appIDs(snap))
	}
	if bootstrapCilium.Namespace != FluxNamespace {
		t.Fatalf("AppID `cilium` resolved to %s/%s — the beta Org's Application displaced the "+
			"platform component", bootstrapCilium.Namespace, bootstrapCilium.HelmReleaseName)
	}
}

// newHRWithReadyReason builds an HR with a single Ready=False condition
// carrying an arbitrary reason and NO Stalled condition — the shape of a
// HelmRelease that is unhealthy but still has remediation attempts left.
func newHRWithReadyReason(ns, name, reason string, labels map[string]string) *unstructured.Unstructured {
	u := newHRWithLabels(ns, name, labels, false)
	_ = unstructured.SetNestedSlice(u.Object, []any{
		map[string]any{
			"type":               "Ready",
			"status":             string(metav1.ConditionFalse),
			"reason":             reason,
			"message":            "Helm upgrade failed: another operation is in progress",
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		},
	}, "status", "conditions")
	return u
}

// TestSnapshot_StalledRetriesExceededIsTerminalFailed — #6045 defect 2.
// DeriveState recognises only the InstallFailed/UpgradeFailed reason
// family as failed, so a Ready=False whose reason IS RetriesExceeded fell
// through to `degraded` and, because the Stalled flag was gated on
// state==StateFailed, ALSO lost its Stalled marker. Flux has exhausted
// remediation.retries at that point and will never reconcile it again.
//
// The second case is the CONTROL: a Ready=False that is genuinely still
// retrying must STAY degraded and un-stalled. Without it a fix that
// promoted every degraded HR to failed would pass the first case.
func TestSnapshot_StalledRetriesExceededIsTerminalFailed(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList",
	}, &unstructured.UnstructuredList{})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		// Retries exhausted → terminal.
		newHRWithReadyReason(FluxNamespace, "bp-exhausted", "RetriesExceeded", nil),
		// Unhealthy but Flux will try again → NOT terminal.
		newHRWithReadyReason(FluxNamespace, "bp-retrying", "UpgradeFailedButRetrying", nil),
	)
	byID := byAppID(mustSnapshot(t, dyn))

	exhausted := byID["exhausted"]
	if exhausted.Status != StateFailed {
		t.Errorf("Ready=False reason=RetriesExceeded means Flux has exhausted remediation.retries "+
			"and will not reconcile again — Status = %q, want %q", exhausted.Status, StateFailed)
	}
	if !exhausted.Stalled {
		t.Errorf("a RetriesExceeded HR must carry Stalled=true; the /jobs anti-flap rule (#3916) "+
			"uses that flag to tell a terminal failure from a transient wobble")
	}

	retrying := byID["retrying"]
	if retrying.Status != StateDegraded {
		t.Errorf("CONTROL: an unhealthy HR with retries REMAINING must stay %q so the /jobs leaf "+
			"does not flap Failed<->Succeeded while Flux converges — got %q",
			StateDegraded, retrying.Status)
	}
	if retrying.Stalled {
		t.Errorf("CONTROL: an HR with retries remaining must not be marked Stalled")
	}
}

func mustSnapshot(t *testing.T, dyn *dynamicfake.FakeDynamicClient) []ComponentSnapshot {
	t.Helper()
	snap, err := ListAndSnapshotHelmReleases(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListAndSnapshotHelmReleases: %v", err)
	}
	return snap
}

func appIDs(snap []ComponentSnapshot) []string {
	out := make([]string, 0, len(snap))
	for _, cs := range snap {
		out = append(out, cs.AppID)
	}
	return out
}
