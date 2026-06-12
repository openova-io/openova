// #3370 — bootstrap-owned ADOPTION tests.
//
// A bootstrap-owned Application (spec.bootstrap=true) is the canonical
// CR a bootstrap-kit chart self-registers for its slot-installed
// HelmRelease (shared-pg ← flux-system/bp-postgres-shared). The
// controller must ADOPT the existing install: status-only reconcile
// mirroring the owning HR's Ready condition, with ZERO HelmRelease
// writes and ZERO Gitea writes — rendering anything would duplicate the
// install the slot HR already owns. These tests are the DoD-2
// "adoption test cited" for the kubectl-get-hr before/after
// zero-duplicate-installs proof.

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeBootstrapOwnedApp mirrors what bp-postgres 0.1.6's
// templates/application-cr.yaml renders for slot 16a: a CR named after
// the instance, spec.bootstrap=true, the owning HR ref, NO
// environmentRef and NO regions (the CRD CEL rule waives them), and the
// Context declarations riding spec.parameters.
func makeBootstrapOwnedApp(namespace, name, hrName, hrNamespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetGeneration(1)
	u.SetLabels(map[string]string{
		"apps.openova.io/bootstrap-owned":  "true",
		"catalyst.openova.io/organization": "platform",
	})
	spec := map[string]interface{}{
		"bootstrap": true,
		"blueprintRef": map[string]interface{}{
			"name":    "bp-postgres",
			"version": "0.1.6",
		},
		"placement":       "single-region",
		"targetNamespace": namespace,
		"releaseName":     name,
		"parameters": map[string]interface{}{
			"databases": []interface{}{
				map[string]interface{}{
					"name":  "gitea",
					"owner": "gitea",
					"consumer": map[string]interface{}{
						"blueprint": "bp-gitea", "mode": "shared",
					},
					"reflect": map[string]interface{}{
						"secretName": "gitea-database-secret",
						"namespaces": []interface{}{"gitea"},
					},
				},
			},
		},
	}
	if hrName != "" {
		spec["helmRelease"] = map[string]interface{}{
			"name":      hrName,
			"namespace": hrNamespace,
		}
	}
	u.Object["spec"] = spec
	return u
}

// makeSlotHR mirrors a bootstrap-kit slot HelmRelease with a Ready
// condition (what Flux writes after install).
func makeSlotHR(namespace, name, readyStatus, message string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetNamespace(namespace)
	hr.SetName(name)
	hr.Object["spec"] = map[string]interface{}{
		"releaseName":     "shared-pg",
		"targetNamespace": "shared-data",
		"chart": map[string]interface{}{
			"spec": map[string]interface{}{
				"chart":   "bp-postgres",
				"version": "0.1.6",
			},
		},
	}
	if readyStatus != "" {
		hr.Object["status"] = map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":    "Ready",
					"status":  readyStatus,
					"reason":  "InstallSucceeded",
					"message": message,
				},
			},
		}
	}
	return hr
}

// countHelmReleases lists every HelmRelease across all namespaces in
// the fake cluster.
func countHelmReleases(t *testing.T, r *Reconciler) int {
	t.Helper()
	list, err := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace("").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HelmReleases: %v", err)
	}
	return len(list.Items)
}

// TestReconcile_BootstrapOwnedAdoption_NoDuplicateInstall is the
// adoption safety gate: even with EVERY parent the normal render path
// would need fully present (Environment + Organization + Blueprint +
// regions + placement), spec.bootstrap=true must short-circuit to the
// status-only path — zero new HelmReleases, zero Gitea writes, no
// controller finalizer, phase mirroring the owning HR's Ready=True.
func TestReconcile_BootstrapOwnedAdoption_NoDuplicateInstall(t *testing.T) {
	bp := makeBlueprint("bp-postgres", "0.1.6", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "True",
		"Helm install succeeded for release shared-data/shared-pg.v1 with chart bp-postgres@0.1.6")
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	// Belt-and-braces: give the CR a resolvable environment + regions
	// so a guard regression would NOT bail on EnvironmentMissing but
	// actually render — making this test fail loudly on duplication.
	_ = unstructured.SetNestedField(app.Object, "acme-prod", "spec", "environmentRef")
	_ = unstructured.SetNestedStringSlice(app.Object, []string{"hetzner-fsn-rtz-prod"}, "spec", "regions")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr)

	before := countHelmReleases(t, r)
	reconcileFromCluster(t, r, "shared-data", "shared-pg")
	after := countHelmReleases(t, r)

	if after != before {
		t.Fatalf("ADOPTION VIOLATION: HelmRelease count changed %d -> %d — the controller rendered a duplicate install for a bootstrap-owned instance", before, after)
	}
	if fg.puts != 0 {
		t.Errorf("expected 0 Gitea writes for a bootstrap-owned instance, got %d", fg.puts)
	}

	got := readApp(t, r, "shared-data", "shared-pg")
	if hasFinalizer(got, FinalizerName) {
		t.Errorf("bootstrap-owned CR must NOT carry the controller finalizer (its lifecycle is Helm's)")
	}
	phase, reason, message := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Errorf("phase = %q, want %q (msg=%q)", phase, PhaseReady, message)
	}
	if reason != ReasonBootstrapAdopted {
		t.Errorf("reason = %q, want %q", reason, ReasonBootstrapAdopted)
	}
	lr, _, _ := unstructured.NestedString(got.Object, "status", "lastReconciledAt")
	if lr == "" {
		t.Errorf("status.lastReconciledAt empty — the adoption path must keep the freshness chip alive")
	}
}

// TestReconcile_BootstrapOwnedAdoption_DegradedOnHRFailure — the owning
// HR Ready=False mirrors as phase=Degraded with the HR message lifted
// into the condition.
func TestReconcile_BootstrapOwnedAdoption_DegradedOnHRFailure(t *testing.T) {
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "False",
		"Helm upgrade failed: timed out waiting for the condition")
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")

	fg := newFakeGitea()
	r := newReconciler(t, fg, app, hr)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	got := readApp(t, r, "shared-data", "shared-pg")
	phase, reason, message := readPhaseAndReason(t, got)
	if phase != PhaseDegraded {
		t.Errorf("phase = %q, want %q", phase, PhaseDegraded)
	}
	if reason != ReasonBootstrapAdopted {
		t.Errorf("reason = %q, want %q", reason, ReasonBootstrapAdopted)
	}
	if message == "" {
		t.Errorf("expected the HR failure message lifted into the condition")
	}
	if countHelmReleases(t, r) != 1 {
		t.Errorf("adoption path must not create/delete HelmReleases")
	}
}

// TestReconcile_BootstrapOwnedAdoption_PendingWhenHRMissing — a CR
// whose owning HR is gone (or not yet applied) surfaces Pending, no
// error, no writes.
func TestReconcile_BootstrapOwnedAdoption_PendingWhenHRMissing(t *testing.T) {
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	got := readApp(t, r, "shared-data", "shared-pg")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhasePending {
		t.Errorf("phase = %q, want %q", phase, PhasePending)
	}
	if reason != ReasonBootstrapAdopted {
		t.Errorf("reason = %q, want %q", reason, ReasonBootstrapAdopted)
	}
	if fg.puts != 0 || countHelmReleases(t, r) != 0 {
		t.Errorf("missing-HR adoption path must not write anything (puts=%d, hrs=%d)", fg.puts, countHelmReleases(t, r))
	}
}

// TestReconcile_BootstrapOwnedAdoption_PendingWithoutHRRef — a CR with
// no spec.helmRelease at all stays Pending (honest unobserved state).
func TestReconcile_BootstrapOwnedAdoption_PendingWithoutHRRef(t *testing.T) {
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "", "")
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	got := readApp(t, r, "shared-data", "shared-pg")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhasePending {
		t.Errorf("phase = %q, want %q", phase, PhasePending)
	}
	if reason != ReasonBootstrapAdopted {
		t.Errorf("reason = %q, want %q", reason, ReasonBootstrapAdopted)
	}
}

// TestReconcile_BootstrapOwnedDeletion_NoTeardown — deleting a
// bootstrap-owned CR drives NO Gitea cleanup and releases any stray
// finalizer; the install belongs to the slot HR, not to us.
func TestReconcile_BootstrapOwnedDeletion_NoTeardown(t *testing.T) {
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "True", "ok")
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	// Simulate a stray finalizer from a CR that pre-dates the marker.
	app.SetFinalizers([]string{FinalizerName})
	now := metav1.Now()
	app.SetDeletionTimestamp(&now)

	fg := newFakeGitea()
	r := newReconciler(t, fg, app, hr)

	if err := r.Reconcile(context.Background(), app); err != nil {
		t.Fatalf("reconcile deletion: %v", err)
	}

	if fg.deletes != 0 {
		t.Errorf("bootstrap-owned deletion drove %d Gitea deletes — must be 0", fg.deletes)
	}
	if countHelmReleases(t, r) != 1 {
		t.Errorf("bootstrap-owned deletion must not touch the slot HR")
	}
	got, err := r.Dynamic.Resource(ApplicationGVR).Namespace("shared-data").
		Get(context.Background(), "shared-pg", metav1.GetOptions{})
	if err == nil && hasFinalizer(got, FinalizerName) {
		t.Errorf("finalizer should be released on bootstrap-owned deletion")
	}
}
