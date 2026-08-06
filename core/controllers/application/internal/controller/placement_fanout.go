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
// # §13 — arming the Continuum lease-witness off the desired-state targets
//
// The per-app Continuum CR producer (continuum.go buildContinuumPlan) is the
// thing that makes a Hot standby ARMED for promotion (§13): it mints a
// Continuum.dr.openova.io/v1 CR whose leaseClient the continuum-controller
// drives so exactly one region holds the write-lease at a time. That producer
// gates on `variant.Switchover.Mechanism != ""`. Before this change the
// synthetic variant carried ONLY a Placement block (no Switchover), so a
// `targets[]`-declared {Primary + Hot Standby} placement produced NO Continuum
// CR — the new desired-state model could declare a Hot standby but never arm
// it. synthesizeSwitchover closes that: when the targets describe a DR-capable
// placement (≥2 targets with ≥1 Standby), it stamps a Switchover block on the
// synthetic variant so the SAME producer mints the DR contract off the new
// model — keyed off the targets' roles, NOT the deprecated Blueprint.spec.
// topology posture. The mechanism resolves from the Blueprint's declared
// replication backend (when it still declares one) so a cnpg-pair app arms a
// cnpg-pair switchover and a stateless app arms the generic DNS-flip Promoter.
//
// Returns (choice, variant). variant is always non-nil with a non-empty
// Clusters slice when targets is non-empty (the caller guards len>0). When the
// placement is DR-capable the variant additionally carries a synthesized
// Switchover (+ Replication) block so the Continuum producer arms it.
func placementVariantFromTargets(
	targets []bpv1alpha1.PlacementTarget,
	capability bpv1alpha1.PlacementCapability,
	bpTopo *bpv1alpha1.Topology,
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

	// §13 — arm the Continuum lease-witness off the desired-state targets.
	// A DR-capable placement (≥2 targets, ≥1 Standby) gets a synthesized
	// Switchover (+ Replication) block so the per-app Continuum CR producer
	// (buildContinuumPlan) mints the DR contract. A singleton / active-active
	// placement gets no Switchover (the producer skips it — nothing to fail
	// over to / no primary-standby flip to drive).
	if sw, repl := synthesizeSwitchover(targets, choice, bpTopo); sw != nil {
		variant.Switchover = sw
		variant.Replication = repl
	}

	return choice, variant
}

// synthesizeSwitchover builds the Switchover (+ Replication) block that ARMS
// the per-app Continuum lease-witness for a DR-capable desired-state placement
// (§13). It returns (nil, nil) when the placement is NOT DR-capable — a
// singleton (one Primary, no Standby) or active-active (multi-master, no
// primary/standby flip to orchestrate) — so the Continuum producer skips it,
// preserving the existing "no CR for non-DR apps" invariant.
//
// DR-capable = a single-primary topology with ≥1 Standby target:
// active-hot-standby (Hot standby — a live, lease-armed replica) or
// active-passive (Cold standby — rebuild-on-failover). Both flow to the
// producer; the standby TYPE (Hot|Cold) is what the Continuum lease-witness
// uses to decide whether the standby is a live promotion target vs a
// rebuild-from-bucket follower.
//
// Mechanism resolution carries the Blueprint's DECLARED replication backend
// forward (when it still declares one for this pattern) so the synthesized
// switchover resolves to the right concrete mechanism:
//   - a cnpg-pair backend → cnpg-pair switchover (CNPG primary streaming flip);
//   - a raft backend       → raft-transition;
//   - any other / none     → stateless (the generic DNS-flip-only Promoter).
//
// When the Blueprint declares NO topology block at all (the pure-#3969 path —
// the placement is the SOLE source of truth), the mechanism defaults to
// `bp-continuum`, the authoring alias resolveSwitchoverMechanism maps onto the
// concrete mechanism by replication backend (stateless when none). This keeps
// the arming keyed off the targets' roles, never the deprecated posture enum.
func synthesizeSwitchover(
	targets []bpv1alpha1.PlacementTarget,
	choice bpv1alpha1.BcpTopology,
	bpTopo *bpv1alpha1.Topology,
) (*bpv1alpha1.SwitchoverSpec, *bpv1alpha1.ReplicationSpec) {
	// Only the two single-primary DR topologies have a switchover to arm.
	if choice != bpv1alpha1.BcpActiveHotStandby && choice != bpv1alpha1.BcpActivePassive {
		return nil, nil
	}
	// Defensive: a DR topology must carry ≥2 targets with ≥1 Standby. (The
	// pattern derivation already guarantees this for the two choices above,
	// but we re-check so a malformed target list can never arm an empty
	// hot-standby contract.)
	var primaries, standbys int
	for _, t := range targets {
		switch t.Role {
		case bpv1alpha1.DataRolePrimary:
			primaries++
		case bpv1alpha1.DataRoleStandby:
			standbys++
		}
	}
	if primaries == 0 || standbys == 0 {
		return nil, nil
	}

	// Carry forward the Blueprint's declared replication block for this
	// pattern, when it still declares one — that backend drives the concrete
	// mechanism. Absent ⇒ nil, and the bp-continuum alias resolves to
	// stateless (the generic DNS-flip Promoter).
	repl := blueprintReplicationFor(choice, bpTopo)

	mechanism := "bp-continuum" // authoring alias — resolved by backend below.
	if repl != nil {
		switch repl.Backend {
		case "cnpg-pair":
			mechanism = "cnpg-pair"
		case "raft", "openbao-raft", "raft-transition":
			mechanism = "raft-transition"
		}
	}
	return &bpv1alpha1.SwitchoverSpec{Mechanism: mechanism}, repl
}

// blueprintReplicationFor returns the Blueprint's declared replication block
// for the resolved pattern, when the Blueprint still carries a
// `spec.topology.perTopology[<choice>].replication` block. Returns nil when
// the Blueprint declares no topology (the pure-#3969 placement-only path) or
// no replication for this pattern — in which case the synthesized switchover
// arms the generic stateless Promoter. Read-only; never mutates bpTopo.
func blueprintReplicationFor(choice bpv1alpha1.BcpTopology, bpTopo *bpv1alpha1.Topology) *bpv1alpha1.ReplicationSpec {
	if bpTopo == nil || bpTopo.PerTopology == nil {
		return nil
	}
	v, ok := bpTopo.PerTopology[choice]
	if !ok {
		return nil
	}
	return v.Replication
}

// patternToBcpTopology maps the #3969 DERIVED pattern label onto the legacy
// BcpTopology enum, used ONLY for the HR `LabelTopology` value +
// observability rollups. It does not gate the render (the per-target roles
// do). The mapping is the obvious 1:1 between the two vocabularies.
//
// #5515 — `PatternNotReported` has NO honest BcpTopology counterpart (the
// enum carries no "unknown" member), and the `default:` arm below would map
// it onto `singleton` — re-introducing, one layer down, exactly the fail-open
// DerivePattern just closed. It cannot arrive here: this function is only
// reached from placementVariantFromTargets, which the application-controller
// calls at step §8c AFTER step 5.1 has run ValidatePlacement over the same
// target list and markFailed'd on `NoPrimary` — and a no-Primary list is the
// only input that derives `not-reported`. That ordering is the actual guard,
// so it is pinned by an executable test
// (TestPatternNotReported_5515_UnreachableInFanout) rather than left to a
// comment. The explicit case below exists so the fall-through can never
// silently absorb it if that ordering is ever changed.
func patternToBcpTopology(p bpv1alpha1.Pattern) bpv1alpha1.BcpTopology {
	switch p {
	case bpv1alpha1.PatternActiveActive:
		return bpv1alpha1.BcpActiveActive
	case bpv1alpha1.PatternActiveHotStandby:
		return bpv1alpha1.BcpActiveHotStandby
	case bpv1alpha1.PatternActivePassive:
		return bpv1alpha1.BcpActivePassive
	case bpv1alpha1.PatternSingleton:
		return bpv1alpha1.BcpSingleton
	default:
		// Includes PatternNotReported — unreachable per the ValidatePlacement
		// ordering above; kept as the conservative label rather than a
		// fabricated DR posture.
		return bpv1alpha1.BcpSingleton
	}
}
