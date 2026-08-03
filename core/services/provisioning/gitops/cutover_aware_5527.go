package gitops

// Cutover-aware bp-* catalog OCI base (#5527) — the per-Org tree emitters'
// half of the fix PR #5529 shipped for the catalyst-api org-tenants writer.
//
// THE DEFECT (hw291 live, 2026-07-30): every OCI HelmRepository this module
// emits declared a hardcoded `url: oci://ghcr.io/openova-io`. Cutover step-06
// (helmrepository-patches) pivots the LIVE objects to the Sovereign-local
// Harbor, but Flux drift-corrects them straight back from the generated git
// source — and the NEXT org mutation regenerates the tree pointing at ghcr
// again, silently re-tethering a cut-over Sovereign (Pillar-5 violation;
// step-08's deny-egress hold fails with `OFFENDER ...=oci://ghcr.io/...`).
// The invariant (#5527): no Flux-owned object may be pivoted only at the
// object layer — the pivot must land in whatever source Flux asserts from.
//
// THE SEAM: unlike catalyst-api (whose step-07 Phase-3b issuer env pair is an
// in-process pivot fact, see PR #5529), the provisioning Deployment
// (org-services/provisioning, products/catalyst/chart/templates/org-services/
// provisioning.yaml) is not touched by step-07's issuer stamp. So step-07
// Phase 3d (added with this fix) stamps the EXPLICIT override —
// CATALYST_LOCAL_REGISTRY_URL=oci://registry.<sovereign-fqdn>/openova-io —
// onto the provisioning Deployment via `kubectl set env`. That is precisely
// the forward-compatible seam PR #5529 designed: "a non-empty override is
// itself the pivot fact, so a future cutover step-07 that stamps the exact
// value flips the generator with no code change". The `kubectl set env`
// triggers a rollout, so the regenerating process always carries the fact,
// and the env entry survives pod restarts + helm/Flux 3-way merges (env
// lists strategic-merge by name; an entry absent from both chart manifests
// is preserved) — the same durability catalyst-api's post-cutover GitOps
// loop already stakes on step-07's CATALYST_GITOPS_REPO_URL patch.
//
// Why NOT the neighbouring candidates:
//
//   - SOVEREIGN_FQDN alone: set from BIRTH on every Sovereign (the chart
//     stamps it pre-cutover), so branching on it would emit local registry
//     URLs on a pre-cutover Sovereign whose Harbor holds no charts yet.
//   - The #3667 durable seal (secret/catalyst/cutover-complete in OpenBao,
//     mirrored as cutoverComplete=true in the self-sovereign-cutover-status
//     ConfigMap): this process has no OpenBao client, no ConfigMap RBAC —
//     and, decisive even with both, the seal flips only AFTER step-08's
//     deny-egress hold passes, while the generated source must already be
//     local DURING the hold (the hw291 wedge was exactly an org-mutation
//     window between step-06 and the seal). The step-07 stamp covers that
//     window: it lands after step-03's harbor-prewarm, so the local Harbor
//     already serves every chart these trees sourceRef.
//   - Probing the catalyst-api Deployment's issuer envs over the K8s API at
//     render time: a transient apiserver hiccup mid-mutation would fail
//     safe to ghcr — i.e. nondeterministically reproduce the exact silent
//     re-tether this issue is about. A process-local env fact renders
//     deterministically.

import (
	"os"
	"strings"
)

// orgBPCatalogGHCRBase is the pre-cutover (public catalog) OCI base every
// emitted bp-* HelmRepository declares — byte-identical to the historical
// hardcode so pre-cutover output produces zero Flux drift.
const orgBPCatalogGHCRBase = "oci://ghcr.io/openova-io"

// resolveCatalogOCIBase is the pure resolution core (unit-tested directly,
// no env):
//
//   - localRegistryURL non-empty → the normalized override wins outright
//     (the step-07 Phase-3d stamp; also Inviolable Principle #4's operator
//     escape hatch). Missing `oci://` scheme is prefixed, a trailing slash
//     is trimmed, an `https://`/`http://` scheme is swapped for `oci://`
//     (the value derives from sovereign.harborPublicURL, which carries
//     https).
//   - else pinIssuer/handoverIssuer (the step-07 Phase-3b pair, present if
//     a future train stamps this Deployment with the issuer fact instead)
//     non-empty AND an FQDN resolvable → oci://registry.<fqdn>/openova-io,
//     the SAME value shape step-06 patches the live objects to (hw292:
//     oci://registry.hw292.omani.works/openova-io), per the platform canon
//     `registry.<fqdn>` (bp-harbor HTTPRoute host,
//     clusters/_template/bootstrap-kit/19-harbor.yaml).
//   - fact without an FQDN is an inconsistent state this generator cannot
//     mint a registry host from — fail safe to the public catalog rather
//     than emit a URL that resolves nowhere.
//   - no fact → the public catalog (mothership / Catalyst-Zero /
//     pre-cutover Sovereign).
//
// secretRef stays `ghcr-pull` in both phases: the cutover rewrites that
// Secret's CONTENTS to local-registry credentials without renaming it
// (step-06 parity — the pivoted objects on hw291/hw292 keep the name).
func resolveCatalogOCIBase(localRegistryURL, pinIssuer, handoverIssuer, sovereignFQDN, otechFQDN string) string {
	if v := strings.TrimSpace(localRegistryURL); v != "" {
		v = strings.TrimPrefix(v, "https://")
		v = strings.TrimPrefix(v, "http://")
		if !strings.HasPrefix(v, "oci://") {
			v = "oci://" + v
		}
		return strings.TrimSuffix(v, "/")
	}
	if strings.TrimSpace(pinIssuer) == "" && strings.TrimSpace(handoverIssuer) == "" {
		return orgBPCatalogGHCRBase
	}
	fqdn := strings.TrimSpace(sovereignFQDN)
	if fqdn == "" {
		// Synonym order matches catalyst-api's sovereign_self.go.
		fqdn = strings.TrimSpace(otechFQDN)
	}
	if fqdn == "" {
		return orgBPCatalogGHCRBase
	}
	return "oci://registry." + fqdn + "/openova-io"
}

// catalogOCIBase reads the process env and resolves the OCI base the emitted
// bp-* HelmRepositories declare. Called at render time (per mutation), so the
// value tracks the Deployment's CURRENT env — the pod rolls when step-07
// stamps it, and every subsequent regeneration emits the local base.
func catalogOCIBase() string {
	return resolveCatalogOCIBase(
		os.Getenv("CATALYST_LOCAL_REGISTRY_URL"),
		os.Getenv("CATALYST_PIN_ISSUER"),
		os.Getenv("CATALYST_HANDOVER_JWT_ISSUER"),
		os.Getenv("SOVEREIGN_FQDN"),
		os.Getenv("CATALYST_OTECH_FQDN"),
	)
}
