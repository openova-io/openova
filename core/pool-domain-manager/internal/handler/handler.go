// Package handler — HTTP surface for pool-domain-manager.
//
// Endpoints (all JSON; per the issue body):
//
//	GET    /api/v1/pool/{domain}/check?sub=X        Fast read; PDM-DB only.
//	POST   /api/v1/pool/{domain}/reserve            Atomic reserve; 10-min TTL.
//	POST   /api/v1/pool/{domain}/commit             Promote → ACTIVE + Dynadot.
//	DELETE /api/v1/pool/{domain}/release            Free; remove Dynadot.
//	GET    /api/v1/pool/{domain}/list               Operator-facing list.
//	GET    /api/v1/reserved                          Public reserved-name list.
//	GET    /healthz                                  Liveness probe.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the handler does not hardcode domain
// names — every value comes from the URL path or request body, validated
// against the runtime DYNADOT_MANAGED_DOMAINS list.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/allocator"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/dynadot"
	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/reserved"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/store"
)

// Handler holds the dependencies shared by every endpoint.
type Handler struct {
	Alloc    *allocator.Allocator
	Store    *store.Store // exposed for /healthz Ping
	Log      *slog.Logger
	Registry registrar.Registry // populated by main via SetRegistry
}

// New constructs a Handler.
func New(alloc *allocator.Allocator, s *store.Store, log *slog.Logger) *Handler {
	return &Handler{Alloc: alloc, Store: s, Log: log}
}

// Routes returns the chi.Router with all PDM routes wired up.
func (h *Handler) Routes() *chi.Mux {
	r := chi.NewRouter()
	r.Get("/healthz", h.Healthz)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/reserved", h.ListReserved)
		r.Route("/pool/{domain}", func(r chi.Router) {
			r.Get("/check", h.Check)
			r.Get("/list", h.List)
			r.Post("/reserve", h.Reserve)
			r.Post("/commit", h.Commit)
			r.Delete("/release", h.Release)
		})
		r.Route("/registrar/{registrar}", func(r chi.Router) {
			r.Post("/set-ns", h.SetNS)
			// /validate is the read-only twin of /set-ns — checks that the
			// supplied token CAN reach the registrar and CAN see the named
			// domain, without flipping any nameservers. The wizard's BYO
			// Flow B uses this before letting the customer hit Continue.
			r.Post("/validate", h.Validate)
		})
	})
	return r
}

// ── Healthz ────────────────────────────────────────────────────────────

// Healthz returns 200 if Postgres is reachable and the dynadot config is
// loaded; otherwise 503. The response includes the runtime managed-domain
// list so operators can grep for misconfiguration.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unhealthy",
			"db":     err.Error(),
		})
		return
	}
	out := map[string]any{
		"status":         "ok",
		"managedDomains": dynadot.ManagedDomains(),
	}
	if h.Registry != nil {
		out["registrars"] = h.Registry.Names()
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Reserved-list ──────────────────────────────────────────────────────

// ListReserved exposes the canonical reserved-subdomain list. The wizard can
// consume this to render an inline hint instead of waiting for the user to
// type a reserved name and seeing an error.
func (h *Handler) ListReserved(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"reserved": reserved.All(),
	})
}

// ── /pool/{domain}/check ───────────────────────────────────────────────

// Check is the read-only availability query. Always returns 200 OK with a
// JSON body — clients use the body's `available` field, not the HTTP
// status, to decide.
func (h *Handler) Check(w http.ResponseWriter, r *http.Request) {
	domain := normaliseLabel(chi.URLParam(r, "domain"))
	sub := normaliseLabel(r.URL.Query().Get("sub"))

	if !isValidDNSLabel(sub) {
		writeJSON(w, http.StatusOK, allocator.CheckResult{
			Available: false,
			Reason:    "invalid-format",
			Detail:    "subdomain must be a-z, 0-9 and hyphens, start with a letter, max 63 characters",
		})
		return
	}

	res, err := h.Alloc.Check(r.Context(), domain, sub)
	if err != nil {
		h.Log.Error("check failed", "domain", domain, "sub", sub, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ── /pool/{domain}/reserve ─────────────────────────────────────────────

// ReserveRequest is the body shape POSTed to /reserve.
type ReserveRequest struct {
	Subdomain string `json:"subdomain"`
	CreatedBy string `json:"createdBy,omitempty"`
}

// ReserveResponse is the wire shape returned to the caller.
type ReserveResponse struct {
	PoolDomain       string `json:"poolDomain"`
	Subdomain        string `json:"subdomain"`
	State            string `json:"state"`
	ReservedAt       string `json:"reservedAt"`
	ExpiresAt        string `json:"expiresAt"`
	ReservationToken string `json:"reservationToken"`
	CreatedBy        string `json:"createdBy"`
}

// Reserve atomically reserves the (domain, subdomain) pair for the
// configured TTL. Returns 201 Created on success, 409 Conflict if the name
// is taken, 422 Unprocessable Entity on validation failure.
func (h *Handler) Reserve(w http.ResponseWriter, r *http.Request) {
	domain := normaliseLabel(chi.URLParam(r, "domain"))

	var req ReserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	sub := normaliseLabel(req.Subdomain)
	if !isValidDNSLabel(sub) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-format",
			"detail": "subdomain must be a-z, 0-9 and hyphens, start with a letter, max 63 characters",
		})
		return
	}

	alloc, err := h.Alloc.Reserve(r.Context(), domain, sub, allocator.ReserveInput{CreatedBy: req.CreatedBy})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "conflict",
				"detail": "this subdomain is already reserved or active",
			})
		case errors.Is(err, dynadot.ErrUnmanagedDomain):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error":  "unsupported-pool",
				"detail": "pool domain " + domain + " is not managed by OpenOva",
			})
		default:
			h.Log.Error("reserve failed", "domain", domain, "sub", sub, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}

	resp := ReserveResponse{
		PoolDomain:       alloc.PoolDomain,
		Subdomain:        alloc.Subdomain,
		State:            string(alloc.State),
		ReservedAt:       alloc.ReservedAt.Format("2006-01-02T15:04:05Z07:00"),
		ReservationToken: alloc.ReservationToken,
		CreatedBy:        alloc.CreatedBy,
	}
	if alloc.ExpiresAt != nil {
		resp.ExpiresAt = alloc.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ── /pool/{domain}/commit ──────────────────────────────────────────────

// CommitRequest carries the data needed to flip RESERVED → ACTIVE.
type CommitRequest struct {
	Subdomain        string `json:"subdomain"`
	ReservationToken string `json:"reservationToken"`
	SovereignFQDN    string `json:"sovereignFQDN"`
	LoadBalancerIP   string `json:"loadBalancerIP"`
}

// Commit promotes a reservation to active and writes Dynadot records.
// Status codes:
//
//	200 OK            — committed; row is active and DNS records exist
//	202 Accepted      — committed in DB but Dynadot write failed (caller
//	                    can retry Commit with same body; idempotent)
//	404 Not Found     — no row exists for this (domain, subdomain)
//	409 Conflict      — row is already active (re-commit attempt)
//	410 Gone          — reservation TTL expired before commit; caller must
//	                    Reserve again
//	403 Forbidden     — reservation token mismatch
func (h *Handler) Commit(w http.ResponseWriter, r *http.Request) {
	domain := normaliseLabel(chi.URLParam(r, "domain"))

	var req CommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	sub := normaliseLabel(req.Subdomain)
	if !isValidDNSLabel(sub) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-format",
			"detail": "subdomain must be a-z, 0-9 and hyphens, start with a letter, max 63 characters",
		})
		return
	}
	if strings.TrimSpace(req.LoadBalancerIP) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-lb-ip",
			"detail": "loadBalancerIP is required for commit",
		})
		return
	}

	alloc, err := h.Alloc.Commit(r.Context(), domain, sub, allocator.CommitInput{
		ReservationToken: req.ReservationToken,
		SovereignFQDN:    req.SovereignFQDN,
		LoadBalancerIP:   req.LoadBalancerIP,
	})
	if err != nil {
		// Allocator returns a wrapped "powerdns write" error AFTER the row
		// was flipped to active. Surface 202 in that case so the caller
		// knows the row is committed but the canonical 6-record set in the
		// child zone is pending — calling Commit again with the same body
		// is idempotent (PowerDNS PATCH replaces existing RRsets in place).
		if alloc != nil && strings.Contains(err.Error(), "powerdns write") {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"warning":        "row committed but PowerDNS write failed; retry commit to publish DNS",
				"detail":         err.Error(),
				"poolDomain":     alloc.PoolDomain,
				"subdomain":      alloc.Subdomain,
				"state":          string(alloc.State),
				"sovereignFQDN":  alloc.SovereignFQDN,
				"loadBalancerIP": alloc.LoadBalancerIP,
			})
			return
		}
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "not-found",
				"detail": "no reservation exists for this (poolDomain, subdomain) — call /reserve first",
			})
		case errors.Is(err, store.ErrConflict):
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "already-active",
				"detail": "this allocation is already active",
			})
		case errors.Is(err, store.ErrTokenMismatch):
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "token-mismatch",
				"detail": "reservation token does not match the held reservation",
			})
		case errors.Is(err, store.ErrExpired):
			writeJSON(w, http.StatusGone, map[string]string{
				"error":  "reservation-expired",
				"detail": "the reservation TTL elapsed before commit; reserve again",
			})
		case errors.Is(err, dynadot.ErrUnmanagedDomain):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error":  "unsupported-pool",
				"detail": "pool domain " + domain + " is not managed by OpenOva",
			})
		default:
			h.Log.Error("commit failed", "domain", domain, "sub", sub, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}

	writeJSON(w, http.StatusOK, allocationResponse(alloc))
}

// ── /pool/{domain}/release ─────────────────────────────────────────────

// ReleaseRequest is the body shape DELETEd to /release. We accept a body
// rather than a query param so the wire shape matches reserve/commit.
type ReleaseRequest struct {
	Subdomain string `json:"subdomain"`
}

// Release deletes the row and removes Dynadot records (when state was
// active). Returns 200 OK on success, 404 if no row, 422 on validation.
//
// We do NOT require the reservation token here — Release is operator-side
// (also invoked by catalyst-api on tofu destroy) and the catalyst-api may
// not still hold the original token by the time the destroy fires.
func (h *Handler) Release(w http.ResponseWriter, r *http.Request) {
	domain := normaliseLabel(chi.URLParam(r, "domain"))

	var req ReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Allow ?sub= query fallback so curl -X DELETE without a body works.
		req.Subdomain = r.URL.Query().Get("sub")
	}
	sub := normaliseLabel(req.Subdomain)
	if !isValidDNSLabel(sub) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-format",
			"detail": "subdomain must be a-z, 0-9 and hyphens, start with a letter, max 63 characters",
		})
		return
	}

	alloc, err := h.Alloc.Release(r.Context(), domain, sub)
	if err != nil {
		// Partial: row deleted but PowerDNS teardown failed (either
		// DeleteZone or RemoveNSDelegation surfaced an error). Operator
		// can re-run Release — both PowerDNS calls are idempotent.
		if alloc != nil && (strings.Contains(err.Error(), "powerdns delete zone") || strings.Contains(err.Error(), "powerdns remove delegation")) {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"warning": "row deleted but PowerDNS teardown failed; re-run release to clean up DNS (idempotent)",
				"detail":  err.Error(),
				"freed":   allocationResponse(alloc),
			})
			return
		}
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "not-found",
				"detail": "no allocation exists for this (poolDomain, subdomain)",
			})
		case errors.Is(err, dynadot.ErrUnmanagedDomain):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error":  "unsupported-pool",
				"detail": "pool domain " + domain + " is not managed by OpenOva",
			})
		default:
			h.Log.Error("release failed", "domain", domain, "sub", sub, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"freed": allocationResponse(alloc),
	})
}

// ── /pool/{domain}/list ────────────────────────────────────────────────

// List returns every allocation for the given pool domain. Operator-only;
// the manifest gates the path behind a Traefik auth middleware.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	domain := normaliseLabel(chi.URLParam(r, "domain"))
	allocs, err := h.Alloc.List(r.Context(), domain)
	if err != nil {
		if errors.Is(err, dynadot.ErrUnmanagedDomain) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error":  "unsupported-pool",
				"detail": "pool domain " + domain + " is not managed by OpenOva",
			})
			return
		}
		h.Log.Error("list failed", "domain", domain, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(allocs))
	for i := range allocs {
		out = append(out, allocationResponse(&allocs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"poolDomain":  domain,
		"allocations": out,
	})
}

// ── helpers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func normaliseLabel(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// isValidDNSLabel validates an RFC 1035 label (lower-case-only, since we
// normalise upstream).
func isValidDNSLabel(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	// Allow domain labels with embedded dots ONLY for the {domain} URL
	// param — those are validated separately via dynadot.IsManagedDomain.
	// For subdomain inputs we require a single label.
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			continue
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
			continue
		case r == '-':
			if i == 0 || i == len(s)-1 {
				return false
			}
			continue
		default:
			return false
		}
	}
	return true
}

func allocationResponse(a *store.Allocation) map[string]any {
	out := map[string]any{
		"poolDomain": a.PoolDomain,
		"subdomain":  a.Subdomain,
		"state":      string(a.State),
		"reservedAt": a.ReservedAt.Format("2006-01-02T15:04:05Z07:00"),
		"createdBy":  a.CreatedBy,
	}
	if a.ExpiresAt != nil {
		out["expiresAt"] = a.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
	}
	if a.SovereignFQDN != "" {
		out["sovereignFQDN"] = a.SovereignFQDN
	}
	if a.LoadBalancerIP != "" {
		out["loadBalancerIP"] = a.LoadBalancerIP
	}
	if a.ReservationToken != "" {
		out["reservationToken"] = a.ReservationToken
	}
	return out
}
