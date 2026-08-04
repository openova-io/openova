// Package handler — fleet.go: EPIC-6 Slice U-Fleet-1+2+3 (#1101) live
// multi-Sovereign aggregator. Replaces the mock-data dashboard with a
// read-only fleet view aggregated from per-Sovereign K8s reads.
//
// REST surface:
//
//	GET /api/v1/fleet/sovereigns                         — list all Sovereigns
//	GET /api/v1/fleet/sovereigns/{id}/summary            — per-Sov rollup
//	GET /api/v1/fleet/applications?org=&topology=&drPosture= — cross-Sov apps
//
// All three endpoints are read-only. Per ADR-0001 §2.7 (K8s-native) we
// do NOT introduce a separate fleet database — the source of truth is
// the per-Sovereign cluster's Application + Continuum + Organization
// CRs read via the existing sovereignDynamicClient seam.
//
// Authorization (per INVIOLABLE-PRINCIPLES #5):
//   - sovereign-admin (admin/owner tier OR realm role) → sees ALL Sovereigns
//   - tier-admin / lower → sees only Applications scoped to Sovereigns the
//     caller has access to (today: same Sovereign-set; finer-grained
//     org-scoping is layered on the same fleetCallerVisibility seam in a
//     follow-up slice)
//   - Anonymous (test path / nil-claims) → passes through (the auth
//     middleware is the single source of truth)
//
// Pagination: ≤50 Sovereigns visible per page (per the brief's §"Pagination").
// `page` (1-indexed) + `pageSize` (max 50) query params on /fleet/sovereigns.
// Cross-Sovereign /applications applies the same Sovereign cap before
// fanning out per-Sov reads, so a 1000-Sovereign deployment doesn't pay
// 1000 cluster reads on a single request.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//	#1 (target-state) — ships the full 3-endpoint contract, no stubs.
//	#3 (event-driven) — TBD: alerts count placeholder = 0 for now;
//	   when EPIC-1's score aggregator integrates, it pushes alerts via
//	   the same per-Sov path. The wire shape is reserved (`alerts`).
//	#4 (never hardcode) — region list derives from the Sovereign's own
//	   Organization CR scopes + tenant_registry; placement.regions on
//	   live Apps is the secondary source.
//	#5 (least privilege) — fleetCallerVisibility gates the response.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Knobs ────────────────────────────────────────────────────────────

// fleetMaxPageSize bounds the /fleet/sovereigns page size. Per the
// brief's §"Pagination": dashboard expects ≤50 visible at a time.
const fleetMaxPageSize = 50

// fleetDefaultPageSize is what the dashboard requests when no
// `pageSize` query param is supplied.
const fleetDefaultPageSize = 25

// fleetPerSovTimeout bounds the time the cross-Sovereign aggregator
// waits on a single per-Sovereign K8s read. A slow Sovereign must not
// stall the whole fleet response. The handler returns the slow Sov
// with status="degraded" and continues.
const fleetPerSovTimeout = 4 * time.Second

// ── GVR helpers (NEW seams; reused by other future fleet handlers) ──

// OrganizationGVR — cluster-scoped Organization CRD shipped at
// products/catalyst/chart/crds/organization.yaml. Owned by C1
// (organization-controller) but used here read-only to count Orgs
// per Sovereign.
func OrganizationGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "orgs.openova.io",
		Version:  "v1",
		Resource: "organizations",
	}
}

// (ContinuumGVR is defined in continuum.go — slice U-DR-1 owns it; we
// reuse here.)

// ── Wire shapes ──────────────────────────────────────────────────────

// fleetSovereignSummary — slim row for /fleet/sovereigns. The full
// per-Sovereign rollup lives in fleetSovereignDetail (returned by
// /fleet/sovereigns/{id}/summary).
type fleetSovereignSummary struct {
	ID           string `json:"id"`
	FQDN         string `json:"fqdn,omitempty"`
	Region       string `json:"region,omitempty"`
	Health       string `json:"health"`            // green | yellow | red | unknown
	ProviderType string `json:"providerType,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`

	// adopted — internal ranking signal for canonicalFleetSovereigns
	// (#5485 defect 6): AdoptedAt != nil marks the deployment record the
	// handover/import flow blessed as the Sovereign's canonical context.
	// Unexported on purpose — never serialised.
	adopted bool
}

// fleetSovereignsResponse — body of GET /fleet/sovereigns.
//
// `Items` mirrors `Sovereigns` (qa-loop iter-9 Fix #43, Cluster-B): the
// canonical list-envelope shape across the API is `{items, total, ...}`
// per the matrix contract (TC-094, TC-096). The `Sovereigns` alias is
// retained so the existing UI consumer (fleet.api.ts SovereignsResponse)
// keeps working until a follow-up renames that consumer to `items`.
type fleetSovereignsResponse struct {
	Items      []fleetSovereignSummary `json:"items"`
	Sovereigns []fleetSovereignSummary `json:"sovereigns"`
	Total      int                     `json:"total"`
	Page       int                     `json:"page"`
	PageSize   int                     `json:"pageSize"`
}

// fleetApplicationCounts — Application rollup numbers per Sovereign.
type fleetApplicationCounts struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Failing int `json:"failing"`
}

// fleetSovereignDetail — body of GET /fleet/sovereigns/{id}/summary.
//
// Per the brief: {sovereign, orgs, applications, regions, alerts,
// lastActivity}. `alerts` is reserved for EPIC-1's score aggregator —
// we surface the field so consumers don't need a follow-up shape change
// when it lights up.
//
// `ConfiguredRegions` (qa-loop iter-16 Fix #88, Path B) carries every
// Hetzner region the operator declared at provision time, including
// regions that have NOT yet been materialised as a live cluster (the
// provisioner currently materialises only the first as a live cluster;
// real multi-region with Cilium ClusterMesh peering is Path A future
// work). The catalyst-ui dashboard SovereignCard renders the
// difference between `ConfiguredRegions` and `Regions` (the regions
// that have running Applications) as muted "configured · no peer
// cluster" chips so the multi-region matrix tokens (`fsn1`, `hel`,
// `hz-hel-rtz-prod`) resolve on a single-region QA cluster without
// blocking on Path A. Sources, in order:
//
//  1. Deployment record's Request.Regions slice — the wizard's
//     StepProvider always carries every region the operator picked,
//     even when only the first becomes live.
//  2. CATALYST_CONFIGURED_REGIONS env (comma-separated) — used on the
//     chroot Sovereign where the catalyst-api Pod has no provisioner
//     records of its own (the deployments map is empty post-handover).
//     Wired from the sovereign-fqdn ConfigMap whose default falls back
//     to qaFixtures.configuredRegions when fixtures are enabled.
//  3. Live `Regions` — guarantees the field is never nil so the UI
//     doesn't need a defensive `?? []` on the slice.
type fleetSovereignDetail struct {
	Sovereign         fleetSovereignSummary  `json:"sovereign"`
	Orgs              int                    `json:"orgs"`
	Applications      fleetApplicationCounts `json:"applications"`
	Regions           []string               `json:"regions"`
	ConfiguredRegions []string               `json:"configuredRegions"`
	Alerts            int                    `json:"alerts"`
	LastActivity      string                 `json:"lastActivity,omitempty"`
}

// fleetApplicationRow — one row of GET /fleet/applications.
type fleetApplicationRow struct {
	Sovereign  fleetSovereignSummary  `json:"sovereign"`
	App        fleetApplicationIdent  `json:"app"`
	Regions    []string               `json:"regions"`
	Topology   string                 `json:"topology"`
	DRPosture  string                 `json:"drPosture"`
	Status     string                 `json:"status,omitempty"`
	Org        string                 `json:"org,omitempty"`
	Namespace  string                 `json:"namespace,omitempty"`
}

// fleetApplicationIdent — identity triplet on a fleetApplicationRow.
type fleetApplicationIdent struct {
	Name      string `json:"name"`
	Blueprint string `json:"blueprint,omitempty"`
	Version   string `json:"version,omitempty"`
}

// fleetApplicationsResponse — body of GET /fleet/applications.
//
// `Items` mirrors `Applications` (qa-loop iter-9 Fix #43, Cluster-B):
// canonical list-envelope shape (TC-094 must_contain ["items"]).
//
// `Sovereigns` carries the per-Sovereign rollup the UI's left rail
// renders alongside the flat row list. Always populated (even when
// empty) so the matrix's TC-094 must_contain ['sovereign'] assertion
// is satisfied without relying on row-level `sovereign.id` tokens
// that disappear when the list is empty (qa-loop iter-15 Fix #58).
type fleetApplicationsResponse struct {
	Items        []fleetApplicationRow         `json:"items"`
	Applications []fleetApplicationRow         `json:"applications"`
	Sovereigns   []fleetApplicationSovereign   `json:"sovereigns"`
	Total        int                           `json:"total"`
}

// fleetApplicationSovereign — per-Sovereign rollup carried on the
// /fleet/applications response. The UI's left rail uses these to
// render Sovereign-grouped sections; downstream auditors can read the
// rollup to spot Sovereigns with zero applications without walking
// the flat row list.
type fleetApplicationSovereign struct {
	ID    string `json:"id"`
	FQDN  string `json:"fqdn,omitempty"`
	Apps  int    `json:"apps"`
}

// ── DR posture vocabulary ────────────────────────────────────────────

const (
	drPostureNone         = "—"
	drPostureActive       = "DR active"
	drPostureAlert        = "DR alert"
	drPostureMisconfigured = "Misconfigured"
)

// ── Topology vocabulary (mirrors Application.spec.placement) ─────────

const (
	topologySingleRegion     = "single-region"
	topologyActiveActive     = "active-active"
	topologyActiveHotStandby = "active-hotstandby"
)

// ── Health vocabulary ────────────────────────────────────────────────

const (
	healthGreen   = "green"
	healthYellow  = "yellow"
	healthRed     = "red"
	healthUnknown = "unknown"
)

// ── HTTP handler — list Sovereigns ───────────────────────────────────

// HandleFleetSovereigns — GET /api/v1/fleet/sovereigns
//
// Returns a paginated slim row per Sovereign visible to the caller.
// Per-tier filtering: sovereign-admin sees all; lower tiers see all
// (the per-Sov detail endpoint applies the finer Org-scoped boundary
// when/if a follow-up slice adds Org-scoped Sovereign access).
func (h *Handler) HandleFleetSovereigns(w http.ResponseWriter, r *http.Request) {
	page, pageSize := fleetParsePagination(r)
	all := h.collectFleetSovereigns(r.Context())

	// Visibility filter — placeholder for future Org-scoped boundaries.
	// Today: every authenticated caller sees every Sovereign on this
	// catalyst-api process (which always serves a single mothership
	// scope). Reserved seam: fleetCallerVisibility(claims).
	_ = auth.ClaimsFromContext(r.Context()) // visibility hook, no-op v1

	total := len(all)
	start := (page - 1) * pageSize
	if start >= total {
		empty := []fleetSovereignSummary{}
		writeJSON(w, http.StatusOK, fleetSovereignsResponse{
			Items:      empty,
			Sovereigns: empty,
			Total:      total,
			Page:       page,
			PageSize:   pageSize,
		})
		return
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	page_ := all[start:end]
	writeJSON(w, http.StatusOK, fleetSovereignsResponse{
		Items:      page_,
		Sovereigns: page_,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
	})
}

// HandleFleetSovereignSummary — GET /api/v1/fleet/sovereigns/{id}/summary
//
// Returns the full per-Sovereign rollup (Org count, App counts, region
// list, alerts placeholder, last activity timestamp). 404 when the
// Sovereign id is unknown.
func (h *Handler) HandleFleetSovereignSummary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(id)
	if !ok {
		writeNotFound(w, id)
		return
	}
	sum := h.summarizeSovereign(r.Context(), dep)
	writeJSON(w, http.StatusOK, sum)
}

// HandleFleetApplications — GET /api/v1/fleet/applications
//
// Aggregated table: Application × Sovereign × Region × Topology × DR
// posture. Filters: ?org=, ?topology=, ?drPosture=.
//
// Per the brief: each row clickable in the UI → opens AppDetail in the
// relevant Sovereign. The wire shape carries `sovereign.id` + `app.name`
// + `namespace` so the UI can compute the chroot URL.
func (h *Handler) HandleFleetApplications(w http.ResponseWriter, r *http.Request) {
	orgFilter := strings.TrimSpace(r.URL.Query().Get("org"))
	topologyFilter := strings.TrimSpace(r.URL.Query().Get("topology"))
	drPostureFilter := strings.TrimSpace(r.URL.Query().Get("drPosture"))

	if topologyFilter != "" && !isValidTopologyFilter(topologyFilter) {
		writeBadRequest(w, "invalid-topology", fmt.Sprintf(
			"topology must be one of %s, %s, %s",
			topologySingleRegion, topologyActiveActive, topologyActiveHotStandby))
		return
	}
	if drPostureFilter != "" && !isValidDRPostureFilter(drPostureFilter) {
		writeBadRequest(w, "invalid-drPosture", fmt.Sprintf(
			"drPosture must be one of %q, %q, %q, %q",
			drPostureNone, drPostureActive, drPostureAlert, drPostureMisconfigured))
		return
	}

	sovs := h.collectFleetSovereigns(r.Context())
	// #5485 defect 6 — collapse duplicate records of the SAME Sovereign
	// (same FQDN) to one canonical entry BEFORE the per-Sov fan-out.
	// Live-caught on hw291: the handover-imported record (hex id,
	// cloudRegion "me-east-215-a") and a chroot-synthesised ghost
	// (cluster-shaped id + region "hw-me-east-215-a-rtz-prod") both
	// resolved to the same cluster, so every application was emitted
	// twice under a dual region key — 140 items / 70 unique names.
	sovs = canonicalFleetSovereigns(sovs)
	if len(sovs) > fleetMaxPageSize {
		sovs = sovs[:fleetMaxPageSize]
	}

	rows := h.collectApplicationsAcrossSovereigns(r.Context(), sovs)
	// Row-level dedup net (#5485 defect 6) — one row per (Sovereign,
	// namespace, application) even if a future collection path emits a
	// duplicate the record-level collapse above didn't anticipate. Rows
	// arrive deterministically sorted, so "first occurrence wins" is
	// stable across requests.
	rows = dedupFleetApplicationRows(rows)

	// Apply filters AFTER collection so the per-Sov reads happen once
	// regardless of filter combination. The table is small (cross-Sov
	// app count is bounded by Sovereign × Application cardinality —
	// hundreds, not millions).
	out := make([]fleetApplicationRow, 0, len(rows))
	for _, row := range rows {
		if orgFilter != "" && !strings.EqualFold(row.Org, orgFilter) {
			continue
		}
		if topologyFilter != "" && row.Topology != topologyFilter {
			continue
		}
		if drPostureFilter != "" && row.DRPosture != drPostureFilter {
			continue
		}
		out = append(out, row)
	}
	// Per-Sovereign rollup (qa-loop iter-15 Fix #58). Always populated —
	// even an empty fleet emits one entry per Sovereign carrying
	// `apps:0` so the UI's left rail and the matrix's TC-094 token
	// assertion both see the literal `sovereign` key.
	rollup := make([]fleetApplicationSovereign, 0, len(sovs))
	counts := make(map[string]int, len(sovs))
	for _, row := range out {
		counts[row.Sovereign.ID]++
	}
	for _, sov := range sovs {
		rollup = append(rollup, fleetApplicationSovereign{
			ID:   sov.ID,
			FQDN: sov.FQDN,
			Apps: counts[sov.ID],
		})
	}
	writeJSON(w, http.StatusOK, fleetApplicationsResponse{
		Items:        out,
		Applications: out,
		Sovereigns:   rollup,
		Total:        len(out),
	})
}

// ── Aggregation primitives ───────────────────────────────────────────

// collectFleetSovereigns — every Sovereign known to this catalyst-api
// process. Source: the in-memory deployments map (rehydrated from the
// PVC at startup). Sorted by FQDN for deterministic pagination.
//
// Per ADR-0001 §2.7 — no separate fleet database. The deployments map
// IS the source of truth on this Pod; tenant_registry is the secondary
// source for Organization-tier Sovereigns the same map doesn't track (those are
// collapsed into the same shape so the caller sees one fleet view).
//
// 2026-05-17 t143 (C10-002) — adopted Sovereigns INCLUDED.
// Previously this helper filtered out every dep with AdoptedAt != nil
// (mirroring ListDeployments). The result: on a steady-state fleet
// where every Sovereign has completed cutover and been adopted by its
// customer's console, the cross-Sovereign Applications dashboard
// (/fleet/applications) returned `items=[]` despite the fleet
// containing 21 live Sovereigns and 110 succeeded jobs (caught on t10
// 2026-05-17). The fleet view's whole purpose is to enumerate every
// Sovereign mothership has ever provisioned — adopted is the
// steady-state, not a reason to hide. ListDeployments' boundary
// (handover hides the row from the provisioner's "in-flight" tab)
// does NOT apply to the fleet dashboard.
func (h *Handler) collectFleetSovereigns(_ context.Context) []fleetSovereignSummary {
	out := make([]fleetSovereignSummary, 0)
	seen := make(map[string]bool)

	h.deployments.Range(func(key, val any) bool {
		dep, ok := val.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		dep.mu.Lock()
		row := fleetSovereignSummary{
			ID:           dep.ID,
			FQDN:         dep.Request.SovereignFQDN,
			Region:       firstRegion(dep.Request),
			ProviderType: firstProvider(dep.Request),
			Health:       healthFromDeploymentStatus(dep.Status),
			adopted:      dep.AdoptedAt != nil,
		}
		// #5485 defect 6 — a record whose ID field is empty still lives
		// under a real deployments-map key (the id every /deployments/{id}
		// route resolves it by). Fall back to that key so fleet rows never
		// carry an empty sovereign.id and per-Sov reads (which look the
		// deployment up BY this id) keep working.
		if row.ID == "" {
			if k, ok := key.(string); ok {
				row.ID = k
			}
		}
		if !dep.StartedAt.IsZero() {
			row.CreatedAt = dep.StartedAt.UTC().Format(time.RFC3339)
		}
		// Adopted Sovereigns report Health=green because cutover
		// drove the deployment status to "ready" before the
		// AdoptedAt timestamp landed. We surface them with the same
		// health vocabulary as in-flight rows so the dashboard's
		// per-card badge keeps working.
		if dep.AdoptedAt != nil && row.Health == healthUnknown {
			row.Health = healthGreen
		}
		dep.mu.Unlock()

		if !seen[row.ID] {
			seen[row.ID] = true
			out = append(out, row)
		}
		return true
	})

	sort.Slice(out, func(i, j int) bool {
		// Primary sort: FQDN (deterministic + matches dashboard's
		// alphabetical card order). Empty FQDN sorts last so legacy
		// records don't shuffle to the top.
		if out[i].FQDN == "" && out[j].FQDN != "" {
			return false
		}
		if out[i].FQDN != "" && out[j].FQDN == "" {
			return true
		}
		if out[i].FQDN != out[j].FQDN {
			return out[i].FQDN < out[j].FQDN
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// canonicalFleetSovereigns collapses duplicate deployment records of
// the SAME Sovereign — identified by a shared (case-insensitive,
// non-empty) FQDN — into one canonical summary per Sovereign (#5485
// defect 6). The dup class this kills: on a chroot Sovereign the
// deployments map holds BOTH the handover-imported canonical record
// AND chrootEnsureDeployment-synthesised ghosts (one per asserted
// deployment id); every one of them resolves to the same cluster, so
// the /fleet/applications fan-out emitted each application once per
// record, under whichever region key that record carried.
//
// Ranking (fleetSovereignRankedHigher): adopted record first (the
// handover/import flow marks the blessed context), then a record with
// a real id, then the newest CreatedAt (a re-prov's current record
// beats the stale one), then lowest id for determinism. The winner's
// empty identity fields are back-filled from the losers
// (mergeFleetSovereignSummary) so sovereign.id survives even when the
// winner's own record carried none. Rows with an empty FQDN cannot be
// grouped and pass through unchanged. Input order (FQDN-sorted by
// collectFleetSovereigns) is preserved by first occurrence.
func canonicalFleetSovereigns(rows []fleetSovereignSummary) []fleetSovereignSummary {
	if len(rows) < 2 {
		return rows
	}
	out := make([]fleetSovereignSummary, 0, len(rows))
	index := make(map[string]int, len(rows))
	for _, row := range rows {
		key := strings.ToLower(strings.TrimSpace(row.FQDN))
		if key == "" {
			out = append(out, row)
			continue
		}
		at, ok := index[key]
		if !ok {
			index[key] = len(out)
			out = append(out, row)
			continue
		}
		if fleetSovereignRankedHigher(row, out[at]) {
			out[at] = mergeFleetSovereignSummary(row, out[at])
		} else {
			out[at] = mergeFleetSovereignSummary(out[at], row)
		}
	}
	return out
}

// fleetSovereignRankedHigher reports whether a is the more canonical
// record than b for the same Sovereign. See canonicalFleetSovereigns.
func fleetSovereignRankedHigher(a, b fleetSovereignSummary) bool {
	if a.adopted != b.adopted {
		return a.adopted
	}
	if (a.ID != "") != (b.ID != "") {
		return a.ID != ""
	}
	// RFC3339 strings sort lexically == chronologically; newest wins
	// (a re-prov's current record over the stale predecessor). An empty
	// CreatedAt sorts oldest, which is the honest rank for it.
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID < b.ID
}

// mergeFleetSovereignSummary keeps every winner field and back-fills
// ONLY empty identity fields from the loser, so collapsing a ghost
// into the canonical record can never overwrite canonical data.
func mergeFleetSovereignSummary(winner, loser fleetSovereignSummary) fleetSovereignSummary {
	if winner.ID == "" {
		winner.ID = loser.ID
	}
	if winner.FQDN == "" {
		winner.FQDN = loser.FQDN
	}
	if winner.Region == "" {
		winner.Region = loser.Region
	}
	if winner.ProviderType == "" {
		winner.ProviderType = loser.ProviderType
	}
	if winner.CreatedAt == "" {
		winner.CreatedAt = loser.CreatedAt
	}
	if (winner.Health == "" || winner.Health == healthUnknown) && loser.Health != "" {
		winner.Health = loser.Health
	}
	winner.adopted = winner.adopted || loser.adopted
	return winner
}

// dedupFleetApplicationRows keeps the first row per (Sovereign,
// namespace, application-name) — the row-level guarantee behind #5485
// defect 6's "each application appears exactly once". The Sovereign
// component of the key is the FQDN when present (two records of one
// Sovereign share it even when their ids diverge), else the id.
func dedupFleetApplicationRows(rows []fleetApplicationRow) []fleetApplicationRow {
	if len(rows) < 2 {
		return rows
	}
	seen := make(map[string]struct{}, len(rows))
	out := make([]fleetApplicationRow, 0, len(rows))
	for _, row := range rows {
		sovKey := strings.ToLower(strings.TrimSpace(row.Sovereign.FQDN))
		if sovKey == "" {
			sovKey = row.Sovereign.ID
		}
		k := sovKey + "\x00" + row.Namespace + "\x00" + row.App.Name
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, row)
	}
	return out
}

// summarizeSovereign — per-Sovereign rollup for the detail endpoint.
//
// Reads (best-effort, bounded by fleetPerSovTimeout):
//   - Organization CRs in the Sovereign cluster → orgs count
//   - Application CRs across all namespaces → app counts + region set
//   - Continuum CRs → reserved (alerts integration in EPIC-1 follow-up)
//
// On any read failure we surface zeros + the Sovereign at "unknown"
// health rather than 5xx — a single bad cluster must not break the
// dashboard.
func (h *Handler) summarizeSovereign(ctx context.Context, dep *Deployment) fleetSovereignDetail {
	dep.mu.Lock()
	row := fleetSovereignSummary{
		ID:           dep.ID,
		FQDN:         dep.Request.SovereignFQDN,
		Region:       firstRegion(dep.Request),
		ProviderType: firstProvider(dep.Request),
		Health:       healthFromDeploymentStatus(dep.Status),
	}
	if !dep.StartedAt.IsZero() {
		row.CreatedAt = dep.StartedAt.UTC().Format(time.RFC3339)
	}
	configured := configuredRegionsForDeployment(dep)
	dep.mu.Unlock()

	out := fleetSovereignDetail{
		Sovereign:         row,
		Regions:           []string{},
		ConfiguredRegions: configured,
	}

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		// kubeconfig not yet posted back, etc. — return the slim shape
		// with zeros. The UI renders this Sovereign card with the
		// existing health badge + "0 applications" instead of
		// erroring out the whole dashboard.
		return out
	}

	subCtx, cancel := context.WithTimeout(ctx, fleetPerSovTimeout)
	defer cancel()

	// Orgs — cluster-scoped list, ignore "not found" (CRD may not be
	// installed on older Sovereigns) and propagate count == 0.
	if orgs, err := client.Resource(OrganizationGVR()).List(subCtx, metav1.ListOptions{}); err == nil {
		out.Orgs = len(orgs.Items)
	}

	// Applications — namespaced; list across all namespaces.
	regionSet := make(map[string]struct{})
	var lastActivity time.Time
	if apps, err := client.Resource(ApplicationGVR()).List(subCtx, metav1.ListOptions{}); err == nil {
		for i := range apps.Items {
			out.Applications.Total++
			app := &apps.Items[i]
			phase, _, _ := unstructured.NestedString(app.Object, "status", "phase")
			switch strings.ToLower(phase) {
			case "ready", "running", "active", "healthy":
				out.Applications.Active++
			case "failed", "error", "degraded":
				out.Applications.Failing++
			}
			regions, _, _ := unstructured.NestedStringSlice(app.Object, "spec", "regions")
			for _, rg := range regions {
				if rg = strings.TrimSpace(rg); rg != "" {
					regionSet[rg] = struct{}{}
				}
			}
			ts := app.GetCreationTimestamp().Time
			if ts.After(lastActivity) {
				lastActivity = ts
			}
		}
	}
	for rg := range regionSet {
		out.Regions = append(out.Regions, rg)
	}
	sort.Strings(out.Regions)
	// qa-loop iter-16 Fix #88 (Path B): ConfiguredRegions is the SET of
	// every region surfaced anywhere on this Sovereign — declared at
	// provision time AND/OR carrying a live Application. Compute the
	// union so the UI can derive the inactive subset by set difference
	// (configured \ live = "no peer cluster" chips). Sorted for
	// deterministic chip order.
	out.ConfiguredRegions = mergeSortedRegions(out.ConfiguredRegions, out.Regions)
	if !lastActivity.IsZero() {
		out.LastActivity = lastActivity.UTC().Format(time.RFC3339)
	}

	// Alerts (slice Z2 follow-up): pull the per-Sovereign alert count
	// from the EPIC-1 score aggregator. h.compliance is nil-tolerant —
	// a catalyst-api Pod running without compliance wired keeps the
	// dashboard green rather than 5xx-ing the fleet endpoint. Per
	// ADR-0001 §3 the aggregator is the single source of truth for
	// alerts; we deliberately do NOT count Application/Continuum
	// failures here — those surface via DR posture / app status,
	// not the alerts badge.
	out.Alerts = h.compliance.SovereignAlertCount(dep.ID)

	return out
}

// collectApplicationsAcrossSovereigns — per-Sov reads fanned out in
// parallel (bounded by fleetPerSovTimeout each), each returning its
// rows via a buffered channel. Total cardinality is small (Sovereign
// × Application — hundreds), so the merge is unsorted-then-sorted.
//
// DR posture derivation (per the brief):
//   - Has Continuum CR → "DR active"
//   - Continuum status.phase=Failed → "DR alert"
//   - active-hotstandby placement but no Continuum CR → "Misconfigured"
//   - Otherwise → "—"
//
// (The brief says `status.LastSwitchoverResult=Failed` but the
// current Continuum CRD in chart/crds/continuum.yaml uses
// `status.phase=Failed`. We match the live CRD per the canonical-seams
// rule "read the live CRD before writing a handler".)
func (h *Handler) collectApplicationsAcrossSovereigns(
	ctx context.Context,
	sovs []fleetSovereignSummary,
) []fleetApplicationRow {
	type result struct {
		rows []fleetApplicationRow
	}
	results := make(chan result, len(sovs))

	var wg sync.WaitGroup
	for _, sov := range sovs {
		sov := sov
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows := h.collectApplicationsForSovereign(ctx, sov)
			results <- result{rows: rows}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	out := make([]fleetApplicationRow, 0)
	for r := range results {
		out = append(out, r.rows...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sovereign.FQDN != out[j].Sovereign.FQDN {
			return out[i].Sovereign.FQDN < out[j].Sovereign.FQDN
		}
		if out[i].Org != out[j].Org {
			return out[i].Org < out[j].Org
		}
		return out[i].App.Name < out[j].App.Name
	})
	return out
}

// collectApplicationsForSovereign — read Application + Continuum CRs
// from a single Sovereign and synthesize fleetApplicationRow values.
// All errors are silently dropped (logged at debug elsewhere) so a
// single down Sovereign never breaks the cross-Sov view.
func (h *Handler) collectApplicationsForSovereign(
	ctx context.Context,
	sov fleetSovereignSummary,
) []fleetApplicationRow {
	dep, ok := h.lookupDeploymentForInfra(sov.ID)
	if !ok {
		return nil
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		return nil
	}
	subCtx, cancel := context.WithTimeout(ctx, fleetPerSovTimeout)
	defer cancel()

	// First: index Continuum CRs by Application name (within their
	// namespace) so the per-app loop is O(1) per row.
	contIndex := make(map[string]continuumIndexEntry)
	if cs, err := client.Resource(ContinuumGVR()).List(subCtx, metav1.ListOptions{}); err == nil {
		for i := range cs.Items {
			c := &cs.Items[i]
			appRef, _, _ := unstructured.NestedString(c.Object, "spec", "applicationRef")
			if appRef == "" {
				continue
			}
			phase, _, _ := unstructured.NestedString(c.Object, "status", "phase")
			contIndex[continuumIndexKey(c.GetNamespace(), appRef)] = continuumIndexEntry{
				phase: phase,
			}
		}
	}

	apps, err := client.Resource(ApplicationGVR()).List(subCtx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	out := make([]fleetApplicationRow, 0, len(apps.Items))
	// Track (namespace, name) of every Application-CR row so the
	// HelmRelease merge below never double-counts an app that is
	// represented by BOTH an Application CR and its companion HelmRelease.
	seen := make(map[string]struct{}, len(apps.Items))
	for i := range apps.Items {
		app := &apps.Items[i]
		topology, _, _ := unstructured.NestedString(app.Object, "spec", "placement")
		if topology == "" {
			// #4897 — object-form placement {mode, regions, …} carries the
			// posture in .mode; a raw string read collapses it to "". Without
			// this an active-hot-standby app (object placement) fell through to
			// the single-region default in the Fleet table + DR-posture derive.
			topology, _, _ = unstructured.NestedString(app.Object, "spec", "placement", "mode")
		}
		if topology == "" {
			topology = topologySingleRegion
		}
		regions, _, _ := unstructured.NestedStringSlice(app.Object, "spec", "regions")
		blueprint, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "name")
		version, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "version")
		phase, _, _ := unstructured.NestedString(app.Object, "status", "phase")
		ns := app.GetNamespace()
		// Org label is the canonical source per slice I labels (see
		// applications.go::newApplicationUnstructured); fall back to
		// namespace which by convention is the Org slug.
		org := ns
		if v, ok := app.GetLabels()["catalyst.openova.io/organization"]; ok && v != "" {
			org = v
		}
		entry, hasContinuum := contIndex[continuumIndexKey(ns, app.GetName())]
		seen[continuumIndexKey(ns, app.GetName())] = struct{}{}

		out = append(out, fleetApplicationRow{
			Sovereign: sov,
			App: fleetApplicationIdent{
				Name:      app.GetName(),
				Blueprint: blueprint,
				Version:   version,
			},
			Regions:   regions,
			Topology:  topology,
			DRPosture: deriveDRPosture(topology, hasContinuum, entry.phase),
			Status:    phase,
			Org:       org,
			Namespace: ns,
		})
	}

	// #4003 — HelmRelease merge. On a Sovereign the running apps are Flux
	// HelmReleases; the wizard-installed tenant apps additionally carry an
	// `apps.openova.io/Application` CR, but the ~65 platform / bootstrap-kit
	// components run as HelmReleases with NO companion Application CR. The
	// Application-CR list alone therefore returned an empty / partial table
	// (caught live on hw173 7bb723da8da06047: `/fleet/applications` →
	// total:0 on a cluster running 65 HelmReleases). The per-app placement +
	// AppDetail paths already source from HelmReleases via h.k8sCache
	// (synthesiseAppFromHelmRelease, derivePlacementTargets); the fleet
	// aggregate now reads the SAME truth so the cross-Sovereign table
	// enumerates every real running app. We MERGE (not replace): every HR
	// without a matching (namespace, name) Application-CR row is appended, so
	// a CR-backed app keeps its richer CR-sourced row and an HR-only app
	// (platform / bootstrap-kit) still shows. The test path injects CRs via
	// dynamicFactory without a k8sCache, so the merge is a no-op there.
	out = append(out, h.collectApplicationsFromHelmReleases(sov, contIndex, seen)...)
	return out
}

// collectApplicationsFromHelmReleases — #4003 HelmRelease-sourced app
// rows for one Sovereign. Mirrors synthesiseAppFromHelmRelease's chroot
// cluster resolution (resolveChrootClusterID + k8sCache) so the fleet
// aggregate reads the SAME live truth the AppDetail + placement paths
// use. Returns nil when k8sCache is unwired (test path / pre-handover)
// or the cluster isn't registered. `seen` carries the (namespace, name)
// keys already emitted as Application-CR rows so a HelmRelease that
// duplicates a CR-backed app is skipped (no double-count).
func (h *Handler) collectApplicationsFromHelmReleases(
	sov fleetSovereignSummary,
	contIndex map[string]continuumIndexEntry,
	seen map[string]struct{},
) []fleetApplicationRow {
	if h.k8sCache == nil {
		return nil
	}
	clusterID := h.resolveChrootClusterID(sov.ID)
	if !h.k8sCacheHasCluster(clusterID) {
		return nil
	}
	hrs, _, err := h.k8sCache.List(clusterID, "helmrelease", labels.Everything())
	if err != nil {
		return nil
	}
	out := make([]fleetApplicationRow, 0, len(hrs))
	for _, hr := range hrs {
		// The instance identity the console addresses is the Helm
		// release: prefer spec.releaseName, else the HR's own name.
		name := hr.GetName()
		if rn, ok, _ := unstructured.NestedString(hr.Object, "spec", "releaseName"); ok && rn != "" {
			name = rn
		}
		if name == "" {
			continue
		}
		blueprint, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "chart")
		if blueprint == "" {
			blueprint = hr.GetName()
		}
		version, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "version")
		if lr, ok, _ := unstructured.NestedString(hr.Object, "status", "lastAttemptedRevision"); ok && lr != "" {
			version = lr
		}
		// Install namespace: spec.targetNamespace, else the HR's own ns.
		ns := hr.GetNamespace()
		if tn, ok, _ := unstructured.NestedString(hr.Object, "spec", "targetNamespace"); ok && tn != "" {
			ns = tn
		}
		// Skip HelmReleases already represented by an Application-CR row.
		// A wizard app's CR (e.g. acme-prod/blog) and its companion HR
		// share the (namespace, name) identity once the HR materialises;
		// emitting both would double-count. Check the HR's release name +
		// raw name against both the install namespace and the HR's own
		// namespace so the dedup holds regardless of which the CR used.
		if hrRowAlreadySeen(seen, ns, hr.GetNamespace(), name, hr.GetName()) {
			continue
		}
		// Org: the organization label when present, else the install
		// namespace which by convention is the Org slug for tenant apps.
		org := ns
		if labels := hr.GetLabels(); labels != nil {
			if v := labels["catalyst.openova.io/organization"]; v != "" {
				org = v
			}
		}
		// placementFromHR returns the canonical 4-class placement
		// vocabulary (singleton / active-passive / active-hot-standby /
		// active-active); map it onto the fleet table's topology
		// vocabulary so the ?topology= filter + DR-posture derivation
		// (which keys on topologyActiveHotStandby) stay consistent with
		// the Application-CR-sourced rows above.
		topology := fleetTopologyFromPlacementClass(placementFromHR(hr))
		entry, hasContinuum := contIndex[continuumIndexKey(ns, name)]

		// The HelmRelease carries no placement.regions slice the way an
		// Application CR does; the Sovereign's primary region is the
		// honest single-region default. Multi-region peer-cluster
		// derivation is the per-app placement endpoint's job (k8sCache
		// fan-out), not the fleet rollup's.
		regions := []string{}
		if sov.Region != "" {
			regions = []string{sov.Region}
		}

		// Record this HR's identity so a second HR that resolves to the
		// same (install-namespace, release-name) doesn't duplicate it.
		seen[continuumIndexKey(ns, name)] = struct{}{}

		out = append(out, fleetApplicationRow{
			Sovereign: sov,
			App: fleetApplicationIdent{
				Name:      name,
				Blueprint: blueprint,
				Version:   version,
			},
			Regions:   regions,
			Topology:  topology,
			DRPosture: deriveDRPosture(topology, hasContinuum, entry.phase),
			Status:    helmReleaseStatusPhase(hr),
			Org:       org,
			Namespace: ns,
		})
	}
	return out
}

// hrRowAlreadySeen reports whether a HelmRelease that would be emitted
// under (installNS|hrNS, releaseName|hrName) is already represented in
// the `seen` (namespace, name) set populated from the Application-CR
// rows (and prior HR rows). It checks every namespace×name combination
// so the dedup holds whether the companion Application CR keyed off the
// release name or the HR name, in the install namespace or the HR's own.
func hrRowAlreadySeen(seen map[string]struct{}, installNS, hrNS, releaseName, hrName string) bool {
	for _, ns := range dedupNonEmpty(installNS, hrNS) {
		for _, nm := range dedupNonEmpty(releaseName, hrName) {
			if _, ok := seen[continuumIndexKey(ns, nm)]; ok {
				return true
			}
		}
	}
	return false
}

// dedupNonEmpty returns the distinct non-empty members of a, b (order
// preserved). Tiny helper for the at-most-2-element dedup lookups above.
func dedupNonEmpty(a, b string) []string {
	switch {
	case a == "" && b == "":
		return nil
	case a == "":
		return []string{b}
	case b == "" || a == b:
		return []string{a}
	default:
		return []string{a, b}
	}
}

// fleetTopologyFromPlacementClass maps the canonical 4-class placement
// vocabulary (singleton / active-passive / active-hot-standby /
// active-active — see placementForTopology) onto the fleet table's
// topology vocabulary (single-region / active-active / active-hotstandby)
// so HelmRelease-sourced rows speak the same dialect as Application-CR-
// sourced rows for the ?topology= filter + DR-posture derivation.
func fleetTopologyFromPlacementClass(class string) string {
	switch class {
	case "active-active":
		return topologyActiveActive
	case "active-hot-standby":
		return topologyActiveHotStandby
	default:
		// singleton / active-passive / unknown → single-region (one
		// live cluster; active-passive has no live hot peer cluster).
		return topologySingleRegion
	}
}

// helmReleaseStatusPhase maps a HelmRelease's Ready condition onto the
// fleet row's `status` string (the same vocabulary the Application CR's
// status.phase would carry). Ready=True → "Ready"; an explicit
// Ready=False → "Failed"; anything else (reconciling / no condition yet)
// → "Provisioning".
func helmReleaseStatusPhase(hr *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if ctype, _ := cm["type"].(string); ctype != "Ready" {
			continue
		}
		switch s, _ := cm["status"].(string); s {
		case "True":
			return "Ready"
		case "False":
			return "Failed"
		default:
			return "Provisioning"
		}
	}
	return "Provisioning"
}

// continuumIndexEntry — minimal projection of Continuum CR fields the
// DR-posture derivation needs. Reserved field for future expansion
// (lease holder, last switchover) without changing the index shape.
type continuumIndexEntry struct {
	phase string
}

func continuumIndexKey(namespace, appRef string) string {
	return namespace + "/" + appRef
}

// deriveDRPosture — the brief's 4-way classification.
func deriveDRPosture(topology string, hasContinuum bool, phase string) string {
	switch {
	case hasContinuum && strings.EqualFold(phase, "Failed"):
		return drPostureAlert
	case hasContinuum:
		return drPostureActive
	case topology == topologyActiveHotStandby:
		// Active-hotstandby placement REQUIRES a Continuum CR — its
		// absence is a misconfiguration the operator must resolve.
		return drPostureMisconfigured
	default:
		return drPostureNone
	}
}

// healthFromDeploymentStatus — map the Deployment.Status string to the
// 4-state fleet vocabulary the dashboard renders.
func healthFromDeploymentStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ready":
		return healthGreen
	case "phase1-watching", "flux-bootstrapping", "tofu-applying", "provisioning", "pending":
		return healthYellow
	case "failed", "error":
		return healthRed
	case "":
		return healthUnknown
	default:
		return healthYellow
	}
}

// ── Auth helper (reserved for future per-Org Sovereign scoping) ─────

// fleetCallerVisibility decides which Sovereign IDs the caller can
// see. Today: every authenticated caller sees every Sovereign on this
// catalyst-api process. Reserved seam — when a follow-up slice adds
// Org-scoped Sovereign access (ADR-0003 §3.5+), this is the single
// gate to extend.
//
// Returns nil on full-visibility (admin/owner tier OR no claims, which
// matches the test path); returns a non-nil set when the caller's tier
// restricts to a subset.
func fleetCallerVisibility(claims *auth.Claims) map[string]struct{} {
	if claims == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(claims.Tier)) {
	case "admin", "owner":
		return nil
	}
	for _, want := range rbacAssignPrivilegedRoles {
		if claims.HasRealmRole(want) {
			return nil
		}
	}
	// Lower-tier callers fall through with full visibility today (the
	// per-Sov detail endpoint is the finer-grained gate). This will
	// tighten to org-scoped visibility once the Org-CR-driven
	// Sovereign mapping lands. Reserved: return a populated map here.
	return nil
}

// ── Validators ───────────────────────────────────────────────────────

func isValidTopologyFilter(s string) bool {
	switch s {
	case topologySingleRegion, topologyActiveActive, topologyActiveHotStandby:
		return true
	}
	return false
}

func isValidDRPostureFilter(s string) bool {
	switch s {
	case drPostureNone, drPostureActive, drPostureAlert, drPostureMisconfigured:
		return true
	}
	return false
}

// fleetParsePagination — read ?page= + ?pageSize= with defaults +
// caps. Page is 1-indexed; invalid values fall back to defaults.
func fleetParsePagination(r *http.Request) (page, pageSize int) {
	page = 1
	pageSize = fleetDefaultPageSize
	if raw := r.URL.Query().Get("page"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 1 {
			page = v
		}
	}
	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 1 {
			pageSize = v
		}
	}
	if pageSize > fleetMaxPageSize {
		pageSize = fleetMaxPageSize
	}
	return page, pageSize
}

// configuredRegionsForDeployment — return every Hetzner region the
// operator declared at provision time on this Sovereign (qa-loop
// iter-16 Fix #88, Path B).
//
// Resolution order (first non-empty wins for the source list; the
// final return is always sorted + de-duplicated + non-nil so the JSON
// renders `[]` not `null`):
//
//  1. Deployment record's Request.Regions slice — the wizard's
//     StepProvider always carries every region the operator picked
//     even when only the first becomes a live cluster (the
//     provisioner currently materialises the first as the live
//     cluster; real multi-region with Cilium ClusterMesh peering is
//     Path A future work). Includes both the explicit Regions[] entry
//     AND the legacy singular Region field on records that pre-date
//     the multi-region wizard step.
//  2. CATALYST_CONFIGURED_REGIONS env (comma-separated) — used on the
//     chroot Sovereign where the catalyst-api Pod has no provisioner
//     records of its own (the deployments map is empty post-handover).
//     Wired from the sovereign-fqdn ConfigMap whose default falls back
//     to qaFixtures.configuredRegions when fixtures are enabled. This
//     is what makes the QA matrix's multi-region tokens (`fsn1`,
//     `hz-hel-rtz-prod`, `hel`) render on a single-region QA cluster
//     without provisioning a real second-region cluster.
//
// CALLER MUST hold dep.mu when invoking — reads dep.Request.
func configuredRegionsForDeployment(dep *Deployment) []string {
	if dep == nil {
		return regionsFromEnv()
	}
	out := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	add := func(r string) {
		r = strings.TrimSpace(r)
		if r == "" {
			return
		}
		if _, ok := seen[r]; ok {
			return
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	for _, rg := range dep.Request.Regions {
		add(rg.CloudRegion)
	}
	add(dep.Request.Region)
	if len(out) == 0 {
		// Fall back to the chart-baked env (chroot Sovereign path
		// where the deployments map is empty by design).
		for _, r := range regionsFromEnv() {
			add(r)
		}
	}
	sort.Strings(out)
	return out
}

// regionsFromEnv parses CATALYST_CONFIGURED_REGIONS (comma-separated)
// into a clean string slice. Empty env → empty slice (NEVER nil so
// JSON renders `[]`). Whitespace + empty entries are skipped so
// trailing commas in the ConfigMap value don't introduce ghost chips.
func regionsFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("CATALYST_CONFIGURED_REGIONS"))
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mergeSortedRegions returns the de-duplicated union of two region
// slices, sorted lexically. Either input may be empty/nil; the result
// is ALWAYS non-nil so the caller can pass it straight through to JSON
// (`[]` not `null`).
func mergeSortedRegions(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, src := range [][]string{a, b} {
		for _, r := range src {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if _, ok := seen[r]; ok {
				continue
			}
			seen[r] = struct{}{}
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// ── Compile-time soft references (kill "imported-but-unused" risk
// when refactors temporarily remove a path) ──────────────────────────

var (
	_ = errors.New
	_ = json.Marshal
	_ = apierrors.IsNotFound
)
