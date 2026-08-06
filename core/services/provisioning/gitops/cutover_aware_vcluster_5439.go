package gitops

// Cutover-aware vCluster/app IMAGE registry host (#5439) — the provisioning
// half. Companion to cutover_aware_5527.go, which made the OCI *chart* base
// cutover-aware but left the IMAGE registry host a compile-time literal.
//
// THE DEFECT: every image this module proxies — the per-Org CNPG/MySQL/Valkey
// datastore images and every catalog app image routed through proxyImage() —
// is re-tagged through `defaultVClusterRegistryMirror` = harbor.openova.io,
// the MOTHERSHIP Harbor. The trees are regenerated on EVERY org mutation, so
// post-cutover the mothership host is written back into a source Flux asserts
// from. Same class as #5439's named defect, one field over. Live on hw292
// (cutoverComplete=true): deploy/provisioning carried
// VCLUSTER_IMAGE_REGISTRY=harbor.openova.io.
//
// THE SEAM is the same one cutover_aware_5527.go documents, extended by
// step-07 Phase 3e: CATALYST_LOCAL_IMAGE_REGISTRY_HOST=harbor.<sovereign-fqdn>,
// the exact host step-10 (vcluster-registry-pivot) pivots the three platform
// vClusters to (`target_host="harbor.${SOVEREIGN_FQDN}"`). It appears in NO
// chart manifest, so `kubectl set env` survives restarts and helm/Flux 3-way
// merges — unlike VCLUSTER_IMAGE_REGISTRY itself, which products/catalyst/
// chart/templates/org-services/provisioning.yaml ALWAYS stamps and a helm
// upgrade therefore reverts.
//
// Refs #5439 #5527 #4885.

import (
	"os"
	"strings"
)

// mothershipHarborHost is the bootstrap (pre-cutover) image-registry host this
// module ships as its default — the OpenOva mothership Harbor. Its presence in
// a post-cutover render IS the defect.
const mothershipHarborHost = defaultVClusterRegistryMirror

// resolveVClusterImageRegistry is the pure resolution core (unit-tested
// directly, no env). Precedence, highest first:
//
//   - configured non-empty AND not the mothership literal → an operator (or a
//     future cutover step) named an explicit host; it wins outright
//     (Inviolable Principle #4).
//   - localImageRegistryHost → the step-07 Phase 3e stamp, the exact final
//     host; normalised (scheme + trailing slash stripped).
//   - a pivot fact (localRegistryURL — Phase 3d's chart OCI base — or the
//     Phase 3b issuer pair) AND a resolvable FQDN → harbor.<fqdn>.
//   - a fact WITHOUT an FQDN → fail safe to the configured host rather than
//     emit one that resolves nowhere.
//   - no fact → the configured host, unchanged: pre-cutover output stays
//     byte-identical to the historical render and Flux sees zero drift.
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

// normalizeRegistryHost strips any URL scheme and trailing slash so a stamp
// supplied as `https://harbor.<fqdn>/` resolves to the bare host an image
// reference needs.
func normalizeRegistryHost(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	for _, scheme := range []string{"https://", "http://", "oci://"} {
		s = strings.TrimPrefix(s, scheme)
	}
	s = strings.TrimSuffix(s, "/")
	return s
}

// vclusterImageRegistryFor reads the process env and resolves the image
// registry host the emitted manifests declare. Called at render time (per
// mutation), so the value tracks the Deployment's CURRENT env — the pod rolls
// when step-07 Phase 3e stamps it, and every subsequent regeneration emits the
// local host.
func vclusterImageRegistryFor(configured string) string {
	return resolveVClusterImageRegistry(
		configured,
		os.Getenv("CATALYST_LOCAL_IMAGE_REGISTRY_HOST"),
		os.Getenv("CATALYST_LOCAL_REGISTRY_URL"),
		os.Getenv("CATALYST_PIN_ISSUER"),
		os.Getenv("CATALYST_HANDOVER_JWT_ISSUER"),
		os.Getenv("SOVEREIGN_FQDN"),
		os.Getenv("CATALYST_OTECH_FQDN"),
	)
}
