// HTTPRoute drainer — patches Cilium HTTPRoute backendRefs[].weight
// at switchover time. The continuum-controller imports this from
// the per-CR goroutine; tests inject a fake.
//
// Per ADR-0001 §2.7 we use the dynamic client + Unstructured for
// the gateway.networking.k8s.io HTTPRoute CRD (same pattern the rest
// of the controller uses for CR access).

package switchover

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// HTTPRouteGVR — Gateway API HTTPRoute v1.
var HTTPRouteGVR = schema.GroupVersionResource{
	Group:    "gateway.networking.k8s.io",
	Version:  "v1",
	Resource: "httproutes",
}

// RegionLabelKey is the BackendRef metadata field the drainer
// matches when looking for "the backendRef that points at <region>".
// HTTPRoute spec doesn't expose a region attribute directly, but the
// upstream Service that backendRef.name resolves to carries the
// `openova.io/region` label. The drainer interprets backendRef.name
// by convention: `<app>-<region>` (the Application controller's
// render layer guarantees that naming).
//
// When the convention is broken (e.g. legacy HTTPRoute with bare
// `<app>` backend names), the drainer falls back to setting weight=0
// on EVERY backendRef, which preserves the safety invariant ("nothing
// goes to the old primary") at the cost of also draining the new
// primary — the next reconcile pass restores the correct weights.
const RegionConventionSuffix = "-"

// DynamicHTTPRouteDrainer satisfies HTTPRouteDrainer via a dynamic
// client. Production wiring; tests use a hand-written fake.
type DynamicHTTPRouteDrainer struct {
	Dyn dynamic.Interface
}

// NewDynamicHTTPRouteDrainer returns a drainer backed by the dynamic
// client.
func NewDynamicHTTPRouteDrainer(dyn dynamic.Interface) *DynamicHTTPRouteDrainer {
	return &DynamicHTTPRouteDrainer{Dyn: dyn}
}

// SetWeightZero patches HTTPRoute.spec.rules[].backendRefs[]: each
// backend whose name suffix matches `<region>` gets weight=0.
// Returns the prior weights (in-order) for rollback.
func (d *DynamicHTTPRouteDrainer) SetWeightZero(ctx context.Context, namespace, name, region string) ([]int, error) {
	if d == nil || d.Dyn == nil {
		return nil, errors.New("drainer: nil Dyn")
	}
	cr, err := d.Dyn.Resource(HTTPRouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("drainer: get httproute: %w", err)
	}
	prior, err := patchHTTPRouteWeights(cr, region, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Dyn.Resource(HTTPRouteGVR).Namespace(namespace).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		return prior, fmt.Errorf("drainer: update httproute: %w", err)
	}
	return prior, nil
}

// RestoreWeights re-patches the HTTPRoute with the prior weights
// (rollback path). Idempotent.
func (d *DynamicHTTPRouteDrainer) RestoreWeights(ctx context.Context, namespace, name string, weights []int) error {
	if d == nil || d.Dyn == nil {
		return errors.New("drainer: nil Dyn")
	}
	cr, err := d.Dyn.Resource(HTTPRouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("drainer: get httproute (restore): %w", err)
	}
	if err := setHTTPRouteWeights(cr, weights); err != nil {
		return err
	}
	if _, err := d.Dyn.Resource(HTTPRouteGVR).Namespace(namespace).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("drainer: update httproute (restore): %w", err)
	}
	return nil
}

// patchHTTPRouteWeights walks spec.rules[].backendRefs[]. For each
// backendRef whose name has suffix `-<region>` it sets weight=newWeight
// and records the prior value in the returned slice. When NO backend
// matches, every backendRef's weight goes to newWeight (fail-safe
// fallback documented at the top of this file).
func patchHTTPRouteWeights(cr *unstructured.Unstructured, region string, newWeight int) ([]int, error) {
	if cr == nil {
		return nil, errors.New("nil HTTPRoute")
	}
	rules, found, err := unstructured.NestedSlice(cr.Object, "spec", "rules")
	if err != nil {
		return nil, fmt.Errorf("read rules: %w", err)
	}
	if !found {
		return nil, nil
	}
	prior := []int{}
	matchedAny := false
	suffix := RegionConventionSuffix + region

	for i, rule := range rules {
		rm, ok := rule.(map[string]interface{})
		if !ok {
			continue
		}
		brs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
		for j, br := range brs {
			bm, ok := br.(map[string]interface{})
			if !ok {
				continue
			}
			bn, _, _ := unstructured.NestedString(bm, "name")
			if hasSuffix(bn, suffix) {
				matchedAny = true
				prior = append(prior, intWeight(bm))
				bm["weight"] = int64(newWeight)
				brs[j] = bm
			}
		}
		_ = unstructured.SetNestedSlice(rm, brs, "backendRefs")
		rules[i] = rm
	}
	// Fallback: no backend matched → drain everything (safety wins).
	if !matchedAny {
		for i, rule := range rules {
			rm, _ := rule.(map[string]interface{})
			brs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
			for j, br := range brs {
				bm, ok := br.(map[string]interface{})
				if !ok {
					continue
				}
				prior = append(prior, intWeight(bm))
				bm["weight"] = int64(newWeight)
				brs[j] = bm
			}
			_ = unstructured.SetNestedSlice(rm, brs, "backendRefs")
			rules[i] = rm
		}
	}
	if err := unstructured.SetNestedSlice(cr.Object, rules, "spec", "rules"); err != nil {
		return nil, fmt.Errorf("write rules: %w", err)
	}
	return prior, nil
}

// setHTTPRouteWeights overwrites every backendRef's weight with the
// values in `weights`, in encounter-order. Used by the rollback path.
func setHTTPRouteWeights(cr *unstructured.Unstructured, weights []int) error {
	rules, found, err := unstructured.NestedSlice(cr.Object, "spec", "rules")
	if err != nil {
		return fmt.Errorf("read rules: %w", err)
	}
	if !found {
		return nil
	}
	idx := 0
	for ri, rule := range rules {
		rm, ok := rule.(map[string]interface{})
		if !ok {
			continue
		}
		brs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
		for bi, br := range brs {
			bm, ok := br.(map[string]interface{})
			if !ok {
				continue
			}
			if idx >= len(weights) {
				break
			}
			bm["weight"] = int64(weights[idx])
			idx++
			brs[bi] = bm
		}
		_ = unstructured.SetNestedSlice(rm, brs, "backendRefs")
		rules[ri] = rm
	}
	return unstructured.SetNestedSlice(cr.Object, rules, "spec", "rules")
}

// intWeight reads the int64 weight from a backendRef map; defaults
// to 1 (the Gateway API default).
func intWeight(bm map[string]interface{}) int {
	if w, ok := bm["weight"]; ok {
		switch v := w.(type) {
		case int64:
			return int(v)
		case int:
			return v
		case float64:
			return int(v)
		}
	}
	return 1
}

// hasSuffix is a stdlib-free check.
func hasSuffix(s, suf string) bool {
	if len(s) < len(suf) {
		return false
	}
	return s[len(s)-len(suf):] == suf
}
