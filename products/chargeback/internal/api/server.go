// Package api is the JSON API under /api/v1 plus the ops endpoints and the
// embedded UI. Every handler resolves the session first and passes its Scope
// to the store, so a customer principal can only ever read its own rows.
package api

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/mail"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// VerifyError classifies a failed credential check. Message never carries
// the secret; Code is the gateway/service error code when one was returned.
type VerifyError struct {
	Code         string
	Message      string
	Unauthorized bool
	NotPublished bool
}

func (e *VerifyError) Error() string {
	if e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return e.Message
}

// Verifier performs the activation check against the cloud.
type Verifier interface {
	VerifyProject(ctx context.Context, region, projectID, accessKey, secretKey string) error
}

// StatementHook is notified after a statement is issued (ADR-0014 D6).
// The OpenOva adapter's billing hook implements it; nil = off. A hook
// failure never un-issues the statement — issuing is idempotent, so
// re-POSTing /statements/{id}/issue repeats the (idempotent) hook.
type StatementHook interface {
	StatementIssued(ctx context.Context, st store.Statement, c store.Customer) error
}

// Deps wires the handler.
type Deps struct {
	Store    *store.Store
	Keys     *crypto.Keyring
	Mail     mail.Sender
	Verifier Verifier
	Config   config.Config
	Metrics  *metrics.Registry
	UI       fs.FS
	Now      func() time.Time
	Version  string

	// StatementHook, when set, receives issued statements (ADR-0014 D6).
	StatementHook StatementHook
}

// Handler serves the API.
type Handler struct {
	Deps
}

const (
	sessionCookie = "cb_session"
	sessionTTL    = 24 * time.Hour
	pinTTL        = 10 * time.Minute
	pinThrottle   = 30 * time.Second
	pinMaxTries   = 5
	inviteTTL     = 7 * 24 * time.Hour
)

// New builds the full http.Handler.
func New(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Metrics == nil {
		d.Metrics = metrics.Default
	}
	h := &Handler{Deps: d}
	mux := http.NewServeMux()

	// Ops.
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.HandleFunc("GET /metrics", h.metricsHandler)

	// Auth.
	mux.HandleFunc("POST /api/v1/auth/pin/request", h.pinRequest)
	mux.HandleFunc("POST /api/v1/auth/pin/verify", h.pinVerify)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("GET /api/v1/auth/me", h.me)

	// Customers.
	mux.HandleFunc("GET /api/v1/customers", h.listCustomers)
	mux.HandleFunc("POST /api/v1/customers", h.createCustomer)
	mux.HandleFunc("POST /api/v1/customers/import", h.importCustomers)
	mux.HandleFunc("GET /api/v1/customers/{id}", h.getCustomer)
	mux.HandleFunc("PATCH /api/v1/customers/{id}", h.patchCustomer)
	mux.HandleFunc("POST /api/v1/customers/{id}/invite", h.inviteCustomer)
	mux.HandleFunc("GET /api/v1/customers/{id}/users", h.listUsers)
	mux.HandleFunc("POST /api/v1/customers/{id}/users", h.addUser)
	mux.HandleFunc("DELETE /api/v1/customers/{id}/users/{email}", h.deleteUser)
	mux.HandleFunc("GET /api/v1/customers/{id}/audit", h.customerAudit)

	// Invites (public by token).
	mux.HandleFunc("GET /api/v1/invites/{token}", h.getInvite)
	mux.HandleFunc("POST /api/v1/invites/{token}/activate", h.activateInvite)

	// Sources.
	mux.HandleFunc("GET /api/v1/customers/{id}/sources", h.listSources)
	mux.HandleFunc("POST /api/v1/customers/{id}/sources", h.createSource)
	mux.HandleFunc("POST /api/v1/sources/{id}/credential", h.rotateCredential)
	mux.HandleFunc("POST /api/v1/sources/{id}/verify", h.verifySource)
	mux.HandleFunc("DELETE /api/v1/sources/{id}", h.deleteSource)

	// Usage + inventory.
	mux.HandleFunc("GET /api/v1/customers/{id}/usage", h.customerUsage)
	mux.HandleFunc("GET /api/v1/customers/{id}/inventory", h.customerInventory)

	// Price books.
	mux.HandleFunc("GET /api/v1/pricebooks", h.listPriceBooks)
	mux.HandleFunc("POST /api/v1/pricebooks", h.createPriceBook)
	mux.HandleFunc("GET /api/v1/pricebooks/template.csv", h.priceBookTemplate)
	mux.HandleFunc("GET /api/v1/pricebooks/{id}", h.getPriceBook)
	mux.HandleFunc("PUT /api/v1/pricebooks/{id}", h.updatePriceBook)
	mux.HandleFunc("PUT /api/v1/pricebooks/{id}/items", h.putPriceItems)
	mux.HandleFunc("POST /api/v1/pricebooks/{id}/import", h.importPriceBook)

	// Statements.
	mux.HandleFunc("POST /api/v1/statements/run", h.runStatements)
	mux.HandleFunc("GET /api/v1/statements", h.listAllStatements)
	mux.HandleFunc("GET /api/v1/customers/{id}/statements", h.listCustomerStatements)
	mux.HandleFunc("GET /api/v1/statements/{id}", h.getStatement)
	mux.HandleFunc("POST /api/v1/statements/{id}/issue", h.issueStatement)

	// Operator.
	mux.HandleFunc("GET /api/v1/overview", h.overview)

	// Anything else under /api is 404 JSON; everything else is the UI.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, http.StatusNotFound, "not found") })
	mux.Handle("/", h.uiHandler())

	return h.chain(mux)
}

// chain applies recovery, request logging, security headers and session
// loading around the mux.
func (h *Handler) chain(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := h.Now()
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic in handler", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			r = r.WithContext(h.loadSession(r))
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Metrics.Inc("chargeback_http_requests_total", "API requests by method and status", map[string]string{"method": r.Method, "status": http.StatusText(sw.status)}, 1)
			slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// ---------------------------------------------------------------------------
// sessions in context
// ---------------------------------------------------------------------------

type ctxKey int

const sessionKey ctxKey = 1

func (h *Handler) loadSession(r *http.Request) context.Context {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return r.Context()
	}
	sess, err := h.Store.GetSession(r.Context(), c.Value)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("session lookup", "error", err)
		}
		return r.Context()
	}
	return context.WithValue(r.Context(), sessionKey, sess)
}

// withSession injects a session for tests and internal calls.
func withSession(ctx context.Context, sess store.Session) context.Context {
	return context.WithValue(ctx, sessionKey, sess)
}

func sessionFrom(r *http.Request) (store.Session, bool) {
	s, ok := r.Context().Value(sessionKey).(store.Session)
	return s, ok
}

// requireAuth answers 401 when no session is present.
func (h *Handler) requireAuth(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	s, ok := sessionFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return store.Session{}, false
	}
	return s, true
}

// requireOperator answers 401/403 for non-operators.
func (h *Handler) requireOperator(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return s, false
	}
	if s.Role != store.RoleOperator {
		writeErr(w, http.StatusForbidden, "operator role required")
		return s, false
	}
	return s, true
}

// requireCustomer answers 401/403/404 unless the session may act on the
// customer: operators always; customer principals only on their own customer,
// and only admins for writes. Customers outside the session's scope read as
// 404 so ids of other customers are not confirmed.
func (h *Handler) requireCustomer(w http.ResponseWriter, r *http.Request, customerID string, write bool) (store.Session, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return s, false
	}
	if s.Role == store.RoleOperator {
		return s, true
	}
	if s.CustomerID == nil || *s.CustomerID != customerID {
		writeErr(w, http.StatusNotFound, "not found")
		return s, false
	}
	if write && s.Role != store.RoleCustomerAdmin {
		writeErr(w, http.StatusForbidden, "customer admin role required")
		return s, false
	}
	return s, true
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(h.Config.PublicURL, "https://"),
		Expires:  expires,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(h.Config.PublicURL, "https://"),
		MaxAge:   -1,
	})
}

// audit records a mutation attributed to the session (or "system").
func (h *Handler) audit(r *http.Request, customerID *string, action string, details map[string]any) {
	actor := "system"
	if s, ok := sessionFrom(r); ok {
		actor = s.Email
	}
	if err := h.Store.Audit(r.Context(), customerID, actor, action, details); err != nil {
		slog.Warn("audit write failed", "action", action, "error", err)
	}
}
