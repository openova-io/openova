// jobs_perorg_install_6045_test.go — #6045, at the surface the User
// actually looks at.
//
// The helmwatch-level guards (jobs_projection_6045_test.go) prove the
// projection. This file proves the wiring: that a GET
// /api/v1/deployments/{depId}/jobs returns a row for a per-Organization
// Application install, and that the `failed` filter — the filter a User
// reaches for when their install breaks — actually contains it.
//
// Before the fix the page answered "No jobs match the current filters"
// for the only class of install a User can cause to fail.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// perOrgAppHR builds the HelmRelease the application-controller authors
// for a per-Org Application: named after the Application, in the Org's
// own namespace, carrying catalyst.openova.io/organization. Stalled ⇒
// the Stalled=True / Ready=False+RetriesExceeded terminal pair.
func perOrgAppHR(ns, name string, stalled bool) *unstructured.Unstructured {
	now := time.Now().UTC().Format(time.RFC3339)
	conds := []any{map[string]any{
		"type": "Ready", "status": string(metav1.ConditionTrue),
		"reason": "ReconciliationSucceeded", "message": "Helm install succeeded",
		"lastTransitionTime": now,
	}}
	if stalled {
		conds = []any{
			map[string]any{
				"type": "Ready", "status": string(metav1.ConditionFalse),
				"reason": "RetriesExceeded", "message": "Helm install failed: timed out",
				"lastTransitionTime": now,
			},
			map[string]any{
				"type": "Stalled", "status": string(metav1.ConditionTrue),
				"reason": "RetriesExceeded", "message": "Failed to install after 3 attempt(s)",
				"lastTransitionTime": now,
			},
		}
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name": name, "namespace": ns,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by":     "application-controller",
				"catalyst.openova.io/application":  name,
				"catalyst.openova.io/organization": ns,
			},
		},
		"spec":   map[string]any{},
		"status": map[string]any{"conditions": conds},
	}}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease",
	})
	return u
}

// perOrgVClusterHR — the control. Every Organization has one of these in
// namespace <slug> (organization-controller gitops vclusterTemplate). Not
// bp- prefixed, not in flux-system, and it genuinely goes
// Stalled/RetriesExceeded on a cold Harbor pull. It must NOT appear.
func perOrgVClusterHR(ns string) *unstructured.Unstructured {
	u := perOrgAppHR(ns, "vcluster", true)
	u.SetLabels(map[string]string{
		"openova.io/organization":      ns,
		"openova.io/vcluster":          ns,
		"app.kubernetes.io/managed-by": "flux",
	})
	return u
}

func listJobs6045(t *testing.T, r http.Handler, depID, query string) []map[string]any {
	t.Helper()
	url := "/api/v1/deployments/" + depID + "/jobs"
	if query != "" {
		url += "?" + query
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: want 200 got %d body=%s", url, w.Code, w.Body.String())
	}
	var body struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /jobs: %v (raw=%s)", err, w.Body.String())
	}
	return body.Jobs
}

func jobNames6045(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, j := range rows {
		if n, ok := j["jobName"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

// perOrgDynamicFactory registers every list kind chrootSeedJobsStoreIfEmpty
// touches (the reconciler-observation leg lists Kustomizations / CronJobs /
// Jobs / Deployments too), so the fake serves an empty list instead of
// panicking — same set as fakeReseedDynamicClient.
func perOrgDynamicFactory(objs ...runtime.Object) func(string) (dynamic.Interface, error) {
	return func(_ string) (dynamic.Interface, error) {
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
		return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
			map[schema.GroupVersionResource]string{
				helmwatch.HelmReleaseGVR: "HelmReleaseList",
				{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: "KustomizationList",
				{Group: "batch", Version: "v1", Resource: "cronjobs"}:                             "CronJobList",
				{Group: "batch", Version: "v1", Resource: "jobs"}:                                 "JobList",
				{Group: "apps", Version: "v1", Resource: "deployments"}:                           "DeploymentList",
			}, objs...), nil
	}
}

func newPerOrgJobsHarness(t *testing.T) (http.Handler, string) {
	t.Helper()
	r, _, h := newBackfillRouter(t)
	h.dynamicFactory = perOrgDynamicFactory(
		// bootstrap-kit — must keep rendering
		makeReadyHR("bp-cilium"),
		makeReadyHR("bp-openbao"),
		// the per-Org Application install a User caused to FAIL
		perOrgAppHR("acme", "marketing-site", true),
		// a healthy per-Org Application install
		perOrgAppHR("acme", "docs-site", false),
		// CONTROL — per-Org infrastructure that must stay off the page
		perOrgVClusterHR("acme"),
	)
	depID := "d6045"
	makeDeploymentForBackfill(t, h, depID, "apiVersion: v1\nkind: Config\n")
	return r, depID
}

// TestJobsPage_ShowsPerOrgApplicationInstall — the decisive surface-level
// guard. Asserts on the VALUE: a row that IS the acme/marketing-site
// install, not merely that the list is non-empty.
func TestJobsPage_ShowsPerOrgApplicationInstall(t *testing.T) {
	r, depID := newPerOrgJobsHarness(t)
	rows := listJobs6045(t, r, depID, "")

	var found map[string]any
	for _, j := range rows {
		if n, _ := j["jobName"].(string); strings.Contains(n, "acme:marketing-site") {
			found = j
			break
		}
	}
	if found == nil {
		t.Fatalf("no /jobs row for the per-Org Application install acme/marketing-site — the only "+
			"installs a User can cause to fail are invisible on the page (#6045). %d rows: %v",
			len(rows), jobNames6045(rows))
	}
	if st, _ := found["status"].(string); st != "failed" {
		t.Errorf("acme/marketing-site is Stalled=True reason=RetriesExceeded — the row must read "+
			"failed, got %q", st)
	}
}

// TestJobsPage_FailedFilterContainsPerOrgInstall — the `failed` filter is
// where a User goes when their install breaks. A row that exists but is
// unreachable through that filter is still invisible in practice.
func TestJobsPage_FailedFilterContainsPerOrgInstall(t *testing.T) {
	r, depID := newPerOrgJobsHarness(t)
	rows := listJobs6045(t, r, depID, "status=failed")

	for _, j := range rows {
		if n, _ := j["jobName"].(string); strings.Contains(n, "acme:marketing-site") {
			return
		}
	}
	t.Fatalf("the `failed` filter does not contain the failing per-Org Application install — "+
		"a User watching a real install fail still sees \"No jobs match the current filters\". "+
		"%d failed rows: %v", len(rows), jobNames6045(rows))
}

// TestJobsPage_ExcludesPerOrgInfrastructure — the CONTROL. A fix that
// simply listed every namespace and dropped the `bp-` filter passes both
// tests above while putting a spurious Failed `vcluster` row on the page
// for every Organization on the Sovereign.
func TestJobsPage_ExcludesPerOrgInfrastructure(t *testing.T) {
	r, depID := newPerOrgJobsHarness(t)
	for _, j := range listJobs6045(t, r, depID, "") {
		n, _ := j["jobName"].(string)
		if strings.Contains(n, "vcluster") {
			t.Fatalf("per-Org INFRASTRUCTURE leaked onto /jobs as %q — it is not an Application a "+
				"User installed, and it legitimately goes Stalled/RetriesExceeded on a cold "+
				"Harbor pull, so this is one spurious Failed row per Organization", n)
		}
	}
}

// TestJobsPage_StillShowsBootstrapKit — the widening must not cost the
// platform rows the page already had.
func TestJobsPage_StillShowsBootstrapKit(t *testing.T) {
	r, depID := newPerOrgJobsHarness(t)
	names := jobNames6045(listJobs6045(t, r, depID, ""))
	for _, want := range []string{"install-cilium", "install-openbao"} {
		hit := false
		for _, n := range names {
			if n == want {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("bootstrap-kit row %q disappeared from /jobs after the #6045 widening. rows: %v",
				want, names)
		}
	}
}
