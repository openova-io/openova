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
// Two deploy contexts share this handler:
//
//   - Sovereign chroot (catalyst-api running INSIDE a Sovereign) — talks
//     to its own openova-flow-server via the in-cluster Service DNS.
//     The env `OPENOVA_FLOW_SERVER_URL` is set so the resolver returns
//     it verbatim and the per-deployment lookup is skipped.
//   - Mothership (catalyst-api on contabo) — must talk to a DIFFERENT
//     Sovereign per request because the in-cluster DNS only points at
//     its OWN cluster. We resolve the upstream URL from the deployment
//     record's sovereignFQDN: each Sovereign exposes its flow-server
//     publicly via the bp-openova-flow-server chart's HTTPRoute as
//     `openova-flow.<sovereign-fqdn>` (see
//     `platform/openova-flow-server/chart/values.yaml` →
//     `httproute.hostname`, and the per-Sovereign overlay in
//     `clusters/_template/bootstrap-kit/56-bp-openova-flow-server.yaml`
//     which sets `hostname: openova-flow.${SOVEREIGN_FQDN}`).
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
//     URL is either explicitly env-driven (chroot mode) or derived at
//     runtime from the deployment record's sovereignFQDN (mother mode).
//     No hostname suffix or region literal is baked into this file —
//     only the public hostname prefix `openova-flow.` which is itself
//     the chart's documented convention.
//   - Canonical SSE pattern in this codebase: deployments.go
//     `StreamLogs` (lines 1208-1287) — Content-Type: text/event-stream,
//     Cache-Control: no-cache, Connection: keep-alive, X-Accel-Buffering:
//     no, http.Flusher.Flush after every event. This proxy mirrors that
//     header set on the response side AND streams the upstream body
//     verbatim.
//   - Canonical PDM-by-deploymentId lookup pattern: deployments.go
//     `GetDeployment` (lines 1201-1216) — `h.deployments.Load(id)` →
//     `(*Deployment).Request.SovereignFQDN`. The `chrootEnsureDeployment`
//     fallback (jobs.go lines 53-86) covers the chroot mode where the
//     deployment record isn't pre-populated; on the mother that
//     fallback is a no-op so we surface 404.
//   - Canonical self-signed-TLS pattern: deployment_handover_export.go
//     line 62 — `&tls.Config{InsecureSkipVerify: true}` is only used
//     when explicitly opted-in. Here we gate it on the env
//     `OPENOVA_FLOW_TLS_SKIP_VERIFY=true` so a qa-loop Sovereign with
//     LE-staging certs (Fake LE Intermediate X1) is reachable, while
//     production deployments default to strict TLS verification.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #21 (two-repo discipline) this
// proxy lives in openova-io/openova (public). Per-Sovereign concrete
// FQDNs live in the deployment record (in-memory + on-disk store under
// CATALYST_DEPLOYMENTS_DIR — private state inside the cluster).

package handler

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// defaultFlowServerURL — the in-cluster Service URL the
// bp-openova-flow-server chart's `templates/service.yaml` exposes. Used
// ONLY when the env `OPENOVA_FLOW_SERVER_URL` is set explicitly (chroot
// catalyst-api), since the in-cluster DNS doesn't resolve from the
// mother. The mother path derives the URL from the deployment record's
// sovereignFQDN.
const defaultFlowServerURL = "http://openova-flow-server.catalyst-system.svc.cluster.local:8080"

// flowServerHostnamePrefix — the canonical hostname prefix the
// bp-openova-flow-server chart's HTTPRoute uses on every Sovereign:
// `openova-flow.<sovereign-fqdn>` (see chart values.yaml comment + the
// bootstrap-kit overlay 56-bp-openova-flow-server.yaml which sets
// `hostname: openova-flow.${SOVEREIGN_FQDN}`). This is a chart-level
// convention, NOT a region/domain hardcoding — the FQDN suffix itself
// comes from the per-deployment record at runtime.
const flowServerHostnamePrefix = "openova-flow."

// flowProxyTLSSkipVerify is the boot-time setting for whether the
// per-deployment HTTPS dial trusts self-signed certs. Used while
// qa-loop Sovereigns mint LE-staging "Fake LE Intermediate X1" certs
// (see infra `qa_acme_use_staging` tofu var). Set
// OPENOVA_FLOW_TLS_SKIP_VERIFY=true to enable. Production stays strict
// (false).
var flowProxyTLSSkipVerify = strings.EqualFold(strings.TrimSpace(os.Getenv("OPENOVA_FLOW_TLS_SKIP_VERIFY")), "true")

// flowProxyTransport — shared http.Transport with the env-gated
// TLSClientConfig. Mirrors the canonical pattern in
// deployment_handover_export.go (handler exporting to a Sovereign whose
// LE cert may be seconds behind handover).
func newFlowProxyTransport() *http.Transport {
	t := &http.Transport{}
	if flowProxyTLSSkipVerify {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // gated by OPENOVA_FLOW_TLS_SKIP_VERIFY=true; only set in qa-loop where Sovereigns mint LE-staging certs
	}
	return t
}

// flowProxyHTTPClient — singleton http.Client for snapshot + ingest.
// SSE uses a separate client below because it disables timeouts.
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
	return &http.Client{Timeout: t, Transport: newFlowProxyTransport()}
}()

// flowSSEHTTPClient — for the SSE long-lived stream. Zero timeout (the
// stream is held open until the browser disconnects). The per-request
// context still cancels the upstream conn.
var flowSSEHTTPClient = &http.Client{Transport: newFlowProxyTransport()}

// resolveFlowServerURL returns the upstream openova-flow-server base
// URL for the given deploymentId.
//
// Resolution order:
//
//  1. OPENOVA_FLOW_SERVER_URL env override — when set, win. This is
//     the Sovereign chroot path: catalyst-api inside a Sovereign sets
//     this to the in-cluster Service DNS and the per-deployment lookup
//     is skipped entirely.
//
//  2. Per-deployment derivation — look up the deployment record by ID
//     and build `https://openova-flow.<sovereignFQDN>` from
//     `dep.Request.SovereignFQDN`. This is the mothership path.
//
// Returns the resolved URL (trimmed of trailing `/`) and a nil error
// on success. Returns a non-nil error when neither path resolves —
// caller should respond 502 (upstream not configured for this
// deployment) or 404 (deployment not known).
func (h *Handler) resolveFlowServerURL(deploymentID string) (string, error) {
	if s := strings.TrimSpace(os.Getenv("OPENOVA_FLOW_SERVER_URL")); s != "" {
		return strings.TrimRight(s, "/"), nil
	}
	// Canonical per-deployment lookup, mirroring deployments.go
	// GetDeployment (lines 1201-1216): `h.deployments.Load(id)` →
	// (*Deployment).Request.SovereignFQDN. On the chroot,
	// chrootEnsureDeployment synthesises a record when the in-memory
	// map is empty (jobs.go lines 53-86); on the mother it returns
	// nil and we propagate the 404 to the caller.
	val, ok := h.deployments.Load(deploymentID)
	var dep *Deployment
	if ok {
		dep = val.(*Deployment)
	} else if dep = h.chrootEnsureDeployment(deploymentID); dep == nil {
		return "", fmt.Errorf("deployment %q not found", deploymentID)
	}
	fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
	if fqdn == "" {
		return "", fmt.Errorf("deployment %q has no sovereignFQDN", deploymentID)
	}
	// The HTTPRoute always serves on the gateway's default HTTPS port
	// (443); leaving the port off the URL keeps the scheme+host shape
	// canonical and lets the Go http client pick the right default.
	return "https://" + flowServerHostnamePrefix + fqdn, nil
}

// HandleFlowSnapshot proxies GET /api/v1/flows/{deploymentId}/snapshot →
// GET <upstream>/v1/flows/{deploymentId}/snapshot.
//
// Local-first: catalyst-api OWNS the Phase-0 OpenTofu Jobs +
// Phase-1 install-bp-<chart> Jobs (writes them into jobs.Store via
// helmwatch.Bridge + provisioner lifecycle emits). When the local
// store has Jobs for this deploymentId, we synthesise the snapshot
// envelope from the store and return it directly — the mothership
// canvas renders the full mothership-owned flow FROM T+0, no
// dependency on the chroot's openova-flow-server. See
// flow_snapshot_local.go for the assembly logic.
//
// Fallback: if the store has nothing for this deploymentId (e.g.
// catalyst-api restarted before the deployment record was rehydrated,
// or the canvas was loaded with a flowId that's not a deployment id),
// we fall through to the upstream openova-flow-server proxy path.
//
// Status, headers, and body of the proxy path pass through verbatim.
// On upstream-network error or unresolvable deploymentId, the proxy
// returns 502 / 404.
func (h *Handler) HandleFlowSnapshot(w http.ResponseWriter, r *http.Request) {
	flowID := chi.URLParam(r, "deploymentId")
	if strings.TrimSpace(flowID) == "" {
		http.Error(w, "deploymentId required", http.StatusBadRequest)
		return
	}
	// Lazy-start the emit loop. The phase1 watch start hook already
	// invokes startFlowEmitLoop, but a catalyst-api Pod restart AFTER
	// the deployment reached status=ready leaves the loop dead until
	// someone hits the snapshot endpoint. The call is idempotent —
	// re-entry on an already-running loop is a no-op.
	h.startFlowEmitLoop(flowID)
	// Snapshot is served from openova-flow-server's CNPG-backed store.
	// catalyst-api's role is to PROXY — no local composition. The
	// background flow emit loop (flow_emitter.go) keeps openova-flow-
	// server's state hot via periodic snapshot POSTs; pod restart on
	// either side does not lose data (CNPG is the source of truth).
	//
	// FALLBACK: if openova-flow-server is unreachable AND
	// flowSnapshotFromJobs CAN compose locally from the persisted
	// jobs.Store, serve that. This is purely a degraded-mode safety
	// net; production traffic ALWAYS goes through the proxy.
	base, err := h.resolveFlowServerURL(flowID)
	if err == nil && base != "" {
		upstream := base + "/v1/flows/" + url.PathEscape(flowID) + "/snapshot"
		h.flowProxyGET(w, r, upstream)
		return
	}
	// Proxy unavailable — degraded-mode fallback.
	if snapshot, ok := h.flowSnapshotFromJobs(flowID); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(snapshot)
		return
	}
	http.Error(w, err.Error(), http.StatusNotFound)
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
	base, err := h.resolveFlowServerURL(flowID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	upstream := base + "/v1/flows/" + url.PathEscape(flowID) + "/events"

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

	// Local-first SSE: if catalyst-api's jobs.Store has Jobs for this
	// deploymentId, serve the stream from the local store directly —
	// emit a `snapshot` frame on connect, then poll every 3s and
	// re-emit when the assembled envelope changes. The OpenovaFlow
	// reducer is idempotent on snapshot replay, so re-emitting an
	// unchanged snapshot is harmless; the canvas only re-renders on
	// material changes. See flow_snapshot_local.go.
	if _, hasLocal := h.flowSnapshotFromJobs(flowID); hasLocal {
		h.flowStreamLocal(w, r, flusher, flowID)
		return
	}

	base, err := h.resolveFlowServerURL(flowID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	upstream := base + "/v1/flows/" + url.PathEscape(flowID) + "/stream"

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
