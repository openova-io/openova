// Package handler — k8s_resource_metrics_mimir.go: mimir-backed Pod
// sparkline (issue #1753, slice G3b).
//
// The legacy metrics-server path (k8s_resource_metrics.go) returns a
// single point-in-time CPU+memory snapshot. Operators want a short
// time-series so the Pod detail panel can render a sparkline that
// distinguishes "spiky but healthy" from "steady saturation". Mimir
// (bp-mimir, bootstrap-kit slot 23) is the Prometheus-compatible
// metrics store the LGTM stack ships with, and it scrapes every Pod
// via Alloy. We can therefore drive a 5-minute / 5-second-step
// sparkline (60 buckets) by issuing a single `query_range` against
// Mimir's query-frontend.
//
// Wire contract:
//
//   GET <mimirURL>/prometheus/api/v1/query_range
//     ?query=<promql>&start=<unix>&end=<unix>&step=5s
//
// Response shape (Prometheus matrix):
//
//   {
//     status: "success",
//     data: {
//       resultType: "matrix",
//       result: [
//         { metric: {...}, values: [[ts, "val"], ...] }
//       ]
//     }
//   }
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) the base URL
// is configuration: CATALYST_MIMIR_URL → Handler.mimirURL. Empty URL
// causes the caller to fall back to the metrics-server single-point
// path; an HTTP error from the upstream mimir does the same so a
// transient query-frontend hiccup never blanks the panel.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// promRangeResponse mirrors the Prometheus query_range matrix shape we
// care about. Extra fields the upstream emits are deliberately ignored.
type promRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			// Values — Prometheus emits `[timestamp_float, "value"]`
			// 2-tuples. Decode into `[]any` so we can pick the string
			// half without fighting the upstream's mixed-type array.
			Values [][]any `json:"values"`
		} `json:"result"`
	} `json:"data"`
	ErrorType string `json:"errorType,omitempty"`
	Error     string `json:"error,omitempty"`
}

// mimirPodSeries — output bucket shape consumed by the FE sparkline.
// Aligned with the per-bucket payload the metrics-server path emits
// for the single-point case so the FE can render either response.
type mimirPodSeries struct {
	Timestamp string `json:"timestamp"`
	CPUMilli  int64  `json:"cpuMilli"`
	MemBytes  int64  `json:"memBytes"`
	CPU       string `json:"cpu"`
	Memory    string `json:"memory"`
}

// mimirSparklineWindow is the operator-visible window for the Pod
// sparkline. 5 minutes at 5-second resolution → 60 buckets, the same
// rule-of-thumb the Grafana node-overview dashboard uses.
const (
	mimirSparklineWindow = 5 * time.Minute
	mimirSparklineStep   = 5 * time.Second
)

// fetchPodSparkline issues two query_range calls (CPU + memory) against
// the wired Mimir query-frontend and returns the merged per-bucket
// series. Returns (nil, err) on any upstream failure so the caller
// falls back to the metrics-server single-point path.
func (h *Handler) fetchPodSparkline(ctx context.Context, ns, name string) ([]mimirPodSeries, error) {
	if h.mimirURL == "" {
		return nil, fmt.Errorf("mimir url not wired")
	}
	end := time.Now().UTC()
	start := end.Add(-mimirSparklineWindow)

	cpuQ := fmt.Sprintf(`rate(container_cpu_usage_seconds_total{pod=%q,namespace=%q}[1m])`, name, ns)
	memQ := fmt.Sprintf(`container_memory_working_set_bytes{pod=%q,namespace=%q}`, name, ns)

	cpuMatrix, err := h.mimirQueryRange(ctx, cpuQ, start, end, mimirSparklineStep)
	if err != nil {
		return nil, fmt.Errorf("mimir cpu: %w", err)
	}
	memMatrix, err := h.mimirQueryRange(ctx, memQ, start, end, mimirSparklineStep)
	if err != nil {
		return nil, fmt.Errorf("mimir mem: %w", err)
	}

	// Mimir returns one series per container; sum to a per-Pod total.
	cpuByTS := sumMatrixByTimestamp(cpuMatrix)
	memByTS := sumMatrixByTimestamp(memMatrix)

	// Emit one bucket per step across the window so the FE sees a
	// stable 60-element series even when a container was briefly
	// missing — gaps render as zero rather than collapsing the X axis.
	buckets := int(mimirSparklineWindow / mimirSparklineStep)
	out := make([]mimirPodSeries, 0, buckets)
	for i := 0; i < buckets; i++ {
		t := start.Add(time.Duration(i) * mimirSparklineStep)
		key := t.Unix()
		cpuCores := cpuByTS[key]
		memBytes := memByTS[key]
		cpuMilli := int64(cpuCores * 1000)
		out = append(out, mimirPodSeries{
			Timestamp: t.Format(time.RFC3339),
			CPUMilli:  cpuMilli,
			MemBytes:  int64(memBytes),
			CPU:       formatCPUMilli(cpuMilli),
			Memory:    formatMemBytes(int64(memBytes)),
		})
	}
	return out, nil
}

// mimirQueryRange issues a single query_range and decodes the matrix
// response. Returns the decoded series slice so the caller can sum
// across containers.
func (h *Handler) mimirQueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*promRangeResponse, error) {
	base := strings.TrimRight(h.mimirURL, "/")
	u, err := url.Parse(base + "/prometheus/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("parse mimir url: %w", err)
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", fmt.Sprintf("%ds", int(step.Seconds())))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	// Mimir multi-tenant mode requires a tenant header; the
	// Catalyst-Zero bp-mimir overlay disables multi-tenancy so the
	// default `anonymous` tenant accepts every query. We still
	// stamp `X-Scope-OrgID: catalyst` so a Sovereign that flips
	// auth_enabled=true later only needs to mirror the tenant config
	// (no API change here).
	req.Header.Set("X-Scope-OrgID", "catalyst")

	client := h.mimirHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Drain a small slice of the body for the log; do not pass
		// the upstream's error envelope through to the caller —
		// fallback-to-metrics-server is the recovery path here.
		_, _ = io.CopyN(io.Discard, resp.Body, 4<<10)
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	var pr promRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("mimir error: %s (%s)", pr.Error, pr.ErrorType)
	}
	return &pr, nil
}

// sumMatrixByTimestamp folds a Prometheus matrix down to a map keyed
// by epoch-second. When multiple series (one per container) share a
// timestamp, the values are summed — yielding a Pod-level total.
func sumMatrixByTimestamp(pr *promRangeResponse) map[int64]float64 {
	out := map[int64]float64{}
	if pr == nil {
		return out
	}
	for _, s := range pr.Data.Result {
		for _, v := range s.Values {
			if len(v) != 2 {
				continue
			}
			tsFloat, ok := v[0].(float64)
			if !ok {
				continue
			}
			valStr, ok := v[1].(string)
			if !ok {
				continue
			}
			val, err := strconv.ParseFloat(valStr, 64)
			if err != nil {
				continue
			}
			out[int64(tsFloat)] += val
		}
	}
	return out
}
