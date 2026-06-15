// Package handler — catalog_overlay.go: #3602 (EPIC #3597) editable-catalog
// overlay.
//
// The Sovereign console's Catalog page (#3601 CatalogPage) reads
// GET /api/v1/catalog, which catalyst-api serves by listing the seed —
// the chart-shipped Blueprint CRs from
// products/catalyst/chart/templates/catalog-seed/blueprints.yaml (via the
// in-cluster CR fallback) plus any Gitea-mirrored catalog. That seed is
// NOT admin-editable on its own.
//
// Founder point 3 (2026-06-15): "a real catalog where the admin can edit
// each app's topology, name, light/dark icons". The persisted edits live
// in the SME commerce catalog store (core/services/catalog, store.App),
// which already exposes an admin CRUD (POST/PUT /catalog/admin/apps) that
// the #3603 edit form drives, and a public read (GET /catalog/apps) that
// returns every App row.
//
// This file is the read-side merge: it OVERLAYS the persisted store edits
// onto the seed so an edit wins while an un-edited entry keeps the seed.
// The overlay is strictly ADDITIVE — a store field is applied ONLY when it
// carries a non-empty value (an actual edit), so:
//
//   - an entry the admin never edited (no matching store row, or a store
//     row whose edit fields are empty) passes the seed through verbatim;
//   - an entry the admin edited (non-empty name / summary / icon /
//     iconLight / iconDark / supportedTopologies) shows the edit.
//
// Matching is by NORMALIZED name: the seed Blueprint CRs are `bp-`-prefixed
// (`bp-grafana`) per docs/NAMING §Blueprint, while the store keys rows by a
// bare slug (`grafana`). normalizeCatalogKey strips the `bp-` prefix on
// both sides so the two line up.
//
// Graceful degradation (the existing smeCatalog() pattern): when the SME
// commerce catalog is not deployed on this Sovereign (DNS NXDOMAIN, non-2xx,
// undecodable body) fetchCatalogEdits returns an empty map and the seed is
// served unchanged. The overlay never fails the catalog read.

package handler

import (
	"context"
	"encoding/json"
	"strings"
)

// catalogEdit is the subset of the SME commerce catalog's store.App that
// the editable-catalog overlay applies onto a seed entry. Field names
// mirror the store's JSON tags VERBATIM (core/services/catalog/store.App)
// so the bare-array GET /catalog/apps response decodes straight into it.
type catalogEdit struct {
	Slug                string   `json:"slug"`
	Name                string   `json:"name"`
	Tagline             string   `json:"tagline"`
	Description         string   `json:"description"`
	Icon                string   `json:"icon"`
	IconLight           string   `json:"icon_light"`
	IconDark            string   `json:"icon_dark"`
	SupportedTopologies []string `json:"supported_topologies"`
}

// hasOverlay reports whether this store row carries at least one non-empty
// edit field. A row with no overlay-relevant content (the common case for
// the ~100 pre-seeded commerce rows that were never touched by the
// editable-catalog feature) is skipped entirely so it can never shadow a
// seed entry that happens to share its slug.
func (e catalogEdit) hasOverlay() bool {
	return strings.TrimSpace(e.Name) != "" ||
		strings.TrimSpace(e.Tagline) != "" ||
		strings.TrimSpace(e.Description) != "" ||
		strings.TrimSpace(e.Icon) != "" ||
		strings.TrimSpace(e.IconLight) != "" ||
		strings.TrimSpace(e.IconDark) != "" ||
		len(e.SupportedTopologies) > 0
}

// normalizeCatalogKey lower-cases and strips the canonical `bp-` Blueprint
// prefix so a seed CR named `bp-grafana` matches a store row keyed
// `grafana`.
func normalizeCatalogKey(name string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "bp-")
}

// overlayCatalogEdits returns a NEW slice with the persisted store edits
// merged onto the seed. Pure (no I/O) so it is unit-testable in isolation.
//
// Merge rules (additive — an edit wins, the seed survives otherwise):
//   - Name        → Card.Title       (when the store Name is non-empty)
//   - Tagline     → Card.Summary     (when the store Tagline is non-empty)
//   - Description → Card.Description  (when the store Description is non-empty)
//   - Icon        → Card.Icon         (when the store Icon is non-empty)
//   - IconLight   → Card.IconLight    (when non-empty)
//   - IconDark    → Card.IconDark     (when non-empty)
//   - SupportedTopologies → top-level SupportedTopologies (when non-empty)
//
// Items with no matching edited store row are returned unchanged.
func overlayCatalogEdits(items []CatalogBlueprint, edits map[string]catalogEdit) []CatalogBlueprint {
	if len(edits) == 0 || len(items) == 0 {
		return items
	}
	out := make([]CatalogBlueprint, len(items))
	copy(out, items)
	for i := range out {
		e, ok := edits[normalizeCatalogKey(out[i].Name)]
		if !ok || !e.hasOverlay() {
			continue
		}
		if v := strings.TrimSpace(e.Name); v != "" {
			out[i].Card.Title = e.Name
		}
		if v := strings.TrimSpace(e.Tagline); v != "" {
			out[i].Card.Summary = e.Tagline
		}
		if v := strings.TrimSpace(e.Description); v != "" {
			out[i].Card.Description = e.Description
		}
		if v := strings.TrimSpace(e.Icon); v != "" {
			out[i].Card.Icon = e.Icon
		}
		if v := strings.TrimSpace(e.IconLight); v != "" {
			out[i].Card.IconLight = e.IconLight
		}
		if v := strings.TrimSpace(e.IconDark); v != "" {
			out[i].Card.IconDark = e.IconDark
		}
		if len(e.SupportedTopologies) > 0 {
			out[i].SupportedTopologies = e.SupportedTopologies
		}
	}
	return out
}

// fetchCatalogEdits pulls every App row from the SME commerce catalog's
// PUBLIC list endpoint (GET /catalog/apps) and indexes the edited ones by
// normalized slug. Returns an empty (non-nil) map on ANY failure — the SME
// catalog being absent (the marketplace.enabled=false Sovereigns) or
// unreachable is a normal state, not an error: the catalog read then serves
// the seed unchanged. Per the existing smeCatalog() contract the public
// list carries no auth, so no bearer is forwarded.
func fetchCatalogEdits(ctx context.Context) map[string]catalogEdit {
	edits := map[string]catalogEdit{}
	status, body, _, err := smeCatalog().PublicProxy(ctx, "/apps")
	if err != nil || status < 200 || status >= 300 || len(body) == 0 {
		return edits
	}
	// GET /catalog/apps returns a BARE JSON array (respond.OK on a slice).
	// Decode tolerantly: on a shape we don't recognise, return the empty
	// map so the seed passes through rather than 500-ing the catalog read.
	var rows []catalogEdit
	if jerr := json.Unmarshal(body, &rows); jerr != nil {
		return edits
	}
	for _, row := range rows {
		key := normalizeCatalogKey(row.Slug)
		if key == "" || !row.hasOverlay() {
			continue
		}
		edits[key] = row
	}
	return edits
}
