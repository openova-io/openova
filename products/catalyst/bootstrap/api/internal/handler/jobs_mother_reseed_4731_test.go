// jobs_mother_reseed_4731_test.go — coverage for the mother-mode
// install/reconcile re-seed (Refs #4731).
//
// Symptom: on the mothership console, a CONVERGED Sovereign's deployment-
// detail treemap showed ONLY task/step/lifecycle/cron kinds — the
// install (HelmRelease) and reconcile (Flux Kustomization) leaves were
// MISSING. Root cause: chrootSeedJobsStoreIfEmpty bailed at the top with
// `if SOVEREIGN_FQDN == "" { return }`, so on the mother (env unset) the
// live-cluster HR seed never ran. It was only ever populated in-memory by
// the Phase-1 helmwatch.Watcher; the catalyst-api Pod roll wiped that, and
// mother mode had no rebuild path.
//
// Fix: gate the seed on REACHABILITY (chroot in-cluster OR mother-with-
// kubeconfig) instead of on SOVEREIGN_FQDN being set. h.sovereignDynamicClient
// already reads the posted-back per-deployment kubeconfig in mother mode, so
// the mother can re-list the Sovereign's HelmReleases on demand.
//
// These tests prove:
//   - a mother-mode Handler (SOVEREIGN_FQDN unset) + a deployment WITH a
//     Result.KubeconfigPath + a fake dynamicFactory serving N HelmReleases
//     seeds N `install` leaves into jobs.Store; and
//   - a mother-mode deployment WITHOUT a posted-back kubeconfig is a
//     graceful no-op (provisioning in flight), never an error.
package handler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeReseedDynamicClient serves the given bp-* HelmReleases (Ready=True)
// AND registers the four §5a reconciler List kinds so the reconciler-
// observation leg (chrootSeedReconcilerObservations) — which runs right
// after the HR seed once the reachability gate is relaxed — Lists cleanly
// (empty) instead of panicking the fake on an unregistered List kind. No
// reconciler objects are served, so that leg no-ops; the test focuses on
// the primary install seed.
func fakeReseedDynamicClient(t *testing.T, hrNames ...string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, lk := range []schema.GroupVersionKind{
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList"},
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList"},
		{Group: "batch", Version: "v1", Kind: "CronJobList"},
		{Group: "batch", Version: "v1", Kind: "JobList"},
		{Group: "apps", Version: "v1", Kind: "DeploymentList"},
	} {
		scheme.AddKnownTypeWithName(lk, &unstructured.UnstructuredList{})
	}
	objs := make([]runtime.Object, 0, len(hrNames))
	for _, name := range hrNames {
		u := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "helm.toolkit.fluxcd.io/v2",
				"kind":       "HelmRelease",
				"metadata": map[string]any{
					"name":      name,
					"namespace": helmwatch.FluxNamespace,
				},
				"status": map[string]any{
					"conditions": []any{
						map[string]any{
							"type":               "Ready",
							"status":             string(metav1.ConditionTrue),
							"reason":             "ReconciliationSucceeded",
							"message":            "Helm install succeeded",
							"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
						},
					},
				},
			},
		}
		u.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease",
		})
		objs = append(objs, u)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			helmwatch.HelmReleaseGVR:   "HelmReleaseList",
			helmwatch.KustomizationGVR: "KustomizationList",
			helmwatch.CronJobGVR:       "CronJobList",
			helmwatch.JobGVR:           "JobList",
			helmwatch.DeploymentGVR:    "DeploymentList",
		},
		objs...,
	)
}

// TestChrootSeedJobsStoreIfEmpty_MotherReseedsInstalls is the end-to-end
// proof for #4731: on the MOTHER (SOVEREIGN_FQDN unset) a deployment that
// already has its kubeconfig posted back must re-seed the live-cluster
// HelmReleases as `install` leaves — the leaves that were missing from a
// converged Sovereign's Dashboard treemap after a catalyst-api Pod roll.
func TestChrootSeedJobsStoreIfEmpty_MotherReseedsInstalls(t *testing.T) {
	// Mother mode — explicitly unset so a polluted env never flips us to the
	// chroot in-cluster path (which would try rest.InClusterConfig()).
	t.Setenv("SOVEREIGN_FQDN", "")

	const depID = "dep4731"
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	hrNames := []string{"bp-cilium", "bp-keycloak", "bp-cnpg", "bp-openbao"}
	fake := fakeReseedDynamicClient(t, hrNames...)
	h := &Handler{
		jobs: st,
		log:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		dynamicFactory: func(string) (dynamic.Interface, error) {
			return fake, nil
		},
	}
	dep := &Deployment{
		ID:     depID,
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN: "hw224.omani.works",
			Regions:       []provisioner.RegionSpec{{Provider: "huawei"}},
		},
		// Kubeconfig posted back at handover — the converged-Sovereign case.
		Result: &provisioner.Result{KubeconfigPath: writeTempKubeconfig(t)},
	}

	h.chrootSeedJobsStoreIfEmpty(context.Background(), dep)

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}

	// Every HelmRelease must have surfaced as an `install-<chart>` leaf with
	// the KindInstall / JobTypeInstall tags under the bootstrap-kit group.
	wantInstall := map[string]bool{
		"install-cilium":   false,
		"install-keycloak": false,
		"install-cnpg":     false,
		"install-openbao":  false,
	}
	installCount := 0
	sawBootstrapKit := false
	for _, j := range got {
		if j.JobName == jobs.GroupBootstrapKit {
			sawBootstrapKit = true
		}
		if j.Kind == jobs.KindInstall && j.Type == jobs.JobTypeInstall {
			installCount++
		}
		if _, tracked := wantInstall[j.JobName]; tracked {
			wantInstall[j.JobName] = true
			if j.Kind != jobs.KindInstall {
				t.Errorf("install leaf %q kind = %q, want %q", j.JobName, j.Kind, jobs.KindInstall)
			}
		}
	}
	for name, seen := range wantInstall {
		if !seen {
			t.Errorf("HelmRelease %q never surfaced as an install leaf on the mother", name)
		}
	}
	if installCount != len(hrNames) {
		t.Errorf("install leaf count = %d, want %d (one per HelmRelease)", installCount, len(hrNames))
	}
	if !sawBootstrapKit {
		t.Errorf("the bootstrap-kit parent group was never materialised on the mother")
	}
}

// TestChrootSeedJobsStoreIfEmpty_MotherNoKubeconfigIsNoOp proves the early
// phase1-watching window (kubeconfig not posted back yet) stays a graceful
// no-op on the mother — the reachability gate returns without touching the
// store and without invoking the dynamicFactory, so a /jobs read never
// errors and the next poll simply retries.
func TestChrootSeedJobsStoreIfEmpty_MotherNoKubeconfigIsNoOp(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")

	const depID = "dep4731nk"
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	factoryCalled := false
	h := &Handler{
		jobs: st,
		log:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		dynamicFactory: func(string) (dynamic.Interface, error) {
			factoryCalled = true
			return fakeReseedDynamicClient(t, "should-not-surface"), nil
		},
	}
	dep := &Deployment{
		ID:      depID,
		Request: provisioner.Request{SovereignFQDN: "hw224.omani.works"},
		// No Result/KubeconfigPath — cloud-init still in flight.
	}

	// Must not panic; store stays empty; the factory is never reached.
	h.chrootSeedJobsStoreIfEmpty(context.Background(), dep)

	if factoryCalled {
		t.Fatalf("no-kubeconfig mother path must short-circuit before building a client")
	}
	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty store when kubeconfig unreachable, got %d jobs", len(got))
	}
}

// TestChrootSeedJobsStoreIfEmpty_MotherReseedsViaConventionalPath is the
// live-verified hw224 case: a CONVERGED Sovereign's mother-side record has a
// NULL Result.KubeconfigPath (the omitempty field was dropped when a
// mothership Pod roll rehydrated the record before PutKubeconfig re-stamped
// it), yet the kubeconfig FILE still lives on the PVC at
// <kubeconfigsDir>/<id>.yaml. The reachability gate + sovereignDynamicClient
// must fall back to that conventional path (#3153) so the install re-seed
// still fires — otherwise the treemap shows zero install leaves despite a
// fully reachable cluster.
func TestChrootSeedJobsStoreIfEmpty_MotherReseedsViaConventionalPath(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")

	const depID = "dep4731conv"
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// The kubeconfig FILE survives on the PVC at the conventional path even
	// though the record's KubeconfigPath field is null.
	kubeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kubeDir, depID+".yaml"),
		[]byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write conventional kubeconfig: %v", err)
	}

	hrNames := []string{"bp-cilium", "bp-flux", "bp-cnpg"}
	fake := fakeReseedDynamicClient(t, hrNames...)
	h := &Handler{
		jobs:           st,
		log:            slog.New(slog.NewJSONHandler(io.Discard, nil)),
		kubeconfigsDir: kubeDir,
		dynamicFactory: func(string) (dynamic.Interface, error) {
			return fake, nil
		},
	}
	dep := &Deployment{
		ID:     depID,
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN: "hw224.omani.works",
			Regions:       []provisioner.RegionSpec{{Provider: "huawei"}},
		},
		// Result present but KubeconfigPath EMPTY — the null-field record.
		Result: &provisioner.Result{KubeconfigPath: ""},
	}

	h.chrootSeedJobsStoreIfEmpty(context.Background(), dep)

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	installCount := 0
	for _, j := range got {
		if j.Kind == jobs.KindInstall && j.Type == jobs.JobTypeInstall {
			installCount++
		}
	}
	if installCount != len(hrNames) {
		t.Fatalf("conventional-path fallback: install leaf count = %d, want %d — the null-KubeconfigPath record did not resolve to the on-PVC file",
			installCount, len(hrNames))
	}
}
