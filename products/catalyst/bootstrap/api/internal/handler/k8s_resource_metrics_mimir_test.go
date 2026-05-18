// k8s_resource_metrics_mimir_test.go — coverage for the mimir-backed
// Pod sparkline path added in issue #1753 (slice G3b).
//
// Test strategy: stand up a httptest.Server that fakes Mimir's
// `query_range` endpoint, point Handler.mimirURL at it, and assert the
// handler returns `source=mimir` plus a multi-bucket series. A second
// test asserts the fallback path: when mimirURL is empty (or upstream
// errors) the handler falls through to the metrics-server single-point
// shape with source=metrics.k8s.io / unavailable.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMimirQueryRange returns a httptest.Server that emits a 60-bucket
// matrix response shaped like Prometheus' query_range. Both the CPU
// and memory queries hit the same handler; the metric label set
// distinguishes the two so the response is appropriate.
func fakeMimirQueryRange(t *testing.T, wantNS, wantPod string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prometheus/api/v1/query_range" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		q := r.URL.Query().Get("query")
		// Sanity-check the namespace + pod labels made it into the
		// PromQL — if the handler ever drops them the matrix would
		// span every Pod in the cluster.
		if !strings.Contains(q, fmt.Sprintf("namespace=%q", wantNS)) {
			http.Error(w, "missing ns label", http.StatusBadRequest)
			return
		}
		if !strings.Contains(q, fmt.Sprintf("pod=%q", wantPod)) {
			http.Error(w, "missing pod label", http.StatusBadRequest)
			return
		}
		// Emit a 60-bucket matrix at the start/end the caller sent.
		startStr := r.URL.Query().Get("start")
		var start int64
		fmt.Sscanf(startStr, "%d", &start)
		// Two containers per Pod to exercise the sum-by-timestamp
		// fold. The query-shape distinguishes CPU vs Memory; for the
		// test we emit different magnitudes so the assertion can tell
		// which is which.
		var value string
		if strings.HasPrefix(q, "rate(") {
			// CPU — 0.25 cores per container → 500m total per bucket.
			value = "0.25"
		} else {
			// Memory — 128Mi per container → 256Mi total per bucket.
			value = "134217728"
		}
		values := [][]any{}
		for i := int64(0); i < 60; i++ {
			ts := float64(start + i*5)
			values = append(values, []any{ts, value})
		}
		resp := map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []any{
					map[string]any{
						"metric": map[string]string{"container": "a"},
						"values": values,
					},
					map[string]any{
						"metric": map[string]string{"container": "b"},
						"values": values,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestHandleK8sResourceMetrics_PodMimirSparkline(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()

	srv := fakeMimirQueryRange(t, "default", "wp-1")
	defer srv.Close()
	rig.h.SetMimirURL(srv.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/k8s/metrics/pod/default/wp-1", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp metricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Source != "mimir" {
		t.Fatalf("source: got %q want mimir; body=%s", resp.Source, rec.Body.String())
	}
	if len(resp.Series) != 60 {
		t.Fatalf("series len: got %d want 60", len(resp.Series))
	}
	// First + last bucket should both carry the per-Pod (summed) totals.
	// CPU: 0.25 cores × 2 containers → 0.5 cores → 500m.
	// Mem: 128Mi × 2 containers → 256Mi → 268435456 bytes.
	first := resp.Series[0]
	if got := first["cpuMilli"]; got != float64(500) {
		t.Fatalf("series[0].cpuMilli: got %v want 500", got)
	}
	if got := first["memBytes"]; got != float64(268435456) {
		t.Fatalf("series[0].memBytes: got %v want 268435456", got)
	}
	if got := resp.Current["cpuMilli"]; got != float64(500) {
		t.Fatalf("current.cpuMilli: got %v want 500", got)
	}
	if !strings.Contains(fmt.Sprintf("%v", resp.Current["cpu"]), "500m") {
		t.Fatalf("current.cpu: got %v want 500m", resp.Current["cpu"])
	}
}

func TestHandleK8sResourceMetrics_PodMimirFallsBackOnUpstreamError(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()

	// Upstream that always 500s — handler must fall through to the
	// metrics-server path (which here also has no PodMetrics
	// indexer → resp.Source must NOT be "mimir").
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "mimir down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	rig.h.SetMimirURL(srv.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/k8s/metrics/pod/default/wp-1", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp metricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Source == "mimir" {
		t.Fatalf("source: must NOT be mimir after upstream 500; got %q", resp.Source)
	}
}

func TestHandleK8sResourceMetrics_PodMimirSkippedWhenUnwired(t *testing.T) {
	pod := newPodObj("default", "wp-1")
	rig := newResourceRig(t, pod)
	defer rig.stop()
	// Deliberately do NOT call SetMimirURL — the handler must skip
	// the mimir attempt and fall through to the metrics-server path.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/k8s/metrics/pod/default/wp-1", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp metricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Source == "mimir" {
		t.Fatalf("source: must NOT be mimir when CATALYST_MIMIR_URL is empty; got %q", resp.Source)
	}
}
