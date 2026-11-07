package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

func pairSpec(desired string) ContinuumSpec {
	return ContinuumSpec{
		PrimaryRegion:        "fsn-a-rtz-prod",
		HotStandbyRegions:    []string{"hel-b-rtz-prod"},
		DesiredPrimaryRegion: desired,
		CNPGPair:             "pg",
		CNPGNamespace:        "db",
	}
}

// The writer is OBSERVED from the pair, never taken from the desired state.
// hw307 2026-11-07: a placement edit declaring B made the CR report B as the
// writer while A served writes.
func TestObservedWriter_FromPairNotFromDesired(t *testing.T) {
	spec := pairSpec("hel-b-rtz-prod")
	cr := newTestContinuumCR("db", "dr-pg", spec.PrimaryRegion, spec.HotStandbyRegions, "in-memory")

	got, known := observedWriter(cr, spec, cnpg.StandbyObservation{Known: true, Available: true})
	if got != "fsn-a-rtz-prod" || !known {
		t.Fatalf("home half is the writer: got %q known=%v, want fsn-a-rtz-prod", got, known)
	}
	got, known = observedWriter(cr, spec, cnpg.StandbyObservation{Known: true, LocalHalfDemoted: true})
	if got != "hel-b-rtz-prod" || !known {
		t.Fatalf("home half demoted: got %q known=%v, want hel-b-rtz-prod", got, known)
	}
	_ = unstructured.SetNestedField(cr.Object, "hel-b-rtz-prod", "status", "primaryRegion")
	if _, known = observedWriter(cr, spec, cnpg.StandbyObservation{}); known {
		t.Fatalf("no pair reading must be reported as unknown (never acted on)")
	}
}

// Mechanisms with no structural read keep the writer a completed switchover
// moved to (status.primaryRegion), not the home region.
func TestObservedWriter_StatelessKeepsLastSwitchedRegion(t *testing.T) {
	spec := ContinuumSpec{PrimaryRegion: "fsn-a-rtz-prod", HotStandbyRegions: []string{"hel-b-rtz-prod"}}
	cr := newTestContinuumCR("ns", "dr-web", spec.PrimaryRegion, spec.HotStandbyRegions, "in-memory")
	_ = unstructured.SetNestedField(cr.Object, "hel-b-rtz-prod", "status", "primaryRegion")
	if got, _ := observedWriter(cr, spec, cnpg.StandbyObservation{}); got != "hel-b-rtz-prod" {
		t.Fatalf("got %q want hel-b-rtz-prod", got)
	}
}

func TestConvergeDecision(t *testing.T) {
	nn := types.NamespacedName{Namespace: "db", Name: "dr-pg"}
	healthy := cnpg.StandbyObservation{Known: true, Available: true, ReplicaCluster: "pg-replica"}

	cases := []struct {
		name     string
		spec     ContinuumSpec
		observed string
		known    bool
		standby  cnpg.StandbyObservation
		run      bool
		reason   string
	}{
		{"nothing declared", pairSpec(""), "fsn-a-rtz-prod", true, healthy, false, ""},
		{"declared == observed", pairSpec("fsn-a-rtz-prod"), "fsn-a-rtz-prod", true, healthy, false, "Converged"},
		{"declared differs, standby following", pairSpec("hel-b-rtz-prod"), "fsn-a-rtz-prod", true, healthy, true, "Converging"},
		{"declared differs, standby not following", pairSpec("hel-b-rtz-prod"), "fsn-a-rtz-prod", true, cnpg.StandbyObservation{Known: true, Available: false, ReplicaCluster: "pg-replica"}, false, "ConvergeBlocked"},
		{"declared differs, writer unknown", pairSpec("hel-b-rtz-prod"), "fsn-a-rtz-prod", false, cnpg.StandbyObservation{}, false, "ConvergeBlocked"},
		{"declared is not a member", pairSpec("dxb-c-rtz-prod"), "fsn-a-rtz-prod", true, healthy, false, "ConvergeBlocked"},
		{"switch back after failover", pairSpec("fsn-a-rtz-prod"), "hel-b-rtz-prod", true, cnpg.StandbyObservation{Known: true, LocalHalfDemoted: true}, true, "Converging"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := newReconciler(t)
			got := r.convergeDecision(nn, tc.spec, tc.observed, tc.known, tc.standby)
			if got.run != tc.run {
				t.Fatalf("run = %v, want %v (view=%+v)", got.run, tc.run, got.view)
			}
			reason := ""
			if got.view != nil {
				reason = got.view.reason
			}
			if reason != tc.reason {
				t.Fatalf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

// A failed attempt is retried after the back-off, never abandoned, and the
// failure is what the Converged condition says meanwhile.
func TestConvergeDecision_BackoffAfterFailure(t *testing.T) {
	nn := types.NamespacedName{Namespace: "db", Name: "dr-pg"}
	r, _, _ := newReconciler(t)
	clock := &fakeClock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	r.Now = clock.Now
	registerGoroutine(r, nn)
	spec := pairSpec("hel-b-rtz-prod")
	healthy := cnpg.StandbyObservation{Known: true, Available: true}

	r.noteConvergeFailure(nn, errors.New("validate-lease: witness unreachable"))
	got := r.convergeDecision(nn, spec, "fsn-a-rtz-prod", true, healthy)
	if got.run || got.view.reason != "ConvergeFailed" || !strings.Contains(got.view.message, "witness unreachable") {
		t.Fatalf("inside back-off: %+v", got.view)
	}
	clock.Advance(DefaultConvergeRetry + time.Second)
	if got = r.convergeDecision(nn, spec, "fsn-a-rtz-prod", true, healthy); !got.run {
		t.Fatalf("after back-off the convergence must retry: %+v", got.view)
	}
	r.noteConvergeSuccess(nn)
	if got = r.convergeDecision(nn, spec, "hel-b-rtz-prod", true, healthy); got.run || got.view.reason != "Converged" {
		t.Fatalf("converged: %+v", got.view)
	}
}

// The Converged condition is ONE entry, rewritten on each determination —
// hw307 2026-11-07: it was appended on every pass, so the stale "Converging"
// entry stayed first after the switchover had converged.
func TestPatchStatus_ConvergedConditionIsReplacedNotAppended(t *testing.T) {
	cr := newTestContinuumCR("db", "dr-pg", "fsn-a-rtz-prod", []string{"hel-b-rtz-prod"}, "in-memory")
	r, _, _ := newReconciler(t, cr)
	ctx := context.Background()
	for _, v := range []*convergeView{
		{converged: false, reason: "Converging", message: "switching"},
		{converged: true, reason: "Converged", message: "done"},
		{converged: true, reason: "Converged", message: "done"},
	} {
		if err := r.patchStatus(ctx, cr, statusUpdate{Phase: PhaseHealthy, Converge: v}); err != nil {
			t.Fatalf("patchStatus: %v", err)
		}
	}
	// A pass with no determination keeps the last one.
	if err := r.patchStatus(ctx, cr, statusUpdate{Phase: PhaseHealthy}); err != nil {
		t.Fatalf("patchStatus: %v", err)
	}
	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("db").Get(ctx, "dr-pg", metav1.GetOptions{})
	conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	var seen []string
	for _, c := range conds {
		m := c.(map[string]interface{})
		if m["type"] == "Converged" {
			seen = append(seen, m["reason"].(string))
		}
	}
	if len(seen) != 1 || seen[0] != "Converged" {
		t.Fatalf("Converged conditions = %v, want exactly [Converged]", seen)
	}
	if c, _, _ := unstructured.NestedBool(got.Object, "status", "converged"); !c {
		t.Fatalf("status.converged = false, want true")
	}
}

// With the writer in the declared (non-home) region, the home half is the
// hot standby: Healthy + StandbyAvailable=True, not "failed over".
func TestPatchStatusFromCR_PlannedRemotePrimaryIsHealthy(t *testing.T) {
	cr := newTestContinuumCR("db", "dr-pg", "fsn-a-rtz-prod", []string{"hel-b-rtz-prod"}, "in-memory")
	// The last observation (a completed switchover) put the writer in hel;
	// the controller holds the lease as that writer.
	_ = unstructured.SetNestedField(cr.Object, "hel-b-rtz-prod", "status", "primaryRegion")
	r, _, _ := newReconciler(t, cr)
	spec := pairSpec("hel-b-rtz-prod")
	home := cnpg.Status{IsReplicaCluster: true, Ready: true}
	demoted := cnpg.StandbyObservation{Known: true, LocalHalfDemoted: true, ReplicaCluster: "pg-replica"}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, witnessStateHeldBy("hel-b-rtz-prod"), home, cnpg.Status{}, demoted, false, ""); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("db").Get(context.Background(), "dr-pg", metav1.GetOptions{})
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	writer, _, _ := unstructured.NestedString(got.Object, "status", "primaryRegion")
	avail, _, _ := unstructured.NestedBool(got.Object, "status", "standbyAvailable")
	if phase != PhaseHealthy || writer != "hel-b-rtz-prod" || !avail {
		t.Fatalf("phase=%q writer=%q standbyAvailable=%v; want Healthy, hel-b-rtz-prod, true", phase, writer, avail)
	}
	// Same observation with NO declaration is an unplanned failover.
	spec.DesiredPrimaryRegion = ""
	_ = r.patchStatusFromCR(context.Background(), cr, spec, witnessStateHeldBy("hel-b-rtz-prod"), home, cnpg.Status{}, demoted, false, "")
	got, _ = r.Dyn.Resource(ContinuumGVR).Namespace("db").Get(context.Background(), "dr-pg", metav1.GetOptions{})
	if phase, _, _ = unstructured.NestedString(got.Object, "status", "phase"); phase != PhaseFailedOver {
		t.Fatalf("undeclared: phase=%q want FailedOver", phase)
	}
}

func witnessStateHeldBy(region string) witness.State {
	return witness.State{Holder: region, ExpiresAt: time.Now().Add(30 * time.Second)}
}
