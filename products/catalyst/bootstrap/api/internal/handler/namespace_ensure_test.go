// namespace_ensure_test.go — #3598 (EPIC #3597) contract coverage for
// the create-into-Org-without-namespace path. The create-from-catalog
// handlers must ENSURE the Org/Environment namespace before creating the
// Application CR so an install never fails with `namespaces "<org>" not
// found`.
package handler

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
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

// ── #3830 — orgNamespace FQDN → RFC-1123 label slug ────────────────────

// TestOrgNamespace_SlugsDottedFQDN is the headline #3830 contract: a
// dotted Organization FQDN maps to a dash-joined RFC-1123 label.
func TestOrgNamespace_SlugsDottedFQDN(t *testing.T) {
	if got := orgNamespace("hw165.omani.works"); got != "hw165-omani-works" {
		t.Fatalf("orgNamespace(\"hw165.omani.works\") = %q, want %q", got, "hw165-omani-works")
	}
}

// TestOrgNamespace_OutputIsAlwaysValidLabel asserts every mapping output
// satisfies Kubernetes' own RFC-1123 label validator — so the namespace
// create can never fail with "must not contain dots" / "must be a valid
// label" again. Inputs are deliberately abusive: dots, uppercase,
// over-63-char, leading/trailing dot, underscores, unicode, all-illegal.
func TestOrgNamespace_OutputIsAlwaysValidLabel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // "" = don't assert exact, only validity
	}{
		{"plain-slug-untouched", "acme", "acme"},
		{"dotted-fqdn", "hw165.omani.works", "hw165-omani-works"},
		{"uppercase-folded", "HW165.Omani.WORKS", "hw165-omani-works"},
		{"leading-dot-trimmed", ".acme.omani.homes", "acme-omani-homes"},
		{"trailing-dot-trimmed", "acme.omani.homes.", "acme-omani-homes"},
		{"underscores-and-spaces", "my_org name.omani.rest", "my-org-name-omani-rest"},
		{"collapse-repeats", "a...b___c", "a-b-c"},
		{"unicode-stripped", "café.omani.works", "caf-omani-works"},
		// 70-char input → capped to ≤63 and still a valid label.
		{"over-63-capped", strings.Repeat("a", 70) + ".omani.works", ""},
		// All-illegal input must not yield an empty (invalid) namespace.
		{"all-illegal-fallback", "...___...", "org"},
		{"whitespace-only-fallback", "   ", "org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := orgNamespace(tc.in)
			if tc.want != "" && got != tc.want {
				t.Fatalf("orgNamespace(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got == "" {
				t.Fatalf("orgNamespace(%q) returned empty string — never a valid namespace", tc.in)
			}
			if len(got) > 63 {
				t.Fatalf("orgNamespace(%q) = %q is %d chars, exceeds the 63-char label cap", tc.in, got, len(got))
			}
			// The canonical assertion: the output passes k8s' own
			// RFC-1123 label validation (the exact rule the apiserver
			// applies to metadata.name on a Namespace).
			if errs := validation.IsDNS1123Label(got); len(errs) > 0 {
				t.Fatalf("orgNamespace(%q) = %q is NOT a valid RFC-1123 label: %v", tc.in, got, errs)
			}
		})
	}
}

// TestOrgNamespace_Idempotent — slugging an already-slugged value is a
// no-op, so a value that round-trips through the helper twice (e.g. a CR
// whose metadata.namespace is read back and re-passed) stays stable.
func TestOrgNamespace_Idempotent(t *testing.T) {
	for _, in := range []string{"hw165.omani.works", "acme", "HW165.Omani.WORKS"} {
		once := orgNamespace(in)
		twice := orgNamespace(once)
		if once != twice {
			t.Fatalf("orgNamespace not idempotent for %q: once=%q twice=%q", in, once, twice)
		}
	}
}

// TestEnsureOrgNamespace_SlugsDottedFQDN — the end-to-end #3830 contract:
// passing a dotted Org FQDN to ensureOrgNamespace creates the SLUGGED
// namespace (never the raw dotted name, which the apiserver rejects).
func TestEnsureOrgNamespace_SlugsDottedFQDN(t *testing.T) {
	client := fakeNamespaceClient()

	if err := ensureOrgNamespace(context.Background(), client, "hw165.omani.works"); err != nil {
		t.Fatalf("ensureOrgNamespace(dotted FQDN) returned error: %v", err)
	}

	// The slugged namespace exists…
	if _, err := getNamespace(t, client, "hw165-omani-works"); err != nil {
		t.Fatalf("slugged namespace hw165-omani-works not present after ensure: %v", err)
	}
	// …and the raw dotted name was NEVER created.
	if _, err := getNamespace(t, client, "hw165.omani.works"); !apierrors.IsNotFound(err) {
		t.Fatalf("raw dotted namespace must not exist, got err=%v", err)
	}
}
