// continuum_transition_observability_test.go — founder, hw307 2026-11-12,
// mid-switchover: "I changed the topology and applied but no idea what is
// happening, zero observability".
//
// The Continuum recorded every step (status.transition + status.activity),
// yet the application API reported switchoverInProgress=false — it looked for
// the pre-ADR-0015 phase name "SwitchingOver" while the engine reports
// "Transitioning" — and carried no step list, so the Topology tab had nothing
// to render. The fixtures are the live dr-wp record from that switchover.
package handler

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func liveContinuum(phase string, states [4]string, outcome string) *unstructured.Unstructured {
	id := "hw-me-east-215-a-rtz-prod-to-hw-me-east-215-b-rtz-prod-1794476028"
	phases := []string{"traffic-off", "data-primary", "peers", "traffic-on"}
	msgs := []string{
		"CNPG Cluster wp-db-6e1e3f9c demoted in hw-me-east-215-a-rtz-prod; demotion token published",
		`waiting: CNPG Cluster wp-db-5ff1066a in hw-me-east-215-b-rtz-prod not a Ready primary (phase "Waiting for the instances to become active")`,
		"", "",
	}
	act := []interface{}{}
	for i, p := range phases {
		act = append(act, map[string]interface{}{
			"id": p + "-hw307-omani-works-wp", "phase": p, "member": "wp", "state": states[i],
			"transition": id, "startedAt": "2026-11-12T09:33:48Z", "message": msgs[i],
		})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "dr.openova.io/v1", "kind": "Continuum",
		"metadata": map[string]interface{}{"name": "dr-wp", "namespace": "hw307-omani-works"},
		"spec":     map[string]interface{}{"desiredPrimaryRegion": "hw-me-east-215-a-rtz-prod"},
		"status": map[string]interface{}{
			"phase": phase, "primaryRegion": "hw-me-east-215-b-rtz-prod",
			"desiredPrimaryRegion": "hw-me-east-215-b-rtz-prod", "converged": outcome == "Converged",
			"activity": act,
			"transition": map[string]interface{}{
				"id": id, "from": "hw-me-east-215-a-rtz-prod", "to": "hw-me-east-215-b-rtz-prod",
				"startedAt": "2026-11-12T09:33:48Z", "outcome": outcome,
			},
		},
	}}
}

func TestContinuumConvergence_TransitioningIsASwitchoverInProgress(t *testing.T) {
	cr := liveContinuum("Transitioning", [4]string{"succeeded", "running", "pending", "pending"}, "Transitioning")
	got := continuumConvergenceFromCR(cr, "")
	if !got.SwitchoverInProgress {
		t.Fatal("phase Transitioning must report switchoverInProgress=true (the live defect)")
	}
	if got.Converged {
		t.Error("a running move is not converged")
	}
	tr := got.Transition
	if tr == nil || !tr.Active || len(tr.Steps) != 4 {
		t.Fatalf("transition = %+v; want active with 4 steps", tr)
	}
	if tr.From != "hw-me-east-215-a-rtz-prod" || tr.To != "hw-me-east-215-b-rtz-prod" {
		t.Errorf("from/to = %s → %s", tr.From, tr.To)
	}
	if tr.Steps[1].State != "running" || !strings.Contains(tr.Steps[1].Message, "wp-db-5ff1066a") {
		t.Errorf("step 2 = %+v; want running with the controller's message verbatim", tr.Steps[1])
	}
	if !strings.HasPrefix(got.Step, "step 2/4 — data-primary: waiting") {
		t.Errorf("step line = %q; want the step the move is on", got.Step)
	}
	// The link must name the SAME Jobs group the projection writes.
	want := jobsTopologyTransitionGroup(cr, activityEntriesOf(cr))
	if tr.JobName != want || !strings.HasPrefix(want, "topology-transition-hw307-omani-works-dr-wp-") {
		t.Errorf("jobName = %q, want the projection's group %q", tr.JobName, want)
	}
}

func TestContinuumConvergence_ConvergedMoveStaysVisibleButInactive(t *testing.T) {
	cr := liveContinuum("Healthy", [4]string{"succeeded", "succeeded", "succeeded", "succeeded"}, "Converged")
	got := continuumConvergenceFromCR(cr, "")
	if got.SwitchoverInProgress || !got.Converged {
		t.Fatalf("converged move: inProgress=%v converged=%v", got.SwitchoverInProgress, got.Converged)
	}
	if got.Transition == nil || got.Transition.Active || got.Transition.Outcome != "Converged" {
		t.Errorf("transition = %+v; want the last move, inactive, outcome Converged", got.Transition)
	}
}

func TestContinuumConvergence_NoTransitionOnRecord(t *testing.T) {
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "dr-x", "namespace": "ns"},
		"status":   map[string]interface{}{"phase": "Healthy", "primaryRegion": "a", "converged": true},
	}}
	if got := continuumConvergenceFromCR(cr, ""); got.Transition != nil {
		t.Errorf("a Continuum that never moved must carry no transition, got %+v", got.Transition)
	}
}
