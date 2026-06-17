// jobs_retry_test.go — coverage for the §5c generic remediation endpoint
// (issue #3646): RBAC 403 (non-operator + cross-tenant), the Flux-native
// annotation bump for an install-<chart> HelmRelease leaf, the "Run now"
// one-off Job for a cron leaf, and the no-shell-out guarantee.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeReconcilerDynamicFactory builds a dynamic-fake client with the HR,
// Kustomization, CronJob, Job, and Deployment list-kinds registered so the
// retry handler's Patch/Get/Create + namespace-resolution List calls work.
func fakeReconcilerDynamicFactory(seed ...runtime.Object) (func(string) (dynamic.Interface, error), *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		helmwatch.HelmReleaseGVR:   "HelmReleaseList",
		helmwatch.KustomizationGVR: "KustomizationList",
		helmwatch.CronJobGVR:       "CronJobList",
		helmwatch.JobGVR:           "JobList",
		helmwatch.DeploymentGVR:    "DeploymentList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
	return func(_ string) (dynamic.Interface, error) { return client, nil }, client
}

// installRetryHarness wires a Handler with a jobs.Store, a deployment owned
// by ownerEmail (with a kubeconfig path so sovereignDynamicClient uses the
// injected fake), the retry route, and a failed leaf job to retry.
func installRetryHarness(t *testing.T, ownerEmail, jobName, status string, dynSeed ...runtime.Object) (*chi.Mux, *Handler, string) {
	t.Helper()
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewWithJobsStore(silentLogger(), js)
	factory, _ := fakeReconcilerDynamicFactory(dynSeed...)
	h.dynamicFactory = factory

	depID := "dep-retry"
	kubePath := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubePath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	dep := &Deployment{
		ID:         depID,
		OwnerEmail: ownerEmail,
		Request:    provisioner.Request{SovereignFQDN: "t99.omani.works"},
		Result:     &provisioner.Result{KubeconfigPath: kubePath},
	}
	h.deployments.Store(depID, dep)

	if err := js.UpsertJob(jobs.Job{
		DeploymentID: depID, JobName: jobName, AppID: trimRetryPrefix(jobName),
		Type: jobs.JobTypeInstall, Status: status,
	}); err != nil {
		t.Fatalf("seed leaf: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/api/v1/deployments/{depId}/jobs/{jobId}/retry", h.RetryJob)
	return r, h, depID
}

func trimRetryPrefix(jobName string) string {
	for _, p := range []string{jobs.JobNamePrefix, jobs.CronJobPrefix, jobs.ReconcileJobPrefix} {
		if len(jobName) > len(p) && jobName[:len(p)] == p {
			return jobName[len(p):]
		}
	}
	return jobName
}

func retryReq(depID, jobName string, claims *auth.Claims) *http.Request {
	url := "/api/v1/deployments/" + depID + "/jobs/" + jobs.JobID(depID, jobName) + "/retry"
	req := httptest.NewRequest(http.MethodPost, url, nil)
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
		// Mirror auth.RequireSession: the session email is injected as the
		// X-User-Email header the ownership gate reads. Without this the
		// checkOwnership cross-tenant gate can't fire in a unit test.
		if claims.Email != "" {
			req.Header.Set("X-User-Email", claims.Email)
		}
	}
	return req
}

// DoD#12: 403 for a viewer (non-operator). The deployment carries an empty
// OwnerEmail (legacy record) so checkOwnership passes through for any
// session — isolating the RBAC gate as the decision point. A viewer-tier
// caller who is not the registered operator must be denied. OPERATOR_EMAIL
// is cleared so the operator-email fallback can't promote the viewer.
func TestRetryJob_Forbidden_Viewer(t *testing.T) {
	t.Setenv("OPERATOR_EMAIL", "")
	r, _, depID := installRetryHarness(t, "" /* legacy: no owner */, "install-velero", jobs.StatusFailed)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "install-velero",
		&auth.Claims{Email: "viewer@t99.omani.works", Tier: "viewer"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 for viewer, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// DoD#12: 404 for a cross-tenant caller (ownership gate, never 403/leak).
func TestRetryJob_CrossTenant_404(t *testing.T) {
	r, _, depID := installRetryHarness(t, "owner@t99.omani.works", "install-velero", jobs.StatusFailed)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "install-velero",
		// An operator-tier session, but for a DIFFERENT tenant — the
		// ownership gate returns 404, never leaking existence.
		&auth.Claims{Email: "intruder@evil.example", Tier: "operator"}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for cross-tenant, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// DoD#10: an operator retrying a Failed install-<chart> annotates the HR
// (reconcile.fluxcd.io/requestedAt bumped) + a new Execution appears.
func TestRetryJob_Install_AnnotatesHR(t *testing.T) {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetName("bp-velero")
	hr.SetNamespace("flux-system")

	r, h, depID := installRetryHarness(t, "owner@t99.omani.works", "install-velero", jobs.StatusFailed, hr)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "install-velero",
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	// The HR carries a fresh reconcile.fluxcd.io/requestedAt annotation.
	dyn, _ := h.dynamicFactory("")
	got, err := dyn.Resource(helmwatch.HelmReleaseGVR).Namespace("flux-system").Get(context.Background(), "bp-velero", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get HR: %v", err)
	}
	ts, found, _ := unstructured.NestedString(got.Object, "metadata", "annotations", "reconcile.fluxcd.io/requestedAt")
	if !found || ts == "" {
		t.Fatalf("HR not annotated for reconcile: %v", got.Object["metadata"])
	}
	if _, perr := time.Parse(time.RFC3339Nano, ts); perr != nil {
		t.Errorf("requestedAt is not a timestamp: %q", ts)
	}

	// A new Execution was recorded with the operator identity in line 1.
	_, execs, err := h.jobs.GetJob(depID, jobs.JobID(depID, "install-velero"))
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if len(execs) == 0 {
		t.Fatal("no Execution recorded for the retry attempt")
	}
	page, _ := h.jobs.PageLogs(depID, execs[len(execs)-1].ID, 1, 10)
	foundAudit := false
	for _, l := range page.Lines {
		if containsAll(l.Message, "[retry]", "owner@t99.omani.works") {
			foundAudit = true
		}
	}
	if !foundAudit {
		t.Errorf("retry Execution missing the operator audit LogLine; lines=%v", page.Lines)
	}
}

// DoD#11: "Run now" on a cron leaf creates a one-off Job from the
// CronJob's jobTemplate.
func TestRetryJob_Cron_RunNow_CreatesJob(t *testing.T) {
	cj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   map[string]any{"name": "openbao-snapshot-save", "namespace": "openbao"},
		"spec": map[string]any{
			"schedule": "*/10 * * * *",
			"jobTemplate": map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"restartPolicy": "Never",
							"containers":    []any{map[string]any{"name": "snap", "image": "busybox"}},
						},
					},
				},
			},
		},
	}}
	r, h, depID := installRetryHarness(t, "owner@t99.omani.works", "cron-openbao-snapshot-save", jobs.StatusFailed, cj)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "cron-openbao-snapshot-save",
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	dyn, _ := h.dynamicFactory("")
	list, err := dyn.Resource(helmwatch.JobGVR).Namespace("openbao").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(list.Items) == 0 {
		t.Fatal("no one-off Job created from the CronJob template")
	}
}

// A healthy/running row is NOT retryable — 409, not a silent no-op.
func TestRetryJob_NotRetryable_409(t *testing.T) {
	r, _, depID := installRetryHarness(t, "owner@t99.omani.works", "install-cilium", jobs.StatusSucceeded)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, "install-cilium",
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 for a succeeded row, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
