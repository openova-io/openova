// removal_condition.go — the deletion branch says, on the Organization
// itself, what it could not remove.
//
// The org-controller owns the removal of an ended Organization in every
// region: the per-region files of its Flux objects (each region's Flux then
// prunes the Org's namespace, vCluster and volume), the console listeners,
// certificate, route and pool DNS records, the IaC repo and the per-Org realm.
// Each step is retried every 30 s until it succeeds. Before this file a step
// that kept failing was visible only in the controller's log, while the CR
// sat Terminating with every downstream object still running and nothing on
// the CR said why. Now the step and the error are written as the named
// condition Removed=False/RemovalIncomplete, which `kubectl get organization
// -o yaml`, the console and the acceptance suite can read.
//
// The condition is written once per failing STEP, not once per attempt: the
// reconciler watches its own status, so rewriting it whenever the error text
// changed would turn a flapping remote error into a hot reconcile loop
// against Gitea and the apiserver (#5305 class).

package controller

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

const (
	// OrganizationRemovedCondition is the condition type the deletion branch
	// writes while an ended Organization's removal is incomplete.
	OrganizationRemovedCondition = "Removed"
	// RemovalIncompleteReason is its reason: a named step could not remove
	// what it owns; the message names the step and the error.
	RemovalIncompleteReason = "RemovalIncomplete"
)

// removalStepPrefix is the stable head of the condition message for a step.
func removalStepPrefix(step string) string {
	return "could not remove " + step + ": "
}

// reportRemovalIncomplete records that the deletion step `step` failed with
// err. Best-effort: a status write failure is logged and the caller's requeue
// retries the step regardless.
func (r *Reconciler) reportRemovalIncomplete(ctx context.Context, org *orgapi.Organization, step string, err error) {
	if perr := markRemovalIncomplete(ctx, r.Client, org, step, err); perr != nil {
		r.Log.Error(perr, "write Removed condition", "organization", org.Name, "step", step)
	}
}

// markRemovalIncomplete writes Removed=False/RemovalIncomplete naming step
// and err on org's status, unless that step is already the one reported.
// Every other condition is kept as it is. Shared by the deletion branch and
// the NATS bridge that ends a marketplace Organization (organization_end.go).
func markRemovalIncomplete(ctx context.Context, c client.Client, org *orgapi.Organization, step string, err error) error {
	prefix := removalStepPrefix(step)
	for _, cond := range org.Status.Conditions {
		if cond.Type == OrganizationRemovedCondition && cond.Reason == RemovalIncompleteReason && strings.HasPrefix(cond.Message, prefix) {
			return nil // already reported for this step
		}
	}
	msg := prefix + err.Error()
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	conds := make([]orgapi.Condition, 0, len(org.Status.Conditions)+1)
	for _, cond := range org.Status.Conditions {
		if cond.Type != OrganizationRemovedCondition {
			conds = append(conds, cond)
		}
	}
	conds = append(conds, orgapi.Condition{
		Type:               OrganizationRemovedCondition,
		Status:             "False",
		Reason:             RemovalIncompleteReason,
		Message:            msg,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	updated := org.DeepCopyObject().(*orgapi.Organization)
	updated.Status.Conditions = conds
	return c.Status().Update(ctx, updated)
}
