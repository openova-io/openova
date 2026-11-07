package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
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
