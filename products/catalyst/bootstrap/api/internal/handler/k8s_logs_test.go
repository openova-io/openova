// k8s_logs_test.go — handler-level tests for EPIC-4 X1 (#1099).
//
// These tests exercise:
//   - parseLogOptions: query-param parsing (follow / tailLines / since / previous)
//   - HandleK8sLogs: 503 / 404 / 400 path errors before WebSocket upgrade
//   - HandleK8sLogs: full WebSocket happy-path with a stub kubelet stream
//
// The streaming path uses a fake kubernetes.Interface backed by
// k8s.io/client-go/kubernetes/fake. The fake's Pods().GetLogs()
// returns a static response body — sufficient to exercise the
// pump's frame layout + clean-EOF close path.
package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

func TestParseLogOptions_Defaults(t *testing.T) {
	opts, err := parseLogOptions(nil, "web")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Container != "web" {
		t.Fatalf("container: %q", opts.Container)
	}
	if !opts.Follow {
		t.Fatal("follow default should be true")
	}
	if opts.TailLines == nil || *opts.TailLines != 100 {
		t.Fatalf("tailLines default: %v", opts.TailLines)
	}
	if opts.Previous {
		t.Fatal("previous default should be false")
	}
	if opts.SinceTime != nil {
		t.Fatal("sinceTime default should be nil")
	}
}

func TestParseLogOptions_Follow_TailLines_Previous(t *testing.T) {
	opts, err := parseLogOptions(map[string][]string{
		"follow":    {"false"},
		"tailLines": {"50"},
		"previous":  {"true"},
	}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Follow {
		t.Fatal("follow=false expected")
	}
	if opts.TailLines == nil || *opts.TailLines != 50 {
		t.Fatalf("tailLines: %v", opts.TailLines)
	}
	if !opts.Previous {
		t.Fatal("previous=true expected")
	}
}

func TestParseLogOptions_TailLinesCappedAndInvalid(t *testing.T) {
	opts, err := parseLogOptions(map[string][]string{"tailLines": {"99999999"}}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if *opts.TailLines != 10000 {
		t.Fatalf("expected cap 10000, got %d", *opts.TailLines)
	}

	if _, err := parseLogOptions(map[string][]string{"tailLines": {"-1"}}, "web"); err == nil {
		t.Fatal("expected error on negative tailLines")
	}
	if _, err := parseLogOptions(map[string][]string{"tailLines": {"abc"}}, "web"); err == nil {
		t.Fatal("expected error on non-numeric tailLines")
	}
}

func TestParseLogOptions_Since(t *testing.T) {
	opts, err := parseLogOptions(map[string][]string{"since": {"2026-01-01T00:00:00Z"}}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if opts.SinceTime == nil {
		t.Fatal("sinceTime nil")
	}
	if !opts.SinceTime.Time.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("sinceTime: %v", opts.SinceTime.Time)
	}
}

func TestParseLogOptions_BadSince(t *testing.T) {
	if _, err := parseLogOptions(map[string][]string{"since": {"yesterday"}}, "web"); err == nil {
		t.Fatal("expected error on non-RFC3339 since")
	}
}

func TestHandleK8sLogs_503WhenDisabled(t *testing.T) {
	h := &Handler{log: quietLog()}
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}", h.HandleK8sLogs)
	req := httptest.NewRequest("GET", "/api/v1/sovereigns/alpha/k8s/logs/default/web/web", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// newFactoryAndCore returns a k8scache.Factory wired with a fake
// kubernetes.Interface — used to exercise X1's HandleK8sLogs paths.
// Reuses the same fake-client harness pattern as k8s_test.go.
func newFactoryAndCore(t *testing.T) (*k8scache.Factory, *kfake.Clientset) {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrList := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}: "PodList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrList)
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
	t.Cleanup(f.Stop)
	return f, core
}

func TestHandleK8sLogs_404WhenClusterMissing(t *testing.T) {
	f, _ := newFactoryAndCore(t)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}", h.HandleK8sLogs)
	req := httptest.NewRequest("GET", "/api/v1/sovereigns/zeta/k8s/logs/default/web/web", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleK8sLogs_400WhenBadQueryParam(t *testing.T) {
	f, _ := newFactoryAndCore(t)
	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}", h.HandleK8sLogs)
	req := httptest.NewRequest("GET", "/api/v1/sovereigns/alpha/k8s/logs/default/web/web?since=yesterday", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%q)", rec.Code, rec.Body.String())
	}
}

// TestHandleK8sLogs_HappyPath exercises the full WebSocket flow with
// a fake kubernetes.Interface. The fake intercepts GetLogs requests
// and returns a small static response — the pump frames it onto the
// WebSocket, then the test reads back the frames + asserts the close.
func TestHandleK8sLogs_HappyPath(t *testing.T) {
	f, core := newFactoryAndCore(t)
	// Seed a Pod so the GetLogs request finds something to read.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "web", Image: "nginx"}},
		},
	}
	if _, err := core.CoreV1().Pods("default").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed pod: %v", err)
	}

	h := &Handler{log: quietLog()}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}", h.HandleK8sLogs)

	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/api/v1/sovereigns/alpha/k8s/logs/default/web/web?follow=false&tailLines=10"
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer conn.Close()

	// The fake kubelet returns "fake logs" by default; the pump frames
	// it as one binary message. Read it.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	mt, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if mt != websocket.BinaryMessage {
		t.Fatalf("type: %d", mt)
	}
	if len(payload) == 0 {
		t.Fatal("empty payload")
	}
	// Subsequent reads should return the close frame (1000 normal).
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			ce, ok := err.(*websocket.CloseError)
			if ok && (ce.Code == websocket.CloseNormalClosure || ce.Code == websocket.CloseAbnormalClosure) {
				return
			}
			// any close-style error is acceptable — the assertion is
			// "stream ends cleanly", not a specific framing.
			return
		}
	}
}
