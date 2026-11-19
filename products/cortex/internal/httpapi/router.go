// Package httpapi is the HTTP interface of the cortex query process.
//
// Handler serves the routes that the Organization's gateway publishes: POST /v1/search and
// GET /v1/sources. The token check wraps the whole router, so it runs before routing: a call
// without an accepted token ends with 401 unauthenticated whatever it addresses, and a route
// handler also refuses a request that reached it without a caller. Health serves the
// cluster-internal paths GET /healthz, GET /readyz and GET /metrics on a listener of its own,
// which is never routed.
//
// One line is logged per call: method, route, status and duration. A request body, a query
// string and a search text are never logged.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/auth"
	"github.com/openova-io/openova/products/cortex/internal/search"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// maxBody bounds the body of a request.
const maxBody = 64 << 10

// Authenticator checks a bearer token; *auth.Checker is one.
type Authenticator interface {
	Check(ctx context.Context, token string) (auth.Caller, error)
}

// Config configures a Server.
type Config struct {
	Auth   Authenticator
	Search *search.Service
	Store  store.Store
	Ready  func(ctx context.Context) error // nil error: the process can serve
	Log    *slog.Logger                    // nil: slog.Default()
}

// Server holds the two handlers.
type Server struct {
	cfg      Config
	mux      *http.ServeMux
	routes   []string
	health   *http.ServeMux
	unrouted []string

	mu    sync.Mutex
	calls map[[2]string]int64 // route, status -> calls
}

type callerKey struct{}

// New returns the server with every route registered.
func New(cfg Config) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), health: http.NewServeMux(), calls: map[[2]string]int64{}}
	s.route("POST /v1/search", s.search)
	s.route("GET /v1/sources", s.sources)
	s.internal("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.internal("GET /readyz", s.ready)
	s.internal("GET /metrics", s.metrics)
	return s
}

// Handler serves the routes behind the token check.
func (s *Server) Handler() http.Handler { return s.authenticate(s.mux) }

// Health serves the cluster-internal paths.
func (s *Server) Health() http.Handler { return s.health }

// Routes returns the patterns that Handler serves.
func (s *Server) Routes() []string { return append([]string(nil), s.routes...) }

// Unrouted returns the patterns that Health serves.
func (s *Server) Unrouted() []string { return append([]string(nil), s.unrouted...) }

// route registers a route of Handler. The handler runs only with the caller the token check found.
func (s *Server) route(pattern string, h func(http.ResponseWriter, *http.Request, auth.Caller)) {
	s.routes = append(s.routes, pattern)
	s.mux.Handle(pattern, s.observe(pattern, func(w http.ResponseWriter, r *http.Request) {
		c, ok := r.Context().Value(callerKey{}).(auth.Caller)
		if !ok || len(c.Principals) == 0 {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		h(w, r, c)
	}))
}

// internal registers a path of Health.
func (s *Server) internal(pattern string, h http.HandlerFunc) {
	s.unrouted = append(s.unrouted, pattern)
	s.health.Handle(pattern, h)
}

// authenticate checks the bearer token of every call before next routes it.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var c auth.Caller
		err := errors.New("no bearer token")
		if ok && token != "" {
			c, err = s.cfg.Auth.Check(r.Context(), token)
		}
		if err != nil {
			s.count("unauthenticated", http.StatusUnauthorized)
			s.cfg.Log.Info("call refused", "method", r.Method, "path", r.URL.Path, "reason", err.Error())
			w.Header().Set("WWW-Authenticate", `Bearer realm="cortex"`)
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// observe counts and logs each call of the route pattern.
func (s *Server) observe(pattern string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		s.count(pattern, rec.status)
		s.cfg.Log.Info("call", "method", r.Method, "route", pattern, "status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) count(route string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[[2]string{route, fmt.Sprint(status)}]++
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req search.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	// One JSON object and nothing after it: a second Decode must meet the end of the body.
	if err := dec.Decode(&req); err != nil || !errors.Is(dec.Decode(new(json.RawMessage)), io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid-request")
		return
	}
	results, err := s.cfg.Search.Search(r.Context(), c.Principals, req)
	switch {
	case errors.Is(err, search.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid-request")
	case errors.Is(err, search.ErrForbidden):
		writeError(w, http.StatusForbidden, "source-forbidden")
	case errors.Is(err, search.ErrUnavailable):
		s.cfg.Log.Warn("search: the gateway failed", "error", err.Error())
		writeError(w, http.StatusServiceUnavailable, "gateway-unavailable")
	case err != nil:
		s.cfg.Log.Error("search failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}

// source is one item of GET /v1/sources.
type source struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	State      string     `json:"state"`
	Documents  int64      `json:"documents"`
	Chunks     int64      `json:"chunks"`
	LastSyncAt *time.Time `json:"lastSyncAt"`
	LastError  *string    `json:"lastError"`
}

func (s *Server) sources(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	list, err := s.cfg.Store.Sources(r.Context(), c.Principals)
	if err != nil {
		s.cfg.Log.Error("sources failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	items := make([]source, 0, len(list))
	for _, x := range list {
		items = append(items, source{x.ID, x.Title, x.State, x.Documents, x.Chunks, x.LastSyncAt, x.LastError})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.cfg.Ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// metrics writes the call counters in the Prometheus text format.
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	lines := make([]string, 0, len(s.calls))
	for k, n := range s.calls {
		lines = append(lines, fmt.Sprintf("cortex_http_requests_total{route=%q,code=%q} %d", k[0], k[1], n))
	}
	s.mu.Unlock()
	sort.Strings(lines)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w, "# HELP cortex_http_requests_total Calls answered, by route and status.\n# TYPE cortex_http_requests_total counter\n%s\n",
		strings.Join(lines, "\n"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
