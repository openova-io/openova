package handler

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
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

// environmentRefForOrg derives a CRD-pattern-safe spec.environmentRef from an
// Organization reference by appending the canonical "-prod" Environment suffix
// (one Org, one Env per chroot Sovereign) and slugging the whole thing. See
// sanitizeEnvironmentRef for the #3922 motivation.
//
//	environmentRefForOrg("hw171.omantel.biz") → "hw171-omantel-biz-prod"
//	environmentRefForOrg("acme")              → "acme-prod"
func environmentRefForOrg(ref string) string {
	return sanitizeEnvironmentRef(strings.TrimSpace(ref) + "-prod")
}

// ensureOrgNamespace creates the target Organization/Environment
// namespace if it does not already exist, returning nil when the
// namespace is present (created-or-verified).
//
// Why this exists (#3598, EPIC #3597): the create-from-catalog paths
// (HandleCreateInstance + HandleApplicationInstall + the backing-service
// auto-create) write the Application CR straight into the Org namespace
// via `.Namespace(org).Create(...)`. That namespace is otherwise created
// asynchronously by the organization controller's GitOps reconcile
// (core/controllers/organization/internal/gitops/manifests.go renders a
// `kind: Namespace`, committed to Gitea and applied by Flux). When an
// operator installs an app into an Org whose GitOps namespace manifest
// has not yet reconciled, the CR create fails with
// `namespaces "<org>" not found` — the exact "namespace not found"
// symptom the founder hit. Ensuring the namespace here closes that race
// so the install never surfaces that error.
//
// Idempotent: an AlreadyExists on Create is treated as success (the
// race-loser path + the steady-state path both return nil). The
// namespace is labelled so it is recognisable as catalyst-managed and so
// a later GitOps apply of the same namespace is a no-op rather than a
// conflict.
//
// A blank namespace name is a programmer error (the caller validated the
// Org slug upstream); we return an error rather than creating a
// cluster-scoped object with an empty name.
//
// #3830 — the argument is an Organization reference (often a dotted FQDN);
// it is run through orgNamespace() so the namespace this function
// creates/verifies is always a valid RFC-1123 label and is byte-identical
// to the name the Application-create call sites address via the same
// helper (no split-brain).
func ensureOrgNamespace(ctx context.Context, client dynamic.Interface, namespace string) error {
	if strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("ensureOrgNamespace: empty namespace")
	}
	ns := orgNamespace(namespace)

	// Fast path: already present.
	if _, err := client.Resource(namespacesGVR()).Get(ctx, ns, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		// A non-NotFound Get error (RBAC, apiserver down) is surfaced so
		// the caller can return a clear 5xx instead of masking it as a
		// create failure below.
		return fmt.Errorf("ensureOrgNamespace: get namespace %q: %w", ns, err)
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": ns,
				"labels": map[string]interface{}{
					// Marks the namespace as created by catalyst-api's
					// install path. The organization controller's GitOps
					// namespace manifest carries its own labels; server-side
					// apply / Flux treats this pre-created namespace as the
					// existing object and reconciles labels onto it.
					"app.kubernetes.io/managed-by":   "catalyst-api",
					"catalyst.openova.io/org":         ns,
					"catalyst.openova.io/provisioned": "install",
				},
			},
		},
	}

	if _, err := client.Resource(namespacesGVR()).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race-loser: another caller (or the GitOps apply) created it
			// between our Get and Create. That's success.
			return nil
		}
		return fmt.Errorf("ensureOrgNamespace: create namespace %q: %w", ns, err)
	}
	return nil
}
