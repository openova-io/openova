// policy_mode_test.go — coverage for EPIC-1 #1096 slice X
// PUT /api/v1/sovereigns/{id}/environments/{env}/policy.
//
// Test strategy mirrors rbac_assign_test.go and user_access_test.go: a
// fake dynamic client seeded with the EnvironmentPolicy / Environment /
// ClusterPolicy GVRs' list-kinds, an installed Deployment with a
// temp-file kubeconfig path so sovereignDynamicClient resolves, and a
// per-test chi router that registers only the endpoint under test.
//
// Coverage matrix:
//
//   - 200 OK created       — fresh PUT (no CR exists) → CR created
//   - 200 OK updated       — existing CR → mode merged
//   - 200 OK no-op         — idempotent re-PUT same value → 2nd is no-op
//   - 400 unknown policy   — request mode for an unregistered policy name
//   - 400 invalid mode     — value outside permissive | enforcing
//   - 400 empty modes      — body has empty modes map
//   - 403 forbidden        — caller without tier-admin/owner
//   - 200 admin allowed    — tier=admin claim authorizes the call
//   - 404 environment      — Environment CR missing
//   - 404 deployment       — sovereign id unknown
//   - 409 retry succeeds   — first Update returns Conflict, retry wins
//   - response shape       — every known policy appears in the response
//
// Plus pure-helper coverage of mergeEnvironmentPolicyModes and
// policyModeCallerAuthorized.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Fixture builders ─────────────────────────────────────────────────

func policyModeListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		EnvironmentPolicyGVR: "EnvironmentPolicyList",
		EnvironmentGVR():     "EnvironmentList",
		ClusterPolicyGVR():   "ClusterPolicyList",
	}
}

func fakePolicyModeDynamicFactory(seed ...runtime.Object) (func(string) (dynamic.Interface, error), *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, policyModeListKinds(), seed...)
	return func(_ string) (dynamic.Interface, error) {
		return client, nil
	}, client
}

// newFakeEnvironment returns a minimal Environment CR matching the CRD
// shape — enough to satisfy the handler's environmentExists check.
func newFakeEnvironment(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Environment")
	u.SetName(name)
	return u
}

// newFakeEnvironmentPolicy returns an EnvironmentPolicy CR with the
// supplied modes seeded under spec.compliance.modes.
func newFakeEnvironmentPolicy(name string, modes map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(EnvironmentPolicyGVR.Group + "/" + EnvironmentPolicyGVR.Version)
	u.SetKind("EnvironmentPolicy")
	u.SetName(name)
	u.SetResourceVersion("1")
	modesIface := map[string]any{}
	for k, v := range modes {
		modesIface[k] = v
	}
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"compliance": map[string]any{
			"modes": modesIface,
		},
	}, "spec")
	return u
}

// newFakeClusterPolicy returns a Kyverno ClusterPolicy with the
// compliance-tier label so the live-list filter picks it up.
func newFakeClusterPolicy(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("kyverno.io/v1")
	u.SetKind("ClusterPolicy")
	u.SetName(name)
	u.SetLabels(map[string]string{
		policyTierLabel: policyTierCompliance,
	})
	return u
}

func registerPolicyModeRoute(r chi.Router, h *Handler) {
	r.Put("/api/v1/sovereigns/{id}/environments/{env}/policy", h.HandleEnvironmentPolicyMode)
}

// stdPolicyModeSeed returns the canonical seed: the Environment CR
// `prod` plus 3 ClusterPolicies the K-slice ships
// (multi-replica-drainability, networkpolicy-present, runasnonroot-readonlyrootfs).
// Tests can append an EnvironmentPolicy CR for the "existing CR" path.
func stdPolicyModeSeed(envName string, extra ...runtime.Object) []runtime.Object {
	out := []runtime.Object{
		newFakeEnvironment(envName),
		newFakeClusterPolicy("multi-replica-drainability"),
		newFakeClusterPolicy("networkpolicy-present"),
		newFakeClusterPolicy("runasnonroot-readonlyrootfs"),
	}
	out = append(out, extra...)
	return out
}

// callPolicyMode builds a PUT request, optionally injects claims into
// the context, and runs it through a fresh router.
func callPolicyMode(
	t *testing.T,
	h *Handler,
	path string,
	body any,
	claims *auth.Claims,
) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	registerPolicyModeRoute(r, h)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")
	if claims != nil {
		ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// ── 200 OK created — no EnvironmentPolicy CR yet ─────────────────────

func TestHandleEnvironmentPolicyMode_CreatesNewWhenAbsent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod")...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-create")

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": policyModeEnforcing,
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp policyModeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied != "created" {
		t.Fatalf("applied: got %q want created", resp.Applied)
	}
	if resp.Environment != "prod" {
		t.Fatalf("environment: got %q want prod", resp.Environment)
	}
	if resp.Modes["multi-replica-drainability"] != policyModeEnforcing {
		t.Fatalf("multi-replica mode: got %q want %q",
			resp.Modes["multi-replica-drainability"], policyModeEnforcing)
	}
	// Every known policy must appear in the response (defaulting to
	// permissive when not in the CR).
	if resp.Modes["networkpolicy-present"] != policyModePermissive {
		t.Fatalf("networkpolicy mode: got %q want %q (default)",
			resp.Modes["networkpolicy-present"], policyModePermissive)
	}
	if resp.Modes["runasnonroot-readonlyrootfs"] != policyModePermissive {
		t.Fatalf("runasnonroot mode: got %q want %q (default)",
			resp.Modes["runasnonroot-readonlyrootfs"], policyModePermissive)
	}
	// Verify the CR was actually created with the right shape.
	got, err := client.Resource(EnvironmentPolicyGVR).Get(
		context.Background(), "prod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after create: %v", err)
	}
	mode, _, _ := unstructured.NestedString(got.Object, "spec", "compliance", "modes", "multi-replica-drainability")
	if mode != policyModeEnforcing {
		t.Fatalf("stored mode: got %q want %q", mode, policyModeEnforcing)
	}
}

// ── 200 OK updated — existing CR, mode merged ────────────────────────

func TestHandleEnvironmentPolicyMode_MergesIntoExistingCR(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	existing := newFakeEnvironmentPolicy("prod", map[string]string{
		"multi-replica-drainability": policyModePermissive,
		"networkpolicy-present":      policyModeEnforcing, // pre-existing other policy
	})
	factory, client := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod", existing)...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-update")

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": policyModeEnforcing, // flip
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp policyModeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied != "updated" {
		t.Fatalf("applied: got %q want updated", resp.Applied)
	}
	if resp.Modes["multi-replica-drainability"] != policyModeEnforcing {
		t.Fatalf("flipped mode: got %q", resp.Modes["multi-replica-drainability"])
	}
	// Pre-existing other-policy mode must be preserved.
	if resp.Modes["networkpolicy-present"] != policyModeEnforcing {
		t.Fatalf("preserved mode: got %q", resp.Modes["networkpolicy-present"])
	}

	// Verify the CR reflects the merge.
	got, err := client.Resource(EnvironmentPolicyGVR).Get(
		context.Background(), "prod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	stored, _, _ := unstructured.NestedStringMap(got.Object, "spec", "compliance", "modes")
	if stored["multi-replica-drainability"] != policyModeEnforcing {
		t.Fatalf("stored multi-replica: got %q", stored["multi-replica-drainability"])
	}
	if stored["networkpolicy-present"] != policyModeEnforcing {
		t.Fatalf("stored networkpolicy preserved: got %q", stored["networkpolicy-present"])
	}
}

// ── 200 OK no-op — idempotent re-PUT ─────────────────────────────────

func TestHandleEnvironmentPolicyMode_IdempotentSecondCallNoOp(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	existing := newFakeEnvironmentPolicy("prod", map[string]string{
		"multi-replica-drainability": policyModeEnforcing,
	})
	factory, _ := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod", existing)...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-noop")

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": policyModeEnforcing, // same value
		},
	}
	// First call.
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("first: status %d; body=%s", rec.Code, rec.Body.String())
	}
	var first policyModeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("first decode: %v", err)
	}
	if first.Applied != "no-op" {
		t.Fatalf("first applied: got %q want no-op (CR already had this value)", first.Applied)
	}

	// Second call — must also be no-op.
	rec2 := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second: status %d; body=%s", rec2.Code, rec2.Body.String())
	}
	var second policyModeResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &second); err != nil {
		t.Fatalf("second decode: %v", err)
	}
	if second.Applied != "no-op" {
		t.Fatalf("second applied: got %q want no-op", second.Applied)
	}
}

// ── 400 unknown policy ───────────────────────────────────────────────

func TestHandleEnvironmentPolicyMode_RejectsUnknownPolicy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod")...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-unknown")

	body := policyModeRequest{
		Modes: map[string]string{
			"made-up-policy-name": policyModeEnforcing,
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown-policy") {
		t.Fatalf("expected unknown-policy error code; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "made-up-policy-name") {
		t.Fatalf("expected unknown name in detail; got %s", rec.Body.String())
	}
}

// ── 400 invalid mode ─────────────────────────────────────────────────

func TestHandleEnvironmentPolicyMode_RejectsInvalidMode(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod")...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-bad-mode")

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": "blocking", // not permissive | enforcing
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid-mode") {
		t.Fatalf("expected invalid-mode error code; got %s", rec.Body.String())
	}
}

// ── 400 empty modes map ──────────────────────────────────────────────

func TestHandleEnvironmentPolicyMode_RejectsEmptyModes(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod")...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-empty")

	body := policyModeRequest{Modes: map[string]string{}}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "empty-modes") {
		t.Fatalf("expected empty-modes error code; got %s", rec.Body.String())
	}
}

// ── 403 caller without tier-admin/owner ──────────────────────────────

func TestHandleEnvironmentPolicyMode_ForbidsLowTier(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod")...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-403")

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": policyModeEnforcing,
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, &auth.Claims{Tier: "developer"}) // below admin/owner
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "forbidden") {
		t.Fatalf("expected forbidden error; got %s", rec.Body.String())
	}
}

// ── 200 OK — admin claim authorizes the call ─────────────────────────

func TestHandleEnvironmentPolicyMode_AdminClaimAllowed(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod")...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-admin")

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": policyModeEnforcing,
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, &auth.Claims{Tier: "admin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (admin tier authorized); body=%s",
			rec.Code, rec.Body.String())
	}
}

// ── Create-on-write when Environment CR is absent ───────────────────

// TestHandleEnvironmentPolicyMode_CreatesWhenEnvironmentMissing reflects
// the post-iter-3 contract change: a missing Environment CR is no
// longer surfaced as a 404 on the policy-mode toggle. The handler
// now treats EnvironmentPolicy as the source of truth and lets the
// merge step create the EnvironmentPolicy CR even when no
// matching Environment CR exists yet. Operators frequently put policy
// modes in place before the Environment CR materialises (or, in
// chroot-mode, the Environment CRD is absent entirely).
func TestHandleEnvironmentPolicyMode_CreatesWhenEnvironmentMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// Seed only the ClusterPolicies + a different environment, NOT
	// the one we PUT to.
	factory, _ := fakePolicyModeDynamicFactory(
		newFakeEnvironment("dev"),
		newFakeClusterPolicy("multi-replica-drainability"),
	)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-create-onwrite")

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": policyModeEnforcing,
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (create-on-write); body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"applied":"created"`) {
		t.Fatalf("expected applied=created; got %s", rec.Body.String())
	}
}

// ── 404 unknown deployment ───────────────────────────────────────────

func TestHandleEnvironmentPolicyMode_404OnUnknownDeployment(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod")...)
	h.dynamicFactory = factory

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": policyModeEnforcing,
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/ghost/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// ── 409 retry — first Update is conflict, retry wins ─────────────────

func TestHandleEnvironmentPolicyMode_RetriesOn409(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	existing := newFakeEnvironmentPolicy("prod", map[string]string{
		"multi-replica-drainability": policyModePermissive,
	})
	factory, client := fakePolicyModeDynamicFactory(stdPolicyModeSeed("prod", existing)...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-409")

	var updateCount atomic.Int32
	client.PrependReactor("update", "environmentpolicies", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if updateCount.Add(1) == 1 {
			// First Update: simulate concurrent edit conflict.
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{
					Group:    EnvironmentPolicyGVR.Group,
					Resource: EnvironmentPolicyGVR.Resource,
				},
				"prod",
				nil,
			)
		}
		return false, nil, nil // fall through to default reactor
	})

	body := policyModeRequest{
		Modes: map[string]string{
			"multi-replica-drainability": policyModeEnforcing,
		},
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/prod/policy",
		body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (retry); body=%s", rec.Code, rec.Body.String())
	}
	var resp policyModeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied != "updated" {
		t.Fatalf("applied: got %q want updated (after retry)", resp.Applied)
	}
	if updateCount.Load() < 2 {
		t.Fatalf("expected at least 2 Update calls (initial + retry); got %d", updateCount.Load())
	}
}

// ── Pure helper: mergeEnvironmentPolicyModes ─────────────────────────

func TestMergeEnvironmentPolicyModes_DetectsNoOpAndChange(t *testing.T) {
	cases := []struct {
		name       string
		existing   map[string]string
		requested  map[string]string
		wantChange bool
		wantMerged map[string]string
	}{
		{
			name:       "empty existing + new key gives change",
			existing:   map[string]string{},
			requested:  map[string]string{"foo": "enforcing"},
			wantChange: true,
			wantMerged: map[string]string{"foo": "enforcing"},
		},
		{
			name:       "same key same value gives no change",
			existing:   map[string]string{"foo": "enforcing"},
			requested:  map[string]string{"foo": "enforcing"},
			wantChange: false,
			wantMerged: map[string]string{"foo": "enforcing"},
		},
		{
			name:       "same key diff value gives change",
			existing:   map[string]string{"foo": "permissive"},
			requested:  map[string]string{"foo": "enforcing"},
			wantChange: true,
			wantMerged: map[string]string{"foo": "enforcing"},
		},
		{
			name:       "preserves unrelated key",
			existing:   map[string]string{"foo": "permissive", "bar": "enforcing"},
			requested:  map[string]string{"foo": "enforcing"},
			wantChange: true,
			wantMerged: map[string]string{"foo": "enforcing", "bar": "enforcing"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cur := newFakeEnvironmentPolicy("prod", c.existing)
			merged, changed := mergeEnvironmentPolicyModes(cur, c.requested)
			if changed != c.wantChange {
				t.Fatalf("changed: got %v want %v", changed, c.wantChange)
			}
			if changed {
				got, _, _ := unstructured.NestedStringMap(merged.Object, "spec", "compliance", "modes")
				if len(got) != len(c.wantMerged) {
					t.Fatalf("merged len: got %d want %d (%+v)", len(got), len(c.wantMerged), got)
				}
				for k, v := range c.wantMerged {
					if got[k] != v {
						t.Fatalf("merged[%q]: got %q want %q", k, got[k], v)
					}
				}
			}
		})
	}
}

// ── Pure helper: policyModeCallerAuthorized ──────────────────────────

func TestPolicyModeCallerAuthorized_Cases(t *testing.T) {
	cases := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{"nil claims", nil, false},
		{"developer tier", &auth.Claims{Tier: "developer"}, false},
		{"viewer tier", &auth.Claims{Tier: "viewer"}, false},
		{"operator tier", &auth.Claims{Tier: "operator"}, false},
		{"admin tier", &auth.Claims{Tier: "admin"}, true},
		{"owner tier", &auth.Claims{Tier: "owner"}, true},
		{"catalyst-admin realm role", &auth.Claims{
			RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-admin"}},
		}, true},
		{"catalyst-owner realm role", &auth.Claims{
			RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}},
		}, true},
		{"application-admin legacy role", &auth.Claims{
			RealmAccess: auth.RealmAccess{Roles: []string{"application-admin"}},
		}, true},
		{"unrelated role only", &auth.Claims{
			RealmAccess: auth.RealmAccess{Roles: []string{"some-other-role"}},
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := policyModeCallerAuthorized(c.claims)
			if got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

// ── Regression: tolerant body shape (TC-101) ─────────────────────────

// TestHandleEnvironmentPolicyMode_AcceptsRoundTripBodyShape is the
// regression test for the iter-3 incident where PUT
// /environments/{env}/policy returned HTTP 400
// `json: unknown field "environment"` because the canonical UAT
// matrix sends a body that includes `environment` and `applied`
// alongside `modes`. The handler now accepts (but ignores) those
// optional fields so callers can round-trip the response shape
// without re-shaping the body.
//
// Also exercises the Kyverno-vocabulary normalisation: the matrix
// sends `"audit"` as the mode value (real Kyverno terminology). The
// handler maps `audit` → `permissive` so the canonical contract
// holds for downstream consumers.
func TestHandleEnvironmentPolicyMode_AcceptsRoundTripBodyShape(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakePolicyModeDynamicFactory(stdPolicyModeSeed("default")...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-pol-roundtrip")

	// Use a raw map so the test exercises the JSON-decode path with
	// extra fields the strict decoder would otherwise reject.
	body := map[string]any{
		"environment": "default",
		"modes": map[string]string{
			// Use a known policy name from stdPolicyModeSeed so the
			// known-policy validation passes (the matrix uses
			// validationFailureAction; the test substitutes the real
			// fixture name to keep the assertion deterministic).
			"multi-replica-drainability": "audit",
		},
		"applied": true,
	}
	rec := callPolicyMode(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/environments/default/policy",
		body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"applied"`) {
		t.Fatalf("expected 'applied' in response (TC-101 must_contain); got %s", rec.Body.String())
	}
	// The mode should be normalised to "permissive" in the response.
	if !strings.Contains(rec.Body.String(), `"multi-replica-drainability":"permissive"`) {
		t.Fatalf("expected mode normalised audit→permissive; got %s", rec.Body.String())
	}
}

// TestNormalizePolicyMode_AcceptsBothVocabularies is the unit
// regression for the Kyverno-synonym mapping.
func TestNormalizePolicyMode_AcceptsBothVocabularies(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		wantOK   bool
		wantNote string
	}{
		{"permissive", "permissive", true, "openova canonical"},
		{"enforcing", "enforcing", true, "openova canonical"},
		{"audit", "permissive", true, "kyverno synonym"},
		{"enforce", "enforcing", true, "kyverno synonym"},
		{"AUDIT", "permissive", true, "case-insensitive"},
		{"  Enforce  ", "enforcing", true, "trimmed"},
		{"warn", "", false, "unknown vocabulary"},
		{"", "", false, "empty rejected"},
		{"strict", "", false, "unknown vocabulary"},
	}
	for _, c := range cases {
		t.Run(c.in+"_"+c.wantNote, func(t *testing.T) {
			got, ok := normalizePolicyMode(c.in)
			if ok != c.wantOK {
				t.Fatalf("ok: got %v want %v", ok, c.wantOK)
			}
			if got != c.want {
				t.Fatalf("normalised: got %q want %q", got, c.want)
			}
		})
	}
}
