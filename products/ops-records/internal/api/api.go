// Package api serves the console routes of the operations-records service.
//
// The browser never calls this service. catalyst-api verifies the session and forwards the
// request with three headers it sets itself, after removing any the caller sent:
//
//	x-openova-organization  the session's Organization; empty for a sovereign-admin
//	x-openova-user          the session's e-mail
//	x-openova-tier          the session's tier: one of Tiers
//
// The routes listen on an address of their own, which the chart opens to catalyst-api pods
// only. The rules:
//
//   - Agents. A request whose bearer is an agent token (claim typ "agent") is refused with
//     403 agent_token_forbidden on every route, before anything is read or written.
//   - Identity. A request without x-openova-user, or with a tier that is not one of Tiers, is
//     refused with 401 missing_identity. An empty Organization, the view of every
//     Organization, is accepted only with the tier sovereign-admin.
//   - Organization. Every read and every write is filtered by the Organization header when it
//     is not empty: a record of another Organization is answered 404 not_found, exactly as one
//     that does not exist.
//   - Actions. A viewer only reads: an action needs a tier above viewer (403 tier_too_low).
//     An action, its timeline entry and its feedback row (merge, detach, set_severity) are
//     written in one transaction, by the actor kind person named by x-openova-user. An action
//     on a resolved incident, or on one merged into another, is refused with 409
//     invalid_state.
//
// Routes:
//
//	GET  /api/v1/ops/incidents?state=&limit=             {"items": [incident]}, newest first
//	GET  /api/v1/ops/incidents/{id}                      incident with "events" and "signals"
//	POST /api/v1/ops/incidents/{id}/actions              the incident as GET answers it, after the action
//	GET  /api/v1/ops/judgements?judge=&outcome=&limit=   {"items": [judgement]}, newest first
//	GET  /api/v1/ops/stream                              server-sent events, one per incident change
//
// An error is {"error": "<code>", "detail": "<text>"}.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// The identity headers catalyst-api sets.
const (
	HeaderOrganization = "x-openova-organization"
	HeaderUser         = "x-openova-user"
	HeaderTier         = "x-openova-tier"
)

// TierSovereignAdmin is the tier of the view of every Organization.
const TierSovereignAdmin = "sovereign-admin"

// Tiers are the tiers the routes accept, lowest first.
var Tiers = []string{"viewer", "developer", "operator", "admin", "owner", TierSovereignAdmin}

// Server serves the console routes.
type Server struct {
	Store store.Store
	Hub   *Hub             // receives a change after every action; nil sends none
	Now   func() time.Time // nil means time.Now
	Wake  func()           // called after every action, so that pending signals are decided; may be nil
	Log   *slog.Logger     // nil means slog.Default
}

// caller is the identity catalyst-api forwarded.
type caller struct {
	org, user, tier string
	rank            int
}

// Register adds the routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/ops/incidents", s.guard(false, s.listIncidents))
	mux.HandleFunc("GET /api/v1/ops/incidents/{id}", s.guard(false, s.getIncident))
	mux.HandleFunc("POST /api/v1/ops/incidents/{id}/actions", s.guard(true, s.act))
	mux.HandleFunc("GET /api/v1/ops/judgements", s.guard(false, s.listJudgements))
	mux.HandleFunc("GET /api/v1/ops/stream", s.guard(false, s.stream))
}

// IsAgentToken reports whether an Authorization header carries a JWT whose claim typ is
// "agent". The signature is not checked: the answer only ever refuses.
func IsAgentToken(authorization string) bool {
	raw := strings.TrimSpace(authorization)
	if len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var claims struct {
		Typ string `json:"typ"`
	}
	return json.Unmarshal(payload, &claims) == nil && strings.EqualFold(strings.TrimSpace(claims.Typ), "agent")
}

func (s *Server) guard(acts bool, h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if IsAgentToken(r.Header.Get("Authorization")) {
			fail(w, http.StatusForbidden, "agent_token_forbidden", "an agent token cannot use the console routes")
			return
		}
		c := caller{org: strings.TrimSpace(r.Header.Get(HeaderOrganization)),
			user: strings.TrimSpace(r.Header.Get(HeaderUser)), tier: strings.TrimSpace(r.Header.Get(HeaderTier)), rank: -1}
		for i, t := range Tiers {
			if c.tier == t {
				c.rank = i
			}
		}
		switch {
		case c.user == "" || c.rank < 0:
			fail(w, http.StatusUnauthorized, "missing_identity", "the headers "+HeaderUser+" and "+HeaderTier+" are required")
		case c.org == "" && c.tier != TierSovereignAdmin:
			fail(w, http.StatusUnauthorized, "missing_identity", "only a sovereign-admin reads every Organization")
		case acts && c.rank == 0:
			fail(w, http.StatusForbidden, "tier_too_low", "a viewer cannot act on an incident")
		default:
			h(w, r, c)
		}
	}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func fail(w http.ResponseWriter, status int, code, detail string) {
	write(w, status, map[string]string{"error": code, "detail": detail})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// storeFailed answers a store error: 404 for ErrNotFound, else 500 with the error logged.
func (s *Server) storeFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found", "no such record")
		return
	}
	s.log().Error("ops-records: console route: the store failed", "error", err)
	fail(w, http.StatusInternalServerError, "store_failed", "the records cannot be read or written now")
}

// limitOf reads the query parameter limit: empty is 0 (the store's default).
func limitOf(r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 0
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func (s *Server) listIncidents(w http.ResponseWriter, r *http.Request, c caller) {
	state := r.URL.Query().Get("state")
	limit, ok := limitOf(r)
	if !ok || !oneOf(state, "", store.IncidentOpen, store.IncidentMitigated, store.IncidentResolved) {
		fail(w, http.StatusBadRequest, "invalid_request", "state is open, mitigated or resolved; limit is a number")
		return
	}
	rows, err := s.Store.ListIncidents(r.Context(), store.IncidentFilter{Organization: c.org, State: state, Limit: limit})
	if err != nil {
		s.storeFailed(w, err)
		return
	}
	items := make([]Incident, 0, len(rows))
	for _, in := range rows {
		items = append(items, IncidentOf(in))
	}
	write(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getIncident(w http.ResponseWriter, r *http.Request, c caller) {
	d, err := ReadDetail(r.Context(), s.Store, c.org, r.PathValue("id"))
	if err != nil {
		s.storeFailed(w, err)
		return
	}
	write(w, http.StatusOK, d)
}

func (s *Server) listJudgements(w http.ResponseWriter, r *http.Request, c caller) {
	q := r.URL.Query()
	f := store.JudgementFilter{Organization: c.org, Judge: q.Get("judge"), Outcome: q.Get("outcome")}
	if f.Outcome == "undecided" {
		f.Outcome, f.Undecided = "", true
	}
	limit, ok := limitOf(r)
	if !ok || !oneOf(f.Outcome, "", store.OutcomeApplied, store.OutcomeRejectedInvalid, store.OutcomeLateApplied,
		store.OutcomeLateRejected, store.OutcomeTimeout, store.OutcomeUnavailable) {
		fail(w, http.StatusBadRequest, "invalid_request", "outcome is undecided or an outcome of a judgement; limit is a number")
		return
	}
	f.Limit = limit
	rows, err := s.Store.ListJudgements(r.Context(), f)
	if err != nil {
		s.storeFailed(w, err)
		return
	}
	items := make([]Judgement, 0, len(rows))
	for _, j := range rows {
		items = append(items, JudgementOf(j))
	}
	write(w, http.StatusOK, map[string]any{"items": items})
}

// ReadDetail reads one incident with its timeline and its signals, for organization (empty
// reads every Organization).
func ReadDetail(ctx context.Context, r store.Reader, organization, id string) (Detail, error) {
	in, err := r.GetIncident(ctx, organization, id)
	if err != nil {
		return Detail{}, err
	}
	events, err := r.ListIncidentEvents(ctx, in.Organization, in.ID)
	if err != nil {
		return Detail{}, err
	}
	signals, err := r.ListSignals(ctx, store.SignalFilter{Organization: in.Organization, IncidentID: in.ID, Limit: store.MaxLimit})
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Incident: IncidentOf(in), Events: make([]Event, 0, len(events)), Signals: make([]Signal, 0, len(signals))}
	for _, e := range events {
		d.Events = append(d.Events, EventOf(e))
	}
	for _, sg := range signals {
		d.Signals = append(d.Signals, SignalOf(sg))
	}
	return d, nil
}
