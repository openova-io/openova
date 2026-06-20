// OpenClaw per-user runtime — identity-blind NewAPI proxy.
//
// Per locked decision [A] of openova-io/openova#795, this binary reads
// ONLY two env vars (NEWAPI_BASE_URL + NEWAPI_KEY). It carries NO
// Keycloak code, NO key-management code, NO Organization-tenant model knowledge.
// Identity flows in through the Secret-mounted env vars; the runtime
// trusts what the controller injected at pod-spawn time.
//
// HTTP surface:
//   - GET  /                       minimal HTML landing
//   - GET  /healthz                200 if env vars are present
//   - GET  /readyz                 200 if NEWAPI_BASE_URL responds
//   - POST /v1/chat/completions    forwards to ${NEWAPI_BASE_URL}/v1/chat/completions
//   - POST /v1/completions         forwards to ${NEWAPI_BASE_URL}/v1/completions
//   - POST /v1/embeddings          forwards to ${NEWAPI_BASE_URL}/v1/embeddings
//   - GET  /v1/models              forwards to ${NEWAPI_BASE_URL}/v1/models
//
// Per Inviolable Principle 4 (never hardcode), the listen port is
// $PORT (default 8080) and the upstream is $NEWAPI_BASE_URL — both
// configurable.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultPort       = "8080"
	upstreamTimeout   = 60 * time.Second
	readyzProbeTimeout = 5 * time.Second
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	baseURL := strings.TrimRight(os.Getenv("NEWAPI_BASE_URL"), "/")
	apiKey := os.Getenv("NEWAPI_KEY")
	if baseURL == "" || apiKey == "" {
		log.Fatalf("FATAL: NEWAPI_BASE_URL and NEWAPI_KEY env vars are required (per locked decision [A] of openova-io/openova#795)")
	}

	upstream, err := url.Parse(baseURL)
	if err != nil {
		log.Fatalf("FATAL: invalid NEWAPI_BASE_URL=%q: %v", baseURL, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/healthz", healthzHandler(baseURL, apiKey))
	mux.HandleFunc("/readyz", readyzHandler(baseURL))
	mux.Handle("/v1/", proxyHandler(upstream, apiKey))

	addr := net.JoinHostPort("", port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("openclaw-runtime listening on %s; upstream=%s", addr, baseURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("FATAL: server error: %v", err)
	}
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html>
<html><head><title>OpenClaw runtime</title></head>
<body style="font-family:system-ui,sans-serif;max-width:640px;margin:2rem auto;color:#222">
<h1>OpenClaw runtime</h1>
<p>Identity-blind NewAPI proxy. Two env vars (NEWAPI_BASE_URL, NEWAPI_KEY) configured.</p>
<p>Endpoints:
  <code>/v1/chat/completions</code>,
  <code>/v1/completions</code>,
  <code>/v1/embeddings</code>,
  <code>/v1/models</code>,
  <code>/healthz</code>,
  <code>/readyz</code>
</p>
</body></html>
`)
}

func healthzHandler(baseURL, apiKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		// Liveness — env vars present.
		if baseURL == "" || apiKey == "" {
			http.Error(w, "missing env", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}
}

func readyzHandler(baseURL string) http.HandlerFunc {
	client := &http.Client{Timeout: readyzProbeTimeout}
	return func(w http.ResponseWriter, _ *http.Request) {
		ctx, cancel := context.WithTimeout(context.Background(), readyzProbeTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
		if err != nil {
			http.Error(w, fmt.Sprintf("readyz: build req: %v", err), http.StatusServiceUnavailable)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("readyz: upstream unreachable: %v", err), http.StatusServiceUnavailable)
			return
		}
		_ = resp.Body.Close()
		// Even 401/403 means the upstream is reachable — readyz only
		// asserts network reachability, not auth (auth is exercised on
		// real requests).
		if resp.StatusCode >= 500 {
			http.Error(w, fmt.Sprintf("readyz: upstream %d", resp.StatusCode), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ready")
	}
}

func proxyHandler(upstream *url.URL, apiKey string) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			// Inject Authorization. The runtime is identity-blind —
			// it forwards every /v1/* request with the per-user
			// NEWAPI_KEY (mounted at startup from the per-user
			// Secret), regardless of who the original caller is. The
			// controller upstream of this pod is responsible for
			// authenticating the Organization end-user via Keycloak before
			// proxying any traffic here.
			pr.Out.Header.Set("Authorization", "Bearer "+apiKey)
			pr.Out.Header.Del("Cookie")
			// Preserve Accept-Encoding etc. as-is.
		},
		ErrorLog: log.New(os.Stderr, "proxy: ", log.LstdFlags),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy: error forwarding %s %s: %v", r.Method, r.URL.Path, err)
			http.Error(w, "upstream proxy error", http.StatusBadGateway)
		},
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: upstreamTimeout,
			IdleConnTimeout:       90 * time.Second,
		},
	}
	return rp
}
