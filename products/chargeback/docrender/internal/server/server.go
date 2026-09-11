// Package server is the HTTP surface of the document renderer: one render
// endpoint plus the three operational endpoints every Catalyst workload
// serves.
//
//	POST /v1/render   {template, locale, currency, document} -> application/pdf
//	                  (text/html when the caller asks for it)
//	GET  /healthz     liveness  — the process is up
//	GET  /readyz      readiness — templates, locales and the PDF engine work
//	GET  /metrics     Prometheus text exposition
//
// The service is STATELESS: it opens no database, writes no file, and makes
// no outbound call of any kind. Everything it needs is embedded in the
// binary, which is what lets the chart deny all egress but DNS.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/html"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/i18n"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/pdf"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/view"
)

// Defaults.
const (
	DefaultMaxBodyBytes   = 2 << 20 // 2 MiB
	DefaultRenderTimeout  = 10 * time.Second
	maxTokenCompareLength = 512
)

// Options wire the server.
type Options struct {
	// Assets carries locales/ and templates/ — the module's embedded FS in
	// production, a fixture FS in a test.
	Assets fs.FS
	// Token, when set, is the shared secret a caller must present in
	// X-Render-Token. Empty = the header is not required, which is only safe
	// behind the chart's NetworkPolicy (the ONLY admitted caller is the BSS).
	Token string
	// MaxBodyBytes caps the request body. 0 = DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// RenderTimeout bounds one render. 0 = DefaultRenderTimeout.
	RenderTimeout time.Duration
	// Metrics is the registry /metrics exposes. nil = a fresh one.
	Metrics *metrics.Registry
	// Now fixes document timestamps; zero = the wall clock.
	Now func() time.Time
	// Version is reported by /healthz.
	Version string
	// FontFile optionally embeds a TrueType font instead of the core one.
	FontFile string
}

// Server is the handler.
type Server struct {
	bundle    *i18n.Bundle
	templates *html.Set
	opt       Options
	metrics   *metrics.Registry
	maxBody   int64
	timeout   time.Duration
	now       func() time.Time

	// ready is nil once the start-up self-render has succeeded, and carries
	// the reason otherwise. Readiness is a PROVEN capability here, not a
	// liveness restatement: it renders the sample invoice through the same
	// path a request takes.
	ready error
}

// New loads the locales and templates and returns the server.
func New(opt Options) (*Server, error) {
	if opt.Assets == nil {
		return nil, errors.New("Options.Assets is required")
	}
	bundle, err := i18n.Load(opt.Assets)
	if err != nil {
		return nil, err
	}
	set, err := html.Load(opt.Assets)
	if err != nil {
		return nil, err
	}
	s := &Server{
		bundle:    bundle,
		templates: set,
		opt:       opt,
		metrics:   opt.Metrics,
		maxBody:   opt.MaxBodyBytes,
		timeout:   opt.RenderTimeout,
		now:       opt.Now,
	}
	if s.metrics == nil {
		s.metrics = metrics.New()
	}
	if s.maxBody <= 0 {
		s.maxBody = DefaultMaxBodyBytes
	}
	if s.timeout <= 0 {
		s.timeout = DefaultRenderTimeout
	}
	if s.now == nil {
		s.now = time.Now
	}
	// Prove the whole path works before announcing readiness.
	if _, _, err := s.render(context.Background(), document.Sample(), formatPDF); err != nil {
		s.ready = fmt.Errorf("start-up self-render: %w", err)
	}
	return s, s.ready
}

// Handler returns the routed, wrapped http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/render", s.handleRender)
	// The catch-all below matches every path, which would otherwise turn the
	// mux's own method-not-allowed answer into a 404. A GET on the render
	// endpoint is a caller using the wrong verb, and saying so is the
	// difference between a one-line fix and an afternoon.
	mux.HandleFunc("/v1/render", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		writeErr(w, http.StatusMethodNotAllowed, "POST is the only method on /v1/render", nil)
	})
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found", nil)
	})
	return s.wrap(mux)
}

// Metrics is the registry, for tests.
func (s *Server) Metrics() *metrics.Registry { return s.metrics }

// ── middleware ─────────────────────────────────────────────────────────────

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				if sw.bytes == 0 {
					writeErr(sw, http.StatusInternalServerError, "internal error", nil)
				}
			}
			if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && r.URL.Path != "/metrics" {
				slog.Info("http",
					"method", r.Method, "path", r.URL.Path, "status", sw.status,
					"bytes", sw.bytes, "ms", time.Since(start).Milliseconds())
			}
			s.metrics.Inc("docrender_http_requests_total",
				"HTTP requests by path and status.",
				map[string]string{"path": routeLabel(r.URL.Path), "status": fmt.Sprint(sw.status)}, 1)
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(sw, r)
	})
}

// routeLabel keeps the metric cardinality bounded: only the paths this
// service serves become label values.
func routeLabel(path string) string {
	switch path {
	case "/v1/render", "/healthz", "/readyz", "/metrics":
		return path
	}
	return "other"
}

// ── handlers ───────────────────────────────────────────────────────────────

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": s.opt.Version,
		"templates": document.Templates, "locales": s.bundle.Locales(),
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if s.ready != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unready", "reason": s.ready.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.metrics.Write(w); err != nil {
		slog.Warn("write metrics", "error", err)
	}
}

type format int

const (
	formatPDF format = iota
	formatHTML
)

func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	if !s.authorised(r) {
		writeErr(w, http.StatusUnauthorized, "X-Render-Token is missing or wrong", nil)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, _ := strings.Cut(ct, ";"); strings.TrimSpace(mt) != "application/json" {
			writeErr(w, http.StatusUnsupportedMediaType, "body must be application/json", nil)
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("document larger than %d bytes", s.maxBody), nil)
			return
		}
		writeErr(w, http.StatusBadRequest, "could not read the request body", nil)
		return
	}

	var req document.Request
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), nil)
		return
	}
	if err := req.Validate(); err != nil {
		var ve *document.ValidationError
		if errors.As(err, &ve) {
			s.metrics.Inc("docrender_render_errors_total", "Renders refused or failed, by reason.",
				map[string]string{"reason": "invalid"}, 1)
			writeErr(w, http.StatusBadRequest, "the document is not renderable", ve.Problems)
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if _, ok := s.bundle.Catalog(req.Locale); !ok {
		s.metrics.Inc("docrender_render_errors_total", "Renders refused or failed, by reason.",
			map[string]string{"reason": "locale"}, 1)
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("locale %q is not available (have %s)", req.Locale, strings.Join(s.bundle.Locales(), ", ")), nil)
		return
	}

	f := formatPDF
	if wantsHTML(r) {
		f = formatHTML
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	start := time.Now()
	body, filename, err := s.render(ctx, req, f)
	elapsed := time.Since(start).Seconds()
	labels := map[string]string{"template": req.Template, "format": formatName(f)}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		s.metrics.Inc("docrender_render_errors_total", "Renders refused or failed, by reason.",
			map[string]string{"reason": "timeout"}, 1)
		slog.Warn("render timed out", "template", req.Template, "lines", len(req.Document.Lines), "timeout", s.timeout)
		writeErr(w, http.StatusGatewayTimeout,
			fmt.Sprintf("the document did not render within %s", s.timeout), nil)
		return
	case err != nil:
		s.metrics.Inc("docrender_render_errors_total", "Renders refused or failed, by reason.",
			map[string]string{"reason": "render"}, 1)
		slog.Error("render failed", "template", req.Template, "error", err)
		writeErr(w, http.StatusInternalServerError, "the document could not be rendered", nil)
		return
	}

	s.metrics.Inc("docrender_renders_total", "Documents rendered, by template and format.", labels, 1)
	s.metrics.Observe("docrender_render_duration_seconds", "Render duration in seconds.",
		metrics.DefaultBuckets, labels, elapsed)
	s.metrics.Observe("docrender_render_bytes", "Rendered document size in bytes.",
		metrics.SizeBuckets, labels, float64(len(body)))

	if f == formatHTML {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/pdf")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filename))
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	if _, err := w.Write(body); err != nil {
		slog.Warn("write response", "error", err)
	}
	slog.Info("rendered",
		"template", req.Template, "format", formatName(f), "locale", req.Locale,
		"currency", req.Currency, "lines", len(req.Document.Lines),
		"bytes", len(body), "ms", time.Since(start).Milliseconds())
}

// render builds the view model and the requested rendition. The layout
// libraries are CPU-bound and take no context, so the deadline is enforced by
// racing the work against ctx: a caller never waits past the timeout, and the
// abandoned goroutine finishes on its own and is collected.
func (s *Server) render(ctx context.Context, req document.Request, f format) ([]byte, string, error) {
	cat, ok := s.bundle.Catalog(req.Locale)
	if !ok {
		return nil, "", fmt.Errorf("locale %q is not available", req.Locale)
	}
	m, err := view.Build(req, cat)
	if err != nil {
		return nil, "", err
	}

	type result struct {
		body []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		if f == formatHTML {
			b, err := s.templates.Render(m)
			done <- result{b, err}
			return
		}
		b, err := pdf.Render(m, pdf.Options{Now: s.now(), FontFile: s.opt.FontFile})
		done <- result{b, err}
	}()

	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case res := <-done:
		if res.err != nil {
			return nil, "", res.err
		}
		ext := "pdf"
		if f == formatHTML {
			ext = "html"
		}
		return res.body, m.Filename(ext), nil
	}
}

// authorised checks the optional shared secret in constant time.
func (s *Server) authorised(r *http.Request) bool {
	if s.opt.Token == "" {
		return true
	}
	got := r.Header.Get("X-Render-Token")
	if len(got) > maxTokenCompareLength {
		return false
	}
	return constantTimeEqual(got, s.opt.Token)
}

func wantsHTML(r *http.Request) bool {
	if f := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))); f != "" {
		return f == "html"
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "application/pdf") {
		return false
	}
	return strings.Contains(accept, "text/html")
}

func formatName(f format) string {
	if f == formatHTML {
		return "html"
	}
	return "pdf"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write response", "error", err)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string, problems []string) {
	body := map[string]any{"error": msg}
	if len(problems) > 0 {
		body["problems"] = problems
	}
	writeJSON(w, status, body)
}
