// Package handler implements the HTTP REST surface of catalyst-catalog.
//
// Endpoints (all under `/api/v1/catalog/...`):
//
//   GET /api/v1/catalog                          — list blueprints visible to caller
//   GET /api/v1/catalog/{name}                   — single blueprint (latest visible version)
//   GET /api/v1/catalog/{name}/versions          — version list with upgrade matrix
//   GET /api/v1/catalog/{name}/versions/{version} — full Blueprint at specific version
//   GET /healthz                                 — liveness/readiness
//
// All blueprint-listing endpoints honor the caller's session: per-Org
// private blueprints are only returned for Orgs the caller has Group
// membership in. See `internal/auth/claims.go`.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/auth"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/cache"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/config"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/source"
)

// Handler aggregates the catalog REST endpoints.
type Handler struct {
	Cfg      config.Config
	Resolver *source.Resolver
	Cache    *cache.LRU
	Logger   *slog.Logger
}

// New returns a Handler.
func New(cfg config.Config, resolver *source.Resolver, c *cache.LRU, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{Cfg: cfg, Resolver: resolver, Cache: c, Logger: logger}
}

// Routes installs the handler on a fresh http.ServeMux. Caller is
// responsible for wrapping it with logging/recovery middleware.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.Healthz)
	mux.HandleFunc("GET /api/v1/catalog", h.List)
	mux.HandleFunc("GET /api/v1/catalog/{name}", h.Get)
	mux.HandleFunc("GET /api/v1/catalog/{name}/versions", h.ListVersions)
	mux.HandleFunc("GET /api/v1/catalog/{name}/versions/{version}", h.GetVersion)
	return mux
}

// errorResponse is the JSON shape for all 4xx/5xx replies.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, errorResponse{Error: errCode, Message: msg})
}

// authenticate extracts and parses Claims from the request. Returns
// (claims, true) when a valid session is present, (nil, false) when
// the caller is anonymous + AnonymousReads is allowed, or writes a
// 401 and returns (nil, false) otherwise.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	tok, err := auth.ExtractFromRequest(r, h.Cfg.SessionCookieName)
	if errors.Is(err, auth.ErrNoSession) {
		if h.Cfg.AnonymousReads {
			return nil, true
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "no session token")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return nil, false
	}
	c, err := auth.ParseClaims(tok)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return nil, false
	}
	return c, true
}

// Healthz reports liveness + cache stats. Always 200.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	hits, misses := uint64(0), uint64(0)
	if h.Cache != nil {
		hits, misses = h.Cache.Stats()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"cache_hits":  hits,
		"cache_misses": misses,
	})
}

// listResponse is the shape returned by GET /api/v1/catalog.
type listResponse struct {
	Items []source.Blueprint `json:"items"`
}

// List handles GET /api/v1/catalog?org=<slug>. The optional ?org=
// query parameter restricts the per-Org private fan-out to a single
// Org (still subject to caller permission).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	visible := auth.VisibleOrgs(claims)
	if filterOrg := strings.TrimSpace(r.URL.Query().Get("org")); filterOrg != "" {
		if !auth.HasOrg(claims, filterOrg) && !h.Cfg.AnonymousReads {
			writeError(w, http.StatusForbidden, "forbidden", "caller has no access to org "+filterOrg)
			return
		}
		visible = []string{filterOrg}
	}
	list, err := h.Resolver.List(r.Context(), visible)
	if err != nil {
		h.Logger.Error("catalog list failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, listResponse{Items: list})
}

// Get handles GET /api/v1/catalog/{name}. Returns the highest-priority
// Blueprint at its declared `spec.version`. The caller's visible Orgs
// are honored when resolving the priority winner.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "blueprint name is required")
		return
	}
	bp, err := h.Resolver.Get(r.Context(), name, "", auth.VisibleOrgs(claims))
	if err != nil {
		if errors.Is(err, source.ErrBlueprintNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such blueprint: "+name)
			return
		}
		h.Logger.Error("catalog get failed", "name", name, "err", err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bp)
}

// versionsResponse is the shape returned by GET /api/v1/catalog/{name}/versions.
//
// Today the resolver exposes a single declared version per source
// (= `spec.version` of the latest blueprint.yaml on `main`). The
// catalog therefore returns N versions where N is the count of distinct
// `spec.version` values across the visible sources for `name`.
//
// `upgradeMatrix[v]` lists the prior versions an install at v can
// upgrade FROM (mirrored from `spec.upgrades.from` on the version v
// blueprint).
type versionsResponse struct {
	Name          string              `json:"name"`
	Versions      []versionEntry      `json:"versions"`
	UpgradeMatrix map[string][]string `json:"upgradeMatrix"`
}

type versionEntry struct {
	Version string `json:"version"`
	Origin  string `json:"origin"`
	Org     string `json:"org,omitempty"`
}

// ListVersions handles GET /api/v1/catalog/{name}/versions.
func (h *Handler) ListVersions(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "blueprint name is required")
		return
	}
	visible := auth.VisibleOrgs(claims)

	out := versionsResponse{
		Name:          name,
		Versions:      []versionEntry{},
		UpgradeMatrix: map[string][]string{},
	}

	// Collect candidates from each source. We do not collapse to the
	// resolver Get because the brief asks for multi-version visibility
	// (each source contributing its own `spec.version`).
	addCandidate := func(bp *source.Blueprint) {
		if bp == nil {
			return
		}
		entry := versionEntry{Version: bp.Version, Origin: bp.Source, Org: bp.Org}
		// De-dupe by (version, origin, org) tuple.
		for _, e := range out.Versions {
			if e.Version == entry.Version && e.Origin == entry.Origin && e.Org == entry.Org {
				return
			}
		}
		out.Versions = append(out.Versions, entry)
		out.UpgradeMatrix[bp.Version] = bp.UpgradeFrom
	}

	for _, org := range visible {
		bp, err := source.NewOrgPrivate(h.Resolver.GC, org, h.Resolver.OrgPrivateRepoSuffix, h.Resolver.Cache).
			Get(r.Context(), name, "")
		if err != nil {
			h.Logger.Error("catalog versions: org-private fetch failed", "org", org, "err", err)
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		addCandidate(bp)
	}
	if bp, err := source.NewSovereign(h.Resolver.GC, h.Resolver.SovereignOrg, h.Resolver.Cache).
		Get(r.Context(), name, ""); err != nil {
		h.Logger.Error("catalog versions: sovereign fetch failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	} else {
		addCandidate(bp)
	}
	if bp, err := source.NewPublic(h.Resolver.GC, h.Resolver.PublicOrg, h.Resolver.Cache).
		Get(r.Context(), name, ""); err != nil {
		h.Logger.Error("catalog versions: public fetch failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	} else {
		addCandidate(bp)
	}

	if len(out.Versions) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no such blueprint: "+name)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetVersion handles GET /api/v1/catalog/{name}/versions/{version}.
// Returns the full Blueprint at the requested version (priority-resolved).
func (h *Handler) GetVersion(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	version := r.PathValue("version")
	if name == "" || version == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name and version required")
		return
	}
	bp, err := h.Resolver.Get(r.Context(), name, version, auth.VisibleOrgs(claims))
	if err != nil {
		if errors.Is(err, source.ErrBlueprintNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such blueprint version: "+name+"@"+version)
			return
		}
		h.Logger.Error("catalog version-get failed", "name", name, "version", version, "err", err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bp)
}
