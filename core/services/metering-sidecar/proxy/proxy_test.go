package proxy

// Tests for the request-observing reverse proxy.
//
// Strategy: spin up an httptest.Server that pretends to be NewAPI,
// point a MeteringProxy at it, drive HTTP requests through, and
// assert (a) the customer-facing response is identical to the
// upstream's, and (b) the publisher saw the right envelope. The
// publisher is a tiny in-memory fake that satisfies the Publisher
// interface — no NATS, no disk.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
)

// fakePublisher captures envelopes for assertion. Satisfies the
// Publisher interface defined in proxy.go.
type fakePublisher struct {
	mu   sync.Mutex
	envs []events.UsageRecordedPayload
}

func (f *fakePublisher) PublishOrSpool(_ context.Context, env events.UsageRecordedPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envs = append(f.envs, env)
	return nil
}

func (f *fakePublisher) snapshot() []events.UsageRecordedPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]events.UsageRecordedPayload, len(f.envs))
	copy(out, f.envs)
	return out
}

// runProxyTest spins up upstream + proxy, sends one request, returns
// captured envelopes + the body the client received.
func runProxyTest(t *testing.T, body []byte, headers http.Header, statusCode int, requestPath string) ([]events.UsageRecordedPayload, []byte) {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, vs := range headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(statusCode)
		w.Write(body)
	}))
	defer upstream.Close()

	upURL, _ := url.Parse(upstream.URL)
	fp := &fakePublisher{}
	proxy := &MeteringProxy{
		Upstream:              upURL,
		Publisher:             fp,
		PriceMicroOMRPerToken: 156,
		TenantIDHeader:        "x-tenant-id",
		CustomerIDHeader:      "x-customer-id",
	}

	server := httptest.NewServer(proxy)
	defer server.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+requestPath, bytes.NewReader([]byte(`{"prompt":"hi"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Customer-Id", "user-uuid-1")
	req.Header.Set("X-Tenant-Id", "tenant-acme")
	req.Header.Set("X-Request-Id", "req-test-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client do: %v", err)
	}
	defer resp.Body.Close()

	gotBody, _ := io.ReadAll(resp.Body)
	return fp.snapshot(), gotBody
}

// TestObserveResponse_Bills2xxJSONUsage covers the canonical happy
// path: /v1/chat/completions returns 200 with an OpenAI usage block;
// one envelope is emitted with the correct micro-OMR amount, and the
// client sees the original response bytes.
func TestObserveResponse_Bills2xxJSONUsage(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","model":"qwen3-coder","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`)
	headers := http.Header{"Content-Type": {"application/json"}}
	envs, gotBody := runProxyTest(t, body, headers, http.StatusOK, "/v1/chat/completions")

	if !bytes.Equal(gotBody, body) {
		t.Errorf("client body differs from upstream\n got: %q\nwant: %q", gotBody, body)
	}
	if len(envs) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(envs))
	}
	e := envs[0]
	if e.CustomerID != "user-uuid-1" {
		t.Errorf("customer_id = %q, want user-uuid-1", e.CustomerID)
	}
	if e.Metadata.TenantID != "tenant-acme" {
		t.Errorf("tenant_id = %q", e.Metadata.TenantID)
	}
	if e.Metadata.Model != "qwen3-coder" {
		t.Errorf("model = %q", e.Metadata.Model)
	}
	if e.Metadata.TokensUsed != 15 {
		t.Errorf("tokens_used = %d, want 15", e.Metadata.TokensUsed)
	}
	if e.Metadata.RequestID != "req-test-1" {
		t.Errorf("request_id = %q, want req-test-1", e.Metadata.RequestID)
	}
	if e.Reason != "usage:newapi:qwen3-coder" {
		t.Errorf("reason = %q", e.Reason)
	}
	wantMicro := int64(-15 * 156)
	if e.AmountMicroOMR != wantMicro {
		t.Errorf("amount_micro_omr = %d, want %d", e.AmountMicroOMR, wantMicro)
	}
	if e.AmountOMR != float64(wantMicro)/1_000_000 {
		t.Errorf("amount_omr = %f", e.AmountOMR)
	}
}

// TestObserveResponse_Skips4xx covers the "failed requests don't bill"
// rule — a 401 from upstream produces NO envelope and the body
// passes through.
func TestObserveResponse_Skips4xx(t *testing.T) {
	body := []byte(`{"error":"invalid api key"}`)
	headers := http.Header{"Content-Type": {"application/json"}}
	envs, _ := runProxyTest(t, body, headers, http.StatusUnauthorized, "/v1/chat/completions")
	if len(envs) != 0 {
		t.Fatalf("expected 0 envelopes for 401, got %d", len(envs))
	}
}

// TestObserveResponse_Skips5xx covers transient backend failures.
func TestObserveResponse_Skips5xx(t *testing.T) {
	body := []byte(`{"error":"upstream timeout"}`)
	headers := http.Header{"Content-Type": {"application/json"}}
	envs, _ := runProxyTest(t, body, headers, http.StatusServiceUnavailable, "/v1/chat/completions")
	if len(envs) != 0 {
		t.Fatalf("expected 0 envelopes for 503, got %d", len(envs))
	}
}

// TestObserveResponse_SkipsAdminPath: admin/internal calls
// (/api/v1/admin/*, /api/status) are NOT billed.
func TestObserveResponse_SkipsAdminPath(t *testing.T) {
	body := []byte(`{"status":"ok","version":"1.0"}`)
	headers := http.Header{"Content-Type": {"application/json"}}
	envs, _ := runProxyTest(t, body, headers, http.StatusOK, "/api/status")
	if len(envs) != 0 {
		t.Fatalf("expected 0 envelopes for /api/status, got %d", len(envs))
	}
}

// TestObserveResponse_SkipsNoUsageBlock: /v1/* returning 200 JSON but
// without a usage block (e.g., model-list) does NOT bill.
func TestObserveResponse_SkipsNoUsageBlock(t *testing.T) {
	body := []byte(`{"data":[{"id":"qwen3-coder","object":"model"}]}`)
	headers := http.Header{"Content-Type": {"application/json"}}
	envs, _ := runProxyTest(t, body, headers, http.StatusOK, "/v1/models")
	if len(envs) != 0 {
		t.Fatalf("expected 0 envelopes for /v1/models, got %d", len(envs))
	}
}

// TestObserveResponse_SkipsNonJSONResponse: streaming responses use
// text/event-stream — we don't bill those at all in v1.
func TestObserveResponse_SkipsNonJSONResponse(t *testing.T) {
	body := []byte(`data: {"choices":[]}`)
	headers := http.Header{"Content-Type": {"text/event-stream"}}
	envs, _ := runProxyTest(t, body, headers, http.StatusOK, "/v1/chat/completions")
	if len(envs) != 0 {
		t.Fatalf("expected 0 envelopes for SSE, got %d", len(envs))
	}
}

// TestObserveResponse_GeneratesRequestIDIfMissing: when the upstream
// did not stamp X-Request-Id, the proxy generates one (UUID) so the
// subscriber's idempotency dedup still works.
func TestObserveResponse_GeneratesRequestIDIfMissing(t *testing.T) {
	body := []byte(`{"model":"qwen3-coder","usage":{"total_tokens":10}}`)
	headers := http.Header{"Content-Type": {"application/json"}}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer upstream.Close()

	upURL, _ := url.Parse(upstream.URL)
	fp := &fakePublisher{}
	proxy := &MeteringProxy{
		Upstream:              upURL,
		Publisher:             fp,
		PriceMicroOMRPerToken: 156,
		TenantIDHeader:        "x-tenant-id",
		CustomerIDHeader:      "x-customer-id",
	}

	server := httptest.NewServer(proxy)
	defer server.Close()

	// No X-Request-Id this time.
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/completions",
		bytes.NewReader([]byte(`{"prompt":"hi"}`)))
	req.Header.Set("X-Customer-Id", "user-uuid-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client do: %v", err)
	}
	resp.Body.Close()

	envs := fp.snapshot()
	if len(envs) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(envs))
	}
	if envs[0].Metadata.RequestID == "" {
		t.Fatal("expected generated request_id, got empty")
	}

	// Use _ on body for the linter
	_ = headers
}

// TestBuildReason verifies the reason normalization.
func TestBuildReason(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"qwen3-coder", "usage:newapi:qwen3-coder"},
		{"  Claude-Sonnet-4-6  ", "usage:newapi:claude-sonnet-4-6"},
		{"", "usage:newapi:unknown"},
	}
	for _, tc := range cases {
		if got := buildReason(tc.in); got != tc.want {
			t.Errorf("buildReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPathLooksLikeLLM ensures only /v1/* paths are billable.
func TestPathLooksLikeLLM(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/chat/completions", true},
		{"/v1/embeddings", true},
		{"/v1/models", true},
		{"/api/v1/admin/users", false},
		{"/api/status", false},
		{"/healthz", false},
	}
	for _, tc := range cases {
		if got := pathLooksLikeLLM(tc.path); got != tc.want {
			t.Errorf("pathLooksLikeLLM(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
