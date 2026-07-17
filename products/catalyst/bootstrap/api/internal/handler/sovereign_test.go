// Tests for the Sovereign Console populated-views endpoints (issue #933).
//
// What this file proves:
//
//  1. /api/v1/sovereign/status — counts HelmReleases (Ready vs total)
//     and pods (Running vs total) from the in-cluster client.
//  2. /api/v1/sovereign/jobs — surfaces HelmReleases, K8s Jobs, and
//     Warning-level Events as Job rows, sorted started-DESC.
//  3. /api/v1/sovereign/apps — joins the embedded blueprint catalog
//     with HelmRelease state on the cluster:
//     - HR present + Ready=True       → "installed"
//     - HR present + Ready=False/none → "installing"
//     - listed catalog, no HR         → "available"
//     - bootstrap-kit                 → "bootstrap"
//  4. /api/v1/sovereign/cloud — emits nodes / namespaces / ingresses /
//     LoadBalancer-services / storage classes / PVCs from the in-cluster
//     client, with HTTPRoutes coming via dynamic client.
//
// Tests inject (kubernetes.Interface, dynamic.Interface) via
// SetSovereignDepsFactory so no real cluster is needed.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakek8s "k8s.io/client-go/kubernetes/fake"
)

// ── Fixtures + helpers ─────────────────────────────────────────────────────

func newSovereignHandler(t *testing.T, coreObjs []runtime.Object, dynObjs []runtime.Object) *Handler {
	t.Helper()
	core := fakek8s.NewSimpleClientset(coreObjs...)

	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		helmReleaseGVR:       "HelmReleaseList",
		httpRouteGVR:         "HTTPRouteList",
		applicationGVR:       "ApplicationList",
		fluxKustomizationGVR: "KustomizationList",
		{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}: "CertificateList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, dynObjs...)

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core, dyn: dyn}, nil
	})
	return h
}

func makeHR(name, ns, ready string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	u.SetName(name)
	u.SetNamespace(ns)
	u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-1 * time.Hour)))
	conds := []interface{}{
		map[string]interface{}{
			"type":               "Ready",
			"status":             ready,
			"message":            "test condition",
			"lastTransitionTime": time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
		},
	}
	_ = unstructured.SetNestedSlice(u.Object, conds, "status", "conditions")
	return u
}

func makeHTTPRoute(name, ns string, hosts []string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "HTTPRoute",
	})
	u.SetName(name)
	u.SetNamespace(ns)
	hostsAny := make([]interface{}, len(hosts))
	for i, h := range hosts {
		hostsAny[i] = h
	}
	_ = unstructured.SetNestedSlice(u.Object, hostsAny, "spec", "hostnames")
	return u
}

// makeApplication builds an apps.openova.io/v1 Application CR with the
// given name (= slug) and spec.environmentRef. Used to seed the dynamic
// client for /api/v1/sovereign/apps environment-chip tests (qa-loop
// iter-7 TC-090). Version matches ApplicationGVR() in applications.go
// and the qa-fixtures chart.
func makeApplication(name, ns, environmentRef string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apps.openova.io",
		Version: "v1",
		Kind:    "Application",
	})
	u.SetName(name)
	u.SetNamespace(ns)
	if environmentRef != "" {
		_ = unstructured.SetNestedField(u.Object, environmentRef, "spec", "environmentRef")
	}
	return u
}

func mustGet[T any](t *testing.T, h http.Handler, path string) T {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, body=%s", path, w.Code, w.Body.String())
	}
	var out T
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

// ── /status ────────────────────────────────────────────────────────────────

func TestSovereignStatus_CountsHelmReleasesAndPods(t *testing.T) {
	dynObjs := []runtime.Object{
		makeHR("bp-cilium", "flux-system", "True"),
		makeHR("bp-cert-manager", "flux-system", "True"),
		makeHR("bp-flux", "flux-system", "False"),
	}
	coreObjs := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	}
	h := newSovereignHandler(t, coreObjs, dynObjs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/status", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d; body=%s", w.Code, w.Body.String())
	}
	var got sovereignStatus
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.HelmReleasesTotal != 3 {
		t.Errorf("HelmReleasesTotal = %d, want 3", got.HelmReleasesTotal)
	}
	if got.HelmReleasesReady != 2 {
		t.Errorf("HelmReleasesReady = %d, want 2", got.HelmReleasesReady)
	}
	if got.PodsTotal != 3 {
		t.Errorf("PodsTotal = %d, want 3", got.PodsTotal)
	}
	if got.PodsRunning != 2 {
		t.Errorf("PodsRunning = %d, want 2", got.PodsRunning)
	}
}

// ── /jobs ──────────────────────────────────────────────────────────────────

func TestSovereignJobs_HelmReleasesAndK8sJobs(t *testing.T) {
	dynObjs := []runtime.Object{
		makeHR("bp-cilium", "flux-system", "True"),
		makeHR("bp-cert-manager", "flux-system", "False"),
	}
	coreObjs := []runtime.Object{
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "bp-keycloak-bootstrap", Namespace: "auth"},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{
					{Type: "Complete", Status: corev1.ConditionTrue},
				},
				StartTime:      &metav1.Time{Time: time.Now().Add(-2 * time.Hour)},
				CompletionTime: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
			},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "warn-1", Namespace: "default"},
			Type:           "Warning",
			Reason:         "FailedMount",
			Message:        "could not mount volume",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "x"},
			FirstTimestamp: metav1.NewTime(time.Now().Add(-15 * time.Minute)),
		},
	}
	h := newSovereignHandler(t, coreObjs, dynObjs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/jobs", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignJobs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d; body=%s", w.Code, w.Body.String())
	}
	var got sovereignJobsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	kinds := map[string]int{}
	for _, j := range got.Jobs {
		kinds[j.Kind]++
	}
	// FINITE-ONLY (#3896 / #3925): the 2 seeded HelmReleases are
	// CONTINUOUS reconcilers and MUST NOT appear on /jobs — they belong
	// on the recon surface. Only the finite K8s Job (+ the warning Event)
	// survive. This is the regression guard for the "83 HelmReleases
	// mislabeled as LIFECYCLE jobs" pollution.
	if kinds["HelmRelease"] != 0 {
		t.Errorf("HelmRelease rows = %d, want 0 (continuous reconcilers excluded from /jobs)", kinds["HelmRelease"])
	}
	if kinds["Job"] != 1 {
		t.Errorf("Job rows = %d, want 1", kinds["Job"])
	}
	if kinds["Event"] != 1 {
		t.Errorf("Event rows = %d, want 1", kinds["Event"])
	}
}

// ── /apps ──────────────────────────────────────────────────────────────────

func TestSovereignApps_StatusJoinHelmReleases(t *testing.T) {
	// Catalog will return embedded JSON blueprints. We don't seed
	// blueprints (they're embedded). We DO seed HelmReleases that
	// claim to install bp-cilium so the apps list reflects that.
	dynObjs := []runtime.Object{
		makeHR("bp-cilium", "flux-system", "True"),
		makeHR("bp-keycloak", "flux-system", "False"),
	}
	h := newSovereignHandler(t, nil, dynObjs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/apps", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignApps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d; body=%s", w.Code, w.Body.String())
	}
	var got sovereignAppsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Apps) == 0 {
		t.Fatal("expected at least one app from embedded catalog, got 0")
	}
	// Find bp-cilium and check status.
	byID := map[string]sovereignAppItem{}
	for _, a := range got.Apps {
		byID[a.ID] = a
	}
	if cilium, ok := byID["bp-cilium"]; ok {
		if cilium.Status != "installed" && cilium.Status != "bootstrap" {
			t.Errorf("bp-cilium status = %q; want installed or bootstrap", cilium.Status)
		}
	}
	if kc, ok := byID["bp-keycloak"]; ok {
		// keycloak HR is not Ready, so status must be either
		// "installing" (if listed and HR present) or "bootstrap"
		// (if it's part of the bootstrap-kit which always renders).
		if kc.Status != "installing" && kc.Status != "bootstrap" {
			t.Errorf("bp-keycloak status = %q; want installing or bootstrap", kc.Status)
		}
	}
	if got.GeneratedAt == "" {
		t.Error("expected GeneratedAt to be populated from embedded catalog")
	}
}

// TestSovereignApps_EnvironmentChipDefaultsToDev proves the qa-loop
// iter-7 TC-090 fix: every app row in the /sovereign/apps response
// MUST carry a non-empty Environment field. With no Application CR
// seeded, every row falls back to defaultSovereignEnvironment ("dev")
// so the AppsPage card always renders an environment chip — matrix
// expectation `must_contain: ["dev"]` on the Apps page.
func TestSovereignApps_EnvironmentChipDefaultsToDev(t *testing.T) {
	dynObjs := []runtime.Object{
		makeHR("bp-cilium", "flux-system", "True"),
	}
	h := newSovereignHandler(t, nil, dynObjs)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/apps", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignApps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d; body=%s", w.Code, w.Body.String())
	}
	var got sovereignAppsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Apps) == 0 {
		t.Fatal("expected at least one app, got 0")
	}
	for _, a := range got.Apps {
		if a.Environment == "" {
			t.Errorf("app %q has empty Environment; every row must carry a chip", a.ID)
		}
		if a.Environment != defaultSovereignEnvironment {
			t.Errorf("app %q Environment = %q; want default %q", a.ID, a.Environment, defaultSovereignEnvironment)
		}
	}
}

// TestSovereignApps_EnvironmentChipFromApplicationCR proves that when
// an Application CR with spec.environmentRef matches the slug, the
// chip surfaces THAT environment instead of the default. Multi-env
// Sovereigns rely on this — without it every row would render "dev"
// regardless of where the workload actually runs.
func TestSovereignApps_EnvironmentChipFromApplicationCR(t *testing.T) {
	dynObjs := []runtime.Object{
		makeHR("bp-cilium", "flux-system", "True"),
		// Match by the bp-cilium slug ("cilium" — Catalog.Slug is
		// the slug WITHOUT the bp- prefix).
		makeApplication("cilium", "default", "prod"),
	}
	h := newSovereignHandler(t, nil, dynObjs)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/apps", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignApps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var got sovereignAppsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]sovereignAppItem{}
	for _, a := range got.Apps {
		byID[a.ID] = a
	}
	cilium, ok := byID["bp-cilium"]
	if !ok {
		t.Fatalf("bp-cilium missing from response")
	}
	if cilium.Environment != "prod" {
		t.Errorf("bp-cilium Environment = %q; want prod (from Application CR)", cilium.Environment)
	}
	// Sibling rows with no matching Application CR still default to dev.
	// #3370 — instance rows (one card per Application CR) legitimately
	// carry their own environment; only blueprint/catalog rows are
	// asserted here.
	for _, a := range got.Apps {
		if a.ID == "bp-cilium" || a.Instance {
			continue
		}
		if a.Environment != defaultSovereignEnvironment {
			t.Errorf("non-matched app %q Environment = %q; want default %q", a.ID, a.Environment, defaultSovereignEnvironment)
		}
	}
	// #3370 — the Application CR ALSO projects as its own instance card.
	inst, ok := byID["cilium"]
	if !ok {
		t.Fatalf("instance card for Application CR 'cilium' missing from response")
	}
	if !inst.Instance {
		t.Errorf("row 'cilium' should be marked instance=true")
	}
}

// makeShareableHR builds a bootstrap-kit HelmRelease for a SHAREABLE
// blueprint (e.g. bp-postgres) with a spec.releaseName (the instance
// identity) and spec.values.databases[] (the declared Contexts). Mirrors
// the shape Flux installs on a zero-touch prov: the shared-pg / -b / -c
// HRs that have NO companion Application CR. Used by
// TestSovereignApps_BootstrapHRShareableInstanceCards (#3370 #3537).
func makeShareableHR(hrName, releaseName string, ready string, dbs []map[string]interface{}) *unstructured.Unstructured {
	u := makeHR(hrName, "flux-system", ready)
	_ = unstructured.SetNestedField(u.Object, "bp-postgres", "spec", "chart", "spec", "chart")
	_ = unstructured.SetNestedField(u.Object, releaseName, "spec", "releaseName")
	dbAny := make([]interface{}, len(dbs))
	for i, d := range dbs {
		dbAny[i] = d
	}
	_ = unstructured.SetNestedSlice(u.Object, dbAny, "spec", "values", "databases")
	return u
}

// TestSovereignApps_BootstrapHRShareableInstanceCards is the #3537
// regression: on a zero-touch converged prov the shared-pg / -b / -c
// instances exist ONLY as bootstrap Flux HelmReleases (0 Application
// CRs). The /api/v1/sovereign/apps handler must still project them as
// instance:true cards WITH the ⛓ Contexts count — the exact gap the
// founder hit live on hw138 (instance rows = 0, contextCount = 0) while
// /catalyst/v1/catalog/postgres/instances correctly rendered them.
func TestSovereignApps_BootstrapHRShareableInstanceCards(t *testing.T) {
	// THREE shareable HRs, ZERO Application CRs — the hw138 shape.
	dynObjs := []runtime.Object{
		makeShareableHR("bp-postgres-shared", "shared-pg", "True", []map[string]interface{}{
			{"name": "registry", "owner": "harbor", "consumer": map[string]interface{}{"blueprint": "bp-harbor"}},
			{"name": "gitea", "owner": "gitea", "consumer": map[string]interface{}{"blueprint": "bp-gitea"}},
			{"name": "keycloak", "owner": "keycloak", "consumer": map[string]interface{}{"blueprint": "bp-keycloak"}},
		}),
		makeShareableHR("bp-postgres-shared-c", "shared-pg-c", "True", []map[string]interface{}{
			{"name": "newapi", "owner": "newapi"},
			{"name": "openova_flow", "owner": "openova-flow"},
		}),
		// A non-shareable bootstrap HR must NOT become an instance card.
		makeHR("bp-cilium", "flux-system", "True"),
	}
	h := newSovereignHandler(t, nil, dynObjs)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/apps", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignApps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var got sovereignAppsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]sovereignAppItem{}
	for _, a := range got.Apps {
		byID[a.ID] = a
	}

	// shared-pg → instance card with 3 Contexts.
	sp, ok := byID["shared-pg"]
	if !ok {
		t.Fatalf("instance card for HR shared-pg missing — bootstrap shareable HR not projected (the #3537 gap)")
	}
	if !sp.Instance {
		t.Errorf("shared-pg row should be marked instance=true")
	}
	if sp.ContextCount != 3 {
		t.Errorf("shared-pg ContextCount = %d; want 3 (databases[])", sp.ContextCount)
	}
	if sp.Blueprint != "bp-postgres" {
		t.Errorf("shared-pg Blueprint = %q; want bp-postgres", sp.Blueprint)
	}
	if sp.Status != "installed" {
		t.Errorf("shared-pg Status = %q; want installed (HR Ready=True)", sp.Status)
	}

	// shared-pg-c → instance card with 2 Contexts.
	spc, ok := byID["shared-pg-c"]
	if !ok {
		t.Fatalf("instance card for HR shared-pg-c missing")
	}
	if spc.ContextCount != 2 {
		t.Errorf("shared-pg-c ContextCount = %d; want 2", spc.ContextCount)
	}

	// The non-shareable bp-cilium HR must NOT be an instance card.
	if c, ok := byID["cilium"]; ok && c.Instance {
		t.Errorf("non-shareable bp-cilium must not project an instance card")
	}
}

// TestResolveAppEnvironment_FallbackOrder unit-tests the helper in
// isolation so the fallback semantics are explicit.
func TestResolveAppEnvironment_FallbackOrder(t *testing.T) {
	cases := []struct {
		name     string
		envs     map[string]string
		slug     string
		expected string
	}{
		{name: "nil-map → dev", envs: nil, slug: "anything", expected: "dev"},
		{name: "empty-map → dev", envs: map[string]string{}, slug: "x", expected: "dev"},
		{name: "miss → dev", envs: map[string]string{"a": "prod"}, slug: "b", expected: "dev"},
		{name: "empty-value → dev", envs: map[string]string{"a": ""}, slug: "a", expected: "dev"},
		{name: "hit → returns", envs: map[string]string{"a": "stg"}, slug: "a", expected: "stg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAppEnvironment(tc.envs, tc.slug)
			if got != tc.expected {
				t.Errorf("got %q want %q", got, tc.expected)
			}
		})
	}
}

// ── /cloud ─────────────────────────────────────────────────────────────────

func TestSovereignCloud_NodesAndNamespaces(t *testing.T) {
	coreObjs := []runtime.Object{
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "cp-1",
				Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""},
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				},
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion:  "v1.31.4",
					OperatingSystem: "linux",
					Architecture:    "amd64",
				},
				Addresses: []corev1.NodeAddress{
					{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
				},
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "auth"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "console", Namespace: "catalyst"},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{{Host: "console.example.com"}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "cilium-gateway", Namespace: "kube-system"},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeLoadBalancer,
				ClusterIP: "10.43.0.1",
				Ports:     []corev1.ServicePort{{Port: 443, Protocol: corev1.ProtocolTCP}},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}},
				},
			},
		},
		&storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "local-path",
				Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
			},
			Provisioner: "rancher.io/local-path",
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data-keycloak", Namespace: "auth"},
			Spec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: ptr("local-path"),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("5Gi"),
					},
				},
			},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		},
	}
	dynObjs := []runtime.Object{
		makeHTTPRoute("console", "catalyst", []string{"console.sov.example.com"}),
	}
	h := newSovereignHandler(t, coreObjs, dynObjs)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/cloud", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignCloud(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d; body=%s", w.Code, w.Body.String())
	}
	var got sovereignCloudResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Nodes) != 1 {
		t.Errorf("nodes = %d, want 1", len(got.Nodes))
	}
	if len(got.Nodes) > 0 {
		n := got.Nodes[0]
		if n.Status != "Ready" {
			t.Errorf("node status = %q, want Ready", n.Status)
		}
		if n.InternalIP != "10.0.0.1" {
			t.Errorf("node internalIP = %q, want 10.0.0.1", n.InternalIP)
		}
	}
	if len(got.Namespaces) != 1 || got.Namespaces[0].Name != "auth" {
		t.Errorf("namespaces = %+v, want [auth]", got.Namespaces)
	}
	if len(got.Ingresses) != 1 || got.Ingresses[0].Hosts[0] != "console.example.com" {
		t.Errorf("ingresses = %+v", got.Ingresses)
	}
	if len(got.HTTPRoutes) != 1 || got.HTTPRoutes[0].Hosts[0] != "console.sov.example.com" {
		t.Errorf("httproutes = %+v", got.HTTPRoutes)
	}
	if len(got.LoadBalancers) != 1 || got.LoadBalancers[0].ExternalIP != "1.2.3.4" {
		t.Errorf("loadbalancers = %+v", got.LoadBalancers)
	}
	if len(got.StorageClasses) != 1 || !got.StorageClasses[0].IsDefault {
		t.Errorf("storageClasses = %+v; want one default class", got.StorageClasses)
	}
	if len(got.PVCs) != 1 {
		t.Errorf("pvcs = %d, want 1", len(got.PVCs))
	}
}

// ── client unavailable path ─────────────────────────────────────────────────

func TestSovereignStatus_503WhenInClusterUnavailable(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// SetSovereignDepsFactory not called → falls back to
	// rest.InClusterConfig() which fails outside K8s.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/status", nil)
	w := httptest.NewRecorder()
	h.HandleSovereignStatus(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503 (in-cluster client missing)", w.Code)
	}
}

func ptr[T any](v T) *T { return &v }
