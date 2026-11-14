// Package health serves the two health routes of the operations-records service.
//
//	GET /healthz  liveness: the process is up. Always 200 {"status":"ok","version":"<version>"}.
//	GET /readyz   readiness: every check passes within its time limit.
//	              200 {"status":"ready","checks":{"<name>":"ok"}}
//	              503 {"status":"degraded","checks":{"<name>":"ok"|"<error text>"}}
//
// A check is a named function. The store's Ping is the first one; the program registers it.
// With no check registered, /readyz answers 503: a service that has checked nothing is not
// ready.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// DefaultTimeout is the time limit of one readiness check.
const DefaultTimeout = 3 * time.Second

// Check is one readiness check. It returns nil when the part it checks works.
type Check func(ctx context.Context) error

// Handler holds the version and the readiness checks.
type Handler struct {
	version string
	timeout time.Duration

	mu     sync.RWMutex
	checks map[string]Check
}

// New returns a Handler that reports version on /healthz and gives each check timeout.
// A timeout of zero or less means DefaultTimeout.
func New(version string, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Handler{version: version, timeout: timeout, checks: map[string]Check{}}
}

// Add registers a readiness check under name. A second check with the same name replaces
// the first.
func (h *Handler) Add(name string, c Check) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = c
}

// Register adds the two routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.Healthz)
	mux.HandleFunc("GET /readyz", h.Readyz)
}

// Healthz answers the liveness route.
func (h *Handler) Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": h.version})
}

// Readyz answers the readiness route. The checks run one after the other, in the order of
// their names, each with its own time limit.
func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	names := make([]string, 0, len(h.checks))
	for n := range h.checks {
		names = append(names, n)
	}
	checks := make(map[string]Check, len(h.checks))
	for n, c := range h.checks {
		checks[n] = c
	}
	h.mu.RUnlock()
	sort.Strings(names)

	results := map[string]string{}
	ready := len(names) > 0
	for _, n := range names {
		ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
		err := run(ctx, checks[n])
		cancel()
		if err != nil {
			results[n] = err.Error()
			ready = false
			continue
		}
		results[n] = "ok"
	}
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "degraded", "checks": results})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": results})
}

// run calls c and returns its error, or the context's error when c does not return in time.
func run(ctx context.Context, c Check) error {
	done := make(chan error, 1)
	go func() { done <- c(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
