// placement_test.go — #3969 admission gate + validating webhook coverage.
//
// Two layers:
//
//  1. EvaluatePlacement (the pure gate) — the multi-primary capability gate,
//     the role/standbyType invariants, the empty-targets no-op.
//  2. PlacementWebhook.Review (the AdmissionReview decode→evaluate→respond) —
//     proves an invalid placement is REJECTED at admission (allowed=false)
//     with the actionable message, a valid one is allowed, and the legacy
//     posture-string placement is never gated.
package admission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

func TestEvaluatePlacement(t *testing.T) {
	cases := []struct {
		name        string
		placement   bpv1alpha1.Placement
		capability  bpv1alpha1.PlacementCapability
		wantAllowed bool
		wantReason  string // substring expected in Message on deny
	}{
		{
			name:        "empty targets — legacy posture, un-gated",
			placement:   bpv1alpha1.Placement{},
			capability:  bpv1alpha1.CapabilityPrimaryStandby,
			wantAllowed: true,
		},
		{
			name: "single Primary — allowed under primary+standby",
			placement: bpv1alpha1.Placement{Targets: []bpv1alpha1.PlacementTarget{
				{Region: "fsn", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
			}},
			capability:  bpv1alpha1.CapabilityPrimaryStandby,
			wantAllowed: true,
		},
		{
			name: "Primary + Hot Standby — allowed under primary+standby",
			placement: bpv1alpha1.Placement{Targets: []bpv1alpha1.PlacementTarget{
				{Region: "fsn", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
				{Region: "nbg", Cluster: "mgmt-B", Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot},
			}},
			capability:  bpv1alpha1.CapabilityPrimaryStandby,
			wantAllowed: true,
		},
		{
			name: "multi-primary REJECTED under primary+standby (the load-bearing gate)",
			placement: bpv1alpha1.Placement{Targets: []bpv1alpha1.PlacementTarget{
				{Region: "fsn", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
				{Region: "nbg", Cluster: "mgmt-B", Role: bpv1alpha1.DataRolePrimary},
			}},
			capability:  bpv1alpha1.CapabilityPrimaryStandby,
			wantAllowed: false,
			wantReason:  bpv1alpha1.MultiPrimaryNotSupportedReason,
		},
		{
			name: "multi-primary ALLOWED under multi-primary capability",
			placement: bpv1alpha1.Placement{Targets: []bpv1alpha1.PlacementTarget{
				{Region: "fsn", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
				{Region: "nbg", Cluster: "mgmt-B", Role: bpv1alpha1.DataRolePrimary},
			}},
			capability:  bpv1alpha1.CapabilityMultiPrimary,
			wantAllowed: true,
		},
		{
			name: "Standby with no type REJECTED",
			placement: bpv1alpha1.Placement{Targets: []bpv1alpha1.PlacementTarget{
				{Region: "fsn", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
				{Region: "nbg", Cluster: "mgmt-B", Role: bpv1alpha1.DataRoleStandby},
			}},
			capability:  bpv1alpha1.CapabilityPrimaryStandby,
			wantAllowed: false,
			wantReason:  "StandbyMissingType",
		},
		{
			name: "no Primary REJECTED",
			placement: bpv1alpha1.Placement{Targets: []bpv1alpha1.PlacementTarget{
				{Region: "nbg", Cluster: "mgmt-B", Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot},
			}},
			capability:  bpv1alpha1.CapabilityPrimaryStandby,
			wantAllowed: false,
			wantReason:  "NoPrimary",
		},
		{
			name: "empty capability folds to primary+standby — multi-primary still rejected",
			placement: bpv1alpha1.Placement{Targets: []bpv1alpha1.PlacementTarget{
				{Region: "fsn", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
				{Region: "nbg", Cluster: "mgmt-B", Role: bpv1alpha1.DataRolePrimary},
			}},
			capability:  "",
			wantAllowed: false,
			wantReason:  bpv1alpha1.MultiPrimaryNotSupportedReason,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := EvaluatePlacement(PlacementRequest{Placement: tc.placement, Capability: tc.capability})
			if d.Allowed != tc.wantAllowed {
				t.Fatalf("allowed=%v, want %v (decision=%s)", d.Allowed, tc.wantAllowed, d)
			}
			if !tc.wantAllowed {
				if d.Code != CodePlacementInvalid {
					t.Errorf("deny code=%q, want %q", d.Code, CodePlacementInvalid)
				}
				if tc.wantReason != "" && !strings.Contains(d.Message, tc.wantReason) {
					t.Errorf("deny message %q must contain %q", d.Message, tc.wantReason)
				}
			}
		})
	}
}

// fixedCapability is a CapabilityResolver that always returns the same value.
type fixedCapability bpv1alpha1.PlacementCapability

func (f fixedCapability) CapabilityFor(_ context.Context, _ string) (bpv1alpha1.PlacementCapability, error) {
	return bpv1alpha1.PlacementCapability(f), nil
}

// buildReview wraps an Application object body in an AdmissionReview CREATE
// request (the shape the apiserver POSTs to the webhook).
func buildReview(t *testing.T, appObj map[string]interface{}) *admissionv1.AdmissionReview {
	t.Helper()
	raw, err := json.Marshal(appObj)
	if err != nil {
		t.Fatalf("marshal app object: %v", err)
	}
	return &admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:       "test-uid-1",
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

// TestPlacementWebhook_RejectsMultiPrimary — the headline acceptance: an
// Application that declares a multi-primary placement against a
// primary+standby Blueprint is REJECTED at admission (allowed=false), with
// the MultiPrimaryNotSupported reason in the status message.
func TestPlacementWebhook_RejectsMultiPrimary(t *testing.T) {
	wh := &PlacementWebhook{Resolver: fixedCapability(bpv1alpha1.CapabilityPrimaryStandby)}
	review := buildReview(t, map[string]interface{}{
		"apiVersion": "apps.openova.io/v1",
		"kind":       "Application",
		"spec": map[string]interface{}{
			"blueprintRef": map[string]interface{}{"name": "bp-grafana"},
			"placement": map[string]interface{}{
				"targets": []interface{}{
					map[string]interface{}{"region": "fsn", "cluster": "mgmt-A", "role": "Primary"},
					map[string]interface{}{"region": "nbg", "cluster": "mgmt-B", "role": "Primary"},
				},
			},
		},
	})
	resp := wh.Review(context.Background(), review)
	if resp.Allowed {
		t.Fatalf("multi-primary on primary+standby Blueprint must be REJECTED at admission")
	}
	if resp.UID != "test-uid-1" {
		t.Errorf("response UID=%q must echo request UID", resp.UID)
	}
	if resp.Result == nil || !strings.Contains(resp.Result.Message, bpv1alpha1.MultiPrimaryNotSupportedReason) {
		t.Errorf("deny message must carry %q, got %+v", bpv1alpha1.MultiPrimaryNotSupportedReason, resp.Result)
	}
}

// TestPlacementWebhook_AllowsValidPlacement — a valid Primary+Standby
// placement is admitted.
func TestPlacementWebhook_AllowsValidPlacement(t *testing.T) {
	wh := &PlacementWebhook{Resolver: fixedCapability(bpv1alpha1.CapabilityPrimaryStandby)}
	review := buildReview(t, map[string]interface{}{
		"spec": map[string]interface{}{
			"blueprintRef": map[string]interface{}{"name": "bp-grafana"},
			"placement": map[string]interface{}{
				"targets": []interface{}{
					map[string]interface{}{"region": "fsn", "cluster": "mgmt-A", "role": "Primary"},
					map[string]interface{}{"region": "nbg", "cluster": "mgmt-B", "role": "Standby", "standbyType": "Hot"},
				},
			},
		},
	})
	resp := wh.Review(context.Background(), review)
	if !resp.Allowed {
		t.Fatalf("valid Primary+Hot-Standby placement must be admitted, got deny: %+v", resp.Result)
	}
}

// TestPlacementWebhook_AllowsMultiPrimaryUnderCapability — multi-primary is
// admitted when the Blueprint declares the multi-primary capability.
func TestPlacementWebhook_AllowsMultiPrimaryUnderCapability(t *testing.T) {
	wh := &PlacementWebhook{Resolver: fixedCapability(bpv1alpha1.CapabilityMultiPrimary)}
	review := buildReview(t, map[string]interface{}{
		"spec": map[string]interface{}{
			"blueprintRef": map[string]interface{}{"name": "bp-clickhouse"},
			"placement": map[string]interface{}{
				"targets": []interface{}{
					map[string]interface{}{"region": "fsn", "cluster": "mgmt-A", "role": "Primary"},
					map[string]interface{}{"region": "nbg", "cluster": "mgmt-B", "role": "Primary"},
				},
			},
		},
	})
	if resp := wh.Review(context.Background(), review); !resp.Allowed {
		t.Fatalf("multi-primary under multi-primary capability must be admitted, got: %+v", resp.Result)
	}
}

// TestPlacementWebhook_LegacyStringPlacement_Allowed — a bare legacy posture
// string in spec.placement carries no targets[] and is never gated.
func TestPlacementWebhook_LegacyStringPlacement_Allowed(t *testing.T) {
	wh := &PlacementWebhook{Resolver: fixedCapability(bpv1alpha1.CapabilityPrimaryStandby)}
	review := buildReview(t, map[string]interface{}{
		"spec": map[string]interface{}{
			"blueprintRef": map[string]interface{}{"name": "bp-grafana"},
			"placement":    "active-hotstandby",
		},
	})
	if resp := wh.Review(context.Background(), review); !resp.Allowed {
		t.Fatalf("legacy string placement must be admitted (no targets ⇒ un-gated), got: %+v", resp.Result)
	}
}

// TestPlacementWebhook_NoResolver_FoldsToPrimaryStandby — with no resolver
// wired, the gate folds to the safe primary+standby default, so multi-primary
// is rejected (fail-safe).
func TestPlacementWebhook_NoResolver_FoldsToPrimaryStandby(t *testing.T) {
	wh := &PlacementWebhook{} // nil resolver
	review := buildReview(t, map[string]interface{}{
		"spec": map[string]interface{}{
			"blueprintRef": map[string]interface{}{"name": "bp-grafana"},
			"placement": map[string]interface{}{
				"targets": []interface{}{
					map[string]interface{}{"region": "fsn", "cluster": "mgmt-A", "role": "Primary"},
					map[string]interface{}{"region": "nbg", "cluster": "mgmt-B", "role": "Primary"},
				},
			},
		},
	})
	if resp := wh.Review(context.Background(), review); resp.Allowed {
		t.Fatalf("nil resolver must fold to primary+standby and reject multi-primary")
	}
}

// TestPlacementWebhook_DeleteAlwaysAllowed — DELETE carries no object.
func TestPlacementWebhook_DeleteAlwaysAllowed(t *testing.T) {
	wh := &PlacementWebhook{Resolver: fixedCapability(bpv1alpha1.CapabilityPrimaryStandby)}
	review := &admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:       "del-1",
			Operation: admissionv1.Delete,
		},
	}
	if resp := wh.Review(context.Background(), review); !resp.Allowed {
		t.Fatalf("DELETE must always be admitted")
	}
}

// ensure metav1 import is used (Status shape referenced indirectly).
var _ = metav1.Status{}
