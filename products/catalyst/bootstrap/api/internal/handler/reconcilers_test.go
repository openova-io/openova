// reconcilers_test.go — coverage for the #3996 lightweight ArgoCD/Flux
// management surface: LIST (status + revision + suspended), the
// reconcile/suspend/resume action patches via the dynamic client (no
// shell-out), and the RBAC 403 / unknown-kind 400 gates.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeManageDynamicFactory registers the manageable Flux GVRs (the six the
// management surface lists + patches) with the dynamic-fake client so the
// List + Patch calls resolve.
func fakeManageDynamicFactory(seed ...runtime.Object) (func(string) (dynamic.Interface, error), *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		helmwatch.HelmReleaseGVR:    "HelmReleaseList",
		helmwatch.KustomizationGVR:  "KustomizationList",
		helmwatch.GitRepositoryGVR:  "GitRepositoryList",
		helmwatch.OCIRepositoryGVR:  "OCIRepositoryList",
		helmwatch.HelmRepositoryGVR: "HelmRepositoryList",
		helmwatch.HelmChartGVR:      "HelmChartList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
	return func(_ string) (dynamic.Interface, error) { return client, nil }, client
}

// helmReleaseObj builds an unstructured HelmRelease with the given Ready
// status + suspend + applied revision.
func helmReleaseObj(name string, ready bool, suspend bool, revision string) *unstructured.Unstructured {
	readyStatus := "True"
	if !ready {
		readyStatus = "False"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      name,
			"namespace": helmwatch.FluxNamespace,
		},
		"spec": map[string]any{"suspend": suspend},
		"status": map[string]any{
			"lastAppliedRevision": revision,
			"conditions": []any{map[string]any{
				"type":               "Ready",
				"status":             readyStatus,
				"reason":             "ReconciliationSucceeded",
				"message":            "Release reconciliation succeeded",
				"lastTransitionTime": "2026-06-20T10:00:00Z",
			}},
		},
	}}
}

// manageHarness wires a Handler with a deployment owned by ownerEmail (with
// a kubeconfig so sovereignDynamicClient/CoreClient use the injected fakes),
// the three management routes, and the seeded dynamic objects.
func manageHarness(t *testing.T, ownerEmail string, dynSeed ...runtime.Object) (*chi.Mux, *Handler, string) {
	t.Helper()
	h := New(silentLogger())
	factory, _ := fakeManageDynamicFactory(dynSeed...)
	h.dynamicFactory = factory
	h.coreFactory = func(_ string) (kubernetes.Interface, error) {
		return kfake.NewSimpleClientset(), nil
	}

	depID := "dep-manage"
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

	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{depId}/reconcilers", h.ListReconcilers)
	r.Get("/api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/logs", h.GetReconcilerLogs)
	r.Post("/api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/{action}", h.ReconcilerAction)
	return r, h, depID
}

func manageActionReq(depID, kind, ns, name, action string, claims *auth.Claims) *http.Request {
	url := "/api/v1/deployments/" + depID + "/reconcilers/" + kind + "/" + ns + "/" + name + "/" + action
	req := httptest.NewRequest(http.MethodPost, url, nil)
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
		if claims.Email != "" {
			req.Header.Set("X-User-Email", claims.Email)
		}
	}
	return req
}

// List returns the seeded reconcilers with live status + revision +
// suspended flag, and the reconciled/total counts.
func TestListReconcilers_StatusRevisionSuspended(t *testing.T) {
	r, _, depID := manageHarness(t, "",
		helmReleaseObj("bp-keycloak", true /*ready*/, false /*suspend*/, "0.4.1"),
		helmReleaseObj("bp-grafana", true, true /*suspended*/, "8.0.0"),
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/deployments/"+depID+"/reconcilers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Reconcilers []helmwatch.ManagedReconciler `json:"reconcilers"`
		Reconciled  int                           `json:"reconciled"`
		Total       int                           `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 2 {
		t.Fatalf("want total=2, got %d", out.Total)
	}
	// Reconciled count = only the non-suspended Ready=True one (the suspended
	// row maps to Suspended, NOT Reconciled).
	if out.Reconciled != 1 {
		t.Fatalf("want reconciled=1 (suspended excluded), got %d", out.Reconciled)
	}
	byName := map[string]helmwatch.ManagedReconciler{}
	for _, rc := range out.Reconcilers {
		byName[rc.Name] = rc
	}
	kc := byName["bp-keycloak"]
	if kc.State != helmwatch.ManageStateReconciled || kc.Revision != "0.4.1" || kc.Suspended {
		t.Fatalf("keycloak row wrong: %+v", kc)
	}
	if kc.Controller != "helm-controller" {
		t.Fatalf("keycloak controller want helm-controller, got %q", kc.Controller)
	}
	gf := byName["bp-grafana"]
	if gf.State != helmwatch.ManageStateSuspended || !gf.Suspended {
		t.Fatalf("grafana row should be Suspended: %+v", gf)
	}
}

// Reconcile annotates reconcile.fluxcd.io/requestedAt on the live object via
// the dynamic client (no shell-out).
func TestReconcilerAction_Reconcile_AnnotatesRequestedAt(t *testing.T) {
	r, h, depID := manageHarness(t, "",
		helmReleaseObj("bp-keycloak", true, false, "0.4.1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, manageActionReq(depID, "HelmRelease", helmwatch.FluxNamespace, "bp-keycloak", "reconcile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	dyn, _ := h.dynamicFactory("")
	got, err := dyn.Resource(helmwatch.HelmReleaseGVR).Namespace(helmwatch.FluxNamespace).
		Get(context.Background(), "bp-keycloak", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	ann := got.GetAnnotations()
	if ann[reconcileRequestedAtAnnotation] == "" {
		t.Fatalf("want %s annotation set, annotations=%v", reconcileRequestedAtAnnotation, ann)
	}
}

// Suspend then Resume flip spec.suspend on the live object.
func TestReconcilerAction_SuspendResume(t *testing.T) {
	r, h, depID := manageHarness(t, "",
		helmReleaseObj("bp-keycloak", true, false, "0.4.1"))
	dyn, _ := h.dynamicFactory("")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, manageActionReq(depID, "HelmRelease", helmwatch.FluxNamespace, "bp-keycloak", "suspend", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := dyn.Resource(helmwatch.HelmReleaseGVR).Namespace(helmwatch.FluxNamespace).
		Get(context.Background(), "bp-keycloak", metav1.GetOptions{})
	if s, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend"); !s {
		t.Fatalf("after suspend want spec.suspend=true, got %v", got.Object["spec"])
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, manageActionReq(depID, "HelmRelease", helmwatch.FluxNamespace, "bp-keycloak", "resume", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume want 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	got, _ = dyn.Resource(helmwatch.HelmReleaseGVR).Namespace(helmwatch.FluxNamespace).
		Get(context.Background(), "bp-keycloak", metav1.GetOptions{})
	if s, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend"); s {
		t.Fatalf("after resume want spec.suspend=false, got %v", got.Object["spec"])
	}
}

// A non-operator (viewer) session is denied 403. OwnerEmail empty +
// OPERATOR_EMAIL cleared isolates the RBAC gate as the decision point.
func TestReconcilerAction_Forbidden_Viewer(t *testing.T) {
	t.Setenv("OPERATOR_EMAIL", "")
	r, _, depID := manageHarness(t, "",
		helmReleaseObj("bp-keycloak", true, false, "0.4.1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, manageActionReq(depID, "HelmRelease", helmwatch.FluxNamespace, "bp-keycloak", "suspend",
		&auth.Claims{Email: "viewer@t99.omani.works", Tier: "viewer"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 for viewer, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// An unknown kind is rejected 400 before touching the cluster.
func TestReconcilerAction_UnknownKind(t *testing.T) {
	r, _, depID := manageHarness(t, "")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, manageActionReq(depID, "Banana", "ns", "x", "reconcile", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown kind, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// An unsupported action is rejected 400.
func TestReconcilerAction_UnsupportedAction(t *testing.T) {
	r, _, depID := manageHarness(t, "",
		helmReleaseObj("bp-keycloak", true, false, "0.4.1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, manageActionReq(depID, "HelmRelease", helmwatch.FluxNamespace, "bp-keycloak", "delete", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unsupported action, got %d; body=%s", rec.Code, rec.Body.String())
	}
}
