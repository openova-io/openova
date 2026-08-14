// Package handler — continuum_standby_crossregion_6268.go (#6268 / UAT row 60).
//
// THE GAP. `augmentReplicationStandbyStatus` (continuum_dr_extras.go) resolves
// the standby half of a cnpg cluster-pair through `h.sovereignDynamicClient`,
// which is scoped to the deployment's REGION-A apiserver. In the 2-region
// active-hot-standby topology the replica half is a cnpg `Cluster` CR living on
// the REGION-B apiserver, so BOTH region-A resolution paths structurally miss
// it:
//
//   - cnpgPairStandbyForContinuum lists the pair label in region A and finds no
//     `openova.io/cnpg-role=replica` half → (_, false);
//   - findCNPGPairForApp requires BOTH halves from the SAME list → (nil, nil).
//
// The endpoint therefore fell through to the Continuum controller's own
// `status.standbyAvailable` probe (a weaker, second-hand provenance) or, when
// the CR carries no probe at all, to an "unverifiable" Warn — and
// `replicaPromotable` stayed at its zero value `false`, which the Topology tab
// renders as a DISABLED Switchover with "the replica is not promotable".
// Measured on hw296 (dep e689e3b34a75fdec, Org `walkfour`, app `r60fresh`):
// `walkfour/dr-r60fresh` carries neither `spec.cnpgPair` nor
// `status.standbyAvailable`, so neither fallback could fire either.
//
// THE LEVER. `h.k8sCache` already runs an informer for the `cnpgcluster` kind
// (k8scache/kinds.go DefaultKinds) against EVERY registered cluster — region B
// included. That is the same source `derivePlacementTargets`
// (applications_placement_runtime.go) fans out over, and it is why the
// `shared-pg` control already reports both legs with a populated cluster id
// while the DR status beside it could not. So the standby-availability oracle
// is one client swap, not a new subsystem.
//
// WHAT THIS FILE IS NOT. It does not relax any honesty invariant:
//
//   - it returns (_, false) — leaving the caller's existing honest-unknown /
//     relay chain untouched — unless BOTH halves of one pair were positively
//     observed. An absent replica is never an available one, and an absent
//     read is never a verdict (the #4901 / #4923 rule);
//   - it is NAMESPACE-SCOPED and refuses an empty namespace outright, and it is
//     DEPLOYMENT-SCOPED via clusterOwnedByDeployment, so it can never resolve a
//     pair belonging to another Organization or (on the mother, whose cache
//     holds every managed Sovereign) another Sovereign;
//   - an explicit `spec.cnpgPair.name` selects THAT pair or nothing — it never
//     silently substitutes a different pair that happens to share the namespace.
package handler

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// crossClusterStandbyState is the resolved standby posture of a cnpg
// cluster-pair read across the registered clusters rather than from one
// region's apiserver.
//
// `Available` and `Following` are DELIBERATELY separate axes. Available is
// "the standby is up" (the #4923 tri-state the red banner keys on). Following
// is "the standby is still a follower" — Ready AND `spec.replica.enabled`. A
// half that was PROMOTED by an earlier switchover is available but no longer
// streaming, so it must not re-arm the Switchover control against itself.
type crossClusterStandbyState struct {
	// PairName is the shared catalyst.openova.io/cnpg-pair label value.
	PairName string

	// PrimaryCluster / ReplicaCluster are the k8scache cluster ids each half
	// was observed in. They are surfaced in the health-gate message so an
	// operator can tell a cross-region verification from the controller-probe
	// relay that produces the same Pass otherwise, and they are the `cluster`
	// value the placement projection emits (the same identifier
	// derivePlacementTargets uses, so both surfaces name clusters alike).
	PrimaryCluster string
	ReplicaCluster string

	// PrimaryName / PrimaryRegion identify the primary half; ReplicaName /
	// ReplicaRegion the standby half. The regions come from each half's
	// `openova.io/region` label — live cluster placement, not config.
	PrimaryName   string
	PrimaryRegion string
	ReplicaName   string
	ReplicaRegion string

	// Available — cnpgStandbyAvailable on the replica half (Ready=True and,
	// when reported, >=1 ready instance). Same predicate the region-A path uses.
	Available bool

	// Following — the replica half is Ready AND still in replica mode.
	Following bool

	// LagSeconds — the replica half's own best-effort lag (0 when CNPG does
	// not report it on this version; the caller keeps its existing reading
	// rather than treating an unreported lag as a measured zero).
	LagSeconds int
}

// crossClusterCNPGPairStandby resolves the standby half of the cnpg
// cluster-pair backing `appName` in `namespace`, reading `cnpgcluster` from
// EVERY cluster this deployment owns in `h.k8sCache` — so a pair whose two
// halves live in DIFFERENT regions resolves, which is the whole point.
//
// `pairName` is the CR's `spec.cnpgPair.name` when it declares one ("" when it
// does not).
//
// Returns (_, false) — meaning "no determination", never "absent" — when the
// cache is not wired, the namespace is unknown, the deployment owns no
// registered cluster, or one half of the pair was not observed.
func (h *Handler) crossClusterCNPGPairStandby(
	urlID, namespace, pairName, appName string,
) (crossClusterStandbyState, bool) {
	if h.k8sCache == nil {
		return crossClusterStandbyState{}, false
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		// 🔒 Organization isolation: a cluster-wide lexical pick could resolve
		// to a DIFFERENT Organization's cnpg-pair, and this verdict arms a
		// destructive control. Refuse rather than guess — the same rule
		// findCNPGPairForApp states for its cluster-wide branch.
		return crossClusterStandbyState{}, false
	}
	sel, err := labels.Parse(cnpgPairLabel)
	if err != nil {
		return crossClusterStandbyState{}, false
	}

	primaryID := h.resolveChrootClusterID(urlID)
	depID, fqdn, _ := h.placementDeploymentIdentity(urlID)

	type observedHalf struct {
		obj     *unstructured.Unstructured
		cluster string
	}
	type pairHalves struct{ primary, replica observedHalf }
	byPair := map[string]*pairHalves{}
	appLabelled := map[string]bool{}
	pairNames := []string{}

	for _, cid := range h.k8sCache.Clusters() {
		// 🔒 Sovereign isolation: the mother's cache holds every managed
		// Sovereign's clusters (#3987), and namespace names repeat across them.
		if !clusterOwnedByDeployment(cid, primaryID, depID, fqdn) {
			continue
		}
		items, _, lerr := h.k8sCache.List(cid, "cnpgcluster", sel)
		if lerr != nil {
			// Unregistered kind / quarantined reflector — says nothing about
			// the standby, so contribute nothing rather than a false absence.
			continue
		}
		for _, it := range items {
			if it == nil || it.GetNamespace() != namespace {
				continue
			}
			lbls := it.GetLabels()
			pair := lbls[cnpgPairLabel]
			if pair == "" {
				continue
			}
			hv, ok := byPair[pair]
			if !ok {
				hv = &pairHalves{}
				byPair[pair] = hv
				pairNames = append(pairNames, pair)
			}
			switch lbls[cnpgRoleLabel] {
			case cnpgRolePrimary:
				if hv.primary.obj == nil {
					hv.primary = observedHalf{obj: it, cluster: cid}
				}
			case cnpgRoleReplica:
				if hv.replica.obj == nil {
					hv.replica = observedHalf{obj: it, cluster: cid}
				}
			}
			if appName != "" && lbls[cnpgAppLabel] == appName {
				appLabelled[pair] = true
			}
		}
	}
	sort.Strings(pairNames)

	chosen := ""
	if want := strings.TrimSpace(pairName); want != "" {
		// The CR NAMES its pair. Resolve that one or nothing — substituting a
		// same-namespace neighbour would report another database's standby as
		// this Continuum's.
		if _, ok := byPair[want]; !ok {
			return crossClusterStandbyState{}, false
		}
		chosen = want
	}
	if chosen == "" {
		for _, p := range pairNames {
			if appLabelled[p] {
				chosen = p
				break
			}
		}
	}
	if chosen == "" && len(pairNames) > 0 {
		// Namespace-scoped lexically-first — safe here for the same reason it
		// is safe in findCNPGPairForApp: the candidate set never left this
		// Organization's namespace.
		chosen = pairNames[0]
	}
	if chosen == "" {
		return crossClusterStandbyState{}, false
	}
	hv := byPair[chosen]
	if hv == nil || hv.primary.obj == nil || hv.replica.obj == nil {
		// A lone half is not a pair. Region B down → its replica object is not
		// observable → NO determination, so the caller's controller-probe relay
		// (which DOES go false during an outage) stays the authority.
		return crossClusterStandbyState{}, false
	}

	primary, replica := hv.primary.obj, hv.replica.obj
	replicaEnabled, _, _ := unstructured.NestedBool(replica.Object, "spec", "replica", "enabled")
	return crossClusterStandbyState{
		PairName:       chosen,
		PrimaryCluster: hv.primary.cluster,
		ReplicaCluster: hv.replica.cluster,
		PrimaryName:    primary.GetName(),
		PrimaryRegion:  primary.GetLabels()[cnpgRegionLabel],
		ReplicaName:    replica.GetName(),
		ReplicaRegion:  replica.GetLabels()[cnpgRegionLabel],
		Available:      cnpgStandbyAvailable(replica),
		Following:      cnpgClusterReady(replica) && replicaEnabled,
		LagSeconds:     cnpgClusterLag(replica),
	}, true
}

// mergeCrossClusterPairIntoTargets folds a pair resolved ACROSS clusters into
// the occupancy-derived placement target list, adding only the legs occupancy
// did not already supply.
//
// WHY THE PLACEMENT PATH NEEDS THIS TOO. `derivePlacementTargets` keys on the
// component's OWN Pods, and for an app whose stateful identity IS its CNPG pair
// those Pods carry the DATABASE's labels: measured on hw296, the Pods behind
// Catalog app `r60fresh` in `walkfour` are labelled
// `app.kubernetes.io/instance=postgres` / `app.kubernetes.io/name=postgresql`,
// so `podBelongsToComponent(p, "r60fresh", …)` is false for every one of them
// and occupancy yields NO Primary. (`shared-pg` only escapes this because its
// app name happens to equal its CNPG instance label.) #6271 added the
// Primary-emitting term for exactly this case, but gated it on
// findCNPGPairForApp — which needs both halves out of the REGION-A list and so
// can never fire for a genuinely 2-region pair. Same seam, same swap.
//
// Pure by design, mirroring mergeCNPGPairIntoTargets: the decision is which
// legs get emitted, so keeping it free of the client makes it directly testable.
//
// The `cluster` field carries the k8scache CLUSTER ID (not the CNPG Cluster CR
// name), which is what `derivePlacementTargets` puts there — so a projected leg
// and an occupancy-derived leg name their cluster the same way.
//
// Callers must have already established that `xs` holds two DISTINCT non-empty
// regions — the same invariant deriveLiveContinuumRecord enforces.
func mergeCrossClusterPairIntoTargets(targets []bpv1.PlacementTarget, xs crossClusterStandbyState) []bpv1.PlacementTarget {
	// Already represented as a Standby in the replica region — nothing to add.
	for _, t := range targets {
		if t.Role == bpv1.DataRoleStandby && t.Region == xs.ReplicaRegion {
			return targets
		}
	}
	if !hasPrimaryTarget(targets) {
		targets = append(targets, bpv1.PlacementTarget{
			Region:  xs.PrimaryRegion,
			Cluster: xs.PrimaryCluster,
			Role:    bpv1.DataRolePrimary,
		})
	}
	return append(targets, bpv1.PlacementTarget{
		Region:      xs.ReplicaRegion,
		Cluster:     xs.ReplicaCluster,
		Role:        bpv1.DataRoleStandby,
		StandbyType: bpv1.StandbyHot,
	})
}
