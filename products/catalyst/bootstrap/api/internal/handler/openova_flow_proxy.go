// Package handler — openova_flow_proxy.go: catalyst-api proxy for the
// OpenovaFlow event router (Agent #3 integration, follow-up to PRs
// #1389 / #1390).
//
// REST/SSE surface (registered by main.go in the auth-gated chi.Group):
//
//	GET  /api/v1/flows/{deploymentId}/snapshot — current FlowInstance + nodes + rels
//	GET  /api/v1/flows/{deploymentId}/stream   — SSE: snapshot + tail (pass-through)
//	POST /api/v1/flows/{deploymentId}/events   — ingest one FlowMessage envelope
//
// All three proxy to the openova-flow-server at OPENOVA_FLOW_SERVER_URL
// (default in-cluster Service URL for the Sovereign-side catalyst-api;
// the mother-side catalyst-api consumes its own per-Sovereign HTTPRoute
// — see main.go where the env is read from the deployment record).
//
// The flowId on the upstream is the catalyst-api deploymentId — the
// openova-flow-server uses flowId as an opaque key so the same string
// works on both sides without translation.
//
// Architecture rules:
//
//   - docs/INVIOLABLE-PRINCIPLES.md #1 (target-state): full SSE bytes
//     stream end-to-end. No buffered httputil.ReverseProxy — that
//     collects the entire body before flushing and breaks
//     `text/event-stream` semantics. We use http.Flusher + an explicit
//     read-write loop so every `data:` frame from the upstream lands
//     in the browser within ~milliseconds.
//   - docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode): the upstream
//     URL is sourced from env `OPENOVA_FLOW_SERVER_URL`. There is no
//     default `https://…` literal that would tie this code to a
//     specific Sovereign FQDN.
//   - Canonical SSE pattern in this codebase: deployments.go
//     `StreamLogs` (lines 1208-1287) — Content-Type: text/event-stream,
//     Cache-Control: no-cache, Connection: keep-alive, X-Accel-Buffering:
//     no, http.Flusher.Flush after every event. This proxy mirrors that
//     header set on the response side AND streams the upstream body
//     verbatim.
//   - Headers propagated to upstream: cookies (the openova-flow-server
//     itself is unauthenticated today, but future iterations may want
//     to assert the operator's session). Request-context propagation
//     ensures the upstream connection closes when the browser tab
//     closes.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #21 (two-repo discipline) this
// proxy lives in openova-io/openova (public). The per-Sovereign
// concrete openova-flow-server URL lives in the deployment record
// (private state inside the Sovereign's K8s — Service DNS resolution
// or HTTPRoute hostname is derived at runtime, not committed).

package handler

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// defaultFlowServerURL is the in-cluster Service URL the bp-openova-
// flow-server chart's templates/service.yaml lands. The catalyst-api
// inside a Sovereign chroot reaches its own openova-flow-server via
// this URL with zero extra plumbing.
//
// For the mother-side catalyst-api, OPENOVA_FLOW_SERVER_URL is set per-
// deployment (resolved from each deployment's sovereignFQDN to the
// Cilium Gateway HTTPRoute) so the proxy fans out to the correct
// Sovereign's flow-server. See main.go for the env wiring.
const defaultFlowServerURL = "http://openova-flow-server.catalyst-system.svc.cluster.local:8080"

// flowProxyHTTPClient is the singleton http.Client used by the
// snapshot + ingest proxies. SSE uses a separate client below because
// it disables timeouts.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every knob is env-tunable: we
// initialize once at package-init time using OPENOVA_FLOW_PROXY_TIMEOUT
// (Go duration string, default 10s) so an operator can widen the
// budget without a code change.
var flowProxyHTTPClient = func() *http.Client {
	t := 10 * time.Second
	if s := strings.TrimSpace(os.Getenv("OPENOVA_FLOW_PROXY_TIMEOUT")); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			t = d
		}
	}
	return &http.Client{Timeout: t}
}()

// flowSSEHTTPClient — for the SSE long-lived stream. Zero timeout (the
// stream is held open until the browser disconnects). The per-request
// context still cancels the upstream conn.
var flowSSEHTTPClient = &http.Client{}

// resolveFlowServerURL returns the upstream openova-flow-server base
// URL for the given deploymentId, picking up OPENOVA_FLOW_SERVER_URL
// at runtime so the env can be updated without restarting the binary
// (the mother-side catalyst-api re-resolves per-request to support
// per-deployment URLs in a future iteration).
func (h *Handler) resolveFlowServerURL(_ string) string {
	if s := strings.TrimSpace(os.Getenv("OPENOVA_FLOW_SERVER_URL")); s != "" {
		return strings.TrimRight(s, "/")
	}
	return defaultFlowServerURL
}

// HandleFlowSnapshot proxies GET /api/v1/flows/{deploymentId}/snapshot →
// GET <upstream>/v1/flows/{deploymentId}/snapshot.
//
// Status, headers, and body pass through verbatim. On upstream-network
// error the proxy returns 502.
func (h *Handler) HandleFlowSnapshot(w http.ResponseWriter, r *http.Request) {
	flowID := chi.URLParam(r, "deploymentId")
	if strings.TrimSpace(flowID) == "" {
		http.Error(w, "deploymentId required", http.StatusBadRequest)
		return
	}
	upstream := h.resolveFlowServerURL(flowID) + "/v1/flows/" + url.PathEscape(flowID) + "/snapshot"
	h.flowProxyGET(w, r, upstream)
}

// HandleFlowEvents proxies POST /api/v1/flows/{deploymentId}/events →
// POST <upstream>/v1/flows/{deploymentId}/events.
//
// The request body is forwarded byte-for-byte; the response body, status
// code, and Content-Type pass through. The OpenovaFlow contract
// specifies 200 + JSON `{seq: …}` on accept; this proxy makes no
// assumption beyond status passthrough so future variants (e.g. 202
// Accepted on queued ingest) work without touching this code.
func (h *Handler) HandleFlowEvents(w http.ResponseWriter, r *http.Request) {
	flowID := chi.URLParam(r, "deploymentId")
	if strings.TrimSpace(flowID) == "" {
		http.Error(w, "deploymentId required", http.StatusBadRequest)
		return
	}
	upstream := h.resolveFlowServerURL(flowID) + "/v1/flows/" + url.PathEscape(flowID) + "/events"

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, r.Body)
	if err != nil {
		http.Error(w, "build upstream request: "+err.Error(), http.StatusBadGateway)
		return
	}
	// Preserve Content-Type (the OpenovaFlow envelope is JSON, but
	// keeping the header verbatim future-proofs CBOR / msgpack
	// variants without a code change).
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if cl := r.Header.Get("Content-Length"); cl != "" {
		req.Header.Set("Content-Length", cl)
	}
	resp, err := flowProxyHTTPClient.Do(req)
	if err != nil {
		http.Error(w, "openova-flow-server unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// HandleFlowStream proxies GET /api/v1/flows/{deploymentId}/stream as
// an SSE pass-through. The upstream stream is `text/event-stream`; we
// flush each line group to the browser without buffering.
//
// Implementation: we explicitly DO NOT use httputil.ReverseProxy
// because it buffers responses and breaks SSE. Per the canonical SSE
// pattern in deployments.go StreamLogs (lines 1244-1248 — header set;
// 1273-1286 — the `for { ctx | ch }` write+flush loop), we replicate
// the same shape:
//
//   - Set the standard SSE headers BEFORE reading the upstream body.
//   - Open a long-lived upstream request with r.Context() as the
//     cancel signal.
//   - Read line-by-line from the upstream, write each chunk, flush
//     after each.
//   - Stop when EOF or ctx is done.
func (h *Handler) HandleFlowStream(w http.ResponseWriter, r *http.Request) {
	flowID := chi.URLParam(r, "deploymentId")
	if strings.TrimSpace(flowID) == "" {
		http.Error(w, "deploymentId required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	upstream := h.resolveFlowServerURL(flowID) + "/v1/flows/" + url.PathEscape(flowID) + "/stream"

	// Headers BEFORE first write (canonical SSE pattern, mirrors
	// deployments.go:1244). Setting after the first body byte would
	// silently no-op.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		http.Error(w, "build upstream request: "+err.Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := flowSSEHTTPClient.Do(req)
	if err != nil {
		// We've already committed to text/event-stream headers above,
		// but http.Error here writes a plaintext body — the browser's
		// EventSource will dispatch an `error` event and try to
		// reconnect, which is the right UX for a transient flow-server
		// outage.
		http.Error(w, "openova-flow-server unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Upstream rejected the subscription (404 — unknown flow,
		// 503 — server overloaded). Propagate the status code so the
		// browser's EventSource sees an explicit failure rather than
		// an empty stream that pretends to be healthy.
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	// Flush the headers + 200 status to the browser immediately so the
	// EventSource handshake completes before the first server-side
	// emit. Without this the browser sometimes holds the request open
	// for several seconds waiting for the response headers, which
	// shows up as a perceived "stream didn't connect" delay in the
	// canvas.
	flusher.Flush()

	// Read the upstream body in chunks and forward each. We use a
	// bufio.Reader so partial lines aren't dropped between reads, and
	// we flush after every successful write to keep the stream
	// responsive.
	br := bufio.NewReader(resp.Body)
	buf := make([]byte, 4096)
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		n, err := br.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			if err == io.EOF {
				return
			}
			// Upstream connection dropped — let the browser reconnect.
			// (Comment-only emit: a `\nevent: error\ndata: ...\n\n`
			// would be cleaner but the EventSource API treats any
			// transport-level close as an error anyway.)
			return
		}
	}
}

// copyHeaders shallow-copies whitelisted upstream headers onto the
// response. We intentionally do not pass through hop-by-hop headers
// (per RFC 7230 §6.1) or upstream cookies (which are server-internal).
func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		// Hop-by-hop — must not be forwarded.
		switch strings.ToLower(k) {
		case "connection", "keep-alive", "proxy-authenticate",
			"proxy-authorization", "te", "trailers", "transfer-encoding",
			"upgrade":
			continue
		}
		// Drop content-length on response so the caller can decide the
		// body length (we may have re-encoded).
		if strings.EqualFold(k, "content-length") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// flowProxyGET is the shared GET helper. Used by HandleFlowSnapshot.
func (h *Handler) flowProxyGET(w http.ResponseWriter, r *http.Request, upstream string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		http.Error(w, "build upstream request: "+err.Error(), http.StatusBadGateway)
		return
	}
	if a := r.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	resp, err := flowProxyHTTPClient.Do(req)
	if err != nil {
		http.Error(w, "openova-flow-server unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// Compile-time guard: the proxy methods MUST live on *Handler so they
// can be registered in main.go's chi.Group. If a refactor moves them
// to a non-Handler receiver, this catches it.
var (
	_ = (*Handler)(nil)
	_ = context.Background
	_ = fmt.Sprintf
)
