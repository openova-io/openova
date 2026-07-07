package handler

// #4846 (Refs #4656 #4275) — the SOVEREIGN_PEER_CLUSTERMESH_NAMES substitute
// that catalyst-api stamps onto each region's bootstrap-kit Kustomization at the
// post-mesh cnpg-pair flip. The bp-postgres / bp-cnpg-pair charts consume it as
// `crossRegionPeerClusters` to render the identity-based cross-region DR
// CiliumNetworkPolicy (admits the ClusterMesh remote replica by
// io.cilium.k8s.policy.cluster — the k8s-netpol ipBlock it replaces was inert
// for ClusterMesh endpoints, proven on hw228).
//
// Invariant under test: each region's value carries the cluster.name(s) of the
// OTHER region(s) — NOT its own — as a YAML flow-sequence string; a single
// region yields "[]" so the chart renders NO CNP (byte-identical default).

import "testing"

func TestBuildPeerClusterMeshNamesValue(t *testing.T) {
	t.Run("two regions — each carries the OTHER's cluster name", func(t *testing.T) {
		// slots[0] = primary (DeriveClusterMeshName → <label>-mesh),
		// slots[1] = secondary (DeriveSecondaryClusterMeshName → <stem>-<region>).
		slots := []regionSlot{
			{key: "", clusterName: "hw228-mesh"},
			{key: "me-east-215-b", clusterName: "hw228-me-east-b"},
		}
		if got := buildPeerClusterMeshNamesValue(slots, 0); got != "[hw228-me-east-b]" {
			t.Fatalf("region-a (primary) peer names = %q, want [hw228-me-east-b] (the secondary's mesh name)", got)
		}
		if got := buildPeerClusterMeshNamesValue(slots, 1); got != "[hw228-mesh]" {
			t.Fatalf("region-b (secondary) peer names = %q, want [hw228-mesh] (the primary's mesh name)", got)
		}
	})

	t.Run("single region — empty flow sequence (no CNP)", func(t *testing.T) {
		slots := []regionSlot{{key: "", clusterName: "solo-mesh"}}
		if got := buildPeerClusterMeshNamesValue(slots, 0); got != "[]" {
			t.Fatalf("single-region peer names = %q, want [] (chart must render NO CiliumNetworkPolicy)", got)
		}
	})

	t.Run("three regions — each carries the two OTHER names, order preserved", func(t *testing.T) {
		slots := []regionSlot{
			{key: "", clusterName: "t129-mesh"},
			{key: "nbg1", clusterName: "t129-nbg"},
			{key: "hel1", clusterName: "t129-hel"},
		}
		if got := buildPeerClusterMeshNamesValue(slots, 0); got != "[t129-nbg,t129-hel]" {
			t.Fatalf("region-0 peer names = %q, want [t129-nbg,t129-hel]", got)
		}
		if got := buildPeerClusterMeshNamesValue(slots, 1); got != "[t129-mesh,t129-hel]" {
			t.Fatalf("region-1 peer names = %q, want [t129-mesh,t129-hel]", got)
		}
		if got := buildPeerClusterMeshNamesValue(slots, 2); got != "[t129-mesh,t129-nbg]" {
			t.Fatalf("region-2 peer names = %q, want [t129-mesh,t129-nbg]", got)
		}
	})

	t.Run("blank peer cluster name is skipped (never emits an empty list element)", func(t *testing.T) {
		slots := []regionSlot{
			{key: "", clusterName: "hw228-mesh"},
			{key: "b", clusterName: "  "}, // whitespace-only → skipped
		}
		if got := buildPeerClusterMeshNamesValue(slots, 0); got != "[]" {
			t.Fatalf("peer with blank clusterName must be skipped: got %q, want []", got)
		}
	})
}
