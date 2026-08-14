// application_controller_hr_timeout_3988_test.go — UAT row 222 / #3988.
//
// WHAT WENT WRONG
// ───────────────
// `renderOneHR` (internal/render/fanout.go) built a HelmRelease spec with
// chart + interval + values + dependsOn + kubeConfig and NOTHING ELSE. No
// `spec.timeout`, no `spec.install` block. So every per-Organization
// Application — the path a User's console click and the Agenity agent's
// `create_application` BOTH land on — inherited helm-controller's defaults:
// a 5-minute operation timeout and `install.remediation.retries: 0`.
//
// Measured live on hw296 (dep e689e3b34a75fdec), ns `walkthree`,
// 2026-08-14. The agent-created `uat-agent-wp-rtz-a`:
//
//	lastDeployed:  2026-08-14T00:24:21Z
//	InstallFailed: 2026-08-14T00:29:21Z
//	  "Helm install failed ... with chart bp-wordpress-tenant@0.4.23:
//	   context deadline exceeded"
//	Stalled=True reason=RetriesExceeded
//	  "Failed to install after 1 attempt(s)"
//
// Exactly 300s — the default — and then permanently stalled, never
// reconciled again. The Organization's OWN pre-existing
// `bp-wordpress-tenant`, installed by the platform's per-Org stack 173
// minutes earlier with no agent involved, carried the identical error: the
// control that proves this is the delivery path and not the agentic chain.
//
// WHY A UNIT TEST ON THE RENDERER IS NOT ENOUGH
// ─────────────────────────────────────────────
// internal/render/fanout_test.go proves renderOneHR CAN stamp these fields
// when a caller passes them. It cannot prove the controller DOES pass them
// — the recurring "the helper was tested, the call site was not" shape.
// These tests drive the real reconcile loop and assert on the HelmRelease
// the reconciler actually upserted onto the (fake) host cluster, so a
// regression that drops the Config wiring, the Defaults(), or the
// FanoutInputs fields goes red here.
package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// listFanoutHRs drives a reconcile and returns the per-cluster HelmReleases
// the reconciler wrote into the mgmt host namespace.
func listFanoutHRs(t *testing.T, bp *unstructured.Unstructured, appName string) []unstructured.Unstructured {
	t.Helper()
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", appName, "acme-prod", bp.GetName(), "1.0.6",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", appName)

	got := readApp(t, r, "acme", appName)
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("mgmt").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in mgmt: %v", err)
	}
	if len(hrList.Items) == 0 {
		t.Fatalf("reconcile produced NO fan-out HelmReleases — this test would " +
			"pass vacuously; the topology fixture is not reaching the fan-out path")
	}
	return hrList.Items
}

// TestReconcile_FanoutHR_CarriesTimeoutAndRemediation — the call-site test.
// Every HelmRelease the reconciler upserts must carry a real operation
// deadline and a non-zero remediation retry count.
func TestReconcile_FanoutHR_CarriesTimeoutAndRemediation(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-grafana", "1.0.6", "mgmt", []string{"mgmt-A", "mgmt-B"},
	)

	for _, hr := range listFanoutHRs(t, bp, "obs") {
		name := hr.GetName()

		// spec.timeout — absent means Flux's 5m, which is the #3988 wound.
		timeout, found, _ := unstructured.NestedString(hr.Object, "spec", "timeout")
		if !found {
			t.Errorf("HR %s has no spec.timeout — helm-controller then applies "+
				"its 5m default, and bp-wordpress-tenant died at exactly 300s "+
				"with `context deadline exceeded` on hw296", name)
		} else if timeout != "900s" {
			t.Errorf("HR %s spec.timeout = %q, want %q (Config default)", name, timeout, "900s")
		}

		// install/upgrade remediation.retries — absent means 0, and 0 means
		// the FIRST failure sets Stalled=True/RetriesExceeded permanently.
		for _, action := range []string{"install", "upgrade"} {
			retries, found, err := unstructured.NestedInt64(hr.Object, "spec", action, "remediation", "retries")
			if err != nil {
				t.Errorf("HR %s spec.%s.remediation.retries unreadable: %v", name, action, err)
				continue
			}
			if !found {
				t.Errorf("HR %s has no spec.%s.remediation.retries — Flux defaults "+
					"it to 0, so the first failed %s stalls the release forever "+
					"(observed: Stalled=True reason=RetriesExceeded \"Failed to "+
					"install after 1 attempt(s)\") and the Application can never "+
					"converge", name, action, action)
				continue
			}
			if retries < 1 {
				t.Errorf("HR %s spec.%s.remediation.retries = %d, want >= 1 — "+
					"0 is exactly the no-self-heal defect", name, action, retries)
			}
		}
	}
}

// TestReconcile_FanoutHR_HonoursBlueprintDisableWait — #4246 on the fan-out
// path. `spec.manifests.helmRelease.disableWait` was read only by the legacy
// per-region generator, which `fanoutOwnsInstall` suppresses whenever the
// fan-out produced HRs. A Blueprint that declares it (bp-agenity does — live
// Blueprint CR on hw296 reads disableWait: true) therefore had it silently
// dropped, and its install waits on a Pod that gates on a value written by
// one of the chart's own post-install hooks. That is a deadlock which ends
// as `context deadline exceeded`.
func TestReconcile_FanoutHR_HonoursBlueprintDisableWait(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-grafana", "1.0.6", "mgmt", []string{"mgmt-A", "mgmt-B"},
	)
	if err := unstructured.SetNestedField(bp.Object, true,
		"spec", "manifests", "helmRelease", "disableWait"); err != nil {
		t.Fatalf("seed disableWait: %v", err)
	}

	for _, hr := range listFanoutHRs(t, bp, "obs-nowait") {
		for _, action := range []string{"install", "upgrade"} {
			dw, found, _ := unstructured.NestedBool(hr.Object, "spec", action, "disableWait")
			if !found || !dw {
				t.Errorf("HR %s spec.%s.disableWait = %v (found=%v), want true — "+
					"the Blueprint declares spec.manifests.helmRelease.disableWait "+
					"and the fan-out path must honour it, not only the legacy "+
					"per-region generator", hr.GetName(), action, dw, found)
			}
		}
	}
}

// CONTROL for the test above: a Blueprint that does NOT declare disableWait
// must not get it stamped. Without this control, a renderer that hard-codes
// `disableWait: true` would pass the positive test while silently disabling
// --wait for every chart in the catalog.
func TestReconcile_FanoutHR_NoDisableWaitWhenBlueprintSilent(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-grafana", "1.0.6", "mgmt", []string{"mgmt-A", "mgmt-B"},
	)

	for _, hr := range listFanoutHRs(t, bp, "obs-wait") {
		for _, action := range []string{"install", "upgrade"} {
			if _, found, _ := unstructured.NestedBool(hr.Object, "spec", action, "disableWait"); found {
				t.Errorf("HR %s stamped spec.%s.disableWait although the Blueprint "+
					"does not declare it — --wait must stay on by default",
					hr.GetName(), action)
			}
		}
	}
}

// TestConfigDefaults_HelmReleaseDeliveryKnobs pins the defaults themselves.
// A zero value here is not a harmless omission: 0 retries is the permanent
// stall, and a 0 timeout hands the release back to Flux's 5m default.
func TestConfigDefaults_HelmReleaseDeliveryKnobs(t *testing.T) {
	got := Config{}.Defaults()
	if got.HelmReleaseTimeoutSeconds != 900 {
		t.Errorf("HelmReleaseTimeoutSeconds default = %d, want 900", got.HelmReleaseTimeoutSeconds)
	}
	if got.HelmReleaseInstallRetries != 3 {
		t.Errorf("HelmReleaseInstallRetries default = %d, want 3 (matches the "+
			"legacy per-region generator, core/controllers/pkg/render/manifests.go)",
			got.HelmReleaseInstallRetries)
	}
	if got.HelmReleaseUpgradeRetries != 3 {
		t.Errorf("HelmReleaseUpgradeRetries default = %d, want 3", got.HelmReleaseUpgradeRetries)
	}

	// A negative retry count is Flux's "retry forever" and must survive
	// Defaults() untouched — only 0 (unset) is replaced.
	forever := Config{HelmReleaseInstallRetries: -1}.Defaults()
	if forever.HelmReleaseInstallRetries != -1 {
		t.Errorf("Defaults() overwrote an explicit -1 (retry forever) with %d",
			forever.HelmReleaseInstallRetries)
	}
}
