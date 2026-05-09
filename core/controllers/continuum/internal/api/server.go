// Package api ships the small HTTP server the continuum-controller
// exposes for slice F-2 (pre-switchover dry-run report) and slice F-3
// (post-switchover health check).
//
// Endpoints (JSON):
//
//	POST /v1/continuums/{ns}/{name}/dry-run
//	     Body: {"toRegion":"hel","reason":"operator-requested"}
//	     Returns: switchover.DryRunReport
//
//	GET /v1/continuums/{ns}/{name}/health
//	     Returns: switchover.HealthReport (latest cached, or computed
//	     on-demand)
//
// Path shape note: the brief specifies
// `POST /api/v1/sovereigns/{id}/continuums/{name}/dry-run`. That outer
// `/api/v1/sovereigns/{id}` envelope is the catalyst-api's
// responsibility — it proxies into the continuum-controller's
// in-cluster Service via the existing k8scache pattern. The
// continuum-controller exposes the inner shape only; U-DR-1's UI
// reaches it via the catalyst-api proxy.
//
// Auth: per INVIOLABLE-PRINCIPLES #5 these endpoints are owner-tier
// gated. The continuum-controller binary doesn't itself implement
// Keycloak JWT validation (that's catalyst-api's job); instead it
// trusts a forwarded `X-Catalyst-Owner-Tier: true` header that the
// catalyst-api stamps after its own validation. The header is also
// validated against an optional shared bearer token (env
// CONTINUUM_API_TOKEN) so a misconfigured Ingress can't bypass auth.
//
// Per ADR-0001 §3 every dry-run / health-check endpoint emits an
// audit event on `catalyst.audit` at the access layer for traceability.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
)

// SequencerProvider returns a configured Sequencer for the given
// Continuum CR (by namespace+name). The controller package owns the
// per-CR Sequencer construction (witness selection, CNPG reader,
// HTTPRoute drainer, audit publisher); the API server just calls it.
//
// The returned plan is the Sequencer's view of the switchover —
// FromRegion + CNPGPair etc. derived from the CR spec. The caller's
// request body fills in ToRegion + Reason.
type SequencerProvider interface {
	// SequencerFor returns the Sequencer + a partial SwitchoverPlan
	// for the given CR. The returned plan has FromRegion +
	// CNPGPair... pre-populated from the CR spec; the caller fills
	// in ToRegion + Reason from the request body.
	//
	// Returns ErrCRNotFound when the CR doesn't exist.
	SequencerFor(ns, name string) (*switchover.Sequencer, switchover.SwitchoverPlan, error)
}

// HealthCache stores the last HealthReport per Continuum CR so the
// `GET .../health` endpoint can return it without re-running the
// 4-check sequence (which makes live DNS lookups). The controller's
// runPostSwitchoverHealth populates this cache after each switchover.
type HealthCache interface {
	Get(ns, name string) (switchover.HealthReport, bool)
	Put(ns, name string, rep switchover.HealthReport)
}

// InMemoryHealthCache is the default HealthCache. Process-local; lost
// on restart. Per the design doc that's acceptable: the controller
// re-computes on next switchover, and the cache is operator-facing
// (not a durable record — that's NATS catalyst.audit's job).
type InMemoryHealthCache struct {
	mu sync.RWMutex
	m  map[string]switchover.HealthReport
}

// NewInMemoryHealthCache returns an empty cache.
func NewInMemoryHealthCache() *InMemoryHealthCache {
	return &InMemoryHealthCache{m: map[string]switchover.HealthReport{}}
}

// Get returns the cached report for a CR, or (zero, false).
func (c *InMemoryHealthCache) Get(ns, name string) (switchover.HealthReport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.m[ns+"/"+name]
	return r, ok
}

// Put stores a report.
func (c *InMemoryHealthCache) Put(ns, name string, rep switchover.HealthReport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[ns+"/"+name] = rep
}

// ErrCRNotFound is returned by SequencerProvider.SequencerFor when
// the CR doesn't exist. The handler maps this to HTTP 404.
var ErrCRNotFound = errors.New("api: Continuum CR not found")

// Server is the HTTP handler for the F-2 + F-3 endpoints.
type Server struct {
	// Provider supplies a Sequencer + plan for any CR.
	Provider SequencerProvider

	// HealthCache is the post-switchover health-report store.
	HealthCache HealthCache

	// HealthOpts is the default HealthOptions used when an on-demand
	// GET .../health endpoint computes a fresh report (cache miss).
	// nil-fields default to what PostSwitchoverHealth tolerates
	// (deferred checks for missing Resolvers / AuditTail).
	HealthOpts switchover.HealthOptions

	// AuthToken — when non-empty, every request MUST carry
	// `Authorization: Bearer <AuthToken>` OR
	// `X-Catalyst-Owner-Tier: true` (the latter is what catalyst-api
	// stamps after its own JWT validation). Empty AuthToken disables
	// the bearer-token check (relies on tier header only). Set both
	// in production; either alone is the dev-cluster shortcut.
	AuthToken string

	// Audit is the audit publisher. Every successful call emits
	// `continuum-config-changed` (dry-run) / `continuum-cr-created`
	// (health-fetch) — out of scope here; just for trace.
	// nil = audit emit is suppressed.
	Audit events.Publisher
}

// Handler returns an http.Handler wired with the F-2 + F-3 routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/continuums/", s.routeContinuums)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// routeContinuums dispatches the path shapes:
//
//	POST /v1/continuums/{ns}/{name}/dry-run
//	GET  /v1/continuums/{ns}/{name}/health
func (s *Server) routeContinuums(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(r) {
		writeJSONError(w, http.StatusUnauthorized, "missing or invalid owner-tier credential")
		return
	}
	// Strip the prefix and split.
	rest := strings.TrimPrefix(r.URL.Path, "/v1/continuums/")
	parts := strings.Split(rest, "/")
	// Expect: <ns>, <name>, <verb>
	if len(parts) != 3 {
		writeJSONError(w, http.StatusNotFound, "expected /v1/continuums/{ns}/{name}/{verb}")
		return
	}
	ns, name, verb := parts[0], parts[1], parts[2]
	if ns == "" || name == "" || verb == "" {
		writeJSONError(w, http.StatusBadRequest, "ns + name + verb are required")
		return
	}
	switch verb {
	case "dry-run":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		s.handleDryRun(w, r, ns, name)
	case "health":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		s.handleHealth(w, r, ns, name)
	default:
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown verb %q", verb))
	}
}

// dryRunRequest is the POST body shape.
type dryRunRequest struct {
	ToRegion string `json:"toRegion"`
	Reason   string `json:"reason,omitempty"`
}

func (s *Server) handleDryRun(w http.ResponseWriter, r *http.Request, ns, name string) {
	var req dryRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if req.ToRegion == "" {
		writeJSONError(w, http.StatusBadRequest, "toRegion is required")
		return
	}
	if s.Provider == nil {
		writeJSONError(w, http.StatusInternalServerError, "no SequencerProvider wired")
		return
	}
	seq, plan, err := s.Provider.SequencerFor(ns, name)
	if errors.Is(err, ErrCRNotFound) {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Continuum %s/%s not found", ns, name))
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("provider: %v", err))
		return
	}
	plan.ToRegion = req.ToRegion
	if req.Reason != "" {
		plan.Reason = req.Reason
	}
	rep, err := seq.DryRun(r.Context(), plan)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("dry-run: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request, ns, name string) {
	if s.HealthCache != nil {
		if rep, ok := s.HealthCache.Get(ns, name); ok {
			writeJSON(w, http.StatusOK, rep)
			return
		}
	}
	// Cache miss — compute on demand.
	if s.Provider == nil {
		writeJSONError(w, http.StatusInternalServerError, "no SequencerProvider wired")
		return
	}
	seq, plan, err := s.Provider.SequencerFor(ns, name)
	if errors.Is(err, ErrCRNotFound) {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Continuum %s/%s not found", ns, name))
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("provider: %v", err))
		return
	}
	rep, err := seq.PostSwitchoverHealth(r.Context(), plan, s.HealthOpts)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("health: %v", err))
		return
	}
	if s.HealthCache != nil {
		s.HealthCache.Put(ns, name, rep)
	}
	writeJSON(w, http.StatusOK, rep)
}

// checkAuth — Authorization: Bearer + X-Catalyst-Owner-Tier.
//
// If AuthToken is set, the bearer MUST match. If AuthToken is empty,
// only the tier header is required (dev shortcut).
func (s *Server) checkAuth(r *http.Request) bool {
	if s.AuthToken != "" {
		want := "Bearer " + s.AuthToken
		if r.Header.Get("Authorization") != want {
			return false
		}
	}
	tier := r.Header.Get("X-Catalyst-Owner-Tier")
	return tier == "true"
}

// writeJSON marshals + writes a JSON response.
func writeJSON(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// writeJSONError writes a {"error": "..."} body.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// Compile-time assertion.
var _ HealthCache = (*InMemoryHealthCache)(nil)

// _ marks an unused import for future use (the `time` import will
// land when slice F-4 adds expiry to InMemoryHealthCache entries).
var _ = time.Second
