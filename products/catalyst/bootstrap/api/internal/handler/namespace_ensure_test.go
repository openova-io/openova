// namespace_ensure_test.go — #3598 (EPIC #3597) contract coverage for
// the create-into-Org-without-namespace path. The create-from-catalog
// handlers must ENSURE the Org/Environment namespace before creating the
// Application CR so an install never fails with `namespaces "<org>" not
// found`.
package handler

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// fakeNamespaceClient builds a fake dynamic client that knows the
// namespaces GVR list-kind (plus the Application GVR so the same client
// can be reused by the create-path tests).
func fakeNamespaceClient(seed ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		namespacesGVR():  "NamespaceList",
		ApplicationGVR(): "ApplicationList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
}

func nsObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]interface{}{"name": name},
		},
	}
}

func getNamespace(t *testing.T, client *dynamicfake.FakeDynamicClient, name string) (*unstructured.Unstructured, error) {
	t.Helper()
	return client.Resource(namespacesGVR()).Get(context.Background(), name, metav1.GetOptions{})
}

// The core #3598 contract: ensuring a namespace that does NOT exist
// creates it (so the subsequent Application CR create won't hit
// "namespace not found").
func TestEnsureOrgNamespace_CreatesWhenAbsent(t *testing.T) {
	client := fakeNamespaceClient()

	// Pre-condition: the namespace is genuinely absent.
	if _, err := getNamespace(t, client, "acme"); !apierrors.IsNotFound(err) {
		t.Fatalf("precondition: expected acme namespace absent, got err=%v", err)
	}

	if err := ensureOrgNamespace(context.Background(), client, "acme"); err != nil {
		t.Fatalf("ensureOrgNamespace returned error: %v", err)
	}

	got, err := getNamespace(t, client, "acme")
	if err != nil {
		t.Fatalf("namespace acme not present after ensure: %v", err)
	}
	if got.GetName() != "acme" {
		t.Fatalf("created namespace name = %q, want acme", got.GetName())
	}
	// It should carry the catalyst-api managed-by marker.
	labels := got.GetLabels()
	if labels["app.kubernetes.io/managed-by"] != "catalyst-api" {
		t.Fatalf("ensured namespace missing managed-by label, labels=%v", labels)
	}
}

// Idempotency: ensuring an already-present namespace is a no-op success
// (the steady-state + GitOps-already-created path).
func TestEnsureOrgNamespace_IdempotentWhenPresent(t *testing.T) {
	client := fakeNamespaceClient(nsObject("acme"))

	if err := ensureOrgNamespace(context.Background(), client, "acme"); err != nil {
		t.Fatalf("ensureOrgNamespace on existing namespace returned error: %v", err)
	}

	// Still exactly one namespace named acme (no duplicate / no error).
	if _, err := getNamespace(t, client, "acme"); err != nil {
		t.Fatalf("namespace acme missing after idempotent ensure: %v", err)
	}
}

// A blank namespace is a programmer error and must be rejected rather
// than creating a cluster-scoped object with an empty name.
func TestEnsureOrgNamespace_RejectsEmpty(t *testing.T) {
	client := fakeNamespaceClient()
	if err := ensureOrgNamespace(context.Background(), client, "   "); err == nil {
		t.Fatalf("ensureOrgNamespace(\"\") = nil, want error")
	}
}
