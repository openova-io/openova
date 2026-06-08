// per_org_realm.go — organization-controller hook that find-or-creates a
// per-Org Keycloak realm (#3084 Part 2).
//
// Context: the controller's Reconcile loop already ensures a Keycloak
// GROUP at /<slug> in the SOVEREIGN realm (organization_controller.go,
// EnsureGroup). That group is NOT a per-Org realm — it shares the
// Sovereign realm's IdP + clients. Tier-3 per-Org SSO needs a realm
// NAMED after the Org slug so each Org gets its own isolation boundary
// (its own broker IdP, its own app clients). Before this hook the only
// path that created per-Org realms was the static `.Values.tenantRealms[]`
// Helm list consumed by bp-keycloak's configmap-per-org-realms.yaml +
// the sso-bridge reconciler — an operator-manual-overlay. This hook
// stitches the Organization CR to a realm via the KC admin API so the
// realm auto-provisions on Org create.
//
// The hook is idempotent: EnsureRealm is a find-or-create with a 409
// re-resolve arm (mirrors EnsureGroup), so a steady-state Organization
// CR makes zero realm writes.
//
// Failure semantics mirror iac_bootstrap.go's soft-failure model: a hard
// failure (KC unreachable, POST /admin/realms rejected) surfaces
// State=Failed + lastError on status.perOrgRealm and DOES downgrade the
// Org's Ready condition (the realm is an auth-critical artifact —
// unlike the iac-bootstrap repo, an Org without its realm cannot ever
// complete Tier-3 SSO, so the reconcile fails + requeues).
//
// Feature flag: PerOrgRealmEnabled gates the path. It defaults to
// enabled (consistent with the always-on EnsureGroup group path) so the
// realm provisions out of the box; an operator can disable it per-Org-
// realm rollout via the controller env. When disabled the hook surfaces
// State=Disabled (parallel to iac-bootstrap's Disabled badge) and the
// reconcile continues.
//
// Finalizer: the controller adds the `orgs.openova.io/per-org-realm`
// finalizer on first observation (when the feature is enabled) and runs
// DeleteRealm when the Organization is being deleted. The finalizer is
// removed last so an interrupted teardown resumes on the next reconcile.

package controller

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// PerOrgRealmFinalizer is the K8s finalizer added to every Organization
// CR (when the per-Org-realm feature is enabled) so DeleteRealm runs
// before the CR tombstones. Scoped to the orgs.openova.io API group per
// the finalizer-naming convention, parallel to IacBootstrapFinalizer.
const PerOrgRealmFinalizer = "orgs.openova.io/per-org-realm"

// reconcilePerOrgRealm find-or-creates the per-Org Keycloak realm and
// projects the result onto Organization.status.perOrgRealm. Returns the
// status sub-struct + a bool indicating whether the realm reconcile
// succeeded (false → the caller should fail the Reconcile so it requeues,
// since the realm is auth-critical). When the feature is disabled the
// status is State=Disabled and ok=true (a disabled feature is not a
// failure).
func (r *Reconciler) reconcilePerOrgRealm(ctx context.Context, org *orgapi.Organization) (orgapi.PerOrgRealmStatus, bool) {
	if !r.PerOrgRealmEnabled {
		return orgapi.PerOrgRealmStatus{
			State:     "Disabled",
			LastError: "per-Org Keycloak realm auto-wiring disabled in this controller deployment",
		}, true
	}

	realmName, err := r.Keycloak.EnsureRealm(ctx, org.Spec.Slug)
	if err != nil {
		return orgapi.PerOrgRealmStatus{
			// Keep any prior realmName on the CR so the operator console
			// can still deep-link even when the controller is mid-flight.
			RealmName: org.Status.PerOrgRealm.RealmName,
			State:     "Failed",
			LastError: truncate(err.Error(), 1024),
		}, false
	}
	return orgapi.PerOrgRealmStatus{
		RealmName: realmName,
		State:     "Ready",
	}, true
}

// teardownPerOrgRealm runs Keycloak.DeleteRealm when the Organization is
// being deleted. Returns the error so the finalizer flow can requeue on
// failure; nil means the finalizer can be removed. Absent-as-success is
// handled inside the LiveKeycloak.DeleteRealm impl (a 404 returns nil).
func (r *Reconciler) teardownPerOrgRealm(ctx context.Context, org *orgapi.Organization) error {
	if !r.PerOrgRealmEnabled {
		// Nothing to undo if the feature never ran.
		return nil
	}
	if org.Spec.Slug == "" {
		// Defensive: Reconcile enforces metadata.name == slug, but guard
		// against a malformed CR tripping the DeleteRealm empty-slug path.
		return nil
	}
	return r.Keycloak.DeleteRealm(ctx, org.Spec.Slug)
}

// ensurePerOrgRealmFinalizer adds the finalizer if missing. Returns true
// when the caller should patch the CR so subsequent code uses the
// updated copy. Mirrors ensureIacBootstrapFinalizer.
func ensurePerOrgRealmFinalizer(org *orgapi.Organization) bool {
	for _, f := range org.Finalizers {
		if f == PerOrgRealmFinalizer {
			return false
		}
	}
	org.Finalizers = append(org.Finalizers, PerOrgRealmFinalizer)
	return true
}

// removePerOrgRealmFinalizer drops the finalizer. Returns true when the
// caller should patch the CR. Mirrors removeIacBootstrapFinalizer.
func removePerOrgRealmFinalizer(org *orgapi.Organization) bool {
	keep := org.Finalizers[:0]
	removed := false
	for _, f := range org.Finalizers {
		if f == PerOrgRealmFinalizer {
			removed = true
			continue
		}
		keep = append(keep, f)
	}
	org.Finalizers = keep
	return removed
}

// perOrgRealmCondition renders the per-CR Condition for the realm
// provisioning. The condition's Type is `PerOrgRealmProvisioned`; Status
// mirrors the realm state machine. Surfacing it as a Condition (alongside
// Ready / IacRepoBootstrapped / IdentityProviderConfigured) lets the
// access-matrix UI render a status column without conditional logic
// against the perOrgRealm sub-status struct. Mirrors iacBootstrapCondition.
func perOrgRealmCondition(s orgapi.PerOrgRealmStatus) orgapi.Condition {
	now := metav1.NewTime(time.Now())
	switch s.State {
	case "Ready":
		return orgapi.Condition{
			Type:               "PerOrgRealmProvisioned",
			Status:             "True",
			Reason:             "RealmReady",
			Message:            "per-Org Keycloak realm provisioned: " + s.RealmName,
			LastTransitionTime: now,
		}
	case "Failed":
		return orgapi.Condition{
			Type:               "PerOrgRealmProvisioned",
			Status:             "False",
			Reason:             "RealmFailed",
			Message:            s.LastError,
			LastTransitionTime: now,
		}
	case "Disabled":
		return orgapi.Condition{
			Type:               "PerOrgRealmProvisioned",
			Status:             "False",
			Reason:             "RealmDisabled",
			Message:            "per-Org Keycloak realm auto-wiring disabled",
			LastTransitionTime: now,
		}
	default:
		return orgapi.Condition{
			Type:               "PerOrgRealmProvisioned",
			Status:             "Unknown",
			Reason:             "RealmPending",
			Message:            "per-Org realm state " + s.State,
			LastTransitionTime: now,
		}
	}
}
