package controller

// cnpg_crossregion_narrowing_6268_test.go — #6268.
//
// #4282 measured a real defect and fixed it with a blunt instrument: a
// CNPG-bearing Blueprint placed in a vCluster tier had `fanoutKubeSecretFor`
// set to nil, which removes the kubeConfig resolution for EVERY leg of the
// Application.
//
// Its warrant is a statement about VCLUSTERS — "a vCluster apiserver registers
// no postgresql.cnpg.io CRD and runs no cnpg operator, so the chart's
// capability-gated cluster.yaml renders zero Cluster CRs". A CROSS-REGION
// cluster ID does not name a vCluster: `rtz-B` is a DIFFERENT REGION's HOST
// cluster, which on hw296 (dep e689e3b34a75fdec) runs
// `cnpg-system/cnpg-cloudnative-pg` and registers
// `clusters.postgresql.cnpg.io` — read live in region B. Suppressing its
// resolution suppressed nothing #4282 was about.
//
// These two tests are a matched pair on ONE seam, so neither can be satisfied
// by weakening the other:
//
//	TestHostCNPGRule_StillSuppressesTheSameRegionVClusterPivot
//	  the #4282 invariant itself. PASSES on origin/main (where the rule nils
//	  everything) and must keep passing — a fix that simply deleted the rule
//	  reddens exactly this.
//
//	TestHostCNPGRule_NoLongerSuppressesTheCrossRegionLeg
//	  RED on origin/main: with `fanoutKubeSecretFor = nil` there is nothing to
//	  call, so no cross-region leg can resolve anything, ever.

import (
	"testing"

	"github.com/openova-io/openova/core/controllers/internal/clusterregistry"
)

// crossRegionResolver is a registry seeded the way an operator would if they
// had wired a remote-region kubeconfig: the local tier keeps its vCluster
// pivot, and region B resolves to a remote Secret.
func crossRegionResolver() clusterregistry.Resolver {
	return clusterregistry.Resolver{
		LocalRegion: clusterregistry.RegionA,
		TierVClusters: map[clusterregistry.Tier]clusterregistry.TierVCluster{
			clusterregistry.TierRtz: {HostNamespace: "rtz", KubeconfigSecret: "vc-rtz"},
		},
		RemoteRegionSecrets: map[clusterregistry.Region]clusterregistry.RemoteRegionSecret{
			clusterregistry.RegionB: {Name: "region-b-kubeconfig", Namespace: "catalyst"},
		},
	}
}

func narrowingReconciler() *Reconciler {
	return &Reconciler{Cfg: Config{LocalRegion: "A"}}
}

// TestHostCNPGRule_StillSuppressesTheSameRegionVClusterPivot pins the #4282
// invariant. `rtz-A` is this controller's OWN region, so the tier's vCluster
// Secret (`vc-rtz`) is exactly the pivot that lands the CNPG Cluster CR in an
// apiserver with no cnpg operator and no CRD. It must still resolve to nothing.
//
// PASSES on origin/main. Its job is to make "just delete the #4282 rule"
// unavailable as a way of satisfying the second test.
func TestHostCNPGRule_StillSuppressesTheSameRegionVClusterPivot(t *testing.T) {
	r := narrowingReconciler()
	narrowed := r.hostCNPGKubeSecretFor(crossRegionResolver().SecretFor)

	name, ns := narrowed("rtz-A")
	if name != "" || ns != "" {
		t.Errorf("#4282: the SAME-region leg rtz-A resolved to kubeConfig Secret %q/%q, want "+
			"(\"\",\"\") — pivoting a CNPG-bearing chart into a vCluster renders a hollow "+
			"InstallSucceeded, because the vCluster apiserver registers no postgresql.cnpg.io "+
			"CRD and the chart's cluster.yaml is capability-gated on it", ns, name)
	}
}

// TestHostCNPGRule_NoLongerSuppressesTheCrossRegionLeg is the #6268 half.
//
// RED on origin/main: the rule there is `fanoutKubeSecretFor = nil`, so a
// cross-region cluster ID has no resolver to reach at all — the leg is denied
// its resolution by a rule whose reason does not apply to it.
func TestHostCNPGRule_NoLongerSuppressesTheCrossRegionLeg(t *testing.T) {
	r := narrowingReconciler()
	narrowed := r.hostCNPGKubeSecretFor(crossRegionResolver().SecretFor)

	name, ns := narrowed("rtz-B")
	if name != "region-b-kubeconfig" || ns != "catalyst" {
		t.Errorf("#6268: the CROSS-region leg rtz-B resolved to kubeConfig Secret %q/%q, want "+
			"catalyst/region-b-kubeconfig — #4282's warrant is about vCLUSTERS, and rtz-B is a "+
			"different REGION's HOST cluster, which runs its own cnpg operator and registers "+
			"the CRD (hw296 region B: cnpg-system/cnpg-cloudnative-pg, "+
			"clusters.postgresql.cnpg.io). Nilling the whole seam denied this leg a resolution "+
			"for a reason that does not apply to it", ns, name)
	}
}

// TestHostCNPGRule_TolerantOfANilBaseAndANonCanonicalID — the two shapes the
// wrapper must not turn into a panic or a fabricated Secret. A legacy bare
// cluster name is not a canonical `<tier>-<region>` ID and must resolve to
// nothing rather than being treated as cross-region; a nil base is "nothing to
// delegate to", which is the pre-#6268 behaviour and never an error.
func TestHostCNPGRule_TolerantOfANilBaseAndANonCanonicalID(t *testing.T) {
	r := narrowingReconciler()

	if name, ns := r.hostCNPGKubeSecretFor(nil)("rtz-B"); name != "" || ns != "" {
		t.Errorf("#6268: a nil base resolved rtz-B to %q/%q, want (\"\",\"\") — with no "+
			"resolver to delegate to there is nothing to return", ns, name)
	}
	if name, ns := r.hostCNPGKubeSecretFor(crossRegionResolver().SecretFor)("legacy-cluster-name"); name != "" || ns != "" {
		t.Errorf("#6268: a non-canonical cluster ID resolved to %q/%q, want (\"\",\"\") — an "+
			"unparseable ID has no region, so calling it cross-region would fabricate a pivot",
			ns, name)
	}
}
