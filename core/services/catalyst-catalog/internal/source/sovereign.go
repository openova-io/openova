package source

import (
	"context"
	"fmt"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/services/catalyst-catalog/internal/cache"
)

// Sovereign reads Blueprints from the Sovereign-curated Gitea Org.
// Layout matches the public mirror: one repo per Blueprint, with
// `blueprint.yaml` at the repo root.
type Sovereign struct {
	GC    *gitea.Client
	Org   string // "catalog-sovereign" by default
	Cache *cache.LRU
}

func NewSovereign(gc *gitea.Client, org string, c *cache.LRU) *Sovereign {
	return &Sovereign{GC: gc, Org: org, Cache: c}
}

func (s *Sovereign) Origin() Origin { return OriginSovereign }

func (s *Sovereign) List(ctx context.Context) ([]Blueprint, error) {
	repos, err := s.GC.ListOrgRepos(ctx, s.Org)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("source.sovereign.List: %w", err)
	}
	out := make([]Blueprint, 0, len(repos))
	for _, r := range repos {
		bp, err := s.fetch(ctx, r.Name)
		if err != nil {
			if IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("source.sovereign.List: read %s: %w", r.Name, err)
		}
		if bp == nil {
			continue
		}
		out = append(out, *bp)
	}
	return out, nil
}

func (s *Sovereign) Get(ctx context.Context, name, version string) (*Blueprint, error) {
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

func (s *Sovereign) fetch(ctx context.Context, name string) (*Blueprint, error) {
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
