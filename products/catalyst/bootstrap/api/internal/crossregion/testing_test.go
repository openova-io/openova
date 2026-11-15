package crossregion

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

const testTimeout = 5 * time.Second

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeRegion is a fake cluster that knows HelmReleases and Kustomizations.
func fakeRegion(t *testing.T, objs ...*unstructured.Unstructured) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{
		{Group: kustomizationGVR.Group, Version: kustomizationGVR.Version, Kind: "Kustomization"},
		{Group: helmwatch.HelmReleaseGVR.Group, Version: helmwatch.HelmReleaseGVR.Version, Kind: "HelmRelease"},
	} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		list := gvk
		list.Kind += "List"
		scheme.AddKnownTypeWithName(list, &unstructured.UnstructuredList{})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kustomizationGVR:         "KustomizationList",
		helmwatch.HelmReleaseGVR: "HelmReleaseList",
	})
	for _, o := range objs {
		res := helmwatch.HelmReleaseGVR
		if o.GetKind() == "Kustomization" {
			res = kustomizationGVR
		}
		if _, err := dyn.Resource(res).Namespace(helmwatch.FluxNamespace).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s %s: %v", o.GetKind(), o.GetName(), err)
		}
	}
	return dyn
}

func cond(typ, status, reason string, at time.Time) map[string]any {
	c := map[string]any{"type": typ, "status": status, "reason": reason}
	if !at.IsZero() {
		c["lastTransitionTime"] = at.UTC().Format(time.RFC3339)
	}
	return c
}

func release(name string, conds ...map[string]any) *unstructured.Unstructured {
	list := make([]any, 0, len(conds))
	for _, c := range conds {
		list = append(list, c)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": helmwatch.HelmReleaseGVR.Group + "/" + helmwatch.HelmReleaseGVR.Version,
		"kind":       "HelmRelease",
		"metadata":   map[string]any{"name": name, "namespace": helmwatch.FluxNamespace},
		"status":     map[string]any{"conditions": list},
	}}
}

func readyRelease(name string) *unstructured.Unstructured {
	return release(name, cond("Ready", "True", "InstallSucceeded", time.Now().Add(-time.Minute)))
}

func kitKustomizationObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kustomizationGVR.Group + "/" + kustomizationGVR.Version,
		"kind":       "Kustomization",
		"metadata":   map[string]any{"name": kitKustomization, "namespace": helmwatch.FluxNamespace},
		"spec":       map[string]any{"interval": "5m"},
	}}
}

func getRelease(t *testing.T, dyn dynamic.Interface, name string) *unstructured.Unstructured {
	t.Helper()
	u, err := dyn.Resource(helmwatch.HelmReleaseGVR).Namespace(helmwatch.FluxNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get HelmRelease %s: %v", name, err)
	}
	return u
}

func updateRelease(t *testing.T, dyn dynamic.Interface, u *unstructured.Unstructured) {
	t.Helper()
	if _, err := dyn.Resource(helmwatch.HelmReleaseGVR).Namespace(helmwatch.FluxNamespace).Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update HelmRelease %s: %v", u.GetName(), err)
	}
}

func seedSecret(t *testing.T, cs kubernetes.Interface, namespace, name string, data map[string]string) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", namespace, err)
	}
	b := make(map[string][]byte, len(data))
	for k, v := range data {
		b[k] = []byte(v)
	}
	if _, err := cs.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       b,
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Secret %s/%s: %v", namespace, name, err)
	}
}

func secretValue(t *testing.T, cs kubernetes.Interface, namespace, name, key string) (string, bool) {
	t.Helper()
	s, err := cs.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("get Secret %s/%s: %v", namespace, name, err)
	}
	return string(s.Data[key]), true
}

// plainCopy is a stand-in for the handler's cross-cluster copy: create or
// replace, and report whether the destination's bytes changed.
func plainCopy(ctx context.Context, src *corev1.Secret, dst kubernetes.Interface, namespace string) (bool, error) {
	want := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: namespace}, Type: src.Type, Data: src.Data}
	have, err := dst.CoreV1().Secrets(namespace).Get(ctx, src.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = dst.CoreV1().Secrets(namespace).Create(ctx, want, metav1.CreateOptions{})
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	same := len(have.Data) == len(src.Data)
	for k, v := range src.Data {
		if string(have.Data[k]) != string(v) {
			same = false
		}
	}
	if same {
		return false, nil
	}
	have.Data = src.Data
	_, err = dst.CoreV1().Secrets(namespace).Update(ctx, have, metav1.UpdateOptions{})
	return err == nil, err
}
