// Package handlers — regions.go: the public GET /catalog/regions endpoint
// (#4525). The marketplace funnel BCP topology picker (BCPStep.svelte)
// needs the Sovereign's REAL configured regions so the active-hot-standby
// region <select>s never offer a region the Sovereign cannot honor.
//
// Before this endpoint the picker hardcoded Hetzner BB names
// (Falkenstein/Helsinki/Nuremberg) — wrong on a Huawei Sovereign running
// me-east-215-a/b. A customer who picked Falkenstein sent
// primary_region=hz-fsn-rtz-prod into cart.appConfigs.postgres, which the
// provisioning gitops generator (core/services/provisioning/gitops/
// gitops.go) cannot map to a live cluster → silent single-cluster
// fallback. This endpoint closes that gap.
//
// Source of truth: CATALYST_CONFIGURED_REGIONS — the SAME comma-separated
// env the fleet handler trusts (products/catalyst/bootstrap/api/internal/
// handler/fleet.go::regionsFromEnv). The value is already in the exact
// region-key format the gitops layer consumes (e.g. `me-east-215-a` on
// Huawei, `hz-fsn-rtz-prod` on Hetzner), so there is no per-provider
// branching here and no risk of emitting a key the provisioner can't map.
//
// On a Sovereign where the env is unset the endpoint returns an empty
// list; the frontend then keeps its static hardcoded fallback (the picker
// is never blocked). Mirrors fleet.go's "empty env → empty slice, never
// nil so JSON renders []" contract.

package handlers

import (
	"net/http"
	"os"
	"strings"

	"github.com/openova-io/openova/core/services/shared/respond"
)

// Region is the wire shape of one entry in GET /catalog/regions.
//
//	key   — the canonical region key the gitops layer consumes verbatim
//	        (e.g. "me-east-215-a"). This is the value the BCPStep picker
//	        writes into cart.appConfigs.postgres.{primary,replica}_region.
//	label — a human-readable label for the <option>. Derived from the key
//	        unless the env carried an explicit "key=label" pair.
type Region struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// ListRegions returns the Sovereign's configured regions from the
// CATALYST_CONFIGURED_REGIONS env (comma-separated). Public + read-only —
// same tier as /catalog/plans. Empty env → empty list (the frontend keeps
// its static fallback). Cache-friendly (5 min) like the other public
// catalog reads.
func (h *Handler) ListRegions(w http.ResponseWriter, r *http.Request) {
	regions := parseConfiguredRegions(os.Getenv("CATALYST_CONFIGURED_REGIONS"))
	w.Header().Set("Cache-Control", "public, max-age=300")
	respond.OK(w, regions)
}

// parseConfiguredRegions splits the comma-separated CATALYST_CONFIGURED_REGIONS
// value into a clean []Region. Mirrors fleet.go::regionsFromEnv: whitespace
// and empty entries (trailing commas in the ConfigMap value) are skipped so
// no ghost regions appear in the picker. Duplicate keys collapse to the
// first occurrence.
//
// Each entry is either a bare key (`me-east-215-a`) or an explicit
// `key=Human Label` pair. A bare key derives its label from the key
// (humanizeRegionKey); an explicit pair takes the right-hand side as the
// label verbatim.
//
// ALWAYS returns a non-nil slice so the JSON renders `[]` not `null` on an
// empty/unset env — the frontend treats `[]` as "fall back to the static
// list" exactly like a failed fetch.
func parseConfiguredRegions(raw string) []Region {
	out := make([]Region, 0)
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, label := part, ""
		if i := strings.Index(part, "="); i >= 0 {
			key = strings.TrimSpace(part[:i])
			label = strings.TrimSpace(part[i+1:])
		}
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if label == "" {
			label = humanizeRegionKey(key)
		}
		out = append(out, Region{Key: key, Label: label})
	}
	return out
}

// humanizeRegionKey turns a raw region key into a readable label when the
// env carried no explicit "key=label" pair. Deliberately minimal — it
// strips the trailing `-rtz-prod` Catalyst cluster suffix when present and
// shows the remaining key verbatim (the keys are already short and
// operator-meaningful, e.g. `me-east-215-a`). Per the #4525 design,
// showing the key verbatim is acceptable v1; an operator who wants a
// prettier label supplies a `key=Label` pair in the env.
func humanizeRegionKey(key string) string {
	trimmed := strings.TrimSuffix(key, "-rtz-prod")
	if trimmed == "" {
		return key
	}
	return trimmed
}
