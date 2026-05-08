// flux.go — Flux-managed evaluator.
//
// EPIC-1 (#1096) §4.3 row "Flux-managed (GitOps)".
//
// Logic (per `02-W-watcher-extension.md` brief):
//
//   - Pass if the target carries the well-known
//     `app.kubernetes.io/managed-by: flux` label.
//   - Pass if any ownerReference has APIVersion containing
//     `helm.toolkit.fluxcd.io` (HelmRelease) or
//     `kustomize.toolkit.fluxcd.io` (Kustomization).
//   - Fail otherwise.
//
// The evaluator runs against Pods (the per-event trigger path) but
// returns a SyntheticReport that points at the Pod's controller
// owner (Deployment / StatefulSet / DaemonSet) when one is
// resolvable. This matches the rest of the pipeline — score
// aggregation rolls up by workload, not by individual Pod.
//
// Pods that are themselves Flux-owned (rare but possible — Flux can
// manage a static Pod manifest) also pass.
//
// Both label key + Flux-owner-suffix are configurable via
// Config.FluxManagedByLabel / Config.FluxManagedByValue. Per
// docs/INVIOLABLE-PRINCIPLES.md #4.
package evaluators

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// FluxEvaluator implements `policy=flux-managed`.
type FluxEvaluator struct {
	ManagedByLabel string
	ManagedByValue string
}

// NewFluxEvaluator builds a FluxEvaluator from cfg.
func NewFluxEvaluator(cfg Config) *FluxEvaluator {
	return &FluxEvaluator{
		ManagedByLabel: cfg.FluxManagedByLabel,
		ManagedByValue: cfg.FluxManagedByValue,
	}
}

func (FluxEvaluator) Name() string { return "flux-managed" }

func (f *FluxEvaluator) Evaluate(ctx context.Context, snap Snapshot, target *unstructured.Unstructured) []SyntheticReport {
	if !isPod(target) {
		return nil
	}

	// 1. Direct check on the Pod itself.
	if f.targetIsFluxOwned(target) {
		return []SyntheticReport{{
			Policy:     f.Name(),
			Rule:       f.Name(),
			Result:     ResultPass,
			Resource:   resourceFor(target),
			Namespace:  target.GetNamespace(),
			Message:    "Pod carries flux managed-by label or HelmRelease / Kustomization owner",
			Properties: map[string]string{"detection": "pod-direct"},
		}}
	}

	// 2. Pod isn't directly Flux-managed — chase its controller.
	owner := f.lookupController(snap, target)
	if owner != nil && f.targetIsFluxOwned(owner) {
		return []SyntheticReport{{
			Policy:    f.Name(),
			Rule:      f.Name(),
			Result:    ResultPass,
			Resource:  resourceFor(target),
			Namespace: target.GetNamespace(),
			Message:   "Pod's controller " + owner.GetKind() + "/" + owner.GetName() + " is Flux-managed",
			Properties: map[string]string{
				"detection":    "controller-flux-owned",
				"controller":   owner.GetKind() + "/" + owner.GetName(),
				"controllerNs": owner.GetNamespace(),
			},
		}}
	}

	return []SyntheticReport{{
		Policy:    f.Name(),
		Rule:      f.Name(),
		Result:    ResultFail,
		Resource:  resourceFor(target),
		Namespace: target.GetNamespace(),
		Message:   "Pod and its controller carry no flux managed-by label and no Flux ownerRef",
	}}
}

// targetIsFluxOwned applies the label + ownerRef check to a single
// object. Pure function — no snapshot reads.
func (f *FluxEvaluator) targetIsFluxOwned(target *unstructured.Unstructured) bool {
	if target == nil {
		return false
	}
	// Label check.
	if v, ok := target.GetLabels()[f.ManagedByLabel]; ok && strings.EqualFold(v, f.ManagedByValue) {
		return true
	}
	// ownerRef check — HelmRelease / Kustomization from
	// helm.toolkit.fluxcd.io / kustomize.toolkit.fluxcd.io.
	for _, ref := range target.GetOwnerReferences() {
		if strings.Contains(ref.APIVersion, "fluxcd.io") {
			return true
		}
	}
	return false
}

// lookupController follows the Pod's controller ownerRef one hop
// (Pod → ReplicaSet / StatefulSet / DaemonSet) and where applicable
// chases the next hop (ReplicaSet → Deployment). Returns nil when
// the controller can't be located in the snapshot.
func (f *FluxEvaluator) lookupController(snap Snapshot, pod *unstructured.Unstructured) *unstructured.Unstructured {
	for _, ref := range pod.GetOwnerReferences() {
		switch ref.Kind {
		case "Deployment":
			return findInList(snap, "deployment", pod.GetNamespace(), ref.Name)
		case "StatefulSet":
			return findInList(snap, "statefulset", pod.GetNamespace(), ref.Name)
		case "DaemonSet":
			return findInList(snap, "daemonset", pod.GetNamespace(), ref.Name)
		case "ReplicaSet":
			rs := findInList(snap, "replicaset", pod.GetNamespace(), ref.Name)
			if rs == nil {
				return nil
			}
			// First check the RS itself — sometimes Flux
			// produces ReplicaSets directly without a Deployment.
			if f.targetIsFluxOwned(rs) {
				return rs
			}
			for _, rsOwner := range rs.GetOwnerReferences() {
				if rsOwner.Kind == "Deployment" {
					return findInList(snap, "deployment", pod.GetNamespace(), rsOwner.Name)
				}
			}
			return rs
		}
	}
	return nil
}

// findInList walks the snapshot and returns the first object matching
// (kind, namespace, name). Returns nil on miss or list error.
func findInList(snap Snapshot, kindName, namespace, name string) *unstructured.Unstructured {
	list, err := snap.List(kindName, labels.Everything())
	if err != nil {
		return nil
	}
	for _, obj := range list {
		if obj.GetNamespace() == namespace && obj.GetName() == name {
			return obj
		}
	}
	return nil
}
