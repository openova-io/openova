package handler

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// namespacesGVR is the core/v1 Namespace resource. We address it through
// the dynamic client (not a typed corev1 clientset) because every
// Application-create path in this package already holds a
// dynamic.Interface — mirroring the read idiom in
// internal/infrastructure/topology_loader.go which lists namespaces via
// the same GVR.
func namespacesGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
}

// ensureOrgNamespace creates the target Organization/Environment
// namespace if it does not already exist, returning nil when the
// namespace is present (created-or-verified).
//
// Why this exists (#3598, EPIC #3597): the create-from-catalog paths
// (HandleCreateInstance + HandleApplicationInstall + the backing-service
// auto-create) write the Application CR straight into the Org namespace
// via `.Namespace(org).Create(...)`. That namespace is otherwise created
// asynchronously by the organization controller's GitOps reconcile
// (core/controllers/organization/internal/gitops/manifests.go renders a
// `kind: Namespace`, committed to Gitea and applied by Flux). When an
// operator installs an app into an Org whose GitOps namespace manifest
// has not yet reconciled, the CR create fails with
// `namespaces "<org>" not found` — the exact "namespace not found"
// symptom the founder hit. Ensuring the namespace here closes that race
// so the install never surfaces that error.
//
// Idempotent: an AlreadyExists on Create is treated as success (the
// race-loser path + the steady-state path both return nil). The
// namespace is labelled so it is recognisable as catalyst-managed and so
// a later GitOps apply of the same namespace is a no-op rather than a
// conflict.
//
// A blank namespace name is a programmer error (the caller validated the
// Org slug upstream); we return an error rather than creating a
// cluster-scoped object with an empty name.
func ensureOrgNamespace(ctx context.Context, client dynamic.Interface, namespace string) error {
	ns := strings.TrimSpace(namespace)
	if ns == "" {
		return fmt.Errorf("ensureOrgNamespace: empty namespace")
	}

	// Fast path: already present.
	if _, err := client.Resource(namespacesGVR()).Get(ctx, ns, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		// A non-NotFound Get error (RBAC, apiserver down) is surfaced so
		// the caller can return a clear 5xx instead of masking it as a
		// create failure below.
		return fmt.Errorf("ensureOrgNamespace: get namespace %q: %w", ns, err)
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": ns,
				"labels": map[string]interface{}{
					// Marks the namespace as created by catalyst-api's
					// install path. The organization controller's GitOps
					// namespace manifest carries its own labels; server-side
					// apply / Flux treats this pre-created namespace as the
					// existing object and reconciles labels onto it.
					"app.kubernetes.io/managed-by":   "catalyst-api",
					"catalyst.openova.io/org":         ns,
					"catalyst.openova.io/provisioned": "install",
				},
			},
		},
	}

	if _, err := client.Resource(namespacesGVR()).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race-loser: another caller (or the GitOps apply) created it
			// between our Get and Create. That's success.
			return nil
		}
		return fmt.Errorf("ensureOrgNamespace: create namespace %q: %w", ns, err)
	}
	return nil
}
