// networking_test.go — coverage for the Sovereign Console Networking
// page REST surface.
//
// The matrix asserts:
//
//   - TC-295 / TC-279 / TC-294: /networking/policies returns rows
//     including the literal token "CiliumNetworkPolicy"
//   - TC-296 / TC-297: /networking/clustermesh returns `clusters`
//     with the multi-region peer names + a `connected` indicator
//   - TC-300 / TC-281/282/283: /networking/netbird surfaces the 3
//     deployments + an `installed` flag
//   - TC-301 / TC-286: /networking/dmz surfaces vCluster + isolation
//     CNPs + a `vCluster` token
//   - /networking/hubble surfaces hubble-relay + hubble-ui state
//
// Per docs/INVIOLABLE-PRINCIPLES.md #2 (quality) and #3 (event-driven)
// every assertion exercises the real Handler with a fake k8scache.Factory
// hydrated by dynamic.NewSimpleDynamicClient — no fixture-data shortcut.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// mkUnstructured — generic unstructured object builder for the
// networking tests. apiVersion + kind drive informer routing in the
// fake dynamic client; rest of fields are passthrough.
func mkUnstructured(apiVersion, kind, namespace, name string, spec, status map[string]any) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":              name,
			"namespace":         namespace,
			"creationTimestamp": time.Now().UTC().Format(time.RFC3339),
		},
	}}
	if spec != nil {
		o.Object["spec"] = spec
	}
	if status != nil {
		o.Object["status"] = status
	}
	return o
}

// netFixtureClients — constructs the same dynamic + core fake pair the
// dashboard tests use, but with the broader scheme our networking
// handlers walk.
func netFixtureClients(objs ...*unstructured.Unstructured) (*dynamicfake.FakeDynamicClient, *kfake.Clientset) {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "configmaps"}:                                                    "ConfigMapList",
		{Version: "v1", Resource: "secrets"}:                                                       "SecretList",
		{Group: "apps", Version: "v1", Resource: "daemonsets"}:                                     "DaemonSetList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:                                    "DeploymentList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}:                   "NetworkPolicyList",
		{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}:                     "CiliumNetworkPolicyList",
		{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}:          "CiliumClusterwideNetworkPolicyList",
		{Group: "vcluster.com", Version: "v1alpha1", Resource: "vclusters"}:                        "VClusterList",
	}
	rtObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		rtObjs = append(rtObjs, o)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, rtObjs...)
	core := kfake.NewSimpleClientset()
	return dyn, core
}

// newNetHandlerWithCache — constructs a Handler with a wired k8scache
// containing the seeded objects. Mirrors newDashHandlerWithCache but
// with the wider GVR registry our networking tests exercise.
func newNetHandlerWithCache(t *testing.T, clusterID string, objs ...*unstructured.Unstructured) *Handler {
	t.Helper()
	dyn, core := netFixtureClients(objs...)
	r := k8scache.NewRegistry()
	for _, k := range []k8scache.Kind{
		{Name: "configmap", GVR: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, Namespaced: true, Sensitive: true},
		{Name: "secret", GVR: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Namespaced: true, Sensitive: true},
		{Name: "daemonset", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, Namespaced: true},
		{Name: "deployment", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespaced: true},
		{Name: "networkpolicy", GVR: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, Namespaced: true},
		{Name: "ciliumnetworkpolicy", GVR: schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}, Namespaced: true},
		{Name: "ciliumclusterwidenetworkpolicy", GVR: schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}, Namespaced: false},
		{Name: "vcluster", GVR: schema.GroupVersionResource{Group: "vcluster.com", Version: "v1alpha1", Resource: "vclusters"}, Namespaced: true},
	} {
		_ = r.Add(k)
	}
	cfg := k8scache.Config{
		Logger:   quietHandlerLogger(),
		Registry: r,
		Clusters: []k8scache.ClusterRef{
			{ID: clusterID, DynamicClient: dyn, CoreClient: core},
		},
	}
	f, err := k8scache.NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)

	// Wait for the informer indexer to populate. Mirrors the dashboard
	// tests: use Synced()'s nested map to confirm every kind reached
	// HasSynced=true. Bounded by a 2-second deadline so a hung fake
	// dynamic client surfaces fast in CI rather than holding a table
	// test slot for minutes.
	deadline := time.Now().Add(2 * time.Second)
	wantedKinds := []string{"deployment", "configmap", "secret", "daemonset", "ciliumnetworkpolicy", "ciliumclusterwidenetworkpolicy", "vcluster", "networkpolicy"}
	for time.Now().Before(deadline) {
		synced := f.Synced()
		clusterSynced, ok := synced[clusterID]
		if !ok {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		ready := true
		for _, k := range wantedKinds {
			if !clusterSynced[k] {
				ready = false
				break
			}
		}
		if ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	return h
}

func netGet(t *testing.T, h *Handler, fn http.HandlerFunc, clusterID, slug string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/"+clusterID+"/networking/"+slug, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", clusterID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// ── /networking/policies ────────────────────────────────────────────

func TestHandleNetworkingPolicies_NoCache(t *testing.T) {
	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	code, body := netGet(t, h, h.HandleNetworkingPolicies, "missing", "policies")
	if code != http.StatusOK {
		t.Fatalf("status: %d body=%s", code, string(body))
	}
	var resp networkingPoliciesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 0 || len(resp.Items) != 0 {
		t.Fatalf("expected empty rows, got %+v", resp)
	}
}

func TestHandleNetworkingPolicies_HappyPath(t *testing.T) {
	clusterID := "test-sovereign"
	cnp := mkUnstructured("cilium.io/v2", "CiliumNetworkPolicy", "qa-omantel", "allow-dns", map[string]any{
		"ingress": []any{map[string]any{}, map[string]any{}},
		"egress":  []any{map[string]any{}},
	}, nil)
	ccnp := mkUnstructured("cilium.io/v2", "CiliumClusterwideNetworkPolicy", "", "default-deny", map[string]any{
		"ingress": []any{map[string]any{}},
		"egress":  []any{map[string]any{}},
	}, nil)
	np := mkUnstructured("networking.k8s.io/v1", "NetworkPolicy", "qa-omantel", "vanilla", nil, nil)

	h := newNetHandlerWithCache(t, clusterID, cnp, ccnp, np)
	code, body := netGet(t, h, h.HandleNetworkingPolicies, clusterID, "policies")
	if code != http.StatusOK {
		t.Fatalf("status: %d body=%s", code, string(body))
	}
	bodyStr := string(body)
	for _, want := range []string{"CiliumNetworkPolicy", "CiliumClusterwideNetworkPolicy", "NetworkPolicy", "default-deny", "allow-dns"} {
		if !contains(bodyStr, want) {
			t.Errorf("missing token %q in body: %s", want, bodyStr)
		}
	}
	var resp networkingPoliciesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("Total: got %d want 3", resp.Total)
	}
	if resp.ByKind["CiliumNetworkPolicy"] != 1 {
		t.Errorf("by-kind CNP count: got %d want 1", resp.ByKind["CiliumNetworkPolicy"])
	}
}

// ── /networking/clustermesh ─────────────────────────────────────────

func TestHandleNetworkingClusterMesh_HappyPath(t *testing.T) {
	clusterID := "test-sovereign"
	keys := mkUnstructured("v1", "Secret", "kube-system", "cilium-clustermesh-keys", nil, nil)
	cm := mkUnstructured("v1", "ConfigMap", "kube-system", "cilium-clustermesh", nil, nil)
	cm.Object["data"] = map[string]any{
		"omantel-fsn": "https://10.0.0.1:32379",
		"omantel-hel": "https://10.10.0.1:32379",
	}
	ds := mkUnstructured("apps/v1", "DaemonSet", "kube-system", "cilium", map[string]any{
		"template": map[string]any{
			"spec": map[string]any{
				"containers": []any{
					map[string]any{
						"name": "cilium-agent",
						"args": []any{"--cluster-name=omantel-fsn", "--cluster-id=1"},
					},
				},
			},
		},
	}, nil)

	h := newNetHandlerWithCache(t, clusterID, keys, cm, ds)
	code, body := netGet(t, h, h.HandleNetworkingClusterMesh, clusterID, "clustermesh")
	if code != http.StatusOK {
		t.Fatalf("status: %d body=%s", code, string(body))
	}
	bodyStr := string(body)
	for _, want := range []string{"clusters", "connected", "omantel-fsn", "omantel-hel", "mesh_keys_present", "true"} {
		if !contains(bodyStr, want) {
			t.Errorf("missing token %q in body: %s", want, bodyStr)
		}
	}
	var resp clusterMeshResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("Total: got %d want 2", resp.Total)
	}
	if resp.SelfClusterName != "omantel-fsn" {
		t.Errorf("SelfClusterName: got %q want omantel-fsn", resp.SelfClusterName)
	}
	if !resp.MeshKeysPresent {
		t.Error("MeshKeysPresent should be true")
	}
}

// ── /networking/netbird ─────────────────────────────────────────────

func TestHandleNetworkingNetBird_HappyPath(t *testing.T) {
	clusterID := "test-sovereign"
	mgmt := mkUnstructured("apps/v1", "Deployment", "netbird", "netbird-management", nil, map[string]any{
		"replicas":      int64(1),
		"readyReplicas": int64(1),
	})
	signal := mkUnstructured("apps/v1", "Deployment", "netbird", "netbird-signal", nil, map[string]any{
		"replicas":      int64(1),
		"readyReplicas": int64(1),
	})
	coturn := mkUnstructured("apps/v1", "Deployment", "netbird", "coturn", nil, map[string]any{
		"replicas":      int64(1),
		"readyReplicas": int64(1),
	})

	h := newNetHandlerWithCache(t, clusterID, mgmt, signal, coturn)
	code, body := netGet(t, h, h.HandleNetworkingNetBird, clusterID, "netbird")
	if code != http.StatusOK {
		t.Fatalf("status: %d body=%s", code, string(body))
	}
	bodyStr := string(body)
	for _, want := range []string{"netbird-management", "netbird-signal", "coturn", "installed", "true"} {
		if !contains(bodyStr, want) {
			t.Errorf("missing token %q in body: %s", want, bodyStr)
		}
	}
	var resp netbirdResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Installed {
		t.Error("Installed should be true")
	}
	if len(resp.Deployments) != 3 {
		t.Errorf("Deployments: got %d want 3", len(resp.Deployments))
	}
	for _, d := range resp.Deployments {
		if !d.Available {
			t.Errorf("deployment %q not Available", d.Name)
		}
	}
}

func TestHandleNetworkingNetBird_NotInstalled(t *testing.T) {
	clusterID := "test-sovereign"
	// No netbird-namespace deployments.
	other := mkUnstructured("apps/v1", "Deployment", "default", "other", nil, nil)
	h := newNetHandlerWithCache(t, clusterID, other)
	code, body := netGet(t, h, h.HandleNetworkingNetBird, clusterID, "netbird")
	if code != http.StatusOK {
		t.Fatalf("status: %d body=%s", code, string(body))
	}
	var resp netbirdResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Installed {
		t.Error("Installed should be false when no netbird namespace deployments")
	}
}

// ── /networking/dmz ─────────────────────────────────────────────────

func TestHandleNetworkingDMZ_HappyPath(t *testing.T) {
	clusterID := "test-sovereign"
	vc := mkUnstructured("vcluster.com/v1alpha1", "VCluster", "dmz", "dmz", nil, map[string]any{
		"phase": "Running",
	})
	iso := mkUnstructured("cilium.io/v2", "CiliumNetworkPolicy", "dmz", "isolation", nil, nil)

	h := newNetHandlerWithCache(t, clusterID, vc, iso)
	code, body := netGet(t, h, h.HandleNetworkingDMZ, clusterID, "dmz")
	if code != http.StatusOK {
		t.Fatalf("status: %d body=%s", code, string(body))
	}
	bodyStr := string(body)
	for _, want := range []string{"dmz", "vclusters", "Running", "isolation", "isolation_cnps"} {
		if !contains(bodyStr, want) {
			t.Errorf("missing token %q in body: %s", want, bodyStr)
		}
	}
	var resp dmzResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Installed {
		t.Error("Installed should be true")
	}
	if len(resp.VClusters) != 1 {
		t.Errorf("VClusters: got %d want 1", len(resp.VClusters))
	}
	if !resp.VClusters[0].Running {
		t.Error("vcluster should be Running")
	}
	if len(resp.IsolationCNPs) != 1 {
		t.Errorf("IsolationCNPs: got %d want 1", len(resp.IsolationCNPs))
	}
}

// ── /networking/hubble ───────────────────────────────────────────────

func TestHandleNetworkingHubble_HappyPath(t *testing.T) {
	clusterID := "test-sovereign"
	relay := mkUnstructured("apps/v1", "Deployment", "kube-system", "hubble-relay", nil, map[string]any{
		"replicas":      int64(1),
		"readyReplicas": int64(1),
	})
	ui := mkUnstructured("apps/v1", "Deployment", "kube-system", "hubble-ui", nil, map[string]any{
		"replicas":      int64(1),
		"readyReplicas": int64(1),
	})
	cm := mkUnstructured("v1", "ConfigMap", "kube-system", "cilium-config", nil, nil)
	cm.Object["data"] = map[string]any{
		"enable-hubble":         "true",
		"hubble-listen-address": ":4244",
	}

	h := newNetHandlerWithCache(t, clusterID, relay, ui, cm)
	code, body := netGet(t, h, h.HandleNetworkingHubble, clusterID, "hubble")
	if code != http.StatusOK {
		t.Fatalf("status: %d body=%s", code, string(body))
	}
	bodyStr := string(body)
	for _, want := range []string{"hubble-relay", "hubble-ui", "hubble_enabled", "relay_ready", "ui_ready"} {
		if !contains(bodyStr, want) {
			t.Errorf("missing token %q in body: %s", want, bodyStr)
		}
	}
	var resp hubbleResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.HubbleEnabled || !resp.RelayReady || !resp.UIReady {
		t.Errorf("expected all flags true: %+v", resp)
	}
}

func TestHumanKindName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"networkpolicy", "NetworkPolicy"},
		{"ciliumnetworkpolicy", "CiliumNetworkPolicy"},
		{"ciliumclusterwidenetworkpolicy", "CiliumClusterwideNetworkPolicy"},
		{"gatewayclass", "GatewayClass"},
		{"unknown", "unknown"},
	}
	for _, c := range cases {
		if got := humanKindName(c.in); got != c.want {
			t.Errorf("humanKindName(%q): got %q want %q", c.in, got, c.want)
		}
	}
}

// contains — case-sensitive substring helper. Mirrors strings.Contains
// without importing it at the call site (keeps the import surface tight
// for the network test file).
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
