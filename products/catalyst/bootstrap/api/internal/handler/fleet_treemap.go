// Package handler — fleet_treemap.go: skeleton mothership-side fleet
// treemap endpoint (TBD-E14).
//
//	GET /api/v1/fleet/treemap[?size_by=apps|deployments|age&color_by=health|age]
//
// Returns a flat list of TreemapItem rows — ONE row per Sovereign in
// the fleet — wire-compatible with the per-Sovereign
// /api/v1/dashboard/treemap shape consumed by the existing recharts
// renderer in products/catalyst/bootstrap/ui/src/pages/sovereign/Dashboard.tsx.
//
// ── Why a separate endpoint ─────────────────────────────────────────
//
// The Sovereign-side `/api/v1/dashboard/treemap` reads Pods/PVCs from
// the in-process k8scache.Factory of ONE Sovereign cluster. The
// mothership (catalyst-zero) does NOT register every Sovereign in
// that cache — each Sovereign owns its own k3s cluster reachable only
// from its own kubeconfig (Cilium-meshed, but the mothership doesn't
// list pods cross-cluster).
//
// For a TRUE fleet-wide treemap the mothership has two viable
// strategies — both deferred behind this skeleton:
//
//	(a) FAN-OUT — for each Sovereign, proxy its /dashboard/treemap
//	    and union the results under a synthetic parent cell per
//	    Sovereign. Heavy: N round-trips per page-load.
//
//	(b) ROLLUP cache — a controller in catalyst-api watches
//	    Application + Pod count summaries from every Sovereign and
//	    surfaces them through a thin local rollup. Lighter at read
//	    time but adds write-path infrastructure.
//
// This handler ships strategy (a)-lite: it reuses the existing
// `collectFleetSovereigns` rollup (already cheap — one PVC walk per
// Sovereign + per-Sov summary closure) to render a single-layer
// "Sovereigns" treemap where:
//
//	• cell AREA  ← apps installed (proxy for resource footprint;
//	   defaults when richer signal is wired)
//	• cell COLOR ← health vocabulary (green | yellow | red |
//	   unknown) mapped to the existing TreemapColorBy='health'
//	   gradient at the renderer layer.
//
// The shape is intentionally identical to the per-Sov treemap items
// so the existing recharts wrapper renders the page with zero new
// component code; the page-level FleetTreemap.tsx caller swaps the
// data source via `getFleetTreemap()` in fleet.api.ts.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   #1 (target-state) — endpoint ships day-one even when the per-Sov
//      data source is sparse; the UI default-renders the empty path
//      explicitly so the page never blanks.
//   #2 (no fixtures) — every cell traces to a real Deployment record;
//      empty fleet → empty array, NOT placeholder rows.
//   #4 (never hardcode) — size_by + color_by tokens flow from
//      treemap.types.ts via the URL.

package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// fleetTreemapItem — wire shape mirroring the per-Sov treemapItem
// (dashboard.go) so the UI's existing TreemapItem TS interface
// consumes both endpoints with no branching.
//
// Kept package-private with json tags identical to the per-Sov
// treemapItem in dashboard.go. Percentage is a pointer so a Sov
// whose health is "unknown" encodes null cleanly (rendered as a
// greyed cell with a tooltip).
type fleetTreemapItem struct {
	ID         *string             `json:"id"`
	Name       string              `json:"name"`
	Count      int                 `json:"count"`
	Percentage *float64            `json:"percentage"`
	SizeValue  float64             `json:"size_value,omitempty"`
	Children   []fleetTreemapItem  `json:"children,omitempty"`
}

type fleetTreemapResponse struct {
	Items      []fleetTreemapItem `json:"items"`
	TotalCount int                `json:"total_count"`
}

// fleetTreemapSizeBy — what drives cell area at the fleet layer.
//
//   • apps          — total Applications installed across the Sov
//                     (default).  Cheap, always-known proxy for
//                     resource footprint at fleet zoom.
//   • age           — days since createdAt; older Sovereigns render
//                     larger so operator attention focuses on drift.
//
// More dimensions (cpu_aggregate / memory_aggregate / orgs) land when
// strategy (b) — the rollup controller — is wired.
var fleetTreemapSizeBy = map[string]struct{}{
	"apps": {},
	"age":  {},
}

// fleetTreemapColorBy — what drives cell color at the fleet layer.
//
//   • health  — vocabulary mapped from fleetSovereignSummary.Health
//               (green=100, yellow=50, red=0). The TS renderer's
//               healthColor() flips so 100→green.
//   • age     — same age normalisation as the per-Sov treemap
//               (AgeNormaliseDays from dashboard.go).
var fleetTreemapColorBy = map[string]struct{}{
	"health": {},
	"age":    {},
}

// fleetTreemapAuthorized — explicit allow-list gate for the fleet
// treemap surface. The fleet view shows EVERY Sovereign on the
// mothership, so we tier-gate to Sovereign-owner / -admin claims
// rather than letting any authenticated user see the full fleet
// (TBD-E14b / #1766).
//
// Returns true when:
//   - claims == nil (test path / Sovereign-mode catalyst-api where
//     RequireSession is a transparent passthrough; the auth
//     middleware is the single source of truth for whether auth was
//     required at all);
//   - Tier is `admin` or `owner` (canonical Sovereign-admin gate);
//   - HasRealmRole holds any of fleetTreemapAllowedRoles — the PIN
//     auth flow mints JWTs that carry `realm_access.roles=[catalyst-owner]`
//     for Sovereign owners but does NOT populate the Tier claim
//     (the realm's `tier` protocolMapper only fires on the federated
//     IdP path, not the operator PIN flow). Wave 30 caught a 401 for
//     legitimate `catalyst-owner` callers on `/api/v1/fleet/treemap`
//     because the handler had no role-based fallback when the Tier
//     claim was empty.
func fleetTreemapAuthorized(claims *auth.Claims) bool {
	if claims == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(claims.Tier)) {
	case "admin", "owner":
		return true
	}
	for _, want := range fleetTreemapAllowedRoles {
		if claims.HasRealmRole(want) {
			return true
		}
	}
	return false
}

// fleetTreemapAllowedRoles — realm roles that grant fleet-treemap
// visibility on the mothership. Mirrors `rbacAssignPrivilegedRoles`
// (rbac_assign.go) PLUS the legacy `mothership-admin` role some
// older realms still mint. Kept package-local so a future tightening
// of fleet visibility doesn't ripple through every privileged-role
// callsite.
var fleetTreemapAllowedRoles = []string{
	"catalyst-owner",
	"catalyst-admin",
	"application-admin",
	"mothership-admin",
}

// HandleFleetTreemap — GET /api/v1/fleet/treemap
//
// Validates query string, then collects the cheap per-Sovereign
// summary (already used by /fleet/sovereigns) and projects into the
// treemap wire shape. Per the FAN-OUT comment above, deeper layers
// (Sovereign → Cluster → Application) land in a follow-up slice that
// proxies each Sov's /dashboard/treemap and unions the children.
//
// Today the response is a flat single-layer list. The UI renders the
// "Sovereigns" layer immediately; clicking a cell deep-links to that
// Sovereign's chroot console (where the existing /dashboard treemap
// already shows the full Region→Cluster→…→Application hierarchy).
//
// Authorisation (TBD-E14b / #1766):
//
//   - RequireSession middleware enforces 401 on missing/invalid
//     session token; this handler only runs for authenticated callers.
//   - fleetTreemapAuthorized() enforces 403 when the caller's claims
//     do NOT include Sovereign-owner / -admin tier OR an equivalent
//     realm role (catalyst-owner, catalyst-admin, application-admin,
//     mothership-admin). Wave 30 caught the regression where the
//     PIN-flow JWT carries `realm_access=[catalyst-owner]` but an
//     empty `tier` claim — the previous wired-only-on-tier gate
//     returned 401 to the front-end fetch, which the SPA then
//     surfaced as "Unauthorised" on /dashboard/treemap.
func (h *Handler) HandleFleetTreemap(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if !fleetTreemapAuthorized(claims) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden",
			"detail": "fleet treemap requires Sovereign-owner / -admin tier or catalyst-owner realm role",
		})
		return
	}

	q := r.URL.Query()

	sizeBy := strings.TrimSpace(q.Get("size_by"))
	if sizeBy == "" {
		sizeBy = "apps"
	}
	if _, ok := fleetTreemapSizeBy[sizeBy]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-size-by",
			"detail": "unsupported size metric: " + sizeBy,
		})
		return
	}

	colorBy := strings.TrimSpace(q.Get("color_by"))
	if colorBy == "" {
		colorBy = "health"
	}
	if _, ok := fleetTreemapColorBy[colorBy]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-color-by",
			"detail": "unsupported color metric: " + colorBy,
		})
		return
	}

	sovs := h.collectFleetSovereigns(r.Context())
	items := make([]fleetTreemapItem, 0, len(sovs))
	now := time.Now()
	for _, s := range sovs {
		id := s.ID
		size := fleetTreemapSizeValue(s, sizeBy)
		pct := fleetTreemapColorValue(s, colorBy, now)
		items = append(items, fleetTreemapItem{
			ID:         &id,
			Name:       fleetTreemapDisplayName(s),
			Count:      1, // 1 Sovereign per row at this layer
			Percentage: pct,
			SizeValue:  size,
		})
	}

	writeJSON(w, http.StatusOK, fleetTreemapResponse{
		Items:      items,
		TotalCount: len(items),
	})
}

// fleetTreemapDisplayName — prefer FQDN, fall back to ID.
// Mirrors the Sovereign-card label in DashboardPage.tsx.
func fleetTreemapDisplayName(s fleetSovereignSummary) string {
	if strings.TrimSpace(s.FQDN) != "" {
		return s.FQDN
	}
	return s.ID
}

// fleetTreemapSizeValue — area metric per size_by token.
//
// Day-one: `apps` defaults to 1 (every Sov contributes at least one
// non-zero cell) until the apps-installed count is wired through the
// fleet summary rollup. `age` returns the day-count since createdAt
// (clamped to a positive integer).
//
// Returning a non-zero floor for `apps` keeps the recharts squarifier
// from collapsing any cell to 0 area — the UI is more useful when
// every Sov is at least visible.
func fleetTreemapSizeValue(s fleetSovereignSummary, sizeBy string) float64 {
	switch sizeBy {
	case "age":
		if s.CreatedAt == "" {
			return 1
		}
		t, err := time.Parse(time.RFC3339, s.CreatedAt)
		if err != nil {
			return 1
		}
		days := time.Since(t).Hours() / 24
		if days < 1 {
			return 1
		}
		return days
	case "apps":
		fallthrough
	default:
		// Day-one floor; replaced by real per-Sov app count once the
		// fleet rollup adds it to fleetSovereignSummary.
		return 1
	}
}

// fleetTreemapColorValue — color percentage per color_by token.
//
// Returns nil when the underlying signal is missing (e.g. unknown
// health, missing createdAt) so the UI renders the cell with the
// neutral "no data" greyed-out variant.
func fleetTreemapColorValue(s fleetSovereignSummary, colorBy string, now time.Time) *float64 {
	switch colorBy {
	case "age":
		if s.CreatedAt == "" {
			return nil
		}
		t, err := time.Parse(time.RFC3339, s.CreatedAt)
		if err != nil {
			return nil
		}
		days := now.Sub(t).Hours() / 24
		pct := (days / AgeNormaliseDays) * 100
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		return &pct
	case "health":
		fallthrough
	default:
		var pct float64
		switch s.Health {
		case "green":
			pct = 100
		case "yellow":
			pct = 50
		case "red":
			pct = 0
		default:
			return nil // unknown → null → greyed cell
		}
		return &pct
	}
}
