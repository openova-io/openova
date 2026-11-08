package handler

// ADR-0015 D7 fixtures — the three live hw307 shapes the status contract must
// answer honestly:
//
//  1. sovereign/test — Application Ready=False EnvironmentMissing
//     'Environment "sovereign-prod" not found'. The console used to show a bare
//     "Pending"; statusOf must carry the reason + message verbatim and /jobs
//     must hold exactly one row for it, "blocked: EnvironmentMissing …".
//  2. acmewalk307/wordpress — every commit blocks on a missing synchronous
//     standby; the controller publishes Serving=False per region and
//     Ready=False. statusOf must say NotReady with the per-region Serving
//     verdicts, never Ready.
//  3. the two-bp-keycloak collision — flux-system/bp-keycloak Ready,
//     acmewalk307/bp-keycloak UpgradeFailed. The answer is namespace-exact.

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

func d7Application(ns, name string, status map[string]interface{}) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps.openova.io/v1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       map[string]interface{}{"environmentRef": "sovereign-prod"},
		"status":     status,
	}}
	return u
}

func d7Cond(t, s, reason, msg, since string) map[string]interface{} {
	return map[string]interface{}{"type": t, "status": s, "reason": reason, "message": msg, "lastTransitionTime": since}
}

func sovereignTestFixture() unstructured.Unstructured {
	return d7Application("sovereign", "test", map[string]interface{}{
		"phase": "Pending",
		"conditions": []interface{}{
			d7Cond("Ready", "False", "EnvironmentMissing", `Environment "sovereign-prod" not found`, "2026-11-07T10:00:00Z"),
		},
	})
}

func TestStatusOf_SovereignTest_EnvironmentMissingVerbatim(t *testing.T) {
	app := sovereignTestFixture()
	st := statusOf(&app)
	if st.State != StateNotReady {
		t.Fatalf("state = %q, want %q (a blocked Application must never read Pending/Ready)", st.State, StateNotReady)
	}
	if st.Reason != "EnvironmentMissing" || st.Message != `Environment "sovereign-prod" not found` {
		t.Fatalf("reason/message not verbatim: %q / %q", st.Reason, st.Message)
	}
	if st.Since != "2026-11-07T10:00:00Z" || st.Namespace != "sovereign" || st.Name != "test" || st.Kind != "Application" {
		t.Fatalf("identity/since not carried: %+v", st)
	}
}

func TestStatusOf_AbsentConditionIsUnknownNotPending(t *testing.T) {
	app := d7Application("o", "fresh", map[string]interface{}{"phase": "Provisioning"})
	if st := statusOf(&app); st.State != StateUnknown || st.Reason != "" {
		t.Fatalf("no Ready condition must be Unknown with no reason, got %+v", st)
	}
	if st := statusOf(nil); st.State != StateUnknown {
		t.Fatalf("nil object must be Unknown, got %+v", st)
	}
}

func TestStatusOf_BlockedSyncWordPress_ServingFalsePerRegion(t *testing.T) {
	msg := "primary acmewalk307/wordpress-db requires 1 synchronous standby(s) (dataDurability=required); standby wordpress-db-replica is not ready (Ready=False: Cluster Is Not Ready) — every commit blocks"
	app := d7Application("acmewalk307", "wordpress", map[string]interface{}{
		"phase": "Degraded",
		"conditions": []interface{}{
			d7Cond("Ready", "False", "SynchronousStandbyNotStreaming", msg, "2026-09-10T18:45:09Z"),
			d7Cond("Serving", "False", "SynchronousStandbyNotStreaming", msg, "2026-09-10T18:45:09Z"),
		},
		"regions": []interface{}{
			map[string]interface{}{"name": "me-east-215-a", "conditions": []interface{}{
				d7Cond("Serving", "False", "SynchronousStandbyNotStreaming", msg, "2026-09-10T18:45:09Z"),
			}},
			map[string]interface{}{"name": "me-east-215-b", "conditions": []interface{}{
				d7Cond("Serving", "Unknown", "ServingNotObservable", "not observable from region me-east-215-a", "2026-09-10T18:45:09Z"),
			}},
		},
	})
	st := statusOf(&app)
	if st.State != StateNotReady || st.Reason != "SynchronousStandbyNotStreaming" || st.Message != msg {
		t.Fatalf("headline must be the controller's verdict verbatim, got %+v", st)
	}
	if len(st.PerRegion) != 2 {
		t.Fatalf("perRegion = %d entries, want 2: %+v", len(st.PerRegion), st.PerRegion)
	}
	if st.PerRegion[0].Region != "me-east-215-a" || st.PerRegion[0].State != StateNotReady {
		t.Fatalf("region a must be NotReady: %+v", st.PerRegion[0])
	}
	if st.PerRegion[1].State != StateUnknown || st.PerRegion[1].Reason != "ServingNotObservable" {
		t.Fatalf("region b must stay Unknown (never Healthy): %+v", st.PerRegion[1])
	}
}

func d7HR(ns, name, ready, reason, msg string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"status": map[string]interface{}{"conditions": []interface{}{
			d7Cond("Ready", ready, reason, msg, "2026-11-06T04:24:53Z"),
		}},
	}}
}

func TestStatusOf_TwoBpKeycloak_NamespaceExact(t *testing.T) {
	fluxKC := d7HR("flux-system", "bp-keycloak", "True", "UpgradeSucceeded", "Helm upgrade succeeded")
	orgKC := d7HR("acmewalk307", "bp-keycloak", "False", "UpgradeFailed",
		"Helm upgrade failed for release acmewalk307/bp-keycloak with chart bp-keycloak@1.5.14: post-upgrade hooks failed")
	// Cache order puts the org copy FIRST — the old name-only lookup answered
	// with whichever came first.
	hrs := []*unstructured.Unstructured{orgKC, fluxKC}

	got := orderHelmReleaseCandidates(hrs, "bp-keycloak", "acmewalk307")
	if len(got) != 1 || got[0].GetNamespace() != "acmewalk307" {
		t.Fatalf("?namespace=acmewalk307 must select exactly the acmewalk307 HR, got %d", len(got))
	}
	if st := statusOf(got[0]); st.State != StateNotReady || st.Reason != "UpgradeFailed" {
		t.Fatalf("acmewalk307/bp-keycloak must read NotReady UpgradeFailed, got %+v", st)
	}

	got = orderHelmReleaseCandidates(hrs, "bp-keycloak", "flux-system")
	if len(got) != 1 || statusOf(got[0]).State != StateReady {
		t.Fatalf("?namespace=flux-system must select the Ready bootstrap HR")
	}

	got = orderHelmReleaseCandidates(hrs, "bp-keycloak", "")
	if len(got) != 2 || got[0].GetNamespace() != "flux-system" {
		t.Fatalf("no namespace: flux-system must be first deterministically, got %v", got[0].GetNamespace())
	}
}

func TestStatusOfAcrossRegions_BrokenRegionIsTheHeadline(t *testing.T) {
	st := statusOfAcrossRegions(map[string]*unstructured.Unstructured{
		"me-east-215-a": d7HR("acmewalk307", "wordpress", "True", "InstallSucceeded", "ok"),
		"me-east-215-b": d7HR("acmewalk307", "wordpress", "False", "InstallFailed", "image pull"),
	})
	if st.State != StateNotReady || st.Reason != "InstallFailed" {
		t.Fatalf("a NotReady region must not hide behind a Ready one: %+v", st)
	}
	if len(st.PerRegion) != 2 {
		t.Fatalf("perRegion: %+v", st.PerRegion)
	}
}

func TestJobsProjection_SovereignTest_OneBlockedRow(t *testing.T) {
	store, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ready := d7Application("acme", "site", map[string]interface{}{"conditions": []interface{}{
		d7Cond("Ready", "True", "Reconciled", "installed", "2026-11-07T10:00:00Z"),
	}})
	apps := []unstructured.Unstructured{sovereignTestFixture(), ready}
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	// Project twice: the second /jobs read must not mint a second execution.
	for i := 0; i < 2; i++ {
		if err := projectApplicationObjects(store, "dep-d7", apps, nil, now); err != nil {
			t.Fatalf("project: %v", err)
		}
	}
	all, err := store.ListJobs("dep-d7")
	if err != nil {
		t.Fatal(err)
	}
	var rows []jobs.Job
	for _, j := range all {
		if j.Kind == jobs.KindApplication && strings.Contains(j.JobName, "sovereign:test") {
			rows = append(rows, j)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly ONE Jobs row for sovereign/test, got %d", len(rows))
	}
	row := rows[0]
	if row.Status != jobs.StatusFailed || row.Reason != "EnvironmentMissing" || row.Message != `Environment "sovereign-prod" not found` {
		t.Fatalf("sovereign/test row must be failed + EnvironmentMissing verbatim, got status=%q reason=%q message=%q",
			row.Status, row.Reason, row.Message)
	}
	_, execs, err := store.GetJob("dep-d7", row.JobName)
	if err != nil {
		t.Fatal(err)
	}
	if len(execs) != 1 {
		t.Fatalf("level-triggered projection must not duplicate executions on re-read: %d", len(execs))
	}
	for _, j := range all {
		if strings.Contains(j.JobName, "acme:site") && j.Status != jobs.StatusSucceeded {
			t.Fatalf("Ready Application must project succeeded, got %q", j.Status)
		}
	}

	// The Application turns green: the row follows and drops its reason.
	fixed := d7Application("sovereign", "test", map[string]interface{}{"conditions": []interface{}{
		d7Cond("Ready", "True", "Reconciled", "installed", "2026-11-08T10:00:00Z"),
	}})
	if err := projectApplicationObjects(store, "dep-d7", []unstructured.Unstructured{fixed}, nil, now); err != nil {
		t.Fatal(err)
	}
	j, _, err := store.GetJob("dep-d7", row.JobName)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != jobs.StatusSucceeded || j.Reason != "Reconciled" {
		t.Fatalf("recovered Application must read succeeded/Reconciled, got %q/%q", j.Status, j.Reason)
	}
}

func TestJobsProjection_TopologyTransitionFromStatusActivity(t *testing.T) {
	store, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cont := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "dr.openova.io/v1", "kind": "Continuum",
		"metadata": map[string]interface{}{"name": "bp-wordpress-tenant", "namespace": "acmewalk307"},
		"status": map[string]interface{}{"activity": []interface{}{
			map[string]interface{}{"id": "t1-traffic-off", "phase": "traffic-off", "member": "me-east-215-a",
				"state": "succeeded", "startedAt": "2026-11-08T01:00:00Z", "finishedAt": "2026-11-08T01:00:05Z", "message": "gateway weight 0"},
			map[string]interface{}{"id": "t1-data-primary", "phase": "data-primary", "member": "wordpress-db",
				"state": "running", "startedAt": "2026-11-08T01:00:06Z", "message": "promoting"},
			map[string]interface{}{"id": "t1-traffic-on", "phase": "traffic-on", "member": "me-east-215-b", "state": "pending"},
		}},
	}}
	if err := projectApplicationObjects(store, "dep-d7", nil, []unstructured.Unstructured{cont}, time.Now()); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListJobs("dep-d7")
	group := jobs.TopologyTransitionGroupPrefix + "acmewalk307-bp-wordpress-tenant"
	want := map[string]string{"traffic-off · me-east-215-a": jobs.StatusSucceeded, "data-primary · wordpress-db": jobs.StatusRunning, "traffic-on · me-east-215-b": jobs.StatusPending}
	sawGroup := false
	for _, j := range all {
		if j.JobName == group {
			sawGroup = true
			if j.Type != jobs.JobTypeGroup {
				t.Fatalf("transition group type = %q", j.Type)
			}
			continue
		}
		if j.Kind != jobs.KindTopologyTransition {
			continue
		}
		ws, ok := want[j.DisplayName]
		if !ok {
			t.Fatalf("unexpected transition row %q", j.DisplayName)
		}
		if j.Status != ws {
			t.Fatalf("%s: status %q want %q", j.DisplayName, j.Status, ws)
		}
		delete(want, j.DisplayName)
	}
	if !sawGroup || len(want) != 0 {
		t.Fatalf("group present=%v, missing rows=%v", sawGroup, want)
	}
}
