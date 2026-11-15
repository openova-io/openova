package crossregion

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// TestKitSubstitutes covers the reader of #7902 defect 2: source order,
// inline on top, an optional source that is absent, a required source that
// is absent (an error, never an empty map), and the inline-only form of
// every installation before the substitutes moved.
func TestKitSubstitutes(t *testing.T) {
	ctx := context.Background()
	cs := kfake.NewSimpleClientset()
	if _, err := cs.CoreV1().ConfigMaps(helmwatch.FluxNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "sovereign-variables", Namespace: helmwatch.FluxNamespace},
		Data:       map[string]string{"A": "from-configmap", "B": "from-configmap"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ConfigMap: %v", err)
	}
	seedSecret(t, cs, helmwatch.FluxNamespace, "later-source", map[string]string{"B": "from-secret", "C": "from-secret"})

	ks := kitKustomizationObject()
	_ = unstructured.SetNestedSlice(ks.Object, []any{
		map[string]any{"kind": "ConfigMap", "name": "sovereign-variables"},
		map[string]any{"kind": "Secret", "name": "later-source"},
		map[string]any{"kind": "ConfigMap", "name": "absent-optional", "optional": true},
	}, "spec", "postBuild", "substituteFrom")
	_ = unstructured.SetNestedStringMap(ks.Object, map[string]string{"C": "inline"}, "spec", "postBuild", "substitute")

	got, err := KitSubstitutes(ctx, cs, ks, testTimeout)
	if err != nil {
		t.Fatalf("KitSubstitutes: %v", err)
	}
	for k, v := range map[string]string{"A": "from-configmap", "B": "from-secret", "C": "inline"} {
		if got[k] != v {
			t.Errorf("substitutes[%s] = %q, want %q", k, got[k], v)
		}
	}

	missing := kitKustomizationObject()
	_ = unstructured.SetNestedSlice(missing.Object, []any{
		map[string]any{"kind": "ConfigMap", "name": "absent-required"},
	}, "spec", "postBuild", "substituteFrom")
	if _, err := KitSubstitutes(ctx, cs, missing, testTimeout); err == nil {
		t.Errorf("no error for an absent required source: the callers decide from these values and must not get an empty map")
	}

	inlineOnly := kitKustomizationObject()
	_ = unstructured.SetNestedStringMap(inlineOnly.Object, map[string]string{"SOVEREIGN_PRIMARY_REGION": "a"}, "spec", "postBuild", "substitute")
	got, err = KitSubstitutes(ctx, nil, inlineOnly, testTimeout)
	if err != nil || got["SOVEREIGN_PRIMARY_REGION"] != "a" {
		t.Errorf("inline-only read = %v, %v; want the inline values without a cluster client", got, err)
	}

	if _, err := KitSubstitutes(ctx, nil, ks, testTimeout); err == nil {
		t.Errorf("no error when a source is named and there is no cluster client to read it")
	}
}
