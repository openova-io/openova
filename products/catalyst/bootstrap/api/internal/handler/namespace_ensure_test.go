// namespace_ensure_test.go — #3598 (EPIC #3597) contract coverage for
// the create-into-Org-without-namespace path. The create-from-catalog
// handlers must ENSURE the Org/Environment namespace before creating the
// Application CR so an install never fails with `namespaces "<org>" not
// found`.
package handler

import (
	"context"
	"regexp"
	"strings"
	"testing"

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


// ── #3922 — environmentRef must satisfy the Application CRD pattern ─────

// applicationEnvironmentRefPattern is the EXACT pattern the Application CRD
// (products/catalyst/chart/crds/application.yaml) applies to
// spec.environmentRef. A dotted value (e.g. a Sovereign FQDN "<fqdn>-prod")
// fails this server-side, surfacing as the HTTP 500 that blocked ALL instance
// creation on hw171. We pin the literal here so the sanitizer's output is
// asserted against the SAME rule the apiserver enforces.
var applicationEnvironmentRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)


// TestSanitizeEnvironmentRef_AlwaysMatchesCRDPattern asserts every output
// satisfies the Application CRD's spec.environmentRef pattern — so the create
// can never again 500 with "should match '^[a-z][a-z0-9-]{2,63}$'". Inputs are
// deliberately abusive (the dotted FQDN that triggered #3922, uppercase, an
// over-length FQDN, and an already-valid label that must round-trip).
func TestSanitizeEnvironmentRef_AlwaysMatchesCRDPattern(t *testing.T) {
	inputs := []string{
		"hw171.omantel.biz-prod", // the exact value from the 500 on hw171
		"hw165.omani.works-prod",
		"acme-prod",                                 // already valid → round-trips
		"HW171.Omantel.BIZ-prod",                    // uppercase folded
		strings.Repeat("a", 70) + ".omani.works-prod", // > 63 chars pre-slug
		"...___...-prod",                            // pathological
	}
	for _, in := range inputs {
		got := sanitizeEnvironmentRef(in)
		if !applicationEnvironmentRefPattern.MatchString(got) {
			t.Fatalf("sanitizeEnvironmentRef(%q) = %q does NOT match the CRD pattern %s",
				in, got, applicationEnvironmentRefPattern.String())
		}
	}
	// An already-valid label round-trips byte-identically.
	if got := sanitizeEnvironmentRef("acme-prod"); got != "acme-prod" {
		t.Fatalf("sanitizeEnvironmentRef(\"acme-prod\") = %q, want unchanged acme-prod", got)
	}
}
