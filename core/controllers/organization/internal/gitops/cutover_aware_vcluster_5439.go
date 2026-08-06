package gitops

// Cutover-aware vCluster/app IMAGE registry host (#5439) — the field #5527
// did not cover.
//
// THE DEFECT (hw292 live, 2026-08-06, cutoverComplete=true since
// 2026-08-03T08:12:04Z): PR #5529 (#5527) made the OCI *chart* base this
// module's siblings emit cutover-aware, but the per-Org vCluster manifest's
// IMAGE registry host stayed a compile-time literal — `harbor.openova.io`,
// the MOTHERSHIP Harbor. Render() writes `vcluster/vcluster.yaml` into the
// per-Org Gitea tree on EVERY Organization reconcile, so a Sovereign that has
// genuinely passed its cutover (including the step-08 deny-egress proof) has
// the mothership host written back into a Flux-owned source by the next org
// mutation. Verified on the live object, not by reading the template:
//
//	kubectl -n uatco get helmrelease vcluster -o json | jq -r '.spec.values'
//	  "coredns":{"deployment":{"image":"harbor.openova.io/proxy-dockerhub/coredns/coredns:1.11.3"}}
//	  "image":{"registry":"harbor.openova.io"
//	  "statefulSet":{"image":{"registry":"harbor.openova.io"
//
// This is the SAME class as #5439's named defect (a control-plane writer
// re-asserting a mothership reference after cutoverComplete), one field over.
// The invariant (#5527, restated): no Flux-owned object may be pivoted only at
// the object layer — the pivot must land in whatever source Flux asserts from.
//
// WHY THE CHART DEFAULT CANNOT FIX IT: products/catalyst/chart/templates/
// controllers/organization-controller-deployment.yaml ALWAYS stamps
// CATALYST_VCLUSTER_IMAGE_REGISTRY (`| default "harbor.openova.io"`), so the
// Go-side zero-value default is dead code and the configured value is the
// mothership literal on every Sovereign from birth. For the same reason a
// cutover `kubectl set env` on THAT var is not durable: it IS present in the
// chart manifest, so the next Flux-driven helm upgrade reverts it (unlike
// #5527's CATALYST_LOCAL_REGISTRY_URL, which is absent from every chart
// manifest and therefore survives a 3-way strategic merge). The durable seam
// has to be here, in the generator.
//
// THE SEAM: cutover step-07 Phase 3e (added with this fix) stamps
// CATALYST_LOCAL_IMAGE_REGISTRY_HOST=harbor.<sovereign-fqdn> onto the
// organization-controller, provisioning and catalyst-api Deployments. That
// var appears in NO chart manifest, so it survives restarts and helm/Flux
// 3-way merges; `kubectl set env` rolls the Deployment, so the regenerating
// process always carries the fact. The value is byte-identical to the host
// step-10 (vcluster-registry-pivot) already pivots the three platform
// vClusters to — `target_host="harbor.${SOVEREIGN_FQDN}"` — so the generated
// source and the pivoted objects agree and Flux has nothing to drift-correct.
//
// Why NOT the neighbouring candidates:
//
//   - SovereignFQDN / HostCluster alone: set from BIRTH on every Sovereign, so
//     branching on it would point a PRE-cutover Sovereign's vClusters at a
//     local Harbor whose proxy-cache projects do not exist yet (step-02) and
//     which holds no mirrored images (step-03) — every per-Org vCluster would
//     ImagePullBackOff during the whole pre-cutover life of the Sovereign.
//   - The #3667 durable seal (cutoverComplete=true): flips only AFTER step-08's
//     deny-egress hold passes, while the generated source must already be local
//     DURING the hold. Same reasoning #5527 recorded.
//   - Rewriting CATALYST_VCLUSTER_IMAGE_REGISTRY in the bootstrap-kit overlay:
//     the overlay is rendered once at prov time from a pre-cutover context; it
//     has no knowledge of when (or whether) the operator fires the cutover.
//
// Refs #5439 #5527 #4885.

import (
	"os"
	"strings"
)

// mothershipHarborHost is the bootstrap (pre-cutover) image-registry host
// every generator in this program ships as its default — the OpenOva
// mothership Harbor. Its presence in a post-cutover render IS the defect.
const mothershipHarborHost = "harbor.openova.io"

// resolveVClusterImageRegistry is the pure resolution core (unit-tested
// directly, no env):
//
//   - configured non-empty AND not the mothership literal → an operator (or a
//     future cutover step) has named an explicit host; it wins outright
//     (Inviolable Principle #4).
//   - localImageRegistryHost non-empty → the step-07 Phase 3e stamp, the exact
//     final host; normalised (scheme + trailing slash stripped) and returned.
//   - else a pivot fact (localRegistryURL — step-07 Phase 3d's chart OCI base
//     — or the step-07 Phase 3b issuer pair) AND a resolvable FQDN →
//     harbor.<fqdn>, matching step-10's target_host exactly.
//   - a pivot fact WITHOUT an FQDN is an inconsistent state this generator
//     cannot mint a registry host from — fail safe to the configured host
//     rather than emit one that resolves nowhere.
//   - no fact → the configured host, unchanged. Pre-cutover output is
//     byte-identical to the historical render, so Flux sees zero drift.
func resolveVClusterImageRegistry(configured, localImageRegistryHost, localRegistryURL, pinIssuer, handoverIssuer string, fqdnCandidates ...string) string {
	cfg := strings.TrimSpace(configured)
	if cfg == "" {
		cfg = mothershipHarborHost
	}
	if cfg != mothershipHarborHost {
		return cfg
	}
	if h := normalizeRegistryHost(localImageRegistryHost); h != "" {
		return h
	}
	if strings.TrimSpace(localRegistryURL) == "" &&
		strings.TrimSpace(pinIssuer) == "" &&
		strings.TrimSpace(handoverIssuer) == "" {
		return cfg
	}
	for _, c := range fqdnCandidates {
		if f := strings.TrimSpace(c); f != "" {
			return "harbor." + f
		}
	}
	return cfg
}

// normalizeRegistryHost strips any URL scheme and trailing slashes/paths so a
// stamp supplied as `https://harbor.<fqdn>/` resolves to the bare host an
// image reference needs.
func normalizeRegistryHost(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	for _, scheme := range []string{"https://", "http://", "oci://"} {
		s = strings.TrimPrefix(s, scheme)
	}
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return ""
	}
	return s
}

// vclusterImageRegistryFor reads the process env and resolves the image
// registry host the rendered per-Org manifests declare. Called at render time
// (per reconcile), so the value tracks the Deployment's CURRENT env — the pod
// rolls when step-07 Phase 3e stamps it, and every subsequent regeneration
// emits the local host.
func vclusterImageRegistryFor(configured string, fqdnCandidates ...string) string {
	return resolveVClusterImageRegistry(
		configured,
		os.Getenv("CATALYST_LOCAL_IMAGE_REGISTRY_HOST"),
		os.Getenv("CATALYST_LOCAL_REGISTRY_URL"),
		os.Getenv("CATALYST_PIN_ISSUER"),
		os.Getenv("CATALYST_HANDOVER_JWT_ISSUER"),
		fqdnCandidates...,
	)
}
