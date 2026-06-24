// Package fluxsource is the SINGLE shared builder for every Flux source CR
// (GitRepository / HelmRepository / OCIRepository) that points at the
// Sovereign-LOCAL Gitea or Harbor.
//
// WHY THIS PACKAGE EXISTS (issue #4285)
// =====================================
// bp-gitea sets `service.REQUIRE_SIGNIN_VIEW=true`
// (platform/gitea/chart/values.yaml). Under that setting an ANONYMOUS git
// clone returns HTTP 401 "authentication required" for EVERY repo —
// including public ones. Therefore EVERY Flux source that clones the
// Sovereign-local Gitea MUST carry a basic-auth `spec.secretRef`. There are
// no exceptions.
//
// Before this package the rule lived, re-implemented, in three independent
// generators — application-controller, organization-controller and
// environment-controller — each with its OWN `if secretRef != "" { stamp }`
// guard and its OWN chart default. Two of the three defaulted the secretRef
// to empty (application-controller, and environment-controller's phantom
// `gitea-flux-token` that no Job ever mints), so they silently emitted
// unauthenticated sources that sat "authentication required" forever. The
// organization-controller had the correct default and worked. That asymmetry
// across three copies of the same law IS the bug class #4285 closes.
//
// THE FIX
// =======
// This package centralizes the law: building a Flux source whose URL targets
// the Sovereign-local Gitea/Harbor with an EMPTY secretRef is an ERROR, not a
// silently-degraded source. A future fourth generator that forgets the secret
// fails LOUDLY (controller reconcile error / failing unit test) instead of
// shipping a dead 401 source to a live Sovereign.
//
// External sources are UNAFFECTED: ghcr.io OCI HelmRepositories (auth'd via
// `ghcr-pull`), upstream public chart repos, etc. do not route through this
// guard — only hosts classified as the local Gitea or local Harbor do.
package fluxsource

import (
	"fmt"
	"net/url"
	"strings"
)

// ErrLocalSourceNeedsSecret is returned (wrapped) when a Flux source whose URL
// targets the Sovereign-local Gitea/Harbor is built with an empty secretRef.
// Callers can errors.As against it; it always carries the offending URL.
type ErrLocalSourceNeedsSecret struct {
	URL string
}

func (e *ErrLocalSourceNeedsSecret) Error() string {
	return fmt.Sprintf(
		"fluxsource: Flux source url %q targets the Sovereign-local Gitea/Harbor "+
			"but secretRef is empty — bp-gitea REQUIRE_SIGNIN_VIEW=true makes "+
			"anonymous clone return 401 'authentication required' for EVERY repo "+
			"(issue #4285). A secretRef (e.g. openova-org-tenants-git-auth) is "+
			"mandatory for local-Gitea/Harbor sources",
		e.URL,
	)
}

// localSourceHostMarkers are the case-insensitive host substrings that mark a
// URL as targeting the Sovereign-LOCAL Gitea or Harbor (the two hosts behind
// REQUIRE_SIGNIN_VIEW / robot-auth). Matched against the URL host only, never
// the path, so an external repo with "gitea" in its PATH is not misclassified.
//
//   - In-cluster Gitea service: gitea-http.gitea.svc.cluster.local
//   - Per-Sovereign Gitea DNS:  gitea.<location-code>.<sovereign-domain>
//   - In-cluster / per-Sovereign Harbor: harbor.<...> / harbor-core.<...>
//
// harbor.openova.io (the MOTHERSHIP proxy-cache, anonymously pullable for the
// public catalog) is deliberately EXCLUDED — only Sovereign-LOCAL hosts need
// the guard. See isMothershipHarbor.
var localSourceHostMarkers = []string{
	"gitea",  // gitea-http.gitea.svc..., gitea.<loc>.<dom>
	"harbor", // harbor.<loc>.<dom>, harbor-core.<ns>.svc...
}

// IsLocalSovereignSource reports whether rawURL targets the Sovereign-local
// Gitea or Harbor (the hosts that REQUIRE authentication). It is host-based
// and resilient to a missing scheme (callers sometimes pass
// `gitea-http.gitea.svc.cluster.local:3000/org/repo.git`). External hosts
// (ghcr.io, upstream chart repos, the mothership proxy-cache) return false.
func IsLocalSovereignSource(rawURL string) bool {
	host := hostOf(rawURL)
	if host == "" {
		return false
	}
	if isMothershipHarbor(host) {
		// harbor.openova.io is the mothership public proxy-cache, not a
		// Sovereign-local auth'd source.
		return false
	}
	for _, marker := range localSourceHostMarkers {
		if strings.Contains(host, marker) {
			return true
		}
	}
	return false
}

// isMothershipHarbor matches the mothership proxy-cache Harbor
// (harbor.openova.io), which is anonymously pullable and must NOT be forced
// through the secretRef guard.
func isMothershipHarbor(host string) bool {
	return host == "harbor.openova.io"
}

// hostOf extracts the lower-cased host (no port) from a URL that may or may not
// carry a scheme. Returns "" when no host can be determined.
func hostOf(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return ""
	}
	// url.Parse needs a scheme to populate u.Host; synthesize one when absent
	// so bare `gitea-http.gitea.svc.cluster.local:3000/...` parses correctly.
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// GitRepositorySpecInput is the minimal field set every generator already has
// when it builds a Flux GitRepository spec.
type GitRepositorySpecInput struct {
	// URL is the GitRepository spec.url (the Gitea HTTP clone URL).
	URL string
	// Branch is the spec.ref.branch.
	Branch string
	// IntervalSeconds is the spec.interval (seconds). <=0 defaults to 60.
	IntervalSeconds int
	// SecretRef is the name of the Flux basic-auth Secret. MUST be non-empty
	// when URL targets the Sovereign-local Gitea/Harbor.
	SecretRef string
}

// BuildGitRepositorySpec returns the `spec` map for a Flux GitRepository,
// enforcing the local-Gitea-needs-secretRef law. It is the canonical builder
// for the unstructured-client generators (application- and
// organization-controller).
//
// Returns *ErrLocalSourceNeedsSecret when the URL is a local Sovereign source
// and SecretRef is empty. For external sources an empty SecretRef is fine and
// no secretRef key is emitted.
func BuildGitRepositorySpec(in GitRepositorySpecInput) (map[string]interface{}, error) {
	secret := strings.TrimSpace(in.SecretRef)
	if secret == "" && IsLocalSovereignSource(in.URL) {
		return nil, &ErrLocalSourceNeedsSecret{URL: in.URL}
	}
	interval := in.IntervalSeconds
	if interval <= 0 {
		interval = 60
	}
	spec := map[string]interface{}{
		"interval": fmt.Sprintf("%ds", interval),
		"url":      in.URL,
		"ref": map[string]interface{}{
			"branch": in.Branch,
		},
	}
	if secret != "" {
		spec["secretRef"] = map[string]interface{}{"name": secret}
	}
	return spec, nil
}

// ValidateGiteaSecretRef is the guard for generators that render their Flux
// source via a path BuildGitRepositorySpec can't produce (e.g. the
// environment-controller's text/template YAML). Call it with the source URL +
// the secretRef before committing the rendered manifest; it returns
// *ErrLocalSourceNeedsSecret when a local Sovereign source has an empty
// secretRef, nil otherwise.
func ValidateGiteaSecretRef(rawURL, secretRef string) error {
	if strings.TrimSpace(secretRef) == "" && IsLocalSovereignSource(rawURL) {
		return &ErrLocalSourceNeedsSecret{URL: rawURL}
	}
	return nil
}
