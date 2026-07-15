package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// TestHandler_ListJobs_InventoryFull pins the #4731 amendment as amended
// by #5019: the default /jobs view drops the open-ended reconcile /
// reconciler leaves but KEEPS the bootstrap-kit install leaves (#5019
// install lens), while ?inventory=full returns the COMPLETE platform
// inventory the Dashboard treemap needs so a converged Sovereign renders
// every HelmRelease install, Flux Kustomization, and reconciler
// Deployment.
func TestHandler_ListJobs_InventoryFull(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-inventory"

	t0 := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	bkParent := jobs.JobID(depID, jobs.GroupBootstrapKit)
	recParent := jobs.JobID(depID, jobs.GroupReconcilers)
	cutParent := jobs.JobID(depID, jobs.GroupCutover)
	seed := []jobs.Job{
		// bootstrap-kit + continuous HelmRelease installs — dropped by the
		// finite view, MUST reappear under ?inventory=full.
		{DeploymentID: depID, JobName: jobs.GroupBootstrapKit, DisplayName: jobs.GroupBootstrapKitDisplay, Type: jobs.JobTypeGroup, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "install-cilium", AppID: "cilium", Kind: jobs.KindInstall, Type: jobs.JobTypeInstall, ParentID: bkParent, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: ptrTime(t0.Add(20 * time.Second))},
		{DeploymentID: depID, JobName: "install-keycloak", AppID: "keycloak", Kind: jobs.KindInstall, Type: jobs.JobTypeInstall, ParentID: bkParent, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: ptrTime(t0.Add(30 * time.Second))},
		// reconcilers group: a Flux Kustomization (dropped by finite) + a
		// reconciler Deployment (dropped) + a CronJob (kept both ways).
		{DeploymentID: depID, JobName: jobs.GroupReconcilers, DisplayName: jobs.GroupReconcilersDisplay, Type: jobs.JobTypeGroup, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "reconcile-flux-system", Kind: jobs.KindReconcile, Type: jobs.JobTypeInstall, ParentID: recParent, Status: jobs.StatusRunning, StartedAt: &t0},
		{DeploymentID: depID, JobName: "reconciler-pool-domain-manager", Kind: jobs.KindReconciler, Type: jobs.JobTypeInstall, ParentID: recParent, Status: jobs.StatusHealthy, StartedAt: &t0},
		{DeploymentID: depID, JobName: "cron-trivy-scan", Kind: jobs.KindCron, Type: jobs.JobTypeInstall, ParentID: recParent, Status: jobs.StatusHealthy, StartedAt: &t0},
		// cutover group: a dormant step (kept both ways — it is finite).
		{DeploymentID: depID, JobName: jobs.GroupCutover, DisplayName: jobs.GroupCutoverDisplay, Type: jobs.JobTypeGroup, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "cutover-step-01-gitea", Kind: jobs.KindStep, Type: jobs.JobTypeInstall, ParentID: cutParent, Status: jobs.StatusPending},
	}
	for _, j := range seed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}

	names := func(path string) map[string]string {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: code %d", path, rec.Code)
		}
		var resp struct {
			Jobs []jobs.Job `json:"jobs"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := map[string]string{}
		for _, j := range resp.Jobs {
			out[j.JobName] = j.Kind
		}
		return out
	}

	// Default view: open-ended reconcile/reconciler leaves are gone; the
	// install leaves (#5019), the finite cutover step, and the cron survive.
	finite := names("/api/v1/deployments/" + depID + "/jobs")
	for _, dropped := range []string{"reconcile-flux-system", "reconciler-pool-domain-manager"} {
		if _, ok := finite[dropped]; ok {
			t.Errorf("default /jobs leaked continuous reconciler %q", dropped)
		}
	}
	for _, kept := range []string{"install-cilium", "install-keycloak"} {
		if got, ok := finite[kept]; !ok {
			t.Errorf("default /jobs dropped install row %q (#5019)", kept)
		} else if got != jobs.KindInstall {
			t.Errorf("default /jobs %q kind = %q, want %q", kept, got, jobs.KindInstall)
		}
	}
	if _, ok := finite["cutover-step-01-gitea"]; !ok {
		t.Errorf("default /jobs dropped the finite cutover step")
	}

	// Full inventory: every leaf present — the 2 installs, the Kustomization,
	// the reconciler Deployment, the cron, and the cutover step.
	full := names("/api/v1/deployments/" + depID + "/jobs?inventory=full")
	for _, want := range []struct{ name, kind string }{
		{"install-cilium", jobs.KindInstall},
		{"install-keycloak", jobs.KindInstall},
		{"reconcile-flux-system", jobs.KindReconcile},
		{"reconciler-pool-domain-manager", jobs.KindReconciler},
		{"cron-trivy-scan", jobs.KindCron},
		{"cutover-step-01-gitea", jobs.KindStep},
	} {
		if got, ok := full[want.name]; !ok {
			t.Errorf("inventory=full missing %q", want.name)
		} else if got != want.kind {
			t.Errorf("inventory=full %q kind = %q, want %q", want.name, got, want.kind)
		}
	}
	// Full inventory is strictly a superset — it carries the open-ended
	// reconcile/reconciler leaves the default view drops.
	if len(full) <= len(finite) {
		t.Errorf("inventory=full (%d leaves) should exceed default (%d)", len(full), len(finite))
	}
}
