// k8s_test.go — handler-level tests for the K8s data plane (issue #321).
//
// Tests use fake clients exclusively; no real cluster.
package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newPod(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"resourceVersion": "1",
		},
	}}
}

func minimalRegistry() *k8scache.Registry {
	r := k8scache.NewRegistry()
	_ = r.Add(k8scache.Kind{
		Name:       "pod",
		GVR:        schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		Namespaced: true,
	})
	return r
}

func newFactoryWithPod(t *testing.T, pod *unstructured.Unstructured) *k8scache.Factory {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, &unstructured.Unstructured{})
	gvrList := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}: "PodList",
	}
	var dyn *dynamicfake.FakeDynamicClient
	if pod != nil {
		dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList, pod)
	} else {
		dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList)
	}
	core := kfake.NewSimpleClientset()
	cfg := k8scache.Config{
		Logger:   quietLog(),
		Registry: minimalRegistry(),
		Clusters: []k8scache.ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
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
	// Wait briefly for sync.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pod != nil {
			items, _, _ := f.List("alpha", "pod", nil)
			if len(items) >= 1 {
				return f
			}
		} else {
			// no-pod path — sync map should still flip.
			s := f.Synced()
			if v, ok := s["alpha"]; ok && v["pod"] {
				return f
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f
}

func newRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/k8s/{kind}", h.HandleK8sList)
	r.Get("/api/v1/sovereigns/{id}/k8s/stream", h.HandleK8sStream)
	r.Get("/api/v1/sovereigns/{id}/k8s/sync", h.HandleK8sSync)
	r.Get("/healthz", h.Health)
	r.Get("/readyz", h.Ready)
	return r
}

func TestHandleK8sList_Returns503WhenDisabled(t *testing.T) {
	h := &Handler{log: quietLog()}
	r := newRouter(h)
	req := httptest.NewRequest("GET", "/api/v1/sovereigns/alpha/k8s/pod", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHandleK8sList_ReturnsItems(t *testing.T) {
	pod := newPod("default", "x")
	f := newFactoryWithPod(t, pod)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	req := httptest.NewRequest("GET", "/api/v1/sovereigns/alpha/k8s/pod", nil)
	// no X-Forwarded-User → SAR gating bypassed (anonymous).
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp K8sListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Cluster != "alpha" || resp.Kind != "pod" {
		t.Fatalf("unexpected response shape: %+v", resp)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	if rec.Header().Get("X-Cache-Stale-Seconds") == "" {
		t.Fatalf("expected X-Cache-Stale-Seconds header")
	}
}

func TestHandleK8sList_UnknownKindReturns404(t *testing.T) {
	f := newFactoryWithPod(t, nil)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	req := httptest.NewRequest("GET", "/api/v1/sovereigns/alpha/k8s/banana", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "availableKinds") {
		t.Fatalf("404 body should advertise available kinds: %s", body)
	}
}

func TestHandleK8sSync_ReturnsPerKindMap(t *testing.T) {
	f := newFactoryWithPod(t, nil)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	req := httptest.NewRequest("GET", "/api/v1/sovereigns/alpha/k8s/sync", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// TestHealth_AlwaysOK — /healthz is liveness; it MUST return 200
// regardless of k8scache state (issue #530). The previous
// implementation returned 503 when a Sovereign was registered but
// its informers had not yet synced, which caused kubelet to
// crashloop the catalyst-api Pod during fresh provisions. This test
// exercises both the cold (no k8scache) and warm (k8scache wired)
// paths with the same expectation.
func TestHealth_AlwaysOK(t *testing.T) {
	t.Run("k8scache_disabled", func(t *testing.T) {
		h := &Handler{log: quietLog()}
		r := newRouter(h)
		req := httptest.NewRequest("GET", "/healthz", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if rec.Body.String() != "ok" {
			t.Fatalf("expected ok, got %q", rec.Body.String())
		}
	})
	t.Run("k8scache_wired", func(t *testing.T) {
		f := newFactoryWithPod(t, nil)
		h := &Handler{log: quietLog()}
		h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
		r := newRouter(h)
		req := httptest.NewRequest("GET", "/healthz", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		// Liveness MUST be 200 even when informers are still syncing.
		if rec.Code != 200 {
			t.Fatalf("expected 200 (liveness), got %d", rec.Code)
		}
	})
}

// TestReadyz_PlainTextWhenK8sCacheDisabled — readiness when k8scache
// is unwired (test/CI env) is unconditionally 200; nothing to wait for.
func TestReadyz_PlainTextWhenK8sCacheDisabled(t *testing.T) {
	h := &Handler{log: quietLog()}
	r := newRouter(h)
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("expected ok, got %q", rec.Body.String())
	}
}

// TestReadyz_JSONWhenAcceptHeaderSet — Accept: application/json
// returns the structured per-cluster sync map. With no Sovereigns
// registered (factory created from empty cluster list) the response
// is 200 + Ready=true.
func TestReadyz_JSONWhenAcceptHeaderSet(t *testing.T) {
	f := newFactoryWithPod(t, nil)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	req := httptest.NewRequest("GET", "/readyz", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"sovereigns"`) {
		t.Fatalf("expected JSON body with sovereigns field: %s", body)
	}
}

func TestHandleK8sStream_EmitsEvent(t *testing.T) {
	pod := newPod("default", "x")
	f := newFactoryWithPod(t, pod)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Subscribe with initialState=1 so we don't have to race a Create.
	resp, err := http.Get(srv.URL + "/api/v1/sovereigns/alpha/k8s/stream?kinds=pod&initialState=1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected SSE content-type, got %q", ct)
	}

	// Read SSE frames in a goroutine; main goroutine times out after
	// 3s. The httptest client doesn't expose SetReadDeadline so we
	// cancel via the response Body close from another goroutine.
	gotEvent := false
	doneCh := make(chan bool, 1)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				doneCh <- false
				return
			}
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				payload = strings.TrimSpace(payload)
				var ev struct {
					Type string `json:"type"`
					Kind string `json:"kind"`
				}
				if err := json.Unmarshal([]byte(payload), &ev); err == nil && ev.Kind == "pod" && ev.Type == "ADDED" {
					doneCh <- true
					return
				}
			}
		}
	}()
	select {
	case got := <-doneCh:
		gotEvent = got
	case <-time.After(3 * time.Second):
		_ = resp.Body.Close()
	}
	if !gotEvent {
		t.Fatalf("never received initial ADDED event for pod")
	}
}

// TestHandleK8sStream_ChrootFanOutAcrossAliasMismatchedSecondary — #5571.
//
// End-to-end proof that /k8s/stream fans the SSE initial-state snapshot
// out across EVERY registered cluster on a chroot, even when the
// self-registered primary carries the "sovereign-<fqdn>" alias
// (buildChrootClusterRef's fallback when CATALYST_SELF_DEPLOYMENT_ID
// isn't stamped — documented there as the TYPICAL post-cutover shape) and
// the secondary kubeconfig is registered under the unrelated real-depID
// convention ("<depID>-<region>"). Before the #5571 fix,
// deploymentScopedClusterIDs prefix-matched "<resolvedID>-" against the
// registered cluster ids; "sovereign-<fqdn>-" never matches
// "<depID>-<region>", so the secondary region's objects never reached the
// stream — the Cloud NetworkPolicy/CiliumNetworkPolicy pages silently
// rendered one region's set with no region label to reveal the omission.
func TestHandleK8sStream_ChrootFanOutAcrossAliasMismatchedSecondary(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")

	const (
		selfAlias   = "sovereign-t99.omani.works"
		realDepID   = "7bb723da8da06047"
		secondaryID = realDepID + "-me-east-215-b-1"
	)

	podA := newPod("default", "region-a-pod")
	podB := newPod("default", "region-b-pod")

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, &unstructured.Unstructured{})
	gvrList := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}: "PodList",
	}
	dynA := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList, podA)
	dynB := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList, podB)

	cfg := k8scache.Config{
		Logger:   quietLog(),
		Registry: minimalRegistry(),
		Clusters: []k8scache.ClusterRef{
			{ID: selfAlias, DynamicClient: dynA, CoreClient: kfake.NewSimpleClientset()},
			{ID: secondaryID, DynamicClient: dynB, CoreClient: kfake.NewSimpleClientset()},
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ia, _, _ := f.List(selfAlias, "pod", nil)
		ib, _, _ := f.List(secondaryID, "pod", nil)
		if len(ia) == 1 && len(ib) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// The URL id doesn't have to match either registered cluster — the
	// chroot path aliases any unresolved id onto the self-registered
	// cluster (resolveChrootClusterID), same as a real browser hitting
	// /sovereigns/<mother-issued-id>/k8s/stream post-cutover.
	resp, err := http.Get(srv.URL + "/api/v1/sovereigns/anything/k8s/stream?kinds=pod&initialState=1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	seenClusters := map[string]bool{}
	seenNames := map[string]bool{}
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			var ev struct {
				Cluster string `json:"cluster"`
				Kind    string `json:"kind"`
				Type    string `json:"type"`
				Object  struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
				} `json:"object"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			if ev.Kind != "pod" || ev.Type != "ADDED" {
				continue
			}
			seenClusters[ev.Cluster] = true
			seenNames[ev.Object.Metadata.Name] = true
			if seenClusters[selfAlias] && seenClusters[secondaryID] {
				return
			}
		}
	}()
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		_ = resp.Body.Close()
	}

	if !seenClusters[selfAlias] {
		t.Fatalf("expected an event from the self-registered alias cluster %q; got clusters=%v names=%v",
			selfAlias, seenClusters, seenNames)
	}
	if !seenClusters[secondaryID] {
		t.Fatalf("#5571 REGRESSION: no event from the alias-mismatched secondary cluster %q — "+
			"the stream fanned out to only ONE region; got clusters=%v names=%v",
			secondaryID, seenClusters, seenNames)
	}
	if !seenNames["region-a-pod"] || !seenNames["region-b-pod"] {
		t.Fatalf("expected both region-a-pod and region-b-pod; got names=%v", seenNames)
	}
}

// keep metav1 imported even if a future test refactor drops the
// explicit reference.
var _ = metav1.GetOptions{}

// TestHandleK8sList_NamespaceAliasFiltering — qa-loop iter-11 Fix #45
// Cluster-C. The handler accepts both `?ns=` (historic short form) and
// `?namespace=` (the kubectl/SPA-canonical form). When neither is set,
// every namespace's items are returned.
func TestHandleK8sList_NamespaceAliasFiltering(t *testing.T) {
	podA := newPod("qa-omantel", "qa-wp")
	podB := newPod("alloy", "alloy-host")
	f := newFactoryWithMultiplePods(t, podA, podB)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	type tc struct {
		name      string
		query     string
		wantCount int
		wantNS    string
	}
	cases := []tc{
		{"namespace_param_filters_to_qa_omantel", "?namespace=qa-omantel", 1, "qa-omantel"},
		{"ns_param_still_works", "?ns=qa-omantel", 1, "qa-omantel"},
		{"ns_wins_when_both_set", "?ns=alloy&namespace=qa-omantel", 1, "alloy"},
		{"no_filter_returns_all_namespaces", "", 2, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/sovereigns/alpha/k8s/pod"+c.query, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			var resp K8sListResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(resp.Items) != c.wantCount {
				gotNS := []string{}
				for _, it := range resp.Items {
					gotNS = append(gotNS, it.GetNamespace()+"/"+it.GetName())
				}
				t.Fatalf("query=%q items=%d want=%d got=%v", c.query, len(resp.Items), c.wantCount, gotNS)
			}
			if c.wantCount == 1 && resp.Items[0].GetNamespace() != c.wantNS {
				t.Fatalf("query=%q expected namespace=%q got %q", c.query, c.wantNS, resp.Items[0].GetNamespace())
			}
		})
	}
}

// TestHandleK8sList_FanOutAcrossClusters — TBD-E6 / C3-010, 2026-05-18.
//
// Multi-region Sovereigns register N kubeconfigs in the k8sCache
// (primary + N-1 secondaries via the handover hook, PRs #1579 + #1581).
// /cloud?view=list&kind=nodes must enumerate items from every cluster
// THAT BELONGS TO THE REQUESTED DEPLOYMENT and stamp each row with its
// source cluster id.
//
// Pre-fix: HandleK8sList queried only the resolveChrootClusterID
// result (primary), returning 1 row on a 3-region Sovereign while the
// aggregate /dashboard chips correctly counted 3.
//
// #3987: the secondary's cluster id MUST follow the canonical
// "<primary>-<region>" convention (k8scache.LoadClustersFromDir stems +
// HandleSovereignSecondaryKubeconfig writer) so the deployment-scoped
// fan-out includes it. A bare unrelated id (a SIBLING deployment) must
// NOT be merged — that was the hw178-page-shows-hw136-nodes leak.
func TestHandleK8sList_FanOutAcrossClusters(t *testing.T) {
	podA := newPod("default", "primary-pod")
	podB := newPod("default", "secondary-pod")
	// A THIRD pod in a SIBLING deployment's cluster — must NOT leak into
	// the requested deployment's page (#3987).
	podSibling := newPod("default", "sibling-pod")

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, &unstructured.Unstructured{})
	gvrList := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}: "PodList",
	}
	dynA := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList, podA)
	dynB := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList, podB)
	dynSibling := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList, podSibling)
	const (
		primaryID   = "02bc518fcb1b4cb3"             // the requested deployment
		secondaryID = primaryID + "-me-east-215-b-1" // its OWN secondary region
		siblingID   = "3a2ee904b1d0366a"             // a DIFFERENT live deployment
	)
	cfg := k8scache.Config{
		Logger:   quietLog(),
		Registry: minimalRegistry(),
		Clusters: []k8scache.ClusterRef{
			{ID: primaryID, DynamicClient: dynA, CoreClient: kfake.NewSimpleClientset()},
			{ID: secondaryID, DynamicClient: dynB, CoreClient: kfake.NewSimpleClientset()},
			{ID: siblingID, DynamicClient: dynSibling, CoreClient: kfake.NewSimpleClientset()},
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
	// Wait for all clusters' pod informers to sync.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ia, _, _ := f.List(primaryID, "pod", nil)
		ib, _, _ := f.List(secondaryID, "pod", nil)
		is, _, _ := f.List(siblingID, "pod", nil)
		if len(ia) == 1 && len(ib) == 1 && len(is) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	req := httptest.NewRequest("GET", "/api/v1/sovereigns/"+primaryID+"/k8s/pod", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp K8sListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Exactly 2 items: the deployment's primary + its own secondary. The
	// sibling deployment's pod MUST NOT appear (#3987 cross-deployment
	// leak guard).
	if len(resp.Items) != 2 {
		t.Fatalf("scoped fan-out expected 2 items (primary+own-secondary); got %d (%v)", len(resp.Items), resp.Items)
	}
	// Both this deployment's clusters MUST appear; the sibling MUST NOT.
	gotClusters := map[string]bool{}
	for _, c := range resp.Clusters {
		gotClusters[c] = true
	}
	if !gotClusters[primaryID] || !gotClusters[secondaryID] {
		t.Fatalf("expected Clusters=[%s,%s]; got %v", primaryID, secondaryID, resp.Clusters)
	}
	if gotClusters[siblingID] {
		t.Fatalf("#3987 LEAK: sibling deployment cluster %s appeared in %s's page: %v", siblingID, primaryID, resp.Clusters)
	}
	// Each row carries its source cluster stamp under top-level "cluster".
	stamps := map[string]bool{}
	for _, it := range resp.Items {
		if cid, ok, _ := unstructured.NestedString(it.Object, "cluster"); ok && cid != "" {
			stamps[cid] = true
		}
	}
	if !stamps[primaryID] || !stamps[secondaryID] {
		t.Fatalf("expected each row to carry source cluster id; got %v", stamps)
	}
	if stamps[siblingID] {
		t.Fatalf("#3987 LEAK: a row was stamped with sibling cluster %s", siblingID)
	}
}

// TestHandleK8sList_SingleClusterBackwardCompat — TBD-E6.
//
// Single-cluster Sovereigns MUST keep the pre-fan-out wire shape
// byte-for-byte: top-level Cluster=<id>, no Clusters header, no
// per-row cluster stamp. Guards against regressions in old UI clients
// that key off the legacy shape.
func TestHandleK8sList_SingleClusterBackwardCompat(t *testing.T) {
	pod := newPod("default", "x")
	f := newFactoryWithPod(t, pod)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	req := httptest.NewRequest("GET", "/api/v1/sovereigns/alpha/k8s/pod", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp K8sListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Cluster != "alpha" {
		t.Fatalf("expected Cluster=alpha, got %q", resp.Cluster)
	}
	if len(resp.Clusters) != 0 {
		t.Fatalf("single-cluster path must omit Clusters header; got %v", resp.Clusters)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	// Backward compat: single-cluster path MUST NOT inject `cluster`
	// on each row (legacy UI clients don't expect the key).
	if _, ok, _ := unstructured.NestedString(resp.Items[0].Object, "cluster"); ok {
		t.Fatalf("single-cluster path must not stamp per-row cluster; got cluster=%v", resp.Items[0].Object["cluster"])
	}
}

// newFactoryWithMultiplePods builds an in-memory K8s cache pre-populated
// with N pods across N namespaces — exercises the namespace-filter path
// (single-ns cache wouldn't surface the bug).
func newFactoryWithMultiplePods(t *testing.T, pods ...*unstructured.Unstructured) *k8scache.Factory {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, &unstructured.Unstructured{})
	gvrList := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}: "PodList",
	}
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objs = append(objs, p)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList, objs...)
	core := kfake.NewSimpleClientset()
	cfg := k8scache.Config{
		Logger:   quietLog(),
		Registry: minimalRegistry(),
		Clusters: []k8scache.ClusterRef{
			{ID: "alpha", DynamicClient: dyn, CoreClient: core},
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, _, _ := f.List("alpha", "pod", nil)
		if len(items) >= len(pods) {
			return f
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f
}
