package source

import (
	"context"
	"fmt"
	"sort"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/cache"
)

// Resolver aggregates the three sources (public + sovereign + per-Org
// private) into a single visible-to-caller catalog. It enforces the
// priority order on name collisions: PRIVATE > SOVEREIGN > PUBLIC.
//
// Construction is per-request (cheap; sources are stateless wrappers
// around the shared Gitea client + LRU cache).
type Resolver struct {
	GC                   *gitea.Client
	PublicOrg            string
	SovereignOrg         string
	OrgPrivateRepoSuffix string
	Cache                *cache.LRU
}

// NewResolver constructs a Resolver wired to the shared Gitea client
// and cache.
func NewResolver(gc *gitea.Client, publicOrg, sovereignOrg, orgPrivateRepoSuffix string, c *cache.LRU) *Resolver {
	return &Resolver{
		GC:                   gc,
		PublicOrg:            publicOrg,
		SovereignOrg:         sovereignOrg,
		OrgPrivateRepoSuffix: orgPrivateRepoSuffix,
		Cache:                c,
	}
}

// List returns the de-duplicated, priority-resolved Blueprint list
// visible to a caller whose visible Orgs are `visibleOrgs`. Pass an
// empty slice for anonymous / public-only listings.
//
// Priority on name collision: private > sovereign > public.
//
// Result is sorted by Name for stable client-side rendering.
func (r *Resolver) List(ctx context.Context, visibleOrgs []string) ([]Blueprint, error) {
	merged := make(map[string]Blueprint)

	// Public — always visible, lowest priority.
	pub := NewPublic(r.GC, r.PublicOrg, r.Cache)
	pubList, err := pub.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolver.List: public: %w", err)
	}
	for _, bp := range pubList {
		merged[bp.Name] = bp
	}

	// Sovereign — always visible, mid priority.
	sov := NewSovereign(r.GC, r.SovereignOrg, r.Cache)
	sovList, err := sov.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolver.List: sovereign: %w", err)
	}
	for _, bp := range sovList {
		merged[bp.Name] = bp
	}

	// Per-Org private — only visible Orgs, highest priority.
	for _, org := range visibleOrgs {
		orgSrc := NewOrgPrivate(r.GC, org, r.OrgPrivateRepoSuffix, r.Cache)
		privList, err := orgSrc.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolver.List: org-private/%s: %w", org, err)
		}
		for _, bp := range privList {
			merged[bp.Name] = bp
		}
	}

	out := make([]Blueprint, 0, len(merged))
	for _, bp := range merged {
		// Strip the Raw payload from List results to keep response
		// size bounded — clients call /catalog/{name} for full detail.
		bp.Raw = nil
		out = append(out, bp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the highest-priority Blueprint matching name, optionally
// pinned to version. Returns nil + ErrBlueprintNotFound if no source
// has a copy.
func (r *Resolver) Get(ctx context.Context, name, version string, visibleOrgs []string) (*Blueprint, error) {
	// Walk in PRIORITY ORDER: private (per-Org) → sovereign → public.
	for _, org := range visibleOrgs {
		orgSrc := NewOrgPrivate(r.GC, org, r.OrgPrivateRepoSuffix, r.Cache)
		bp, err := orgSrc.Get(ctx, name, version)
		if err != nil {
			return nil, fmt.Errorf("resolver.Get: org-private/%s: %w", org, err)
		}
		if bp != nil {
			return bp, nil
		}
	}
	sov := NewSovereign(r.GC, r.SovereignOrg, r.Cache)
	bp, err := sov.Get(ctx, name, version)
	if err != nil {
		return nil, fmt.Errorf("resolver.Get: sovereign: %w", err)
	}
	if bp != nil {
		return bp, nil
	}
	pub := NewPublic(r.GC, r.PublicOrg, r.Cache)
	bp, err = pub.Get(ctx, name, version)
	if err != nil {
		return nil, fmt.Errorf("resolver.Get: public: %w", err)
	}
	if bp != nil {
		return bp, nil
	}
	return nil, ErrBlueprintNotFound
}
