// Package proxy implements the request-observing reverse proxy at the
// heart of the catalyst-metering-sidecar. The proxy:
//
//  1. Forwards the incoming HTTP request unchanged to NewAPI.
//  2. Reads and parses the response body to extract the OpenAI-style
//     `usage.total_tokens` field (when present + the upstream returned
//     2xx).
//  3. Computes amount_micro_omr = -total_tokens * price_micro_omr_per_token
//     and emits one envelope on `catalyst.usage.recorded`.
//  4. Writes the response back to the client unchanged, so the customer-
//     facing latency added by the sidecar is bounded by JSON parsing
//     plus a single fire-and-forget publish.
//
// Responses that are NOT successful, NOT JSON, or do not carry a usage
// block (streaming responses, model-list endpoints, embeddings without
// token counts) are forwarded transparently and produce no envelope.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/openova-io/openova/core/services/shared/events"
)

// Publisher is the interface MeteringProxy uses for emit. Concrete
// production type is *MeteringPublisher; tests substitute a fake.
type Publisher interface {
	PublishOrSpool(ctx context.Context, env events.UsageRecordedPayload) error
}

// MeteringProxy is the http.Handler that fronts NewAPI.
type MeteringProxy struct {
	Upstream              *url.URL
	Publisher             Publisher
	PriceMicroOMRPerToken int64
	// TenantIDHeader is the lower-case HTTP header name carrying the
	// Organization tenant id (default "x-tenant-id"). NewAPI's customer-API
	// admin layer or the cluster ingress is expected to inject it on
	// every authenticated request — the sidecar does not extract it
	// from the bearer token because that would tie us to a specific
	// IdP token shape.
	TenantIDHeader string
	// CustomerIDHeader is the lower-case HTTP header name carrying
	// the Organization-vcluster Keycloak user UUID (default "x-customer-id").
	// Same injection contract as TenantIDHeader.
	CustomerIDHeader string

	// reverseProxy is initialised lazily on first ServeHTTP call so
	// the zero value of MeteringProxy is usable.
	reverseProxy *httputil.ReverseProxy
}

// ServeHTTP implements http.Handler. It is the only entry point.
func (p *MeteringProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.reverseProxy == nil {
		p.reverseProxy = httputil.NewSingleHostReverseProxy(p.Upstream)
		// Customise ModifyResponse so we can observe the response
		// body BEFORE it streams to the client.
		p.reverseProxy.ModifyResponse = p.observeResponse
	}
	p.reverseProxy.ServeHTTP(w, r)
}

// observeResponse is the httputil.ReverseProxy ModifyResponse hook.
// It buffers the response body, parses it for an OpenAI-style usage
// block, emits a NATS envelope for billable calls, and rewinds the
// body so the proxy continues to stream the original bytes to the
// client unchanged.
//
// Responses larger than 4 MiB are passed through without metering —
// LLM API responses are routinely small (the prompt+completion text
// fits in a single envelope), and the buffer cap protects against an
// adversarial oversized body. Models that genuinely need >4 MiB
// responses (rare; usually large embeddings) skip metering for now;
// a follow-up may add a streaming-safe parser.
func (p *MeteringProxy) observeResponse(resp *http.Response) error {
	const maxBodyBytes = 4 << 20 // 4 MiB

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Failed requests are NOT billed (per #798 spec: success only).
		return nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(ct, "application/json") {
		// Non-JSON response — not an LLM completion.
		return nil
	}
	if resp.Request == nil || !pathLooksLikeLLM(resp.Request.URL.Path) {
		// Non-/v1/* path — admin/status/healthz traffic etc.
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		// Read failure — leave the response untouched and skip
		// metering. The proxy will surface the error to the client
		// via its own error path.
		return nil
	}
	resp.Body.Close()

	if len(body) > maxBodyBytes {
		// Oversize body — restore as-is and skip metering.
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	// Restore body for the client write-through.
	resp.Body = io.NopCloser(bytes.NewReader(body))

	// Parse and emit. Failures here are observability errors only —
	// the customer's LLM response is already on its way back.
	var parsed struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// Streaming chunks, malformed bodies, or non-OpenAI shapes —
		// nothing to bill.
		return nil
	}
	if parsed.Usage.TotalTokens == 0 {
		// Endpoint did not emit usage (chat-completion stream chunk,
		// embeddings without count, etc.) — skip.
		return nil
	}

	// Resolve customer + tenant. If the header injection contract is
	// not honoured we still emit the envelope with empty fields so the
	// subscriber can log + skip. NEVER fail-open with billable amounts
	// against an unknown customer.
	customerID := strings.TrimSpace(resp.Request.Header.Get(p.CustomerIDHeader))
	tenantID := strings.TrimSpace(resp.Request.Header.Get(p.TenantIDHeader))

	requestID := strings.TrimSpace(resp.Request.Header.Get("X-Request-Id"))
	if requestID == "" {
		// Generate a stable request id so the subscriber can dedupe
		// retries from the broker side. NewAPI itself logs an
		// internal request id but does not always surface it through
		// the response headers; falling back to a UUID per response
		// is correct because retries by the broker carry the same
		// publish-side Msg-Id (via JetStream WithMsgID).
		requestID = uuid.NewString()
	}

	micro := -int64(parsed.Usage.TotalTokens) * p.PriceMicroOMRPerToken
	envelope := events.UsageRecordedPayload{
		CustomerID:     customerID,
		AmountOMR:      float64(micro) / 1_000_000,
		AmountMicroOMR: micro,
		Reason:         buildReason(parsed.Model),
		Metadata: events.UsageRecordedMetadata{
			TokensUsed:  parsed.Usage.TotalTokens,
			Model:       parsed.Model,
			RequestID:   requestID,
			TenantID:    tenantID,
			LatencyMS:   readLatencyHeader(resp.Header.Get("X-Upstream-Latency-Ms")),
			CompletedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}

	if err := p.Publisher.PublishOrSpool(resp.Request.Context(), envelope); err != nil {
		slog.Warn("metering publish failed — spooled to disk",
			"request_id", requestID, "error", err)
	}
	return nil
}

func pathLooksLikeLLM(path string) bool {
	// /v1/chat/completions, /v1/completions, /v1/embeddings, ...
	// /api/v1/* is NewAPI's admin surface — those calls do NOT bill.
	return strings.HasPrefix(path, "/v1/")
}

func buildReason(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		model = "unknown"
	}
	return "usage:newapi:" + model
}

func readLatencyHeader(s string) int {
	// Best-effort — the upstream may or may not surface latency.
	// Empty / non-numeric value yields zero.
	if s == "" {
		return 0
	}
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
		if n > 1<<30 {
			return 0
		}
	}
	return n
}
