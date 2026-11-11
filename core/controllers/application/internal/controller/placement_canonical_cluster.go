package controller

import (
	"strings"

	"github.com/openova-io/openova/core/controllers/internal/clusterregistry"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// canonicalizeTargetClusters rewrites every placement target's `cluster` to
// the canonical cluster id of the target's REGION (ADR-0015 D3, Refs #7067):
// `<tier>-<region letter>`, the letter being the region's position in the
// region registry — exactly the ids the topology resolver derives for an
// Application created without targets (rtz-A, rtz-B, …).
//
// The cluster token names the per-region HelmRelease (`<app>-<cluster>`).
// The console's Topology Save sends its own cluster identifiers
// (`<deployment-id>`, `<deployment-id>-me-east-215-b-1`), so a placement edit
// RENAMED both releases: the old ones were pruned and new ones installed —
// the app went down mid-move (live hw307 2026-11-11). The region is the
// identity; the free-form cluster field is not. A target whose region is not
// in the registry is left unchanged (the renderer refuses it).
func (r *Reconciler) canonicalizeTargetClusters(targets []bpv1alpha1.PlacementTarget) []bpv1alpha1.PlacementTarget {
	if len(targets) == 0 {
		return targets
	}
	ids := r.Cfg.Regions.IDs()
	out := make([]bpv1alpha1.PlacementTarget, len(targets))
	for i, t := range targets {
		out[i] = t
		if clusterregistry.IsCanonical(t.Cluster) {
			continue
		}
		id, err := r.Cfg.Regions.Canonical(t.Region)
		if err != nil {
			continue
		}
		idx := -1
		for j, rid := range ids {
			if rid == id {
				idx = j
				break
			}
		}
		if idx < 0 || idx >= len(clusterregistry.KnownRegions()) {
			continue
		}
		tier := clusterregistry.TierRtz
		if vc := clusterregistry.Tier(strings.ToLower(strings.TrimSpace(t.VCluster))); clusterregistry.IsKnownTier(vc) {
			tier = vc
		}
		out[i].Cluster = clusterregistry.ClusterID{Tier: tier, Region: clusterregistry.KnownRegions()[idx]}.String()
	}
	return out
}
