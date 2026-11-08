package handler

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// namespacesGVR is the core/v1 Namespace resource. We address it through
// the dynamic client (not a typed corev1 clientset) because every
// Application-create path in this package already holds a
// dynamic.Interface — mirroring the read idiom in
// internal/infrastructure/topology_loader.go which lists namespaces via
// the same GVR.
func namespacesGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
}

// orgNamespace maps an Organization reference to the Kubernetes namespace
// name that hosts that Org's Application CRs.
//
// Why this exists (#3830, EPIC #3376): every Sovereign Organization is
// identified by a dotted FQDN (e.g. "hw165.omani.works"). Kubernetes
// namespace names are RFC-1123 *labels* — they may NOT contain dots, must
// be lowercase, ≤63 chars, and start/end with an alphanumeric. Passing the
// FQDN straight through to `.Namespace(org).Create(...)` made the create
// path fail UNIVERSALLY with `Namespace "<fqdn>" is invalid:
// metadata.name: ... must not contain dots`. This helper is the single
// canonical org→namespace seam: every place an Org ref becomes a namespace
// routes through it, so create + lookup + the Application CR's
// metadata.namespace always resolve to ONE consistent name (no split-brain
// where an instance is created in one namespace and looked up in another).
//
// Mapping rules (sanitise, never reject — the Org identity stays on the
// `catalyst.openova.io/organization` label as the verbatim FQDN; the
// namespace is a derived artifact):
//
//   - lowercase
//   - any character that is not [a-z0-9-] (dots included) → '-'
//   - collapse runs of '-' to a single '-'
//   - trim leading/trailing '-'
//   - cap at 63 chars, then re-trim a trailing '-' the cap may have left
//   - empty result (pathological all-illegal input) → "org" so the caller
//     never addresses a cluster-scoped object with an empty name
//
// orgNamespace("hw165.omani.works") → "hw165-omani-works".
func orgNamespace(ref string) string {
	s := strings.ToLower(strings.TrimSpace(ref))

	var sb strings.Builder
	sb.Grow(len(s))
	prevDash := false
	for _, r := range s {
		isAllowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAllowed {
			sb.WriteRune(r)
			prevDash = false
			continue
		}
		// Every other rune (dots, slashes, spaces, underscores, unicode…)
		// folds to a single dash; collapse consecutive dashes.
		if !prevDash {
			sb.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(sb.String(), "-")

	if len(out) > 63 {
		out = out[:63]
		out = strings.TrimRight(out, "-")
	}
	if out == "" {
		return "org"
	}
	return out
}

// sanitizeEnvironmentRef maps an Environment reference (e.g. "<org>-prod",
// often derived from a dotted Sovereign FQDN) to a value that satisfies the
// Application CRD's spec.environmentRef pattern `^[a-z][a-z0-9-]{2,63}$`.
//
// Why this exists (#3922): the CRD constrains spec.environmentRef to the
// RFC-1123 *label* pattern (no dots). On a chroot/operator-on-Sovereign
// install there is no customer Organization, so both create paths derive the
// env ref from the Sovereign FQDN (`<fqdn>-prod`, e.g.
// "hw171.omantel.biz-prod"). Every FQDN carries dots, so the apiserver
// rejected the create with HTTP 500:
//
//	spec.environmentRef: Invalid value: "hw171.omantel.biz-prod":
//	spec.environmentRef in body should match '^[a-z][a-z0-9-]{2,63}$'
//
// blocking ALL instance creation. #3830 slugged metadata.namespace through
// orgNamespace() but left environmentRef dotted (the FQDN identity stays on
// the catalyst.openova.io/organization label + organizationRef — the env ref
// is a derived, pattern-constrained artifact, exactly like the namespace).
// This helper closes that gap: it slugs the value through the same character
// rules orgNamespace() uses (lowercase, dots→'-', collapse, trim), then caps
// it at the CRD's 63-char ceiling so a long FQDN can never overflow the
// pattern. An already-valid label (e.g. "acme-prod") round-trips unchanged.
//
//	sanitizeEnvironmentRef("hw171.omantel.biz-prod") → "hw171-omantel-biz-prod"
//	sanitizeEnvironmentRef("acme-prod")              → "acme-prod"
//
// orgNamespace already implements exactly these character rules + the 63-char
// cap + the empty-input fallback, so we delegate to it rather than duplicate
// the slugging loop. (orgNamespace's "org" empty-fallback is also a valid
// environmentRef, so the contract holds for pathological all-illegal input.)
func sanitizeEnvironmentRef(ref string) string {
	return orgNamespace(ref)
}

// ADR-0015 D4 — environmentRefForOrg ("<org>-prod") and ensureOrgNamespace
// (a namespace created for any string) were deleted. An Application names
// an existing Environment, and the Organization's namespace is owned by the
// organization controller: validateInstallRefs (install_refs.go) checks
// both and answers 422 / 409 organization-not-ready instead.
