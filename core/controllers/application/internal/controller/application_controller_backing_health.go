// application_controller_backing_health.go — backing-workload readiness.
//
// A downstream Flux HelmRelease reporting Ready=True means only that Helm
// applied the chart's manifests and the apiserver ACCEPTED them — NOT that the
// workload the chart installs actually came up. Every resource that fails
// AFTER admission (a database that never elects a primary, a StatefulSet whose
// pods never pass their probes, an operator that rejects the spec at
// reconcile-time) leaves the HelmRelease legitimately Ready while the thing the
// User asked for is dead. Deriving Application readiness solely from
// HelmRelease readiness therefore paints a green badge over a dead workload.
// That is the defect class #5955 walked live on hw292:
//
//	Application hw292-omani-works/uat-ahs-pg  → Ready=True
//	  "installed across 2 region(s); Ready=True from downstream HelmRelease(s)"
//	backing Cluster hw292-omani-works/postgres → status.conditions[Ready]=False
//	  phase="Setting up primary", status.instances=1, status.readyInstances ABSENT
//
// ...on 2 of 2 per-Org Postgres clusters. The class is NOT Postgres-specific,
// so the gate below is not a Postgres special-case: it observes a REGISTRY of
// backing kinds (backingKinds) and consults each backing object's OWN readiness
// signal — primarily the standard status.conditions[type=Ready], the same
// contract the controller already trusts for HelmReleases.
//
// # The three-state contract (the load-bearing part)
//
// The predecessor of this gate (#5513) asked one narrow yes/no question — "is
// the backing CNPG Cluster in the terminal `unrecoverable` phase?" — and
// swallowed every read failure as a silent "no". On hw292 the
// application-controller's ServiceAccount has NO grant on
// postgresql.cnpg.io/clusters at all:
//
//	$ kubectl auth can-i list clusters.postgresql.cnpg.io -n hw292-omani-works \
//	    --as=system:serviceaccount:catalyst-system:catalyst-application-controller
//	no
//	# control, same impersonation, kind the controller DOES observe:
//	$ kubectl auth can-i list helmreleases.helm.toolkit.fluxcd.io -n hw292-omani-works \
//	    --as=system:serviceaccount:catalyst-system:catalyst-application-controller
//	yes
//
// so every List returned Forbidden, every Forbidden was skipped, and the guard
// could not fail no matter how dead the database was. A guard that cannot
// observe its subject and reports "healthy" is worse than no guard: it converts
// a missing permission into a false green.
//
// This gate therefore returns THREE states, never two:
//
//	backingReady        — every observed backing publishes Ready=True.
//	                      Also the answer when there is NO backing of a
//	                      registered kind (the overwhelmingly common case:
//	                      a web app, a queue, anything not in the registry) —
//	                      absence of a backing is not a failure, so those
//	                      Applications reach Ready exactly as before.
//	backingNotReady     — a backing was READ and it says it is not ready.
//	                      → Application Degraded, Ready=False. A verdict.
//	backingUnobservable — the backing could not be read (Forbidden, timeout,
//	                      API error) or it exists but has published no
//	                      readiness at all.
//	                      → Application Ready="Unknown", reason
//	                      BackingReadinessUnverifiable. NEVER a silent pass.
//
// "Cannot observe" is deliberately NOT collapsed into either "healthy" or
// "broken": claiming Degraded from an unread object would fabricate a failure,
// and claiming Ready would reinstate the exact defect. Unknown is the honest
// third answer and it is what the Ready condition's tri-state is for.
//
// The one read failure that is NOT unobservable is "this kind does not exist in
// this Sovereign" (no CRD → NotFound / no-kind-match). That is positive
// knowledge that there is no backing of that kind to observe, so it is skipped
// — a Sovereign with no CNPG installed must not rotate every Application to
// Unknown.
//
// The gate is READ-ONLY: it lists, it never writes.
package controller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CNPGClusterGVR is the CloudNativePG Cluster CR GVR (postgresql.cnpg.io/v1).
// Namespaced. Read via the dynamic client + Unstructured — the controller
// never depends on a generated CNPG client (mirrors the continuum-controller's
// ADR-0001 §2.7 convention).
var CNPGClusterGVR = schema.GroupVersionResource{
	Group:    "postgresql.cnpg.io",
	Version:  "v1",
	Resource: "clusters",
}

// InstanceLabel is the standard Helm/Kubernetes label a chart stamps on the
// resources it renders, carrying the Helm release name. Charts stamp it on the
// backing objects they create, so it is the stable link from an Application's
// per-cluster HelmRelease (whose name IS the release name) back to the objects
// that HelmRelease installed. Walked live on hw292: the per-Org CNPG Cluster
// `hw292-omani-works/postgres` carries
// `app.kubernetes.io/instance=uat-ahs-pg-rtz-a` (= the HR name).
const InstanceLabel = "app.kubernetes.io/instance"

// backingState is the three-state verdict for one backing object (and, rolled
// up, for the whole Application). Ordered by precedence: a definite not-ready
// verdict outranks an unobservable one, which outranks ready. Never reorder —
// the rollup relies on the ordering.
type backingState int

const (
	backingReady backingState = iota
	backingUnobservable
	backingNotReady
)

// backingVerdict is the rolled-up observation the status gate consults.
type backingVerdict struct {
	state backingState
	// reason is the condition reason to stamp (empty when state is
	// backingReady).
	reason string
	// detail is a human sentence naming the object and WHY, so the operator
	// reading `kubectl describe application` learns which backing object to
	// look at rather than just "something is wrong".
	detail string
}

// backingKind describes one backing-resource kind the readiness gate observes,
// and how to read readiness out of an instance of it. Adding a kind to the
// registry is a data change here — the gate itself stays kind-agnostic.
type backingKind struct {
	// GVR is the resource to list.
	GVR schema.GroupVersionResource
	// Label is the human name used in operator-facing messages.
	Label string
	// Readiness classifies one object of this kind. It returns the state and
	// a short detail phrase (no object name — the caller prefixes that).
	// It must return backingUnobservable — never backingReady — when the
	// object publishes no readiness signal it understands.
	Readiness func(*unstructured.Unstructured) (backingState, string)
}

// backingKinds is the registry the gate walks. It is deliberately small: a kind
// belongs here only when (a) an Application's HelmRelease can be Ready while
// that kind is dead, and (b) the kind publishes a readiness signal we can read.
var backingKinds = []backingKind{
	{
		GVR:       CNPGClusterGVR,
		Label:     "CNPG Cluster",
		Readiness: cnpgClusterReadiness,
	},
}

// observeBackingReadiness rolls up the readiness of every backing object this
// Application installed, across every registered kind.
//
// Discovery: a backing object carries `app.kubernetes.io/instance` set to the
// Helm release name of the HelmRelease that installed it — i.e. each per-cluster
// HR name (the fan-out path) or the bare Application name (the legacy single-HR
// host path). We search that label across the namespaces the HRs were authored
// in, plus the Application's own namespace, so a backing routed host-side
// (#4398) or landed in a vCluster host-ns is found either way.
//
// Precedence: any definite not-ready wins; else any unobservable wins; else
// ready. Read-only.
func (r *Reconciler) observeBackingReadiness(
	ctx context.Context,
	app *unstructured.Unstructured,
	perClusterStatus []map[string]interface{},
) backingVerdict {
	if r == nil || r.Dynamic == nil || app == nil {
		// No client to observe with — that is an unobservable backing, not a
		// healthy one. (In practice unreachable: the reconciler always has a
		// client. Encoded anyway so the "no observation ⇒ not a pass" rule
		// holds on every path.)
		return backingVerdict{
			state:  backingUnobservable,
			reason: ReasonBackingUnverifiable,
			detail: "no Kubernetes client available to observe backing resources",
		}
	}

	// Build the (namespace, instance-label) work list from the materialised
	// per-cluster HRs. Fall back to the bare Application identity when no
	// fan-out ran (legacy single-HR host path).
	nsSet := map[string]struct{}{app.GetNamespace(): {}}
	var instances []string
	for _, pcs := range perClusterStatus {
		hrName, _ := pcs["hr"].(string)
		if hrName == "" {
			continue
		}
		instances = append(instances, hrName)
		if ns, _ := pcs["namespace"].(string); ns != "" {
			nsSet[ns] = struct{}{}
		}
	}
	if len(instances) == 0 {
		instances = append(instances, app.GetName())
	}

	worst := backingVerdict{state: backingReady}
	// promote keeps the highest-precedence verdict seen so far.
	promote := func(v backingVerdict) {
		if v.state > worst.state {
			worst = v
		}
	}

	// De-dup (kind × namespace × instance) probes — the same HR name can
	// appear under several namespaces in the work list.
	type target struct {
		gvr      schema.GroupVersionResource
		ns       string
		instance string
	}
	seen := map[target]struct{}{}
	for _, bk := range backingKinds {
		for ns := range nsSet {
			for _, inst := range instances {
				t := target{gvr: bk.GVR, ns: ns, instance: inst}
				if _, ok := seen[t]; ok {
					continue
				}
				seen[t] = struct{}{}

				list, err := r.Dynamic.Resource(bk.GVR).Namespace(t.ns).List(ctx, metav1.ListOptions{
					LabelSelector: InstanceLabel + "=" + t.instance,
				})
				if err != nil {
					if backingKindAbsent(err) {
						// Positive knowledge: this Sovereign has no such kind,
						// so this Application has no backing of it. Not a
						// failure, not an unobserved object — skip.
						continue
					}
					// Forbidden / timeout / transient API error. We did NOT
					// observe the backing; saying "ready" here is the #5955
					// defect and saying "degraded" would fabricate a failure.
					promote(backingVerdict{
						state:  backingUnobservable,
						reason: ReasonBackingUnverifiable,
						detail: fmt.Sprintf("could not read %s objects in namespace %s (instance=%s): %v",
							bk.Label, t.ns, t.instance, err),
					})
					continue
				}
				for i := range list.Items {
					item := &list.Items[i]
					state, why := bk.Readiness(item)
					switch state {
					case backingReady:
						continue
					case backingNotReady:
						promote(backingVerdict{
							state:  backingNotReady,
							reason: backingNotReadyReason(why),
							detail: fmt.Sprintf("backing %s %s/%s is not ready (%s)",
								bk.Label, item.GetNamespace(), item.GetName(), why),
						})
					default:
						promote(backingVerdict{
							state:  backingUnobservable,
							reason: ReasonBackingUnverifiable,
							detail: fmt.Sprintf("backing %s %s/%s publishes no readiness signal (%s)",
								bk.Label, item.GetNamespace(), item.GetName(), why),
						})
					}
				}
			}
		}
	}
	return worst
}

// unrecoverableDetail is the marker a kind's Readiness func embeds in its
// detail phrase to request the pre-existing, more specific
// BackingClusterUnrecoverable reason (#5513) instead of the generic
// BackingNotReady. Keeping that reason string stable matters: it is asserted by
// the #5513 acceptance test and consumed by operator tooling.
const unrecoverableDetail = "unrecoverable"

func backingNotReadyReason(detail string) string {
	if strings.Contains(strings.ToLower(detail), unrecoverableDetail) {
		return ReasonBackingUnrecoverable
	}
	return ReasonBackingNotReady
}

// backingKindAbsent reports whether a List error means "this kind is not served
// by this cluster" — a CRD that was never installed. That is positive knowledge
// that there is nothing to observe, as opposed to a failure to observe
// something that exists.
//
// The three shapes:
//   - apierrors.IsNotFound — a real apiserver serving no such resource path
//     ("the server could not find the requested resource").
//   - meta.IsNoMatchError — the RESTMapper has no mapping for the GVR.
//   - runtime.IsNotRegisteredError — the fake dynamic client used in tests has
//     no list kind registered for the GVR.
//
// Everything else (Forbidden, Unauthorized, Timeout, ServiceUnavailable,
// TooManyRequests, transport errors) is a failure to observe and must NOT land
// here — that is precisely the class that made the #5513 guard inert on hw292.
func backingKindAbsent(err error) bool {
	if err == nil {
		return false
	}
	return apierrors.IsNotFound(err) ||
		meta.IsNoMatchError(err) ||
		runtime.IsNotRegisteredError(err)
}

// cnpgClusterReadiness classifies one CNPG Cluster CR.
//
// Signal precedence, all field paths verified live on hw292 against 11 CNPG
// Clusters (9 healthy controls + the 2 defective per-Org ones) rather than
// guessed:
//
//  1. status.phase containing "unrecoverable" — CNPG's terminal,
//     manual-intervention state. Kept as its own branch so the #5513 reason
//     survives.
//  2. status.conditions[type=Ready].status — the standard, kind-agnostic
//     readiness contract. Live: "True" on all 9 healthy clusters, "False" on
//     both defective per-Org clusters. This is the load-bearing signal.
//  3. status.readyInstances vs status.instances — used only as a fallback when
//     no Ready condition has been published yet, and as corroborating detail in
//     the message. It is deliberately NOT the primary signal: the live control
//     `catalyst-system/openova-flow-pg` reported instances=2 AND
//     readyInstances=2 while its Ready condition was False (phase="Instance
//     Status Extraction Error"), so gating on the counts alone would have
//     false-passed a cluster the operator's own tooling calls broken.
//
// A Cluster with neither a Ready condition nor instance counts is
// UNOBSERVABLE, not ready — a freshly-admitted CR that has published no status
// is exactly the "failed after admission" window this gate exists to close.
func cnpgClusterReadiness(cr *unstructured.Unstructured) (backingState, string) {
	if cr == nil {
		return backingUnobservable, "nil object"
	}
	status, hasStatus, _ := unstructured.NestedMap(cr.Object, "status")
	if !hasStatus || len(status) == 0 {
		return backingUnobservable, "no status published yet"
	}

	phase, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
	instances, hasInstances, _ := unstructured.NestedInt64(cr.Object, "status", "instances")
	ready, hasReady, _ := unstructured.NestedInt64(cr.Object, "status", "readyInstances")

	// Instance counts, rendered for the operator when we have them. Absent
	// readyInstances is reported as absent, never as zero-and-therefore-fine.
	counts := ""
	switch {
	case hasReady && hasInstances:
		counts = fmt.Sprintf("%d/%d instances ready", ready, instances)
	case hasInstances:
		counts = fmt.Sprintf("status.readyInstances absent, %d instance(s) declared", instances)
	}

	detail := func(head string) string {
		parts := []string{head}
		if phase != "" {
			parts = append(parts, "phase="+phase)
		}
		if counts != "" {
			parts = append(parts, counts)
		}
		return strings.Join(parts, "; ")
	}

	// 1. CNPG's terminal state, kept specific.
	if strings.Contains(strings.ToLower(phase), unrecoverableDetail) {
		return backingNotReady, detail("cluster is unrecoverable and needs manual intervention")
	}

	// 2. The standard Ready condition.
	switch cnpgReadyCondition(cr) {
	case "True":
		return backingReady, ""
	case "False":
		return backingNotReady, detail("status.conditions[type=Ready] is False")
	case "Unknown":
		return backingUnobservable, detail("status.conditions[type=Ready] is Unknown")
	}

	// 3. No Ready condition published. Fall back to the instance counts.
	if hasInstances && hasReady {
		if ready >= instances && instances > 0 {
			return backingReady, ""
		}
		return backingNotReady, detail("no Ready condition published")
	}
	return backingUnobservable, detail("no Ready condition and no readyInstances published")
}

// cnpgReadyCondition returns the status of status.conditions[type=Ready], or ""
// when no such condition exists.
func cnpgReadyCondition(cr *unstructured.Unstructured) string {
	conds, found, err := unstructured.NestedSlice(cr.Object, "status", "conditions")
	if err != nil || !found {
		return ""
	}
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t != "Ready" {
			continue
		}
		s, _ := cm["status"].(string)
		return s
	}
	return ""
}
