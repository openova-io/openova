// application_controller_5955_test.go — #5955, UAT row 238.
//
// An Application reported Ready=True over a database with ZERO ready instances,
// on 2 of 2 per-Org Postgres clusters. The Application's own condition message
// named the mechanism:
//
//	"Application hw292-omani-works/uat-ahs-pg installed across 2 region(s);
//	 Ready=True from downstream HelmRelease(s)"
//
// Readiness was derived SOLELY from HelmRelease readiness. The HelmReleases were
// legitimately True — Helm rendered valid YAML and the apiserver accepted the
// Cluster CR. Nothing in the chain ever read the backing object's own readiness,
// so any backing resource that fails AFTER admission produced a green badge over
// a dead workload. Not Postgres-specific: that is the class.
//
// The live status key sets these fixtures reproduce (dumped read-only off hw292
// region A on 2026-08-09; the key SET was dumped rather than guessed, so
// `readyInstances` absent is reproduced as absent, not as 0):
//
//	control  shared-data/shared-pg          instances=3 readyInstances=3 Ready=True
//	control  cnpg/cnpg-pair-primary         instances=3 readyInstances=3 Ready=True
//	control  catalyst-system/openova-flow-pg instances=2 readyInstances=2 Ready=FALSE
//	PER-ORG  hw292-omani-works/postgres     instances=1 readyInstances=ABSENT Ready=False
//	PER-ORG  uatco/postgres                 instances=1 readyInstances=ABSENT Ready=False
//
// The openova-flow-pg control is why the gate keys on the Ready CONDITION and
// not on the instance counts: readyInstances == instances there while the
// cluster's own Ready condition is False, so a count-only gate would have
// false-passed it.
//
// Asserted in all three directions, because the whole point of the fix is the
// third one:
//
//  1. backing reports NOT-READY  → Application must NOT be Ready (Degraded).
//  2. backing reports ready      → Application must still reach Ready (no
//     over-rotation), and an Application with NO backing of a
//     registered kind must still reach Ready normally.
//  3. backing UNOBSERVABLE       → Application must report Ready=Unknown with
//     reason BackingReadinessUnverifiable — never a
//     silent pass. This is the state the predecessor
//     gate (#5513) collapsed into "healthy", which is
//     why it could not fail on hw292 at all: the
//     controller SA has no grant on the backing kind,
//     so every List returned Forbidden and every
//     Forbidden was skipped.
package controller

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// hw292PerOrgCNPGStatus is the VERBATIM live status shape of the two defective
// per-Org Postgres clusters: a Ready condition of False, a declared instance
// count, and NO readyInstances key at all.
func hw292PerOrgCNPGStatus() map[string]interface{} {
	return map[string]interface{}{
		"phase":     "Setting up primary",
		"instances": int64(1),
		// status.readyInstances deliberately ABSENT — hasKey=False live.
		"conditions": []interface{}{
			map[string]interface{}{"type": "ConsistentSystemID", "status": "False"},
			map[string]interface{}{"type": "Ready", "status": "False"},
		},
	}
}

// newAppOverCNPG builds the hw292 shape: an Application whose single fan-out
// HelmRelease is Ready=True, over one backing CNPG Cluster.
func newAppOverCNPG(t *testing.T, backing ...*unstructured.Unstructured) *Reconciler {
	t.Helper()
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-postgres", "0.2.6", "mgmt",
		[]string{"mgmt-A"},
	)
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6",
		"single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		nil,
	)
	hr := readyHR("obs-mgmt-a", "mgmt", "True")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	objs := append([]*unstructured.Unstructured{app, env, org, bp, hr}, backing...)
	return newReconciler(t, fg, objs...)
}

// TestReconcile_5955_ReadyHR_BackingZeroReadyInstances_NotReady — THE failing-
// pre-fix direction, reproducing UAT row 238 verbatim. The HelmRelease is
// Ready=True (Helm applied the chart, the apiserver accepted the Cluster CR)
// but the backing CNPG Cluster reports Ready=False with status.readyInstances
// absent entirely. The Application must NOT report Ready.
//
// Before the fix the gate asked only "is status.phase unrecoverable?" — and
// "Setting up primary" is not — so the Application settled on
// phase=Ready / Ready=True over a database with zero ready instances.
func TestReconcile_5955_ReadyHR_BackingZeroReadyInstances_NotReady(t *testing.T) {
	cnpg := makeCNPGClusterWithStatus("mgmt", "postgres", "obs-mgmt-a", hw292PerOrgCNPGStatus())
	r := newAppOverCNPG(t, cnpg)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if ready := readyConditionStatus(t, got); ready == "True" {
		t.Fatalf("Ready condition = True over a backing CNPG with status.conditions[Ready]=False and "+
			"status.readyInstances absent — the #5955 green badge over a dead database "+
			"(phase=%q reason=%q msg=%q)", phase, reason, msg)
	}
	if phase == PhaseReady {
		t.Fatalf("phase = Ready over a backing with zero ready instances (msg=%q)", msg)
	}
	if phase != PhaseDegraded {
		t.Fatalf("phase = %q, want %q — the backing was READ and reports not-ready, which is a verdict, "+
			"not an unobservable state", phase, PhaseDegraded)
	}
	if reason != ReasonBackingNotReady {
		t.Errorf("reason = %q, want %q", reason, ReasonBackingNotReady)
	}
	// The message must name the object the operator has to go look at.
	for _, want := range []string{"mgmt/postgres", "Ready"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// TestReconcile_5955_ReadyHR_HealthyBacking_StaysReady — control direction 1.
// A backing that reports itself ready must not be swept up: the Application
// still reaches Ready.
func TestReconcile_5955_ReadyHR_HealthyBacking_StaysReady(t *testing.T) {
	cnpg := makeCNPGClusterWithStatus("mgmt", "postgres", "obs-mgmt-a", map[string]interface{}{
		"phase":          "Cluster in healthy state",
		"instances":      int64(3),
		"readyInstances": int64(3),
		"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True"},
			map[string]interface{}{"type": "ConsistentSystemID", "status": "True"},
		},
	})
	r := newAppOverCNPG(t, cnpg)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Fatalf("phase = %q, want %q over a healthy backing (reason=%q msg=%q)", phase, PhaseReady, reason, msg)
	}
	if ready := readyConditionStatus(t, got); ready != "True" {
		t.Fatalf("Ready condition = %q, want True over a healthy backing", ready)
	}
}

// TestReconcile_5955_NoBackingResource_StaysReady — control direction 2, the
// no-regression bar. The overwhelming majority of Applications install nothing
// of a registered backing kind. Absence of a backing is NOT a failure to
// observe one: those Applications must reach Ready exactly as before.
func TestReconcile_5955_NoBackingResource_StaysReady(t *testing.T) {
	r := newAppOverCNPG(t) // no backing objects at all

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Fatalf("phase = %q, want %q for an Application with no backing resource — "+
			"an empty list must not read as unobservable (reason=%q msg=%q)", phase, PhaseReady, reason, msg)
	}
	if ready := readyConditionStatus(t, got); ready != "True" {
		t.Fatalf("Ready condition = %q, want True for an Application with no backing resource", ready)
	}
}

// TestReconcile_5955_BackingForbidden_IsUnverifiableNotReady — control
// direction 3, and the heart of the fix. This is the LIVE hw292 shape: the
// application-controller ServiceAccount has no grant on
// postgresql.cnpg.io/clusters, so the List returns Forbidden.
//
//	kubectl auth can-i list clusters.postgresql.cnpg.io -n hw292-omani-works \
//	  --as=system:serviceaccount:catalyst-system:catalyst-application-controller
//	-> no          (control, same impersonation, helmreleases -> yes)
//
// The predecessor gate skipped that error and let the Application settle on
// Ready=True — a missing permission silently rendered as a healthy database.
// The Application must instead report Ready=Unknown: we have no evidence of
// failure, only an absence of evidence of health.
func TestReconcile_5955_BackingForbidden_IsUnverifiableNotReady(t *testing.T) {
	cnpg := makeCNPGClusterWithStatus("mgmt", "postgres", "obs-mgmt-a", map[string]interface{}{
		"phase":          "Cluster in healthy state",
		"instances":      int64(3),
		"readyInstances": int64(3),
		"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True"},
		},
	})
	r := newAppOverCNPG(t, cnpg)
	// Even with a HEALTHY backing present, a Forbidden read must not pass:
	// the controller did not see it. Sharing the healthy fixture with the
	// control above makes the ONLY difference the observability of the read.
	forbidCNPGList(t, r)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if ready := readyConditionStatus(t, got); ready == "True" {
		t.Fatalf("Ready condition = True while the backing read was Forbidden — a missing RBAC grant "+
			"must never render as a healthy workload (phase=%q reason=%q msg=%q)", phase, reason, msg)
	} else if ready != "Unknown" {
		t.Fatalf("Ready condition = %q, want Unknown for an unobservable backing", ready)
	}
	if phase == PhaseDegraded {
		t.Fatalf("phase = Degraded for an UNOBSERVED backing — that fabricates a failure we did not measure (msg=%q)", msg)
	}
	if reason != ReasonBackingUnverifiable {
		t.Errorf("reason = %q, want %q", reason, ReasonBackingUnverifiable)
	}
	if !strings.Contains(strings.ToLower(msg), "could not be verified") {
		t.Errorf("message %q does not say the readiness could not be verified", msg)
	}
}

// TestReconcile_5955_BackingKindAbsent_StaysReady — the counterpart of the
// Forbidden case, and the reason "cannot observe" is not simply "any List
// error". A Sovereign with no CNPG CRD installed serves NotFound for the
// resource path; that is positive knowledge that there is no backing of that
// kind, so every Application must keep reaching Ready. Without this branch the
// fix would rotate every Application on a Postgres-free Sovereign to Unknown.
func TestReconcile_5955_BackingKindAbsent_StaysReady(t *testing.T) {
	r := newAppOverCNPG(t)
	errCNPGList(t, r, apierrors.NewNotFound(
		schema.GroupResource{Group: CNPGClusterGVR.Group, Resource: CNPGClusterGVR.Resource}, ""))

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Fatalf("phase = %q, want %q when the backing CRD is not served by this Sovereign "+
			"(reason=%q msg=%q)", phase, PhaseReady, reason, msg)
	}
}

// TestBackingKindAbsent_Classification — the read-error classifier in
// isolation, both directions. Only "the kind is not served here" counts as
// absent; every failure-to-observe must fall through to the unobservable path.
func TestBackingKindAbsent_Classification(t *testing.T) {
	gr := schema.GroupResource{Group: CNPGClusterGVR.Group, Resource: CNPGClusterGVR.Resource}
	absent := []error{
		apierrors.NewNotFound(gr, ""),
		runtime.NewNotRegisteredErrForKind("test", schema.GroupVersionKind{
			Group: CNPGClusterGVR.Group, Version: CNPGClusterGVR.Version, Kind: "ClusterList"}),
	}
	for _, err := range absent {
		if !backingKindAbsent(err) {
			t.Errorf("backingKindAbsent(%v) = false, want true", err)
		}
	}
	unobservable := []error{
		apierrors.NewForbidden(gr, "postgres", nil),
		apierrors.NewUnauthorized("no token"),
		apierrors.NewTimeoutError("too slow", 1),
		apierrors.NewServiceUnavailable("apiserver down"),
		apierrors.NewTooManyRequestsError("throttled"),
		context.DeadlineExceeded,
	}
	for _, err := range unobservable {
		if backingKindAbsent(err) {
			t.Errorf("backingKindAbsent(%v) = true, want false — a failure to observe must never "+
				"be classified as 'there is nothing to observe'", err)
		}
	}
	if backingKindAbsent(nil) {
		t.Errorf("backingKindAbsent(nil) = true, want false")
	}
}

// TestBackingReadiness_NoSignal_IsUnobservable — a backing object that exists
// but has published NO readiness at all (freshly admitted, controller has not
// reconciled it yet) is unobservable, never ready. That window — "the apiserver
// accepted it and nothing has confirmed it came up" — is exactly the one the
// HelmRelease-only derivation reported as green.
func TestBackingReadiness_NoSignal_IsUnobservable(t *testing.T) {
	cases := []struct {
		name   string
		status map[string]interface{}
	}{
		{"empty status", map[string]interface{}{}},
		{"phase only", map[string]interface{}{"phase": "Setting up primary"}},
		{"instances but no readiness", map[string]interface{}{
			"phase": "Setting up primary", "instances": int64(1)}},
		{"Ready condition Unknown", map[string]interface{}{
			"phase":      "Setting up primary",
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "Unknown"}},
		}},
	}
	for _, c := range cases {
		cr := makeCNPGClusterWithStatus("mgmt", "postgres", "obs-mgmt-a", c.status)
		state, detail := cnpgClusterReadiness(cr)
		if state != backingUnobservable {
			t.Errorf("%s: state = %v, want backingUnobservable (detail=%q)", c.name, state, detail)
		}
	}
}

// TestBackingReadiness_ReadyConditionOutranksCounts — the openova-flow-pg
// control, verbatim from hw292 region A: instances=2 AND readyInstances=2 while
// status.conditions[Ready] is False (phase "Instance Status Extraction Error").
// A gate keyed on the counts would call this healthy; the Ready condition is
// the load-bearing signal and must win.
func TestBackingReadiness_ReadyConditionOutranksCounts(t *testing.T) {
	cr := makeCNPGClusterWithStatus("catalyst-system", "openova-flow-pg", "obs-mgmt-a", map[string]interface{}{
		"phase":          "Instance Status Extraction Error: HTTP communication issue",
		"instances":      int64(2),
		"readyInstances": int64(2),
		"conditions": []interface{}{
			map[string]interface{}{"type": "ConsistentSystemID", "status": "False"},
			map[string]interface{}{"type": "Ready", "status": "False"},
		},
	})
	state, detail := cnpgClusterReadiness(cr)
	if state != backingNotReady {
		t.Fatalf("state = %v, want backingNotReady — readyInstances==instances must not override a "+
			"Ready condition of False (detail=%q)", state, detail)
	}
	if backingNotReadyReason(detail) != ReasonBackingNotReady {
		t.Errorf("reason = %q, want %q", backingNotReadyReason(detail), ReasonBackingNotReady)
	}
}

// TestObserveBackingReadiness_NotReadyOutranksUnobservable — rollup precedence.
// An Application whose fan-out spans two namespaces, one unreadable and one
// holding a backing that reports not-ready, must surface the VERDICT (the fact
// we measured) rather than the unobservable state.
func TestObserveBackingReadiness_NotReadyOutranksUnobservable(t *testing.T) {
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	cnpg := makeCNPGClusterWithStatus("mgmt", "postgres", "obs-mgmt-a", hw292PerOrgCNPGStatus())
	fg := newFakeGitea()
	r := newReconciler(t, fg, app, cnpg)
	// Fail the read in the OTHER namespace only, leaving the not-ready backing
	// in `mgmt` readable.
	failListInNamespace(t, r, "acme", apierrors.NewForbidden(
		schema.GroupResource{Group: CNPGClusterGVR.Group, Resource: CNPGClusterGVR.Resource}, "postgres", nil))

	v := r.observeBackingReadiness(context.Background(), app, []map[string]interface{}{
		{"hr": "obs-mgmt-a", "namespace": "mgmt"},
	})
	if v.state != backingNotReady {
		t.Fatalf("state = %v, want backingNotReady (detail=%q)", v.state, v.detail)
	}
	if v.reason != ReasonBackingNotReady {
		t.Errorf("reason = %q, want %q", v.reason, ReasonBackingNotReady)
	}
}

// --- fake-client reactors -------------------------------------------------

// fakeDyn exposes the reactor hook on the fake dynamic client the test
// reconciler is built with.
func fakeDyn(t *testing.T, r *Reconciler) *dynamicfake.FakeDynamicClient {
	t.Helper()
	f, ok := r.Dynamic.(*dynamicfake.FakeDynamicClient)
	if !ok {
		t.Fatalf("test dynamic client is %T, want *dynamicfake.FakeDynamicClient", r.Dynamic)
	}
	return f
}

// errCNPGList makes every CNPG Cluster list return err.
func errCNPGList(t *testing.T, r *Reconciler, err error) {
	t.Helper()
	fakeDyn(t, r).PrependReactor("list", CNPGClusterGVR.Resource,
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, err
		})
}

// forbidCNPGList makes every CNPG Cluster list return Forbidden — the live
// hw292 RBAC shape.
func forbidCNPGList(t *testing.T, r *Reconciler) {
	t.Helper()
	errCNPGList(t, r, apierrors.NewForbidden(
		schema.GroupResource{Group: CNPGClusterGVR.Group, Resource: CNPGClusterGVR.Resource},
		"postgres",
		errNoGrant{},
	))
}

// failListInNamespace makes CNPG Cluster lists fail in ONE namespace only.
func failListInNamespace(t *testing.T, r *Reconciler, ns string, err error) {
	t.Helper()
	fakeDyn(t, r).PrependReactor("list", CNPGClusterGVR.Resource,
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() == ns {
				return true, nil, err
			}
			return false, nil, nil
		})
}

type errNoGrant struct{}

func (errNoGrant) Error() string {
	return `clusters.postgresql.cnpg.io is forbidden: User "system:serviceaccount:catalyst-system:catalyst-application-controller" cannot list resource "clusters"`
}
