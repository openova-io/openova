// iac_bootstrap.go — organization-controller hook that codifies the
// ADR-0009 per-Org IaC repo bootstrap (G117.3 / W2.C3).
//
// The controller's Reconcile loop calls reconcileIacBootstrap immediately
// after the Gitea Org + repo ensure steps and before the vCluster manifest
// render. The hook is idempotent: every step in the iacbootstrap.Bootstrap
// orchestrator is a find-or-create, so a steady-state Organization CR
// produces zero Gitea writes.
//
// Failure semantics:
//
//   - Hard failure (Gitea unreachable, branch-protection rejected) ->
//     Reconcile returns a 30s requeue with the IacBootstrap status
//     phase=Failed and the lastError populated. The Organization's
//     Ready=True condition is NOT downgraded — vCluster + Keycloak
//     reconciliation already completed.
//
//   - Soft failure (bootstrap orchestrator declines to mint a fresh
//     token because the store already holds one) -> handled silently
//     inside iacbootstrap.Bootstrap; the hook returns Ready with
//     AlreadyBootstrapped=true and the Reconcile loop continues.
//
// Finalizer semantics: the controller adds the
// `orgs.openova.io/iac-bootstrap` finalizer on first observation and
// runs iacbootstrap.Teardown when the Organization is being deleted.
// The finalizer is removed last so an interrupted teardown can resume
// on the next reconcile.

package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/core/controllers/organization/internal/iacbootstrap"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// IacBootstrapFinalizer is the K8s finalizer added to every Organization
// CR so iacbootstrap.Teardown runs before the CR tombstones. Per the
// finalizer-naming convention (`<domain>/<purpose>`) it is scoped to
// the orgs.openova.io API group.
const IacBootstrapFinalizer = "orgs.openova.io/iac-bootstrap"

// IacBootstrapTokenStore is the controller-facing token-store interface.
// Production wires iacbootstrap.OpenBaoStore; tests inject a stub.
type IacBootstrapTokenStore interface {
	HasToken(ctx context.Context, org string) (bool, error)
	PutToken(ctx context.Context, org, plaintext string) error
	DeleteToken(ctx context.Context, org string) error
}

// reconcileIacBootstrap runs iacbootstrap.Bootstrap and projects the
// result back onto Organization.status.iacBootstrap. Returns the
// updated status sub-struct so the caller can decide whether to bump
// the Ready condition.
//
// `iacStore` must be non-nil — the controller's main wires
// iacbootstrap.NewOpenBaoStore. The function is split into a helper so
// the existing organization_controller.go file remains under the
// cognitive-load threshold; the bootstrap pipeline grows here.
func (r *Reconciler) reconcileIacBootstrap(ctx context.Context, org *orgapi.Organization) orgapi.IacBootstrapStatus {
	if r.IacBootstrapGitea == nil || r.IacBootstrapTokens == nil {
		// Bootstrap dependencies not wired (e.g. unit-test path that
		// only exercises the Keycloak/Gitea-org code). Surface a
		// "Disabled" state so the operator console can render a clear
		// "feature not wired" badge rather than appearing to hang on
		// Pending.
		return orgapi.IacBootstrapStatus{
			State:     "Disabled",
			LastError: "iacbootstrap dependencies not wired in this controller deployment",
		}
	}

	in := iacbootstrap.Input{
		Slug:          org.Spec.Slug,
		DisplayName:   org.Spec.DisplayName,
		SovereignFQDN: org.Spec.SovereignRef,
	}

	result, err := iacbootstrap.Bootstrap(ctx, r.IacBootstrapGitea, r.IacBootstrapTokens, in)
	if err != nil {
		// Failure: keep any prior repoURL on the CR (let operators
		// follow the link even when the controller is mid-flight) but
		// flip state=Failed + populate lastError.
		prior := org.Status.IacBootstrap
		return orgapi.IacBootstrapStatus{
			State:         "Failed",
			RepoURL:       prior.RepoURL,
			RobotUsername: prior.RobotUsername,
			LastError:     truncate(err.Error(), 1024),
		}
	}
	return orgapi.IacBootstrapStatus{
		State:         "Ready",
		RepoURL:       result.RepoURL,
		RobotUsername: result.RobotUsername,
	}
}

// teardownIacBootstrap runs iacbootstrap.Teardown when the Organization
// is being deleted. Returns an error that the finalizer flow can
// inspect — non-nil means the controller-runtime requeue, nil means
// the finalizer can be removed.
func (r *Reconciler) teardownIacBootstrap(ctx context.Context, org *orgapi.Organization) error {
	if r.IacBootstrapGitea == nil || r.IacBootstrapTokens == nil {
		// Same as the up-flow: nothing to undo if nothing was wired.
		return nil
	}
	if org.Spec.Slug == "" {
		// Defensive — Reconcile already enforces metadata.name = slug;
		// this is the belt-and-braces guard so a malformed CR doesn't
		// trip the Teardown's slug-empty error path.
		return fmt.Errorf("teardownIacBootstrap: spec.slug is empty on %q", org.Name)
	}
	return iacbootstrap.Teardown(ctx, r.IacBootstrapGitea, r.IacBootstrapTokens, org.Spec.Slug)
}

// ensureIacBootstrapFinalizer adds the finalizer if missing.
// Returns true when the controller's caller should patch the CR so
// subsequent code uses the updated copy.
func ensureIacBootstrapFinalizer(org *orgapi.Organization) bool {
	for _, f := range org.Finalizers {
		if f == IacBootstrapFinalizer {
			return false
		}
	}
	org.Finalizers = append(org.Finalizers, IacBootstrapFinalizer)
	return true
}

// removeIacBootstrapFinalizer drops the finalizer. Returns true when
// the caller should patch the CR.
func removeIacBootstrapFinalizer(org *orgapi.Organization) bool {
	keep := org.Finalizers[:0]
	removed := false
	for _, f := range org.Finalizers {
		if f == IacBootstrapFinalizer {
			removed = true
			continue
		}
		keep = append(keep, f)
	}
	org.Finalizers = keep
	return removed
}

// truncate trims s to n runes (NOT bytes) so a long URL doesn't
// silently bust the etcd CR-size cap. The K8s convention is to encode
// status messages as UTF-8; truncating by rune is the correct primitive.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// containsFinalizer reports whether the named finalizer is present in
// the supplied slice. Pulled out so both reconcile and finalizer tests
// can share the predicate.
func containsFinalizer(finalizers []string, name string) bool {
	for _, f := range finalizers {
		if f == name {
			return true
		}
	}
	return false
}

// iacBootstrapCondition renders the per-CR Condition for the IaC
// bootstrap pipeline. The condition's Type is `IacRepoBootstrapped`;
// Status mirrors the bootstrap state machine. Surfacing it as a
// Condition (alongside Ready / IdentityProviderConfigured) lets the
// access-matrix UI render a status column without conditional logic
// against the iacBootstrap sub-status struct.
func iacBootstrapCondition(s orgapi.IacBootstrapStatus) orgapi.Condition {
	now := metav1.NewTime(time.Now())
	switch s.State {
	case "Ready":
		return orgapi.Condition{
			Type:               "IacRepoBootstrapped",
			Status:             "True",
			Reason:             "BootstrapDone",
			Message:            "per-Org IaC repo bootstrapped at " + s.RepoURL,
			LastTransitionTime: now,
		}
	case "Failed":
		return orgapi.Condition{
			Type:               "IacRepoBootstrapped",
			Status:             "False",
			Reason:             "BootstrapFailed",
			Message:            s.LastError,
			LastTransitionTime: now,
		}
	case "Disabled":
		return orgapi.Condition{
			Type:               "IacRepoBootstrapped",
			Status:             "False",
			Reason:             "BootstrapDisabled",
			Message:            "iacbootstrap dependencies not wired",
			LastTransitionTime: now,
		}
	default:
		return orgapi.Condition{
			Type:               "IacRepoBootstrapped",
			Status:             "Unknown",
			Reason:             "BootstrapPending",
			Message:            "iac-bootstrap state " + s.State,
			LastTransitionTime: now,
		}
	}
}
