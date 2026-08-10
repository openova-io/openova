// applications_backing_verdict_5955_test.go — #5955, the API layer.
//
// GET /…/apps/{id} carries an HR-Ready OVERLAY: when the Application CR's phase
// is not Ready but the matching HelmRelease reports Ready=True, the response
// phase is promoted to "Ready". It was built for LAG (#4889 spine/bootstrap
// adoption, C4-003 stale Provisioning) on an argument the code states outright:
// "the catalyst controller already aggregates on HR-Ready, so we're only racing
// it forward, never against its final state".
//
// #5955 falsified that premise. The controller now observes the BACKING
// resource a chart installed and deliberately holds the Application at
// Degraded / Ready=Unknown WHILE the HelmRelease is legitimately Ready — a
// HelmRelease reporting Ready only means Helm applied the manifests and the
// apiserver accepted them. That is the controller's FINAL state, so an
// unconditional overlay races it BACKWARD past a measured verdict and repaints
// the same green badge over a dead database one layer up, where the operator
// actually looks.
//
// Both directions asserted: the overlay must stay suppressed on a measured
// backing verdict, and must still fire on every lag shape it was built for.
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func appWithReadyCondition(status, reason string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace("hw292-omani-works")
	u.SetName("uat-ahs-pg")
	u.Object["status"] = map[string]interface{}{
		"phase": "Degraded",
		"conditions": []interface{}{
			map[string]interface{}{"type": "Reconciling", "status": "False", "reason": "Idle"},
			map[string]interface{}{"type": "Ready", "status": status, "reason": reason},
		},
	}
	return u
}

// TestApplicationHoldsBackingVerdict_SuppressesOverlay — the failing-pre-fix
// direction. Each of these reasons means the controller READ the workload (or
// established it could not) and is reporting that. The HR-Ready overlay must
// not overwrite it.
func TestApplicationHoldsBackingVerdict_SuppressesOverlay(t *testing.T) {
	cases := []struct {
		reason string
		status string
	}{
		{"BackingNotReady", "False"},
		{"BackingClusterUnrecoverable", "False"},
		{"BackingReadinessUnverifiable", "Unknown"},
	}
	for _, c := range cases {
		obj := appWithReadyCondition(c.status, c.reason)
		if !applicationHoldsBackingVerdict(obj) {
			t.Errorf("applicationHoldsBackingVerdict(reason=%q) = false — the HR-Ready overlay would "+
				"promote this Application to Ready over a workload the controller measured as not ready", c.reason)
		}
	}
}

// TestApplicationHoldsBackingVerdict_LeavesLagShapesAlone — the no-regression
// direction. Every not-ready shape that is genuinely a LAG behind Flux must
// keep letting the overlay through, or #4889 (spine/bootstrap apps rendering
// FAILED while their adopted HelmRelease is Ready) comes straight back.
func TestApplicationHoldsBackingVerdict_LeavesLagShapesAlone(t *testing.T) {
	for _, reason := range []string{
		"BootstrapAdopted", // #4889 — the live spine-* / harbor / shared-pg shape
		"Reconciled",
		"DownstreamHelmReleaseFailed",
		"GiteaError",
		"", // no reason recorded
	} {
		obj := appWithReadyCondition("False", reason)
		if applicationHoldsBackingVerdict(obj) {
			t.Errorf("applicationHoldsBackingVerdict(reason=%q) = true — suppressing the overlay here "+
				"regresses the lag cases the overlay exists for", reason)
		}
	}
}

// TestApplicationHoldsBackingVerdict_MissingSignals — an Application with no
// status, no conditions, or no Ready condition holds no verdict. Absence of a
// verdict is not a verdict; the overlay's own behaviour governs those.
func TestApplicationHoldsBackingVerdict_MissingSignals(t *testing.T) {
	if applicationHoldsBackingVerdict(nil) {
		t.Errorf("applicationHoldsBackingVerdict(nil) = true, want false")
	}
	bare := &unstructured.Unstructured{Object: map[string]interface{}{}}
	if applicationHoldsBackingVerdict(bare) {
		t.Errorf("applicationHoldsBackingVerdict(no status) = true, want false")
	}
	noReady := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"phase": "Provisioning",
			"conditions": []interface{}{
				map[string]interface{}{"type": "Reconciling", "status": "True", "reason": "Working"},
			},
		},
	}}
	if applicationHoldsBackingVerdict(noReady) {
		t.Errorf("applicationHoldsBackingVerdict(no Ready condition) = true, want false")
	}
}
