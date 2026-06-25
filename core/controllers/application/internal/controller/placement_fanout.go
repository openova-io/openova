package controller

// placement_fanout.go — #3969 §8c/§8d: drive the per-cluster HelmRelease
// fan-out RENDER from the canonical desired-state placement
// (`spec.placement.targets[]` → the PlacementTarget model) instead of the
// legacy `Blueprint.spec.topology` / `BcpTopology` posture string resolved
// by `ResolveTopology`.
//
// # The gap this closes (§8c — the core write-path gap)
//
// Before this file the fan-out HRs were rendered EXCLUSIVELY from the
// legacy `TopologyVariant` returned by `ResolveTopology(spec.Topology,
// Blueprint.spec.topology, sovRegions)`. The new `Targets[]` /
// `effectivePlacementTargets` fed ONLY the read-model (the observed-target
// rollup + the backing-service cascade) — NOT the rendered HelmReleases. So
// a provision-WRITE / switchover / singleton→multi edit of
// `spec.placement.targets[]` changed the status read-model but NEVER moved
// a single HelmRelease. DoD items 2/3/4 (provision-write, switchover,
// singleton→multi) could not pass.
//
// This file builds a synthetic `TopologyVariant` straight from the
// desired-state `Targets[]` so the EXISTING, proven `render.FanoutHRs`
// renderer + the existing persistence loop project one HR per declared
// target cluster, with the per-target Primary|Standby role mapped onto the
// canonical active|passive|singleton HR role label. The renderer, the
// kubeConfig pivot (clusterregistry), the standby `replicas:0` overlay, the
// reconcile ordering, and the per-cluster status rollup are ALL reused
// untouched — this file is a pure ADAPTER between the two models.
//
// # §8d — legacy retirement
//
// Once an Application declares `Targets[]`, the legacy `Mode` /
// `BcpTopology` / `Blueprint.spec.topology` consumption is NOT used to
// drive its render: `placementVariantFromTargets` supersedes
// `ResolveTopology` for that Application. The legacy path stays as the
// FALLBACK only when `Targets[]` is empty (every pre-#3969 Application + the
// spine producer that still stamps the legacy posture string). No new logic
// is written against `Mode`/`BcpTopology`; the fields remain for back-compat
// read only.
//
// Ref #3969 §7.1, §7.3, §8c, §8d.

import (
	"github.com/openova-io/openova/core/controllers/application/internal/render"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// placementVariantFromTargets converts the canonical desired-state target
// list into the (BcpTopology, *TopologyVariant) pair the fan-out renderer
// consumes — WITHOUT consulting the legacy `Blueprint.spec.topology` or
// `Mode`. It is the §8c bridge: it lets `render.FanoutHRs` (the SOLE HR
// renderer) project the desired-state placement directly.
//
// Mapping:
//
//   - Variant.Placement.Clusters[] = the targets' Cluster IDs, in declared
//     order (the renderer keys per-cluster HR names + the kubeConfig pivot
//     off these).
//   - Variant.Placement.Roles{cluster → "active"|"passive"} = Primary maps
//     to active, Standby maps to passive. A single Primary with no standby
//     is left to the renderer's singleton fallback (roleFor: one cluster, no
//     role ⇒ singleton) so a 1-target placement renders the singleton HR
//     shape, byte-identical to the legacy singleton path.
//   - Variant.Placement.Tier = the targets' shared VCluster tier when every
//     target agrees on one (the common case — an Org's app lands on ONE
//     tier across regions). A mixed/empty tier folds to "" (host placement),
//     matching the legacy default.
//   - the topology label = DerivePattern(targets, capability) mapped onto
//     the BcpTopology enum, for the LabelTopology label + observability only
//     (it never gates the render — the per-target roles do).
//
// Returns (choice, variant). variant is always non-nil with a non-empty
// Clusters slice when targets is non-empty (the caller guards len>0).
func placementVariantFromTargets(
	targets []bpv1alpha1.PlacementTarget,
	capability bpv1alpha1.PlacementCapability,
) (bpv1alpha1.BcpTopology, *bpv1alpha1.TopologyVariant) {
	clusters := make([]string, 0, len(targets))
	roles := make(map[string]string, len(targets))
	tier := ""
	tierSeen := false
	tierAgreed := true

	for _, t := range targets {
		clusters = append(clusters, t.Cluster)
		switch t.Role {
		case bpv1alpha1.DataRolePrimary:
			roles[t.Cluster] = render.RoleActive
		case bpv1alpha1.DataRoleStandby:
			roles[t.Cluster] = render.RolePassive
		}
		// The PlacementTarget.VCluster carries the tier ("mgmt"|"dmz"|"rtz"
		// or "" = host). The fan-out renders per-cluster, but the host
		// write-namespace + kubeConfig pivot are tier-scoped, so we surface
		// a single shared tier when every target agrees.
		vc := bpv1alpha1.NormalizeVCluster(t.VCluster)
		if !tierSeen {
			tier, tierSeen = vc, true
		} else if vc != tier {
			tierAgreed = false
		}
	}
	if !tierAgreed {
		// Mixed tiers across targets — fold to host so the renderer omits
		// the tier-scoped pivot rather than picking an arbitrary winner.
		tier = ""
	}

	// A lone Primary with no standby is a singleton: drop the explicit
	// "active" role so render.roleFor's single-cluster fallback stamps
	// "singleton" (byte-identical to the legacy singleton path). With ≥2
	// clusters we keep the explicit roles so active/passive are stamped.
	if len(clusters) == 1 {
		roles = nil
	}

	variant := &bpv1alpha1.TopologyVariant{
		Placement: &bpv1alpha1.PlacementSpec{
			Tier:     tier,
			Clusters: clusters,
			Roles:    roles,
		},
	}

	choice := patternToBcpTopology(bpv1alpha1.DerivePattern(targets, capability))
	return choice, variant
}

// patternToBcpTopology maps the #3969 DERIVED pattern label onto the legacy
// BcpTopology enum, used ONLY for the HR `LabelTopology` value +
// observability rollups. It does not gate the render (the per-target roles
// do). The mapping is the obvious 1:1 between the two vocabularies.
func patternToBcpTopology(p bpv1alpha1.Pattern) bpv1alpha1.BcpTopology {
	switch p {
	case bpv1alpha1.PatternActiveActive:
		return bpv1alpha1.BcpActiveActive
	case bpv1alpha1.PatternActiveHotStandby:
		return bpv1alpha1.BcpActiveHotStandby
	case bpv1alpha1.PatternActivePassive:
		return bpv1alpha1.BcpActivePassive
	default:
		return bpv1alpha1.BcpSingleton
	}
}
