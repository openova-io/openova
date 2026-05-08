// hpa.go — HPA-effective evaluator.
//
// EPIC-1 (#1096) §4.3 row "Autoscaler (HPA/VPA) effective".
//
// Logic (per `02-W-watcher-extension.md` brief):
//   - If the target is a Pod, walk owner chain Pod → ReplicaSet →
//     Deployment to identify the workload owner (StatefulSet /
//     DaemonSet are also acceptable owners). Pods owned by Jobs /
//     CronJobs / standalone are out-of-scope (skip).
//   - Find an HPA (autoscaling/v2) whose spec.scaleTargetRef points
//     at the workload owner.
//   - No HPA → result=skip (HPA isn't applicable to this workload —
//     e.g. a singleton control-plane pod).
//   - HPA present but currentReplicas < minReplicas → result=fail
//     (the autoscaler isn't keeping the floor).
//   - HPA present + currentReplicas >= minReplicas → result=pass.
//
// The score aggregator (slice S1) drops `skip` rows from the
// denominator so this evaluator does NOT punish workloads that
// legitimately have no HPA.
package evaluators

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// HPAEvaluator implements `policy=hpa-effective`.
type HPAEvaluator struct {
	// MinFloor — synthetic floor below which the evaluator emits FAIL
	// even when the HPA reports happiness. Defaults to Config.HPAMinReplicas
	// at engine wiring.
	MinFloor int32
}

// NewHPAEvaluator constructs an HPAEvaluator with values copied from cfg.
func NewHPAEvaluator(cfg Config) *HPAEvaluator {
	return &HPAEvaluator{MinFloor: cfg.HPAMinReplicas}
}

// Name — canonical policy id.
func (HPAEvaluator) Name() string { return "hpa-effective" }

func (h *HPAEvaluator) Evaluate(ctx context.Context, snap Snapshot, target *unstructured.Unstructured) []SyntheticReport {
	if !isPod(target) {
		return nil
	}
	// 1. Resolve workload owner via Pod → ReplicaSet → Deployment chain.
	ownerKind, ownerName, ownerNamespace := resolveWorkloadOwner(snap, target)
	if ownerKind == "" || ownerName == "" {
		// Standalone Pod / Job-owned — out of scope.
		return []SyntheticReport{newSkip(h.Name(), target, "pod has no controller workload owner")}
	}

	// 2. Find an HPA whose scaleTargetRef matches.
	hpa := findHPAFor(snap, ownerKind, ownerName, ownerNamespace)
	if hpa == nil {
		return []SyntheticReport{newSkip(h.Name(), target, fmt.Sprintf("no HPA targets %s/%s in %s", ownerKind, ownerName, ownerNamespace))}
	}

	// 3. Compare currentReplicas vs minReplicas (and the synthetic
	//    floor MinFloor).
	min, _ := hpaMinReplicas(hpa)
	current, _ := hpaCurrentReplicas(hpa)
	if min < h.MinFloor {
		min = h.MinFloor
	}
	props := map[string]string{
		"hpaName":           hpa.GetName(),
		"hpaNamespace":      hpa.GetNamespace(),
		"minReplicas":       fmt.Sprintf("%d", min),
		"currentReplicas":   fmt.Sprintf("%d", current),
		"workloadKind":      ownerKind,
		"workloadName":      ownerName,
		"workloadNamespace": ownerNamespace,
	}
	if current < min {
		return []SyntheticReport{{
			Policy:     h.Name(),
			Rule:       h.Name(),
			Result:     ResultFail,
			Resource:   resourceFor(target),
			Namespace:  target.GetNamespace(),
			Message:    fmt.Sprintf("HPA %s/%s reports currentReplicas=%d below minReplicas=%d", hpa.GetNamespace(), hpa.GetName(), current, min),
			Properties: props,
		}}
	}
	return []SyntheticReport{{
		Policy:     h.Name(),
		Rule:       h.Name(),
		Result:     ResultPass,
		Resource:   resourceFor(target),
		Namespace:  target.GetNamespace(),
		Message:    fmt.Sprintf("HPA %s/%s satisfies minReplicas=%d (current=%d)", hpa.GetNamespace(), hpa.GetName(), min, current),
		Properties: props,
	}}
}

// resolveWorkloadOwner walks Pod → controller → (ReplicaSet →
// Deployment) and returns (kind, name, namespace) of the top-level
// workload. Returns ("","","") for standalone or Job-owned Pods.
func resolveWorkloadOwner(snap Snapshot, pod *unstructured.Unstructured) (string, string, string) {
	if pod == nil {
		return "", "", ""
	}
	ns := pod.GetNamespace()
	for _, ref := range pod.GetOwnerReferences() {
		// Direct workload owners — StatefulSet, DaemonSet are
		// terminal here.
		switch ref.Kind {
		case "StatefulSet", "DaemonSet", "Deployment":
			return ref.Kind, ref.Name, ns
		case "ReplicaSet":
			// Hop through ReplicaSet to Deployment if the RS itself
			// has a Deployment owner. We look up the RS in the
			// snapshot — if it isn't cached, fall back to "ReplicaSet"
			// directly (HPA can target ReplicaSet too, rarely).
			rsList, err := snap.List("replicaset", labels.Everything())
			if err == nil {
				for _, rs := range rsList {
					if rs.GetName() == ref.Name && rs.GetNamespace() == ns {
						for _, rsOwner := range rs.GetOwnerReferences() {
							if rsOwner.Kind == "Deployment" {
								return "Deployment", rsOwner.Name, ns
							}
						}
					}
				}
			}
			return "ReplicaSet", ref.Name, ns
		case "Job":
			// Job-owned pods are out of scope — return empty and let
			// the caller emit skip.
			return "", "", ""
		}
	}
	return "", "", ""
}

// findHPAFor scans every HPA in the snapshot and returns the first
// one whose spec.scaleTargetRef matches (kind, name, namespace).
// Returns nil when no match.
//
// HPA is a namespace-scoped resource; we iterate every namespace's
// HPA in the cache. Cost is O(hpa-count); typical clusters have
// O(10) HPAs so this is cheap.
//
// Kind name "horizontalpodautoscaler" — the canonical k8scache name
// for autoscaling/v2 HPAs (registered by the operator via the kinds
// ConfigMap on Sovereigns that opt-in; absent on others). When the
// kind is not registered the Snapshot.List returns an error and we
// gracefully report nil (caller emits skip).
func findHPAFor(snap Snapshot, ownerKind, ownerName, ownerNamespace string) *unstructured.Unstructured {
	hpaList, err := snap.List("horizontalpodautoscaler", labels.Everything())
	if err != nil {
		return nil
	}
	for _, hpa := range hpaList {
		if hpa.GetNamespace() != ownerNamespace {
			continue
		}
		ref, found, _ := unstructured.NestedMap(hpa.Object, "spec", "scaleTargetRef")
		if !found {
			continue
		}
		kind, _ := ref["kind"].(string)
		name, _ := ref["name"].(string)
		if strings.EqualFold(kind, ownerKind) && name == ownerName {
			return hpa
		}
	}
	return nil
}

// hpaMinReplicas extracts spec.minReplicas (int32 default 1).
func hpaMinReplicas(hpa *unstructured.Unstructured) (int32, bool) {
	v, found, err := unstructured.NestedInt64(hpa.Object, "spec", "minReplicas")
	if err != nil || !found {
		return 1, false
	}
	return int32(v), true
}

// hpaCurrentReplicas extracts status.currentReplicas (int32 default 0).
func hpaCurrentReplicas(hpa *unstructured.Unstructured) (int32, bool) {
	v, found, err := unstructured.NestedInt64(hpa.Object, "status", "currentReplicas")
	if err != nil || !found {
		return 0, false
	}
	return int32(v), true
}

// newSkip — small helper so each branch above stays readable.
func newSkip(policy string, target *unstructured.Unstructured, msg string) SyntheticReport {
	return SyntheticReport{
		Policy:    policy,
		Rule:      policy,
		Result:    ResultSkip,
		Resource:  resourceFor(target),
		Namespace: target.GetNamespace(),
		Message:   msg,
	}
}
