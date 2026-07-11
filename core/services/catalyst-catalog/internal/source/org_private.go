package source

import (
	"context"
	"fmt"
	"path"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/cache"
)

// OrgPrivate reads Blueprints from a single per-Org `<org>/<repoName>`
// repository. Layout: one directory per Blueprint, each containing
// `blueprint.yaml`.
//
// One OrgPrivate instance per visible Org. The Resolver constructs them
// on-demand from the caller's Claims (see VisibleOrgs).
type OrgPrivate struct {
	GC       *gitea.Client
	Org      string // the Org slug, e.g. "acme"
	RepoName string // "shared-blueprints" by default
	Cache    *cache.LRU
}

func NewOrgPrivate(gc *gitea.Client, org, repoName string, c *cache.LRU) *OrgPrivate {
	return &OrgPrivate{GC: gc, Org: org, RepoName: repoName, Cache: c}
}

func (s *OrgPrivate) Origin() Origin { return OriginOrgPrivate }

func (s *OrgPrivate) List(ctx context.Context) ([]Blueprint, error) {
	entries, err := s.GC.ListContents(ctx, s.Org, s.RepoName, "main", "")
	if err != nil {
		if IsNotFound(err) {
			return nil, nil // Repo not provisioned — empty private catalog is OK.
		}
		return nil, fmt.Errorf("source.orgPrivate.List: %w", err)
	}
	out := make([]Blueprint, 0, len(entries))
	for _, e := range entries {
		if e.Type != "dir" {
			continue
		}
		bp, err := s.fetch(ctx, e.Name)
		if err != nil {
			if IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("source.orgPrivate.List: read %s/%s: %w", s.Org, e.Name, err)
		}
		if bp == nil {
			continue
		}
		out = append(out, *bp)
	}
	return out, nil
}

func (s *OrgPrivate) Get(ctx context.Context, name, version string) (*Blueprint, error) {
	bp, err := s.fetch(ctx, name)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if bp == nil {
		return nil, nil
	}
	if version != "" && bp.Version != version {
		return nil, nil
	}
	return bp, nil
}

func (s *OrgPrivate) fetch(ctx context.Context, name string) (*Blueprint, error) {
	key := CacheKey(s.Origin(), s.Org, name, "")
	if s.Cache != nil {
		if cached, ok := s.Cache.Get(key); ok {
			bp, err := ParseBlueprint(cached, s.Origin(), s.Org, name)
			return bp, err
		}
	}
	// #4990 — prefix-tolerant per-Blueprint directory lookup: a bare-name
	// request resolves against a `bp-<x>` directory and vice-versa (the
	// per-Org repo is fixed, so the "bp-" toggle applies to the directory
	// segment, not the repo name).
	resolved := name
	data, err := readBlueprintYAML(ctx, s.GC, s.Org, s.RepoName, path.Join(name, "blueprint.yaml"))
	if err != nil && IsNotFound(err) {
		if alt := altBPName(name); alt != "" && alt != name {
			d2, e2 := readBlueprintYAML(ctx, s.GC, s.Org, s.RepoName, path.Join(alt, "blueprint.yaml"))
			if e2 == nil {
				data, err, resolved = d2, nil, alt
			} else if !IsNotFound(e2) {
				return nil, e2
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if s.Cache != nil {
		s.Cache.Put(key, data)
	}
	return ParseBlueprint(data, s.Origin(), s.Org, resolved)
}
