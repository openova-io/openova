// Tests for the #5042 bounded Flux-CRD-presence probe.
//
// Background (verified live on hw247 dep 5360c909c6b61472): on an
// intermittent fresh-prov bootstrap wedge the new Sovereign is HEALTHY
// — nodes Ready, CNI up, kubeconfig PUT succeeds — but cloud-init's
// flux-install stage silently didn't land, so the Flux HelmRelease CRD
// (helmreleases.helm.toolkit.fluxcd.io) is ABSENT. Before this fix the
// helmwatch informer would attach and observe zero HelmReleases until
// the 120m WatchTimeout, leaving the deployment "phase1-watching" for
// hours with no fast, named diagnostic — the #5042 forensic gap.
//
// What this file proves:
//
//  1. probeFluxCRDAbsent returns false (proceed to the watcher) the
//     instant the CRD is servable — a List against HelmReleaseGVR that
//     succeeds means Flux is installed.
//  2. probeFluxCRDAbsent returns true when the CRD stays absent
//     (apiserver answers NotFound for the resource path) for the whole
//     probe budget — the positive "flux-install never landed" signal.
//  3. probeFluxCRDAbsent is CONSERVATIVE / fail-open: a transient probe
//     error (connection blip, timeout) is NEVER classified absent — it
//     falls through to the watcher so a flake can't invent a failure.
//  4. runPhase1Watch, driven end-to-end against an absent-CRD cluster,
//     terminates with Status="failed", Phase1Outcome=OutcomeFluxCRDsAbsent
//     and an operator-actionable diagnostic naming cloud-init's
//     flux-install stage (Refs #5042).
//  5. runPhase1Watch against a present-CRD cluster proceeds past the
//     probe and reaches OutcomeReady — the probe never false-positives
//     on a healthy prov.
package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// fakeDynamicFactoryWithHRListError — closure that returns a fake
// dynamic client whose List against the HelmRelease GVR always fails
// with listErr. Used to simulate the two apiserver responses the probe
// must distinguish: a NotFound (CRD absent, positive observation) vs a
// generic transient error (ambiguous, fail-open). A PrependReactor for
// verb "list" / resource "helmreleases" short-circuits the default
// object reactor so the injected error is what the probe's List sees.
func fakeDynamicFactoryWithHRListError(listErr error) func(string) (dynamic.Interface, error) {
	return func(_ string) (dynamic.Interface, error) {
		scheme := newFakeSchemeForHandler()
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			scheme,
			map[schema.GroupVersionResource]string{helmwatch.HelmReleaseGVR: "HelmReleaseList"},
		)
		client.PrependReactor("list", "helmreleases", func(_ clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, listErr
		})
		return client, nil
	}
}

// hrCRDNotFound — the error a real apiserver returns for a dynamic-client
// List against a resource path whose CRD is not installed (HTTP 404 →
// apierrors.IsNotFound == true).
func hrCRDNotFound() error {
	return apierrors.NewNotFound(
		schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"},
		"",
	)
}

// TestProbeFluxCRDAbsent covers the three probe verdicts in isolation:
// present → proceed (false), absent-past-budget → classify (true),
// transient error → fail-open (false).
func TestProbeFluxCRDAbsent(t *testing.T) {
	t.Run("CRDPresentProceeds", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		// No injected error: List against the seeded fake client
		// succeeds (empty list, no error) → CRD servable.
		h.dynamicFactory = fakeDynamicFactoryFromObjects()
		h.fluxCRDProbeBudget = 2 * time.Second
		h.fluxCRDProbePollInterval = 20 * time.Millisecond

		dep := makeDeploymentWithKubeconfig(t, h, "flux-crd-present-probe", "fake-kubeconfig: yaml")

		if got := h.probeFluxCRDAbsent(dep, "fake-kubeconfig: yaml"); got {
			t.Errorf("probeFluxCRDAbsent = true, want false (CRD is servable → proceed to watcher)")
		}
	})

	t.Run("CRDAbsentPastBudgetClassifies", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.dynamicFactory = fakeDynamicFactoryWithHRListError(hrCRDNotFound())
		h.fluxCRDProbeBudget = 150 * time.Millisecond
		h.fluxCRDProbePollInterval = 25 * time.Millisecond

		dep := makeDeploymentWithKubeconfig(t, h, "flux-crd-absent-probe", "fake-kubeconfig: yaml")

		start := time.Now()
		got := h.probeFluxCRDAbsent(dep, "fake-kubeconfig: yaml")
		elapsed := time.Since(start)

		if !got {
			t.Errorf("probeFluxCRDAbsent = false, want true (CRD absent past budget → classify flux-crds-absent)")
		}
		// The probe must exhaust the budget before classifying — it must
		// NOT trip on the first absent observation (the manifest may still
		// land within the budget).
		if elapsed < 150*time.Millisecond {
			t.Errorf("probe returned after %v — it classified before the %s budget elapsed", elapsed, 150*time.Millisecond)
		}
	})

	t.Run("TransientErrorFailsOpen", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		// A generic (non-NotFound, non-NoMatch) error models a transient
		// apiserver blip — the conservative probe must NEVER classify
		// this as absent.
		h.dynamicFactory = fakeDynamicFactoryWithHRListError(errors.New("connection refused: dial tcp 10.0.0.1:6443"))
		h.fluxCRDProbeBudget = 150 * time.Millisecond
		h.fluxCRDProbePollInterval = 25 * time.Millisecond

		dep := makeDeploymentWithKubeconfig(t, h, "flux-crd-transient-probe", "fake-kubeconfig: yaml")

		if got := h.probeFluxCRDAbsent(dep, "fake-kubeconfig: yaml"); got {
			t.Errorf("probeFluxCRDAbsent = true, want false (transient error must fail-open, never invent flux-crds-absent)")
		}
	})

	t.Run("ClientBuildErrorFailsOpen", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		// A dynamic-client construction failure is NOT a positive absent
		// observation — the probe falls through so NewWatcher can surface
		// OutcomeWatcherStartFailed with its own diagnostic.
		h.dynamicFactory = func(string) (dynamic.Interface, error) {
			return nil, errors.New("malformed kubeconfig")
		}
		h.fluxCRDProbeBudget = 150 * time.Millisecond
		h.fluxCRDProbePollInterval = 25 * time.Millisecond

		dep := makeDeploymentWithKubeconfig(t, h, "flux-crd-buildfail-probe", "fake-kubeconfig: yaml")

		if got := h.probeFluxCRDAbsent(dep, "fake-kubeconfig: yaml"); got {
			t.Errorf("probeFluxCRDAbsent = true, want false (client build error must fail-open)")
		}
	})
}

// TestRunPhase1Watch_FluxCRDsAbsentTerminalState drives the full
// runPhase1Watch entry point against a healthy-but-Flux-less Sovereign:
// the kubeconfig is on disk, the apiserver answers, but the HelmRelease
// CRD is absent. The watch must terminate FAST with Status="failed",
// Phase1Outcome=OutcomeFluxCRDsAbsent and an operator-actionable
// diagnostic — never idle "phase1-watching" to the WatchTimeout. #5042.
func TestRunPhase1Watch_FluxCRDsAbsentTerminalState(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryWithHRListError(hrCRDNotFound())
	h.fluxCRDProbeBudget = 150 * time.Millisecond
	h.fluxCRDProbePollInterval = 25 * time.Millisecond
	// Belt-and-braces: if the probe ever mis-classified present and fell
	// through to the watcher, a tiny watch timeout keeps the test bounded.
	h.phase1WatchTimeout = 2 * time.Second
	h.suppressPostHandoverHooks = true

	dep := makeDeploymentWithKubeconfig(t, h, "phase1-flux-crds-absent", "fake-kubeconfig: yaml")

	start := time.Now()
	h.runPhase1Watch(dep)
	elapsed := time.Since(start)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "failed" {
		t.Errorf("Status = %q, want %q (flux-crds-absent is a hard failure)", dep.Status, "failed")
	}
	if dep.Result == nil {
		t.Fatalf("Result is nil")
	}
	if dep.Result.Phase1Outcome != helmwatch.OutcomeFluxCRDsAbsent {
		t.Errorf("Phase1Outcome = %q, want %q", dep.Result.Phase1Outcome, helmwatch.OutcomeFluxCRDsAbsent)
	}
	if dep.Result.Phase1FinishedAt == nil {
		t.Errorf("Phase1FinishedAt should be set on the terminal flux-crds-absent path")
	}
	// The diagnostic must point the operator at cloud-init's flux-install
	// stage — the actual root cause — and reference #5042.
	for _, want := range []string{"Flux", "flux-install", "cloud-init", "#5042"} {
		if !strings.Contains(dep.Error, want) {
			t.Errorf("Error missing %q; got: %q", want, dep.Error)
		}
	}
	// The whole point of #5042 is a FAST diagnostic — the probe budget is
	// 150ms, so the watch must terminate in well under the (belt-and-braces)
	// 2s watch timeout, and nowhere near the 120m production WatchTimeout.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("runPhase1Watch took %v — flux-crds-absent should terminate at the probe budget, not hang", elapsed)
	}
}

// TestRunPhase1Watch_FluxCRDsPresentProceeds proves the probe does NOT
// false-positive on a healthy prov: with the Flux CRD present and every
// bp-* HelmRelease Ready, runPhase1Watch sails past the probe and reaches
// OutcomeReady — the classic happy path is unchanged. #5042.
func TestRunPhase1Watch_FluxCRDsPresentProceeds(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
		makeReadyHR("bp-cert-manager"),
		makeReadyHR("bp-flux"),
	)
	h.fluxCRDProbeBudget = 2 * time.Second
	h.fluxCRDProbePollInterval = 20 * time.Millisecond
	h.phase1WatchTimeout = 5 * time.Second
	h.suppressPostHandoverHooks = true

	dep := makeDeploymentWithKubeconfig(t, h, "phase1-flux-crds-present", "fake-kubeconfig: yaml")

	h.runPhase1Watch(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Result == nil {
		t.Fatalf("Result is nil")
	}
	if dep.Result.Phase1Outcome == helmwatch.OutcomeFluxCRDsAbsent {
		t.Errorf("Phase1Outcome = %q — the probe false-positived on a healthy CRD-present prov", dep.Result.Phase1Outcome)
	}
	if dep.Status != "ready" {
		t.Errorf("Status = %q, want %q (CRD present + all HRs installed → ready)", dep.Status, "ready")
	}
	if dep.Result.Phase1Outcome != helmwatch.OutcomeReady {
		t.Errorf("Phase1Outcome = %q, want %q", dep.Result.Phase1Outcome, helmwatch.OutcomeReady)
	}
}
