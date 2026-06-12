// Package source implements the multi-source Blueprint catalog
// resolver that backs every catalog-svc HTTP endpoint.
//
// Three sources, each implementing the Source interface (List, Get):
//
//   - Public mirror (`catalog` Gitea Org): always visible.
//   - Sovereign-curated (`catalog-sovereign` Gitea Org): always visible.
//   - Per-Org private (`<org>/shared-blueprints` Gitea repo): visible
//     ONLY to callers whose claims grant access to that Org.
//
// Source resolution order on listing — higher priority wins on a name
// collision: PRIVATE > SOVEREIGN > PUBLIC. This matches the design
// rationale in docs/EPICS-1-6-unified-design.md §5.4: an Org-private
// override of a sovereign-curated Blueprint is the explicit
// override-the-default knob.
//
// Each blueprint repo under the public + sovereign-curated Orgs is
// expected to ship `blueprint.yaml` at the repo root. For the per-Org
// private case the layout is a single `<org>/shared-blueprints` repo
// with `<bp-name>/blueprint.yaml` per Blueprint (one repo, many
// Blueprints) — see EPIC-2 master brief.
//
// The sources accept a Gitea client (the unified CC2 client at
// `core/controllers/pkg/gitea`) injected at construction time so the
// in-process integration test can swap in an httptest-backed instance.
package source

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	sigsyaml "sigs.k8s.io/yaml"
)

// Origin identifies which source produced a Blueprint. Lower values are
// LOWER priority (higher integer = higher priority on collision).
type Origin int

const (
	// OriginPublic — the public mirror Org. Lowest priority.
	OriginPublic Origin = iota + 1
	// OriginSovereign — the sovereign-curated Org. Mid priority.
	OriginSovereign
	// OriginOrgPrivate — the caller's Org's `shared-blueprints` repo.
	// Highest priority.
	OriginOrgPrivate
)

func (o Origin) String() string {
	switch o {
	case OriginPublic:
		return "public"
	case OriginSovereign:
		return "sovereign"
	case OriginOrgPrivate:
		return "org-private"
	default:
		return "unknown"
	}
}

// Blueprint is the catalog-svc projection of a Blueprint CR — exposes
// only the fields the UI consumes plus `Raw` (the parsed YAML for
// /versions/{version} responses) and `Origin` (which source produced
// this entry).
type Blueprint struct {
	// Name is the Blueprint repo name (without "bp-" prefix when
	// authored consistently — we use whatever the repo name happens
	// to be).
	Name string `json:"name"`

	// Version is `spec.version` — semver per the CRD pattern.
	Version string `json:"version"`

	// Visibility mirrors `spec.visibility`. May be empty.
	Visibility string `json:"visibility,omitempty"`

	// Card is the user-facing metadata block.
	Card BlueprintCard `json:"card"`

	// PlacementSchema (subset) — surfaced so the UI knows allowed
	// topology modes without re-fetching the full Blueprint.
	PlacementSchema *BlueprintPlacement `json:"placementSchema,omitempty"`

	// DefaultPlacement — #3373: the class-level placement SUGGESTION
	// (`spec.defaultPlacement`) every instance inherits silently
	// unless its own Application.spec.placement overrides a field.
	// Surfaced so the console's generic advanced selector renders
	// blueprint defaults without re-fetching Raw.
	DefaultPlacement *BlueprintDefaultPlacement `json:"defaultPlacement,omitempty"`

	// AllowedPlacements — #3373: `spec.allowedPlacements`, the
	// vCluster tiers (host|mgmt|dmz|rtz) an instance MAY choose.
	// Empty = unrestricted.
	AllowedPlacements []string `json:"allowedPlacements,omitempty"`

	// UpgradeFrom is `spec.upgrades.from` — the list of versions an
	// install at THIS version can upgrade FROM. Used for the version
	// matrix.
	UpgradeFrom []string `json:"upgradeFrom,omitempty"`

	// UpgradeBlocks is `spec.upgrades.blocks`.
	UpgradeBlocks []string `json:"upgradeBlocks,omitempty"`

	// Origin records which source produced this entry (private /
	// sovereign / public). Useful for the UI to show a badge.
	Origin Origin `json:"origin"`

	// Source is the human-readable origin name (string form of Origin).
	// Convenience field for JSON consumers that don't decode the int.
	Source string `json:"source"`

	// Org — populated only for OriginOrgPrivate entries (the caller's
	// Org slug whose `shared-blueprints` repo this Blueprint came from).
	Org string `json:"org,omitempty"`

	// Raw is the full parsed Blueprint YAML as a generic map. Returned
	// only by GET /catalog/{name}/versions/{version} and List* helpers
	// that opt-in.
	Raw map[string]interface{} `json:"raw,omitempty"`
}

// BlueprintCard mirrors the CRD's `spec.card` block.
type BlueprintCard struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Tagline     string   `json:"tagline,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Category    string   `json:"category,omitempty"`
	Family      string   `json:"family,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	License     string   `json:"license,omitempty"`
	Docs        string   `json:"docs,omitempty"`
}

// BlueprintPlacement mirrors `spec.placementSchema` (subset).
type BlueprintPlacement struct {
	Modes      []string `json:"modes,omitempty"`
	Default    string   `json:"default,omitempty"`
	MinRegions int      `json:"minRegions,omitempty"`
	MaxRegions int      `json:"maxRegions,omitempty"`
}

// BlueprintDefaultPlacement mirrors `spec.defaultPlacement` (#3373) —
// the object form of Application.spec.placement at the class level.
type BlueprintDefaultPlacement struct {
	VCluster string   `json:"vcluster,omitempty"`
	Regions  []string `json:"regions,omitempty"`
	Clusters []string `json:"clusters,omitempty"`
}

// Source abstracts a Blueprint provider. Implementations: Public,
// Sovereign, OrgPrivate.
type Source interface {
	// Origin reports which Origin tier this Source represents.
	Origin() Origin

	// List returns Blueprints visible from this source. Per the brief
	// the implementation reads ALL Blueprints (every repo under the
	// configured Org for public/sovereign; every directory under the
	// `<org>/shared-blueprints` repo for per-Org).
	List(ctx context.Context) ([]Blueprint, error)

	// Get returns the Blueprint at the requested version, or nil + nil
	// when the source has no copy of name@version. (nil + non-nil err
	// is reserved for transport / parse errors.)
	Get(ctx context.Context, name, version string) (*Blueprint, error)
}

// ErrBlueprintNotFound is returned by Resolver.Get when no source has
// the requested name (potentially version-pinned).
var ErrBlueprintNotFound = errors.New("source: blueprint not found")

// ParseBlueprint converts a YAML blueprint manifest into the catalog-
// svc projection. Exported so the per-source loaders can share the
// same parse path AND the integration test can build expected
// fixtures without going through the resolver.
func ParseBlueprint(yamlBytes []byte, origin Origin, org, repoName string) (*Blueprint, error) {
	var raw map[string]interface{}
	if err := sigsyaml.Unmarshal(yamlBytes, &raw); err != nil {
		return nil, fmt.Errorf("source: parse blueprint.yaml: %w", err)
	}
	bp := Blueprint{
		Origin: origin,
		Source: origin.String(),
		Org:    org,
		Raw:    raw,
		Name:   repoName,
	}
	spec, _ := raw["spec"].(map[string]interface{})
	if spec == nil {
		return nil, fmt.Errorf("source: blueprint.yaml has no spec")
	}
	if v, ok := spec["version"].(string); ok {
		bp.Version = v
	}
	if v, ok := spec["visibility"].(string); ok {
		bp.Visibility = v
	}
	if metaName, ok := raw["metadata"].(map[string]interface{}); ok {
		if n, ok := metaName["name"].(string); ok && n != "" {
			bp.Name = n
		}
	}
	if cardRaw, ok := spec["card"].(map[string]interface{}); ok {
		bp.Card = parseCard(cardRaw)
	}
	if pl, ok := spec["placementSchema"].(map[string]interface{}); ok {
		bp.PlacementSchema = parsePlacement(pl)
	}
	// #3373 — instance-placement class defaults.
	if dp, ok := spec["defaultPlacement"].(map[string]interface{}); ok {
		out := &BlueprintDefaultPlacement{}
		if v, ok := dp["vcluster"].(string); ok {
			out.VCluster = v
		}
		if v, ok := dp["regions"].([]interface{}); ok {
			out.Regions = strSlice(v)
		}
		if v, ok := dp["clusters"].([]interface{}); ok {
			out.Clusters = strSlice(v)
		}
		bp.DefaultPlacement = out
	}
	if ap, ok := spec["allowedPlacements"].([]interface{}); ok {
		bp.AllowedPlacements = strSlice(ap)
	}
	if upg, ok := spec["upgrades"].(map[string]interface{}); ok {
		if from, ok := upg["from"].([]interface{}); ok {
			bp.UpgradeFrom = strSlice(from)
		}
		if blocks, ok := upg["blocks"].([]interface{}); ok {
			bp.UpgradeBlocks = strSlice(blocks)
		}
	}
	return &bp, nil
}

func parseCard(c map[string]interface{}) BlueprintCard {
	out := BlueprintCard{}
	if v, ok := c["title"].(string); ok {
		out.Title = v
	}
	if v, ok := c["summary"].(string); ok {
		out.Summary = v
	}
	if v, ok := c["description"].(string); ok {
		out.Description = v
	}
	if v, ok := c["tagline"].(string); ok {
		out.Tagline = v
	}
	if v, ok := c["icon"].(string); ok {
		out.Icon = v
	}
	if v, ok := c["category"].(string); ok {
		out.Category = v
	}
	if v, ok := c["family"].(string); ok {
		out.Family = v
	}
	if v, ok := c["license"].(string); ok {
		out.License = v
	}
	if v, ok := c["docs"].(string); ok {
		out.Docs = v
	}
	if v, ok := c["documentation"].(string); ok && out.Docs == "" {
		out.Docs = v
	}
	if tags, ok := c["tags"].([]interface{}); ok {
		out.Tags = strSlice(tags)
	}
	return out
}

func parsePlacement(p map[string]interface{}) *BlueprintPlacement {
	out := &BlueprintPlacement{}
	if modes, ok := p["modes"].([]interface{}); ok {
		out.Modes = strSlice(modes)
	}
	if v, ok := p["default"].(string); ok {
		out.Default = v
	}
	if v, ok := p["minRegions"].(float64); ok {
		out.MinRegions = int(v)
	}
	if v, ok := p["maxRegions"].(float64); ok {
		out.MaxRegions = int(v)
	}
	if out.Modes == nil && out.Default == "" && out.MinRegions == 0 && out.MaxRegions == 0 {
		return nil
	}
	return out
}

func strSlice(in []interface{}) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// CacheKey composes the LRU cache key for a Blueprint read.
func CacheKey(origin Origin, org, name, version string) string {
	return fmt.Sprintf("%s|%s|%s|%s", origin, org, name, version)
}

// readBlueprintYAML fetches blueprint.yaml at the configured path on
// the repo's main branch, returning typed Gitea sentinels through
// IsNotFound when applicable.
func readBlueprintYAML(ctx context.Context, gc *gitea.Client, org, repo, path string) ([]byte, error) {
	f, err := gc.GetFile(ctx, org, repo, "main", path)
	if err != nil {
		return nil, err
	}
	return f.Decoded()
}

// IsNotFound surfaces the gitea client's typed-sentinel test (so callers
// here don't need to import the gitea package directly).
func IsNotFound(err error) bool {
	return gitea.IsNotFound(err)
}

// trimBPPrefix strips the conventional "bp-" prefix from a name. Used
// when matching repo-name → Blueprint name. Idempotent on names that
// don't carry the prefix.
func trimBPPrefix(s string) string {
	return strings.TrimPrefix(s, "bp-")
}
