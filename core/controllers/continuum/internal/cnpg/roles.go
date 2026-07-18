// Managed-role password re-assert through CNPG's OWN contract (#5224,
// Refs #5220 #4915 — the hw273 harbor 28P01 lockout).
//
// CNPG reconciles a managed role's password FROM the Secret named by
// `spec.managed.roles[].passwordSecret`, and it detects drift ONLY via
// that Secret's resourceVersion (status.managedRolesStatus records the
// rv it last applied). An OUT-OF-BAND `ALTER ROLE` on the database —
// the hw273 G12 promote-flap casualty, where the shared primary's
// `harbor` role was asserted with the replica region's divergent
// locally-minted password — is therefore INVISIBLE to CNPG: the status
// stays "reconciled" at the old rv while the database rejects the very
// password that Secret carries, and every consumer's NEW connection
// fails with SQLSTATE 28P01 until an operator touches the Secret by
// hand.
//
// ReassertManagedRoles is the structured form of that manual heal: a
// metadata-only annotation bump on each referenced passwordSecret
// changes its resourceVersion WITHOUT changing the credential bytes,
// which forces CNPG's role reconciler to re-apply the canonical
// password (its own ALTER ROLE, on the cluster it manages). This is
// the ONLY sanctioned role-password assertion path for the DR
// promote/failback machinery — Continuum/dr actors must NEVER issue
// direct SQL against the shared write endpoint (they cannot know which
// region's Secret set is authoritative mid-flap; CNPG + the primary
// region's Secret always are).
package cnpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// SecretGVR — core-v1 Secrets via the same dynamic client the Reader
// already holds (ADR-0001 §2.7 forbids typed CNPG bindings; staying
// dynamic-only here keeps the package's client surface uniform).
var SecretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// RoleReassertAnnotation is stamped (with the touch timestamp) on each
// managed-role passwordSecret ReassertManagedRoles touches. The VALUE
// changing is what bumps the Secret's resourceVersion; the key doubles
// as the operator-visible audit trail of when the DR machinery last
// forced a canonical re-apply.
const RoleReassertAnnotation = "catalyst.openova.io/dr-role-reassert-at"

// ManagedRolePasswordSecrets returns the DEDUPED list of Secret names
// referenced by `spec.managed.roles[].passwordSecret.name` on a CNPG
// Cluster CR, in spec order. Nil/absent spec.managed.roles → nil (a
// replica-half CR carries no managed roles — bp-postgres
// replica-cluster.yaml — and must yield a clean no-op).
func ManagedRolePasswordSecrets(cr *unstructured.Unstructured) []string {
	if cr == nil {
		return nil
	}
	roles, found, err := unstructured.NestedSlice(cr.Object, "spec", "managed", "roles")
	if !found || err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, ri := range roles {
		rm, ok := ri.(map[string]interface{})
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(rm, "passwordSecret", "name")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// ReassertManagedRoles touches every managed-role passwordSecret the
// named Cluster CR references (RoleReassertAnnotation = `at`, in the
// CR's namespace — CNPG requires passwordSecrets co-located with the
// Cluster) so each Secret's resourceVersion changes and CNPG re-applies
// the canonical password on the cluster it manages.
//
// Semantics:
//   - Idempotent + credential-preserving: only annotation metadata
//     changes; the password bytes are untouched, so a re-assert against
//     an already-canonical role is a harmless same-value ALTER.
//   - A MISSING referenced Secret is SKIPPED, not an error: a
//     replica-role region never mints role Secrets (bp-postgres ≥0.2.13
//     side-gate, the #4915-class single-sourcing), so dangling refs on
//     a pre-flip placeholder CR are an expected shape.
//   - Per-Secret API failures are aggregated (errors.Join) and the
//     remaining Secrets still processed — the caller retries on its
//     next tick, so a transient conflict self-heals.
//
// Returns the names actually touched.
func (r *Reader) ReassertManagedRoles(ctx context.Context, namespace, name string, at time.Time) ([]string, error) {
	if r == nil || r.Dyn == nil {
		return nil, errors.New("cnpg: nil Reader")
	}
	cr, err := r.Dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("cnpg: get cluster for role re-assert %s/%s: %w", namespace, name, err)
	}
	stamp := at.UTC().Format(time.RFC3339)
	var touched []string
	var errs []error
	for _, secretName := range ManagedRolePasswordSecrets(cr) {
		sec, gErr := r.Dyn.Resource(SecretGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
		if apierrors.IsNotFound(gErr) {
			// Replica-role regions carry no local role Secrets by design
			// (single-sourced from the primary region) — skip, no error.
			continue
		}
		if gErr != nil {
			errs = append(errs, fmt.Errorf("cnpg: get passwordSecret %s/%s: %w", namespace, secretName, gErr))
			continue
		}
		ann := sec.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[RoleReassertAnnotation] = stamp
		sec.SetAnnotations(ann)
		if _, uErr := r.Dyn.Resource(SecretGVR).Namespace(namespace).Update(ctx, sec, metav1.UpdateOptions{}); uErr != nil {
			errs = append(errs, fmt.Errorf("cnpg: touch passwordSecret %s/%s: %w", namespace, secretName, uErr))
			continue
		}
		touched = append(touched, secretName)
	}
	return touched, errors.Join(errs...)
}
