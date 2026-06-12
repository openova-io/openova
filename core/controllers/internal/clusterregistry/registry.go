// Package clusterregistry resolves the canonical 6-cluster reference
// Sovereign cluster IDs (mgmt-A / mgmt-B / dmz-A / dmz-B / rtz-A / rtz-B)
// to the kubeConfig Secret the application-controller stamps on a
// fanned-out HelmRelease (`spec.kubeConfig.secretRef.name`).
//
// # Why this package exists (#3375 DoD-4)
//
// The Blueprint topology declaration (spec.topology.perTopology.<bcp>.
// placement.clusters[]) names canonical cluster IDs straight out of the
// agreement table (docs/topology-matrix.md): a tier prefix (mgmt|dmz|rtz)
// + a region suffix (-A = the FIRST region, -B = the SECOND region). The
// fan-out renderer (core/controllers/application/internal/render/
// fanout.go) carries a `KubeConfigSecretFor func(cluster string)
// (secretName, secretNamespace string)` seam so each per-cluster HR can
// pivot into the right cluster's apiserver via Flux v2's kubeConfig
// secretRef (the G92.1 #2674 pivot pattern). Before this package that
// seam resolved every ID to `vc-<id>` — which is WRONG for the -B IDs:
// `mgmt-B` is a DIFFERENT region's cluster, not a same-host vCluster
// named `vc-mgmt-b` (which never exists). That is the "populated-by-
// nobody" gap #3375 §3 (a) calls out.
//
// # The split-side model (the proven cross-region convention)
//
// A 2-region Sovereign is TWO SEPARATE k3s clusters joined by Cilium
// ClusterMesh — NOT one stretched cluster (see platform/cnpg-pair/chart
// header + clusters/_template/bootstrap-kit/16b-bp-cnpg-pair.yaml). The
// proven cross-region primitive (cnpg-pair, hw128 region-kill PASS) is
// applied on BOTH control planes, and each region's own Flux renders
// ITS half (cnpgPair.side primary|replica) — the two halves connect by
// ClusterMesh global Services, never by one control plane fanning an HR
// into the other via a remote kubeconfig.
//
// This package encodes that convention precisely:
//
//   - A cluster ID in the SAME region as the controller resolves to the
//     local tier's vCluster Secret (the existing G92.1 pivot) — or to
//     the host cluster when the tier has no vCluster.
//   - A cluster ID in a DIFFERENT region resolves to a remote-region
//     kubeconfig Secret ONLY when the operator has explicitly wired one
//     (Resolver.RemoteRegionSecrets). Absent that, it resolves to the
//     empty string, which the fan-out renderer reads as "omit the
//     kubeConfig block" — i.e. the split-side default where the remote
//     region's own Flux owns its half. This is the safe, IaC-pure
//     behaviour: nothing requires the local catalyst-api to reach into
//     the remote cluster.
//
// The package is pure data + pure functions: no K8s client, no I/O. It
// is unit-testable in isolation and imported by the application-
// controller's Config wiring.
//
// Refs #3375 (DoD-4) · #3241/#3307/#3322 (ClusterMesh) · #1101 (cnpg-pair).
package clusterregistry

import (
	"fmt"
	"sort"
	"strings"
)

// Tier is the canonical cluster-tier prefix of a cluster ID.
type Tier string

const (
	// TierMgmt — Catalyst control-plane cluster (mgmt-A / mgmt-B).
	TierMgmt Tier = "mgmt"
	// TierDmz — DMZ tenant workload cluster (dmz-A / dmz-B).
	TierDmz Tier = "dmz"
	// TierRtz — Regulated tenant workload cluster (rtz-A / rtz-B).
	TierRtz Tier = "rtz"
)

// KnownTiers is the LOCKED tier set. Mirrors the CRD placement.tier
// enum (products/catalyst/chart/crds/blueprint.yaml) and the agreement
// table's cluster columns.
func KnownTiers() []Tier { return []Tier{TierMgmt, TierDmz, TierRtz} }

// IsKnownTier reports whether t is one of the three canonical tiers.
func IsKnownTier(t Tier) bool {
	switch t {
	case TierMgmt, TierDmz, TierRtz:
		return true
	}
	return false
}

// Region is the region suffix of a canonical cluster ID. "A" is the
// FIRST region (the primary control plane in the agreement table's
// active-hot-standby rows), "B" is the SECOND region.
type Region string

const (
	// RegionA — the first region (primary in active-hot-standby rows).
	RegionA Region = "A"
	// RegionB — the second region (standby in active-hot-standby rows).
	RegionB Region = "B"
)

// KnownRegions is the LOCKED region set for the 6-cluster reference
// Sovereign. A larger fleet would extend this with C/D… in a future
// ADR; the agreement table fixes it at A/B today.
func KnownRegions() []Region { return []Region{RegionA, RegionB} }

// IsKnownRegion reports whether r is one of the canonical regions.
func IsKnownRegion(r Region) bool {
	switch r {
	case RegionA, RegionB:
		return true
	}
	return false
}

// ClusterID is a parsed canonical cluster identifier (e.g. mgmt-A).
type ClusterID struct {
	Tier   Tier
	Region Region
}

// String renders the canonical wire form `<tier>-<region>` (e.g.
// "mgmt-A"). It is the inverse of Parse and matches the CRD pattern
// `^(mgmt|dmz|rtz)-[A-Z0-9]+$` exactly.
func (c ClusterID) String() string {
	return fmt.Sprintf("%s-%s", c.Tier, c.Region)
}

// CanonicalIDs returns every cluster ID of the 6-cluster reference
// Sovereign, in a stable order (mgmt-A, mgmt-B, dmz-A, dmz-B, rtz-A,
// rtz-B). Used by tests + the lint that asserts a Blueprint's declared
// clusters[] are all canonical.
func CanonicalIDs() []ClusterID {
	out := make([]ClusterID, 0, len(KnownTiers())*len(KnownRegions()))
	for _, t := range KnownTiers() {
		for _, r := range KnownRegions() {
			out = append(out, ClusterID{Tier: t, Region: r})
		}
	}
	return out
}

// CanonicalIDStrings returns CanonicalIDs() as the wire strings, sorted.
func CanonicalIDStrings() []string {
	ids := CanonicalIDs()
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	sort.Strings(out)
	return out
}

// Parse splits a canonical cluster ID string into its Tier + Region.
// Accepts the exact `<tier>-<region>` form the agreement table + the
// CRD pattern use; the region suffix is upper-cased on parse so both
// "mgmt-b" and "mgmt-B" resolve identically (the CRD pattern only
// admits upper-case, but the controller tolerates either defensively).
//
// Returns an error for an unknown tier, an unknown region, or a
// malformed string — the caller surfaces this as Ready=False
// reason=Invalid (same shape as the topology resolver).
func Parse(id string) (ClusterID, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ClusterID{}, fmt.Errorf("clusterregistry: empty cluster ID")
	}
	i := strings.LastIndex(id, "-")
	if i <= 0 || i == len(id)-1 {
		return ClusterID{}, fmt.Errorf("clusterregistry: %q is not <tier>-<region>", id)
	}
	tier := Tier(id[:i])
	region := Region(strings.ToUpper(id[i+1:]))
	if !IsKnownTier(tier) {
		return ClusterID{}, fmt.Errorf("clusterregistry: %q has unknown tier %q (want one of %v)", id, tier, KnownTiers())
	}
	if !IsKnownRegion(region) {
		return ClusterID{}, fmt.Errorf("clusterregistry: %q has unknown region %q (want one of %v)", id, region, KnownRegions())
	}
	return ClusterID{Tier: tier, Region: region}, nil
}

// IsCanonical reports whether id parses to a known tier + region.
func IsCanonical(id string) bool {
	_, err := Parse(id)
	return err == nil
}

// TierVCluster carries the local-region resolution for one tier: the
// host namespace the tier's vCluster pods live in + the kubeconfig
// Secret the vCluster chart exports. Empty KubeconfigSecret = the tier
// installs onto the host cluster directly (no kubeConfig pivot) — the
// substrate case (cilium, flux, kyverno…).
//
// This mirrors the application-controller's VClusterPlacement so the
// registry can be seeded from the existing Config without a second
// source of truth.
type TierVCluster struct {
	HostNamespace    string
	KubeconfigSecret string
}

// RemoteRegionSecret names a remote-region kubeconfig Secret.
type RemoteRegionSecret struct {
	Name      string
	Namespace string
}

// Resolver resolves a canonical cluster ID to (secretName,
// secretNamespace) for the fan-out KubeConfigSecretFor seam.
type Resolver struct {
	// LocalRegion is the region this controller instance runs in
	// (RegionA on the primary mgmt cluster; RegionB on the secondary).
	// Read from the controller's environment (SOVEREIGN_REGION_ROLE:
	// primary→A, secondary→B). Defaults to RegionA.
	LocalRegion Region

	// TierVClusters maps a Tier to its local vCluster placement. Seeded
	// from the application-controller's VClusterPlacements so the
	// same-region resolution reuses the proven G92.1 pivot.
	TierVClusters map[Tier]TierVCluster

	// RemoteRegionSecrets maps a non-local Region to the kubeconfig
	// Secret (name + namespace) the operator wired for cross-region
	// fan-out, when one exists. Absent an entry, a non-local cluster ID
	// resolves to the empty string — the split-side default where the
	// remote region's own Flux owns its half (#3375 §3 (b), the proven
	// cnpg-pair convention). Operators populate this ONLY for the rare
	// rows that genuinely require one control plane to write into the
	// other (none in the §6 reference set).
	RemoteRegionSecrets map[Region]RemoteRegionSecret
}

// SecretFor implements the fan-out KubeConfigSecretFor contract:
// returns (secretName, secretNamespace) for the given canonical cluster
// ID. A non-canonical ID, or a cluster that resolves to host/split-side
// placement, returns ("", "") so the fan-out renderer omits the
// kubeConfig block (HR targets the local cluster / the remote region's
// own Flux owns it).
//
// Resolution:
//
//	same region, tier has a vCluster   → (tier vCluster Secret, its ns)
//	same region, tier on host          → ("", "")  [substrate; no pivot]
//	other region, remote Secret wired  → (remote Secret, its ns)
//	other region, no remote Secret     → ("", "")  [split-side default]
//	non-canonical ID                   → ("", "")  [renderer ignores]
func (r Resolver) SecretFor(clusterID string) (string, string) {
	id, err := Parse(clusterID)
	if err != nil {
		// Non-canonical (e.g. a legacy bare cluster name) — let the
		// renderer omit the pivot rather than fabricate a Secret.
		return "", ""
	}
	local := r.LocalRegion
	if local == "" {
		local = RegionA
	}
	if id.Region == local {
		// Same region — reuse the local tier vCluster pivot.
		tv, ok := r.TierVClusters[id.Tier]
		if !ok || tv.KubeconfigSecret == "" {
			// No vCluster for this tier ⇒ host placement, no pivot.
			return "", ""
		}
		return tv.KubeconfigSecret, tv.HostNamespace
	}
	// Cross-region — only when an operator explicitly wired a remote
	// kubeconfig. Otherwise split-side: the remote region's Flux owns it.
	if rs, ok := r.RemoteRegionSecrets[id.Region]; ok && rs.Name != "" {
		return rs.Name, rs.Namespace
	}
	return "", ""
}
