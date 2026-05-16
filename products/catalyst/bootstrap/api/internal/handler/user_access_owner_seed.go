// user_access_owner_seed.go — auto-seed owner UserAccess CR on chroot
// at handover (DoD gate D21).
//
// D21 (docs/SOVEREIGN-MULTI-REGION-DOD.md):
//
//	"Sovereign Console /users MUST list the operator who completed
//	 PIN-login as tier=owner with their email."
//
// Verified on t132 (2026-05-16) that a fresh Sovereign returns
// `{"items":[]}` from /admin/user-access — Keycloak group membership in
// `sovereign-admins` is established by the handover flow, but no
// UserAccess CR exists for the operator, so the /users page is empty.
//
// This helper closes the gap: after auth_handover.go's
// `keycloak.EnsureUser` succeeds, it upserts a UserAccess CR that the
// existing useraccess Composition (issue #322) reconciles into per-app
// RoleBindings on the Sovereign cluster, so the operator surfaces in
// /users as the canonical owner-tier identity.
//
// # Architect-first
//
// The CR shape mirrors `userAccessToUnstructured` in user_access.go
// verbatim — same group/version/kind, same `spec.user`,
// `spec.sovereignRef`, `spec.applications` keys, same dynamic-client
// seam used by `CreateUserAccess`. The CR's owner-tier hint is stamped
// via `catalyst.openova.io/user-email` annotation, matching the read
// pattern at rbac_matrix.go:309-315 (rbacAssignReadEmailHint).
//
// # Tier mapping
//
// The 5-tier RBAC model (rbac_matrix.go:282 canonicalTierOrder) places
// `owner` at level 50, but the UserAccess CRD's per-application grant
// (user_access.go:86-90 userAccessRoles) only accepts admin|editor|viewer.
// The same lossy mapping `userAccessTierToRole("owner") → "admin"`
// (user_access.go:379-387) is used here for the wildcard "*" app entry.
// The /rbac/assign path remains the canonical way to bind the owner
// tier ClusterRole directly; this helper is just enough to make the
// operator visible in /users on first handover.
//
// # Idempotency
//
// Multiple handovers of the same operator (e.g. re-handover after a
// browser session expires) MUST NOT error. AlreadyExists on Create is
// folded to a nil return. The helper is safe to call on every handover.
//
// # Best-effort
//
// If the UserAccess CRD is not installed yet (the access.openova.io
// chart may roll later than catalyst-api), or the dynamic client is
// unavailable for any other reason, the helper returns an error. The
// caller (auth_handover.go) MUST log and continue — the operator's
// session is more important than this CR, and D21 will be satisfied on
// the next handover after the CRD rolls.
package handler

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// userAccessOwnerEmailAnnotation is the canonical key the rbac_matrix
// reader (rbac_matrix.go:309-315) looks up to surface a UserAccess CR's
// human-readable email on the /users page.
const userAccessOwnerEmailAnnotation = "catalyst.openova.io/user-email"

// userAccessOwnerNamePrefix is the deterministic name prefix for the
// auto-seeded owner CR. The full name is
// `useraccess-owner-<sanitized-email>` truncated to RFC 1123's 63-char
// limit (the apiserver rejects longer names with a 422).
const userAccessOwnerNamePrefix = "useraccess-owner-"

// EnsureOwnerUserAccess creates an owner-tier UserAccess CR for the
// operator who just completed PIN-login + handover, if one does not
// already exist. Idempotent: AlreadyExists is folded to a nil return.
//
// The CR shape:
//
//	apiVersion: access.openova.io/v1alpha1
//	kind: UserAccess
//	metadata:
//	  name: useraccess-owner-<sanitized-email>
//	  annotations:
//	    catalyst.openova.io/user-email: <email>
//	spec:
//	  user:
//	    keycloakSubject: <email>        # realm uses email-as-username
//	  sovereignRef: <fqdn-first-label>  # rbacAssignSlug-equivalent
//	  applications:
//	    - app: "*"                      # wildcard: every Application
//	      role: admin                   # owner → admin per tierToRole
//
// sovereignRef is derived from sovereignFQDN's first DNS label, matching
// rbacAssignSlug at rbac_assign.go:986-995 so the same provider-kubernetes
// ProviderConfig (`sovereign-<slug>`) resolves the Claim.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) the
// operator's email is logged at Info, never the JWT contents.
func EnsureOwnerUserAccess(ctx context.Context, client dynamic.Interface, email, sovereignFQDN string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return fmt.Errorf("EnsureOwnerUserAccess: email is required")
	}
	if client == nil {
		return fmt.Errorf("EnsureOwnerUserAccess: dynamic client is nil")
	}

	name := ownerUserAccessName(email)
	sovRef := rbacAssignSlug(sovereignFQDN)

	obj := buildOwnerUserAccessUnstructured(name, email, sovRef)

	_, err := client.Resource(UserAccessGVR()).
		Namespace("").
		Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Re-handover for the same operator: nothing to do.
			return nil
		}
		return fmt.Errorf("create owner UserAccess %q: %w", name, err)
	}
	return nil
}

// ownerUserAccessName builds the deterministic owner-CR name. The full
// pattern is `useraccess-owner-<sanitized-email>` truncated to 63 chars
// per RFC 1123 (the apiserver rejects longer names with a 422). The
// sanitized email is lowercase alphanumeric + hyphens, hyphen-trimmed
// at both ends (sanitizeK8sNameSegment from rbac_assign.go:936-951).
func ownerUserAccessName(email string) string {
	lower := strings.ToLower(strings.TrimSpace(email))
	// Translate the structural separators into hyphens BEFORE
	// sanitizeK8sNameSegment strips them — otherwise
	// `emrah.baysal@openova.io` collapses to `emrahbaysalopenovaio`
	// and loses every token boundary that makes the resulting CR name
	// human-scannable.
	lower = strings.ReplaceAll(lower, "@", "-at-")
	lower = strings.ReplaceAll(lower, ".", "-")
	emailSegment := sanitizeK8sNameSegment(lower)
	full := userAccessOwnerNamePrefix + emailSegment
	if len(full) > 63 {
		full = full[:63]
	}
	// After truncation a trailing hyphen is illegal; trim any.
	full = strings.TrimRight(full, "-")
	if full == strings.TrimRight(userAccessOwnerNamePrefix, "-") {
		// Pathological: email sanitized to empty string. Fall back to a
		// stable literal so the CR still gets created (matches the
		// `sanitizeK8sNameSegment → "user"` fallback in rbacAssignName).
		full = userAccessOwnerNamePrefix + "operator"
	}
	return full
}

// buildOwnerUserAccessUnstructured composes the dynamic-client payload
// for the owner CR. Shape mirrors userAccessToUnstructured exactly so
// the existing Composition (issue #322) reconciles it without surprise.
func buildOwnerUserAccessUnstructured(name, email, sovereignRef string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(userAccessGroup + "/" + userAccessVersion)
	obj.SetKind("UserAccess")
	obj.SetName(name)
	obj.SetAnnotations(map[string]string{
		userAccessOwnerEmailAnnotation: email,
	})

	spec := map[string]any{
		"user": map[string]any{
			// Keycloak issues subjects equal to the email when the realm
			// uses email-as-username, which is the default for
			// OpenOva-shipped Sovereigns (see user_access.go:103-105).
			"keycloakSubject": email,
		},
		"sovereignRef": sovereignRef,
		"applications": []any{
			map[string]any{
				"app":  "*",
				"role": "admin", // owner → admin per userAccessTierToRole
			},
		},
	}
	_ = unstructured.SetNestedMap(obj.Object, spec, "spec")
	return obj
}
