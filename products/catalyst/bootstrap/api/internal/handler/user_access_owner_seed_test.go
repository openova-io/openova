// user_access_owner_seed_test.go — coverage for EnsureOwnerUserAccess
// (D21). Mirrors user_access_test.go's fake-dynamic-client pattern:
// register the UserAccess GVR's list-kind on a scheme, seed (or don't)
// an existing CR, call the helper, assert the CR's shape via the same
// dynamic client.
package handler

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func newOwnerSeedFakeClient(seed ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		UserAccessGVR(): "UserAccessList",
	}, seed...)
}

// TestEnsureOwnerUserAccess_CreatesCanonicalCR proves a fresh seed
// produces a CR with the canonical shape (correct name, email
// annotation, keycloakSubject, sovereignRef, wildcard app entry,
// admin role).
func TestEnsureOwnerUserAccess_CreatesCanonicalCR(t *testing.T) {
	client := newOwnerSeedFakeClient()

	const email = "emrah.baysal@openova.io"
	const sov = "omantel.omani.works"
	if err := EnsureOwnerUserAccess(context.Background(), client, email, sov); err != nil {
		t.Fatalf("EnsureOwnerUserAccess: unexpected err %v", err)
	}

	// The owner CR is created in userAccessOwnerNamespace (catalyst-system)
	// per the D21 fix on t134 2026-05-17 — useraccesses.access.openova.io
	// is NAMESPACED (Claim semantics from the XRD claimNames block).
	got, err := client.Resource(UserAccessGVR()).Namespace(userAccessOwnerNamespace).
		Get(context.Background(), ownerUserAccessName(email), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after seed: %v", err)
	}

	// Name.
	wantName := "useraccess-owner-emrah-baysal-at-openova-io"
	if got.GetName() != wantName {
		t.Errorf("name: got %q want %q", got.GetName(), wantName)
	}

	// Annotation surfaces the email for /users page rendering.
	if a := got.GetAnnotations()[userAccessOwnerEmailAnnotation]; a != email {
		t.Errorf("annotation %q: got %q want %q",
			userAccessOwnerEmailAnnotation, a, email)
	}

	// spec.user.keycloakSubject = email (realm uses email-as-username).
	gotSub, _, _ := unstructured.NestedString(got.Object, "spec", "user", "keycloakSubject")
	if gotSub != email {
		t.Errorf("spec.user.keycloakSubject: got %q want %q", gotSub, email)
	}

	// spec.sovereignRef = first DNS label (rbacAssignSlug).
	gotRef, _, _ := unstructured.NestedString(got.Object, "spec", "sovereignRef")
	if gotRef != "omantel" {
		t.Errorf("spec.sovereignRef: got %q want %q", gotRef, "omantel")
	}

	// D21 fix on t135 2026-05-17: the owner CR uses spec.tierRoleRef
	// (not per-application entries). The XRD's CRD rejects `app: "*"`
	// (pattern `^[a-z0-9][a-z0-9-]{0,62}$`), so the owner-tier semantic
	// is conveyed via the canonical `openova:tier-owner` ClusterRole
	// reference per platform/crossplane-claims/.../xrds/useraccess.yaml.
	gotTier, _, _ := unstructured.NestedString(got.Object, "spec", "tierRoleRef")
	if gotTier != "openova:tier-owner" {
		t.Errorf("spec.tierRoleRef: got %q want %q", gotTier, "openova:tier-owner")
	}
	if _, present, _ := unstructured.NestedSlice(got.Object, "spec", "applications"); present {
		t.Errorf("spec.applications: must be absent (XRD pattern rejects `app: \"*\"`); use tierRoleRef instead")
	}

	// apiVersion + kind.
	if got.GetAPIVersion() != "access.openova.io/v1alpha1" {
		t.Errorf("apiVersion: got %q want access.openova.io/v1alpha1", got.GetAPIVersion())
	}
	if got.GetKind() != "UserAccess" {
		t.Errorf("kind: got %q want UserAccess", got.GetKind())
	}
}

// TestEnsureOwnerUserAccess_IdempotentOnAlreadyExists proves a second
// call (after the CR already exists) returns nil and does NOT mutate.
// Mirrors the production case of a re-handover for the same operator.
func TestEnsureOwnerUserAccess_IdempotentOnAlreadyExists(t *testing.T) {
	const email = "emrah.baysal@openova.io"
	const sov = "omantel.omani.works"

	client := newOwnerSeedFakeClient()

	// Seed once.
	if err := EnsureOwnerUserAccess(context.Background(), client, email, sov); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// Second call MUST return nil even though the CR already exists.
	if err := EnsureOwnerUserAccess(context.Background(), client, email, sov); err != nil {
		t.Fatalf("second seed (idempotent): got err %v want nil", err)
	}
	// Confirm only one CR exists.
	list, err := client.Resource(UserAccessGVR()).Namespace("").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 CR after idempotent re-seed; got %d", len(list.Items))
	}
}

// TestEnsureOwnerUserAccess_WrapsNonAlreadyExistsError proves any error
// other than AlreadyExists is wrapped and returned (so the caller can
// log a structured Warn).
func TestEnsureOwnerUserAccess_WrapsNonAlreadyExistsError(t *testing.T) {
	client := newOwnerSeedFakeClient()
	wantErr := errors.New("synthetic apiserver outage")
	client.PrependReactor("create", "useraccesses", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, wantErr
	})

	err := EnsureOwnerUserAccess(context.Background(), client,
		"emrah.baysal@openova.io", "omantel.omani.works")
	if err == nil {
		t.Fatalf("expected error from helper; got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped wantErr; got %v", err)
	}
	if !strings.Contains(err.Error(), "useraccess-owner-") {
		t.Errorf("wrap should include CR name; got %q", err.Error())
	}
}

// TestEnsureOwnerUserAccess_RejectsEmptyEmail proves an empty email
// returns an error rather than creating a malformed CR.
func TestEnsureOwnerUserAccess_RejectsEmptyEmail(t *testing.T) {
	client := newOwnerSeedFakeClient()
	if err := EnsureOwnerUserAccess(context.Background(), client, "  ", "x.example"); err == nil {
		t.Fatalf("expected error on empty email; got nil")
	}
}

// TestEnsureOwnerUserAccess_RejectsNilClient proves a nil dynamic
// client returns an error rather than panicking.
func TestEnsureOwnerUserAccess_RejectsNilClient(t *testing.T) {
	if err := EnsureOwnerUserAccess(context.Background(), nil,
		"emrah.baysal@openova.io", "x.example"); err == nil {
		t.Fatalf("expected error on nil client; got nil")
	}
}

// TestEnsureOwnerUserAccess_AlreadyExistsFoldedNoErrSentinel proves the
// helper folds k8s apierrors.IsAlreadyExists to nil specifically (and
// not via a generic "ignore all errors" path).
func TestEnsureOwnerUserAccess_AlreadyExistsFoldedNoErrSentinel(t *testing.T) {
	client := newOwnerSeedFakeClient()
	client.PrependReactor("create", "useraccesses", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(
			UserAccessGVR().GroupResource(),
			"useraccess-owner-emrah-baysal-at-openova-io",
		)
	})
	if err := EnsureOwnerUserAccess(context.Background(), client,
		"emrah.baysal@openova.io", "x.example"); err != nil {
		t.Fatalf("AlreadyExists should fold to nil; got %v", err)
	}
}

/* ── name-derivation unit tests ─────────────────────────────────── */

func TestOwnerUserAccessName_Sanitizes(t *testing.T) {
	cases := []struct {
		email string
		want  string
	}{
		{"emrah.baysal@openova.io", "useraccess-owner-emrah-baysal-at-openova-io"},
		{"  EMRAH.BAYSAL@openova.io  ", "useraccess-owner-emrah-baysal-at-openova-io"},
		{"a@b.c", "useraccess-owner-a-at-b-c"},
		// Pathological: punctuation-only sanitizes to empty → fallback.
		{"!!!", "useraccess-owner-operator"},
	}
	for _, c := range cases {
		t.Run(c.email, func(t *testing.T) {
			got := ownerUserAccessName(strings.TrimSpace(c.email))
			if got != c.want {
				t.Errorf("ownerUserAccessName(%q): got %q want %q", c.email, got, c.want)
			}
			if len(got) > 63 {
				t.Errorf("ownerUserAccessName(%q): exceeded RFC 1123 limit (got %d chars)", c.email, len(got))
			}
		})
	}
}

func TestOwnerUserAccessName_TruncatesLongEmail(t *testing.T) {
	long := strings.Repeat("a", 60) + "@openova.io"
	got := ownerUserAccessName(long)
	if len(got) > 63 {
		t.Errorf("name exceeded 63 chars: %q (%d)", got, len(got))
	}
	if !strings.HasPrefix(got, userAccessOwnerNamePrefix) {
		t.Errorf("name lost prefix after truncation: %q", got)
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("truncation left trailing hyphen: %q", got)
	}
}
