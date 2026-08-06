package handler

// Cutover-aware vCluster/app IMAGE registry host (#5439) — the catalyst-api
// org-tenants leg. Companion to organization_gitops.go's
// orgTenantSharedHelmRepositoriesYAML (#5527), which made the OCI *chart* base
// cutover-aware but left the IMAGE registry host a compile-time literal.
//
// THE DEFECT: orgTenantTemplateData.VClusterImageRegistry is read from
// CATALYST_VCLUSTER_IMAGE_REGISTRY with the mothership Harbor
// (harbor.openova.io) as the default — and the chart ALWAYS stamps that env
// with exactly that literal, so the value is the mothership on every Sovereign
// from birth. WriteTenantOverlay regenerates the overlay on every Org
// signup/teardown, so post-cutover the mothership host is written back into a
// source Flux asserts from — the same re-tether #5439 names, one field over.
//
// THE SEAM: cutover step-07 Phase 3e stamps
// CATALYST_LOCAL_IMAGE_REGISTRY_HOST=harbor.<sovereign-fqdn> — the exact host
// step-10 pivots the platform vClusters to (`target_host=
// harbor.${SOVEREIGN_FQDN}`). It appears in NO chart manifest, so it survives
// restarts and helm/Flux 3-way merges; CATALYST_VCLUSTER_IMAGE_REGISTRY does
// NOT (products/catalyst/chart/templates/controllers/
// organization-controller-deployment.yaml templates it), so a `kubectl set env`
// on that var is reverted by the next Flux-driven helm upgrade. The stamp also
// repairs a residual #5527 hole observed live on hw292: catalyst-api carries
// the step-07 issuer fact but BOTH SOVEREIGN_FQDN and CATALYST_OTECH_FQDN are
// empty, so a derivation with no explicit stamp fails safe to the mothership.
//
// Refs #5439 #5527 #4885.

import (
	"os"
	"strings"
)

// mothershipVClusterImageRegistry is the bootstrap (pre-cutover) image-registry
// host this leg ships as its default — the OpenOva mothership Harbor.
const mothershipVClusterImageRegistry = "harbor.openova.io"

// resolveVClusterImageRegistry is the pure resolution core (unit-tested
// directly, no env). Precedence, highest first:
//
//   - configured non-empty AND not the mothership literal → explicit operator
//     host, wins outright (Inviolable Principle #4).
//   - localImageRegistryHost → the step-07 Phase 3e stamp, normalised.
//   - a pivot fact (localRegistryURL / the issuer pair) AND a resolvable FQDN
//     → harbor.<fqdn>.
//   - a fact WITHOUT an FQDN → fail safe to the configured host.
//   - no fact → the configured host, unchanged (byte-identical pre-cutover).
func resolveVClusterImageRegistry(configured, localImageRegistryHost, localRegistryURL, pinIssuer, handoverIssuer string, fqdnCandidates ...string) string {
	cfg := strings.TrimSpace(configured)
	if cfg == "" {
		cfg = mothershipVClusterImageRegistry
	}
	if cfg != mothershipVClusterImageRegistry {
		return cfg
	}
	if h := normalizeVClusterRegistryHost(localImageRegistryHost); h != "" {
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

// normalizeVClusterRegistryHost strips any URL scheme and trailing slash so a
// stamp supplied as `https://harbor.<fqdn>/` resolves to the bare host an image
// reference needs.
func normalizeVClusterRegistryHost(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	for _, scheme := range []string{"https://", "http://", "oci://"} {
		s = strings.TrimPrefix(s, scheme)
	}
	return strings.TrimSuffix(s, "/")
}

// vclusterImageRegistryFor reads the process env and resolves the image
// registry host the rendered org-tenant overlay declares. Called at render
// time (per Org mutation), so the value tracks the Deployment's CURRENT env.
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
