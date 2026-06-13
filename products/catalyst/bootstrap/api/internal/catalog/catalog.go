// Package catalog exposes the Blueprint marketplace catalog the Sovereign
// Console reads at /api/v1/sovereign/apps. The catalog is the SAME
// data the Catalyst-Zero wizard's StepComponents renders — per
// docs/INVIOLABLE-PRINCIPLES.md #4, the Blueprint catalog has exactly
// ONE source of truth (every platform/<name>/blueprint.yaml +
// products/<name>/blueprint.yaml in the monorepo).
//
// The build pipeline is:
//
//  1. products/catalyst/bootstrap/ui/scripts/build-catalog.mjs walks
//     the blueprint.yaml tree at build time and emits TWO files:
//     a) catalog.generated.ts (TS) — for the wizard UI
//     b) blueprints.json (JSON) — embedded here for the API
//  2. The Containerfile build stage `COPY products/catalyst/bootstrap/api/`
//     pulls the JSON into the image alongside the Go source.
//  3. This file uses go:embed to ship the JSON inside the catalyst-api
//     binary so a Sovereign-side Pod renders the catalog without
//     network calls back to the mothership.
//
// On a freshly-handed-over Sovereign the local Console can therefore
// render the FULL marketplace card grid the operator expects without
// waiting for the gitea-mirror cutover step to land. After cutover,
// the same catalog continues to serve from the same in-binary copy —
// the Sovereign catalog and the mothership catalog stay byte-identical
// because they are SHA-pinned to the same Catalyst-API image build.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// blueprintsJSON is the embedded JSON catalog produced by the UI
// build-catalog.mjs script. The file is always present in the source
// tree (committed alongside catalog.generated.ts); a missing or
// truncated file is a build-time error that fails go:embed before
// the binary ever ships.
//
//go:embed blueprints.json
var blueprintsJSON []byte

// Visibility mirrors the TS BlueprintVisibility union — listed
// blueprints appear in the marketplace card grid; unlisted are
// mandatory infra (bootstrap-kit) that the Sovereign Console renders
// as "always installed" pills rather than installable cards; private
// is org-only and is filtered out of every public response.
type Visibility string

const (
	VisibilityListed   Visibility = "listed"
	VisibilityUnlisted Visibility = "unlisted"
	VisibilityPrivate  Visibility = "private"
)

// Blueprint is the catalogue entry shape — JSON tags match the
// blueprints.json file emitted by build-catalog.mjs verbatim so a
// future schema bump only needs to edit one side.
type Blueprint struct {
	ID         string     `json:"id"`
	Slug       string     `json:"slug"`
	Title      string     `json:"title"`
	Summary    string     `json:"summary"`
	Icon       *string    `json:"icon"`
	Category   *string    `json:"category"`
	Tagline    *string    `json:"tagline"`
	Tags       []string   `json:"tags"`
	Visibility Visibility `json:"visibility"`
	Version    *string    `json:"version"`
	Section    *string    `json:"section"`
	Depends    []string   `json:"depends"`

	// Shareable (#3370) — instances of this Blueprint support
	// multi-application reuse: one instance serves many consumer
	// applications, each through its own Context. Declared as
	// `spec.shareable: true` in the Blueprint's blueprint.yaml.
	Shareable bool `json:"shareable"`
	// ContextSchema (#3370) — the Context declaration for shareable
	// Blueprints (nil otherwise). Every generic console surface
	// (catalog badge, Contexts tab, reuse selector) renders from this.
	ContextSchema *ContextSchema `json:"contextSchema"`

	// Topology (#3375) — the declared topology + DR contract lifted from
	// the Blueprint's spec.topology (the source docs/topology-matrix.md
	// promotes). nil when the Blueprint ships no topology block. The
	// console reads this back per-app on the Topology tab; the catalyst-api
	// can surface it on the application-status endpoint so the read-back
	// works for chroot-installed apps with no wizard-store entry.
	Topology *Topology `json:"topology,omitempty"`
}

// Topology (#3375) is the per-Blueprint topology declaration. Mirrors the
// BlueprintTopology TS shape emitted by build-catalog.mjs verbatim.
type Topology struct {
	Supported    []string                   `json:"supported"`
	MultiRegion  *string                    `json:"multiRegion"`
	SingleRegion *string                    `json:"singleRegion"`
	PerTopology  map[string]TopologyVariant `json:"perTopology"`
}

// TopologyVariant is the DR contract for one declared variant.
type TopologyVariant struct {
	Replication *TopologyReplication `json:"replication"`
	Switchover  *TopologySwitchover  `json:"switchover"`
	Placement   *TopologyPlacement   `json:"placement"`
}

// TopologyReplication is the state-preservation backend for a variant.
type TopologyReplication struct {
	Backend       *string `json:"backend"`
	Mode          *string `json:"mode"`
	LagSloSeconds *int    `json:"lagSloSeconds"`
}

// TopologySwitchover is the promotion mechanism + RTO/RPO for a variant.
type TopologySwitchover struct {
	Mechanism  *string `json:"mechanism"`
	RtoSeconds *int    `json:"rtoSeconds"`
	RpoSeconds *int    `json:"rpoSeconds"`
}

// TopologyPlacement is the tier + per-cluster role map for a variant.
type TopologyPlacement struct {
	Tier     *string           `json:"tier"`
	Clusters []string          `json:"clusters"`
	Roles    map[string]string `json:"roles"`
}

// ContextSchema (#3370) describes what a Context is for a shareable
// Blueprint: the kind label (db | topic | bucket | keyspace) that
// prefixes every Context in the console, the key in the instance's
// IaC values where Context entries live, and the declared
// needs/produces field lists.
type ContextSchema struct {
	Kind      string   `json:"kind"`
	ValuesKey string   `json:"valuesKey"`
	Needs     []string `json:"needs"`
	Produces  []string `json:"produces"`
}

// BootstrapKitEntry mirrors the wizard's BOOTSTRAP_KIT shape. These
// are the always-installed platform components the Sovereign
// already has — the Console marks them "Installed (bootstrap)" so
// the operator sees them as part of the catalog, not gaps in it.
type BootstrapKitEntry struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Label string `json:"label"`
	File  string `json:"file"`
	Order int    `json:"order"`
}

// catalogFile mirrors the JSON wrapper emitted by build-catalog.mjs.
type catalogFile struct {
	GeneratedAt   string              `json:"generatedAt"`
	Blueprints    []Blueprint         `json:"blueprints"`
	BootstrapKit  []BootstrapKitEntry `json:"bootstrapKit"`
}

var (
	loadOnce sync.Once
	loaded   *catalogFile
	loadErr  error
)

// load decodes the embedded JSON exactly once on first call.
// Subsequent calls reuse the parsed slices. Returns an error iff
// the embedded JSON is corrupt — which would have been caught at
// build time before this binary ever shipped, so callers can treat
// non-nil err as a 500.
func load() (*catalogFile, error) {
	loadOnce.Do(func() {
		var c catalogFile
		if err := json.Unmarshal(blueprintsJSON, &c); err != nil {
			loadErr = fmt.Errorf("catalog: unmarshal blueprints.json: %w", err)
			return
		}
		// Stable sort by ID so callers don't depend on JSON
		// emission order. The TS side already sorts; we sort
		// again here defensively so the two ends agree even
		// if the build script's sort key ever changes.
		sort.Slice(c.Blueprints, func(i, j int) bool {
			return c.Blueprints[i].ID < c.Blueprints[j].ID
		})
		sort.Slice(c.BootstrapKit, func(i, j int) bool {
			return c.BootstrapKit[i].Order < c.BootstrapKit[j].Order
		})
		loaded = &c
	})
	return loaded, loadErr
}

// AllBlueprints returns every Blueprint in the catalog regardless of
// visibility. Callers that want the marketplace-visible subset use
// ListedBlueprints below.
func AllBlueprints() ([]Blueprint, error) {
	c, err := load()
	if err != nil {
		return nil, err
	}
	out := make([]Blueprint, len(c.Blueprints))
	copy(out, c.Blueprints)
	return out, nil
}

// BlueprintByID returns the catalog entry for a full bp-<name> id.
// (#3370 — the contextSchema fallback resolution path when the
// in-cluster Blueprint CR is absent/unwired.)
func BlueprintByID(id string) (Blueprint, bool) {
	c, err := load()
	if err != nil {
		return Blueprint{}, false
	}
	for _, b := range c.Blueprints {
		if b.ID == id {
			return b, true
		}
	}
	return Blueprint{}, false
}

// ListedBlueprints returns the Blueprints with visibility=listed —
// the same set the wizard's StepComponents card grid renders. This
// is the canonical "marketplace" view a Sovereign Console operator
// browses on /console/apps.
func ListedBlueprints() ([]Blueprint, error) {
	c, err := load()
	if err != nil {
		return nil, err
	}
	out := make([]Blueprint, 0, len(c.Blueprints))
	for _, b := range c.Blueprints {
		if b.Visibility == VisibilityListed {
			out = append(out, b)
		}
	}
	return out, nil
}

// BootstrapKit returns the always-installed platform components in
// numerical install order.
func BootstrapKit() ([]BootstrapKitEntry, error) {
	c, err := load()
	if err != nil {
		return nil, err
	}
	out := make([]BootstrapKitEntry, len(c.BootstrapKit))
	copy(out, c.BootstrapKit)
	return out, nil
}

// GeneratedAt returns the timestamp the embedded JSON was generated at.
// Callers can surface this in a /api/v1/sovereign/apps?meta=true response
// so an operator (or automated drift-check) can see when the catalog
// was last rebuilt.
func GeneratedAt() (string, error) {
	c, err := load()
	if err != nil {
		return "", err
	}
	return c.GeneratedAt, nil
}
