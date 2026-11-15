package helmwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// The three annotations that make helm-controller act again on a
// HelmRelease it has given up on. requestedAt asks for a reconcile; resetAt
// clears the install and upgrade failure counters; forceAt makes it act on
// a release whose desired state did not change. helm-controller honours
// resetAt and forceAt only when their value equals requestedAt's, so one
// token is written to all three (the shape `flux reconcile helmrelease
// --reset --force` writes, and the console's reconcile action since #6795).
const (
	AnnotationReconcileRequestedAt = "reconcile.fluxcd.io/requestedAt"
	AnnotationReconcileForceAt     = "reconcile.fluxcd.io/forceAt"
	AnnotationReconcileResetAt     = "reconcile.fluxcd.io/resetAt"

	// ReasonMissingRollbackTarget is the Stalled reason helm-controller sets
	// when a failed upgrade is to be rolled back and no earlier revision
	// exists.
	ReasonMissingRollbackTarget = "MissingRollbackTarget"
)

// NewRetryToken returns the annotation value for one retry request.
func NewRetryToken(now time.Time) string {
	return now.UTC().Format(time.RFC3339Nano)
}

// RetryPatch is the JSON merge patch that asks helm-controller for a fresh
// attempt on a stalled HelmRelease: one token on all three annotations.
func RetryPatch(token string) []byte {
	b, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				AnnotationReconcileRequestedAt: token,
				AnnotationReconcileForceAt:     token,
				AnnotationReconcileResetAt:     token,
			},
		},
	})
	return b
}

// RequestRetry writes one retry request (RetryPatch) on the named flux-system
// HelmRelease and returns its token. It is the one place the provisioning
// API writes this request from: the Phase-1 watcher (a never-deployed
// rollback stall), and the cross-region work of the parent (a follower
// release that failed before its inputs arrived).
//
// It is a direct write to the cluster by design. A retry request is not
// desired state: it asks a controller to act once on the state it already
// has, so there is no Git commit that could carry it.
func RequestRetry(ctx context.Context, dyn dynamic.Interface, name string, now time.Time) (string, error) {
	token := NewRetryToken(now)
	_, err := dyn.Resource(HelmReleaseGVR).Namespace(FluxNamespace).Patch(ctx, name, types.MergePatchType, RetryPatch(token), metav1.PatchOptions{})
	return token, err
}

// RetryRequestDropped reports whether helm-controller has seen the last
// retry request on u and did not act on it.
//
// helm-controller records status.lastHandledReconcileAt for every reconcile
// it starts, also one that returns early because a dependency is not ready.
// It honours resetAt / forceAt only in a reconcile whose requestedAt it has
// not handled yet. So a request that arrives while a dependency is not
// ready is consumed without effect: lastHandledReconcileAt takes the token,
// lastHandledResetAt / lastHandledForceAt never do, and the same annotation
// values are ignored from then on. Measured on a fresh two-region
// installation (15 November 2026): a reset of the follower region's
// bp-keycloak sent while bp-postgres-shared was re-rendering did nothing;
// the same request sent again with the dependency Ready was handled at once.
func RetryRequestDropped(u *unstructured.Unstructured) bool {
	if u == nil {
		return false
	}
	ann := u.GetAnnotations()
	requested := ann[AnnotationReconcileRequestedAt]
	if requested == "" {
		return false
	}
	handled, _, _ := unstructured.NestedString(u.Object, "status", "lastHandledReconcileAt")
	if handled != requested {
		return false // not seen yet: still pending, not dropped
	}
	for annotation, statusField := range map[string]string{
		AnnotationReconcileResetAt: "lastHandledResetAt",
		AnnotationReconcileForceAt: "lastHandledForceAt",
	} {
		want := ann[annotation]
		if want == "" || want != requested {
			continue
		}
		got, _, _ := unstructured.NestedString(u.Object, "status", statusField)
		if got != want {
			return true
		}
	}
	return false
}

// RetryRequestedSince reports whether u carries a retry request written at
// or after t (the token is the request's time).
func RetryRequestedSince(u *unstructured.Unstructured, t time.Time) bool {
	token := u.GetAnnotations()[AnnotationReconcileResetAt]
	if token == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, token)
	return err == nil && !at.Before(t)
}

// WaitingOnDependency reports whether u is not Ready because a dependency
// is not ready. A retry request sent in this state is dropped (see
// RetryRequestDropped), so callers wait for it to end.
func WaitingOnDependency(u *unstructured.Unstructured) bool {
	conds, _ := extractConditions(u)
	ready := findCondition(conds, "Ready")
	if ready == nil || ready.Status == metav1.ConditionTrue {
		return false
	}
	return ready.Reason == "DependencyNotReady" || isDependencyMessage(ready.Message)
}

// AttemptInProgress reports whether helm-controller is in the middle of an
// install or upgrade of u (Ready is Unknown). Such a release is left alone.
func AttemptInProgress(u *unstructured.Unstructured) bool {
	conds, _ := extractConditions(u)
	ready := findCondition(conds, "Ready")
	return ready != nil && ready.Status == metav1.ConditionUnknown
}

// NeverDeployedRollbackStall reports whether u is stalled on a rollback
// that has no target because the release was never deployed, and when that
// stall began.
//
// The path: helm-controller dies (a node reboot) while the first install is
// `pending-install`. On restart Flux unlocks the release and, since a
// release object now exists, continues with an UPGRADE. If that upgrade
// fails, the upgrade remediation rolls back, and there is no deployed
// revision to roll back to: `Stalled=True MissingRollbackTarget`. Flux
// never retries a stalled release, so the release, and every release that
// depends on it, stays down until a spec change or a reset.
//
// "Never deployed" is read from status.history: no entry is `deployed` or
// `superseded` (a superseded entry was deployed once). A release that was
// deployed before and still reports this reason is a different defect and
// is not touched.
func NeverDeployedRollbackStall(u *unstructured.Unstructured) (bool, time.Time) {
	if u == nil {
		return false, time.Time{}
	}
	if suspended, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); ok && suspended {
		return false, time.Time{}
	}
	conds, _ := extractConditions(u)
	stalled := findCondition(conds, "Stalled")
	if stalled == nil || stalled.Status != metav1.ConditionTrue || stalled.Reason != ReasonMissingRollbackTarget {
		return false, time.Time{}
	}
	history, _, _ := unstructured.NestedSlice(u.Object, "status", "history")
	for _, raw := range history {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		status, _ := entry["status"].(string)
		switch strings.ToLower(status) {
		case "deployed", "superseded":
			return false, time.Time{}
		}
	}
	return true, stalled.LastTransitionTime.Time
}

// healNeverDeployedStalls resets every bp-* HelmRelease of the list that is
// stalled on a missing rollback target and was never deployed (#7902 defect
// 1), ONCE per stall: the stall's own transition time is the key, so the
// 45-second re-census does not patch the same stall twice, and a release
// that stalls again after the retry (a new transition time) gets one more
// reset. A release that was never deployed is still in its first install,
// which the kit retries without limit; this gives the interrupted first
// install the same.
//
// A request helm-controller dropped because a dependency was not ready is
// sent again once the dependency is ready (RetryRequestDropped).
//
// Best-effort: a failed patch is reported as an event and tried again on
// the next re-census. It never changes the watch's outcome by itself; the
// release's own conditions do.
func (w *Watcher) healNeverDeployedStalls(ctx context.Context, dyn dynamic.Interface, list *unstructured.UnstructuredList) {
	if list == nil || dyn == nil {
		return
	}
	for i := range list.Items {
		u := &list.Items[i]
		name := u.GetName()
		if !IsBlueprintRelease(name) {
			continue
		}
		stalledNow, since := NeverDeployedRollbackStall(u)
		if !stalledNow {
			continue
		}
		key := since.UTC().Format(time.RFC3339)

		w.mu.Lock()
		if w.healedStalls == nil {
			w.healedStalls = map[string]string{}
		}
		// Reset already: by this watcher, or by another one (a region can
		// have more than one watcher, and a watch can be re-attached). The
		// request's own token is its time, so the release itself says so.
		already := w.healedStalls[name] == key || RetryRequestedSince(u, since)
		w.mu.Unlock()

		reason := "its first install was interrupted and the upgrade that continued it failed with no revision to roll back to"
		if already {
			// Same stall, already reset. Send again only when the first
			// request was dropped and can be heard now.
			if !RetryRequestDropped(u) || WaitingOnDependency(u) {
				continue
			}
			reason = "the earlier reset arrived while a dependency was not ready and was dropped"
		} else if WaitingOnDependency(u) {
			// A request sent now would be dropped; the next re-census
			// looks again.
			continue
		}

		patchCtx, cancel := context.WithTimeout(ctx, DefaultRESTConfigTimeout)
		_, err := RequestRetry(patchCtx, dyn, name, w.cfg.Now())
		cancel()
		if err != nil {
			w.dispatch(provisioner.Event{
				Time:    w.cfg.Now().UTC().Format(time.RFC3339),
				Phase:   PhaseComponent,
				Level:   "warn",
				Message: fmt.Sprintf("%s is stalled (%s) and was never deployed; the reset could not be written (%v) and is tried again on the next re-census", name, ReasonMissingRollbackTarget, err),
			})
			continue
		}
		w.mu.Lock()
		w.healedStalls[name] = key
		w.mu.Unlock()
		w.dispatch(provisioner.Event{
			Time:    w.cfg.Now().UTC().Format(time.RFC3339),
			Phase:   PhaseComponent,
			Level:   "warn",
			Message: fmt.Sprintf("%s was stalled (%s): %s. Flux never retries a stalled release, so its failure counters were reset and a fresh attempt requested.", name, ReasonMissingRollbackTarget, reason),
		})
	}
}

// inFlightIfNeverDeployedStall maps the state of a release that is stalled
// on a missing rollback target and was never deployed to StateInstalling.
//
// The census must not count such a release as terminally failed: the
// re-census resets it (healNeverDeployedStalls) and it gets a fresh attempt.
// Counted as failed, a release nothing depends on would close the watch on
// the informer event, before any re-census ran, and the late-poll (which
// does not re-census) would wait out its window for a retry Flux never
// makes. While the reset cannot be written the release stays in flight and
// the watch ends on its time budget, which is the recoverable outcome.
//
// The same holds for a release that reads Ready from BEFORE a change of its
// desired state: until helm-controller has observed the current generation
// the release has not been reconciled against what is now asked of it. The
// every-region gate depends on this: right after the cross-region database
// pair is switched on, every release still reads Ready, and a watch that
// took that for installed would conclude ready before the new render lands.
func inFlightIfNeverDeployedStall(u *unstructured.Unstructured, state string) string {
	if state == StateInstalled && !ObservedCurrentGeneration(u) {
		if suspended, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); !ok || !suspended {
			return StateInstalling
		}
		return state
	}
	if state != StateFailed && state != StateDegraded {
		return state
	}
	if stalled, _ := NeverDeployedRollbackStall(u); stalled {
		return StateInstalling
	}
	return state
}

// IsBlueprintRelease reports whether a HelmRelease name is a Blueprint
// release of the bootstrap kit (the population the Phase-1 watch counts).
func IsBlueprintRelease(name string) bool {
	return ComponentIDFromHelmRelease(name) != name
}

// SnapshotHelmRelease projects one bp-* HelmRelease into a ComponentSnapshot
// by the rules of ListAndSnapshotHelmReleases, for a caller that already
// holds the object (the every-region gate lists once and needs the object
// for ObservedCurrentGeneration too).
func SnapshotHelmRelease(u *unstructured.Unstructured) ComponentSnapshot {
	return snapshotFromHelmRelease(u, ComponentIDFromHelmRelease(u.GetName()))
}

// ObservedCurrentGeneration reports whether a Flux object's controller has
// observed the object's current generation. A Ready=True condition can be
// from before a spec change; until status.observedGeneration catches up the
// object has not been reconciled against what is now asked of it. An object
// that carries no generation (a fixture) counts as observed.
func ObservedCurrentGeneration(u *unstructured.Unstructured) bool {
	generation := u.GetGeneration()
	if generation == 0 {
		return true
	}
	observed, found, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if !found {
		return false
	}
	return observed >= generation
}
