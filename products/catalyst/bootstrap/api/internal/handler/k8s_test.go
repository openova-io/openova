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

func TestHealth_PlainTextWhenK8sCacheDisabled(t *testing.T) {
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
}

func TestHealth_JSONWhenAcceptHeaderSet(t *testing.T) {
	f := newFactoryWithPod(t, nil)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := newRouter(h)

	req := httptest.NewRequest("GET", "/healthz", nil)
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

// keep metav1 imported even if a future test refactor drops the
// explicit reference.
var _ = metav1.GetOptions{}
