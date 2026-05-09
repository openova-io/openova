package source

import (
	"context"
	"fmt"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/cache"
)

// Public reads Blueprints from the public-mirror Gitea Org. Layout:
// one repo per Blueprint; `blueprint.yaml` at the repo root on `main`.
type Public struct {
	GC    *gitea.Client
	Org   string // "catalog" by default
	Cache *cache.LRU
}

func NewPublic(gc *gitea.Client, org string, c *cache.LRU) *Public {
	return &Public{GC: gc, Org: org, Cache: c}
}

func (s *Public) Origin() Origin { return OriginPublic }

func (s *Public) List(ctx context.Context) ([]Blueprint, error) {
	repos, err := s.GC.ListOrgRepos(ctx, s.Org)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil // Org not provisioned — empty catalog is OK.
		}
		return nil, fmt.Errorf("source.public.List: %w", err)
	}
	out := make([]Blueprint, 0, len(repos))
	for _, r := range repos {
		bp, err := s.fetch(ctx, r.Name)
		if err != nil {
			if IsNotFound(err) {
				continue // repo without blueprint.yaml — skip silently
			}
			return nil, fmt.Errorf("source.public.List: read %s: %w", r.Name, err)
		}
		if bp == nil {
			continue
		}
		out = append(out, *bp)
	}
	return out, nil
}

func (s *Public) Get(ctx context.Context, name, version string) (*Blueprint, error) {
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

func (s *Public) fetch(ctx context.Context, name string) (*Blueprint, error) {
	key := CacheKey(s.Origin(), s.Org, name, "")
	if s.Cache != nil {
		if cached, ok := s.Cache.Get(key); ok {
			return ParseBlueprint(cached, s.Origin(), "", name)
		}
	}
	data, err := readBlueprintYAML(ctx, s.GC, s.Org, name, "blueprint.yaml")
	if err != nil {
		return nil, err
	}
	if s.Cache != nil {
		s.Cache.Put(key, data)
	}
	return ParseBlueprint(data, s.Origin(), "", name)
}
