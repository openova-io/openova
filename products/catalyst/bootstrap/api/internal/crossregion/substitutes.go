// Package crossregion holds what the provisioning API does for the regions
// of one Sovereign that are not its primary region: the inputs a follower
// region cannot make itself, the restart of follower releases that failed
// before those inputs arrived, and the rule that a deployment is ready only
// when every region is (#7902).
//
// It is a package of its own, outside internal/handler, on purpose: the
// handler package may only shrink (scripts/check-api-structure.py). The
// handler's ClusterMesh orchestrator and its Phase-1 watch call in here with
// clients they already hold.
package crossregion

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
)

// KitSubstitutes returns the substitute values Flux applies to a
// Kustomization, read the way Flux reads them: every source named in
// spec.postBuild.substituteFrom in order (a later source wins over an
// earlier one), then the inline spec.postBuild.substitute map on top.
//
// The ClusterMesh orchestrator used to read the inline map alone (#7902
// defect 2). Since the control-plane boot template moved every substitute
// into the ConfigMap flux-system/sovereign-variables and left the
// bootstrap-kit Kustomization with a substituteFrom reference only, the
// inline map does not exist on a fresh installation: the orchestrator
// refused the database-pair switch for ever, with a message that named a
// map nobody writes any more.
//
// The orchestrator still WRITES its own keys inline (a JSON merge patch
// creates the map), which is correct: an inline value wins over a source
// value, so a key it sets overrides the boot-time value and leaves the
// ConfigMap, which the boot template owns, untouched.
//
// A source marked optional may be absent. Any other read failure is
// returned: the caller must not decide from a partial map (a missing
// region key would refuse the switch, a missing shared-database key would
// skip a copy the follower needs). callTimeout bounds each read.
func KitSubstitutes(ctx context.Context, cs kubernetes.Interface, ks *unstructured.Unstructured, callTimeout time.Duration) (map[string]string, error) {
	out := map[string]string{}
	if ks == nil {
		return out, fmt.Errorf("Kustomization is nil")
	}
	namespace := ks.GetNamespace()

	sources, _, err := unstructured.NestedSlice(ks.Object, "spec", "postBuild", "substituteFrom")
	if err != nil {
		return out, fmt.Errorf("spec.postBuild.substituteFrom is not a list: %w", err)
	}
	for _, raw := range sources {
		src, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := src["kind"].(string)
		name, _ := src["name"].(string)
		optional, _ := src["optional"].(bool)
		if name == "" {
			continue
		}
		if cs == nil {
			return out, fmt.Errorf("no cluster client to read %s %s/%s named in spec.postBuild.substituteFrom", kind, namespace, name)
		}
		getCtx, cancel := context.WithTimeout(ctx, callTimeout)
		var data map[string]string
		var getErr error
		switch kind {
		case "ConfigMap":
			cm, e := cs.CoreV1().ConfigMaps(namespace).Get(getCtx, name, metav1.GetOptions{})
			if e == nil {
				data = cm.Data
			}
			getErr = e
		case "Secret":
			sec, e := cs.CoreV1().Secrets(namespace).Get(getCtx, name, metav1.GetOptions{})
			if e == nil {
				data = make(map[string]string, len(sec.Data))
				for k, v := range sec.Data {
					data[k] = string(v)
				}
			}
			getErr = e
		default:
			getErr = fmt.Errorf("unknown substituteFrom kind %q", kind)
		}
		cancel()
		if getErr != nil {
			if optional && apierrors.IsNotFound(getErr) {
				continue
			}
			return out, fmt.Errorf("read %s %s/%s named in spec.postBuild.substituteFrom: %w", kind, namespace, name, getErr)
		}
		for k, v := range data {
			out[k] = v
		}
	}

	inline, _, err := unstructured.NestedStringMap(ks.Object, "spec", "postBuild", "substitute")
	if err != nil {
		return out, fmt.Errorf("spec.postBuild.substitute is not a string map: %w", err)
	}
	for k, v := range inline {
		out[k] = v
	}
	return out, nil
}
