// Package handler — sandbox_sessions_test.go covers the
// unstructured-to-wire projection that the Wave 14
// /api/v1/sandbox/sessions handlers depend on for status reflection.
//
// The handler itself is exercised end-to-end by the FE Playwright
// suite (products/catalyst/bootstrap/ui/e2e/sandbox.spec.ts); these
// tests pin the projection contract so the wire shape can't silently
// drift away from the controller's .status fields.
package handler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestSandboxBackendUnavailable pins the degrade classifier: transient
// backend-unreachable errors (401/403/503/timeout — e.g. the sandbox
// reflector hitting a clustermesh peer apiserver that rejects its token
// during a cutover/roll/region-kill recovery window, observed live on
// hw261) MUST degrade to the honest 503 "API pending", while a genuine
// object-not-found or already-exists MUST NOT (those keep each handler's
// own 200/404/409 semantics).
func TestSandboxBackendUnavailable(t *testing.T) {
	gr := schema.GroupResource{Group: "sandbox.openova.io", Resource: "sandboxes"}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unauthorized-401", apierrors.NewUnauthorized("token rejected by peer apiserver"), true},
		{"forbidden-403", apierrors.NewForbidden(gr, "s1", errors.New("no")), true},
		{"service-unavailable-503", apierrors.NewServiceUnavailable("apiserver draining"), true},
		{"server-timeout", apierrors.NewServerTimeout(gr, "list", 1), true},
		{"not-found-object", apierrors.NewNotFound(gr, "s1"), false},
		{"already-exists", apierrors.NewAlreadyExists(gr, "s1"), false},
		{"plain-error", errors.New("boom"), false},
		{"nil", nil, false},
		// #5140 — transient errors the OLD typed-only classifier missed and that
		// fell through to a hard 500 on hw261's region-kill recovery window.
		{"internal-error", apierrors.NewInternalError(errors.New("etcdserver: request timed out")), true},
		{"no-kind-match-discovery-miss", &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "sandbox.openova.io", Kind: "Sandbox"}}, true},
		{"deadline-exceeded-wrapped", fmt.Errorf("list sandboxes: %w", context.DeadlineExceeded), true},
		{"discovery-404-untyped", errors.New("the server could not find the requested resource"), true},
		{"connection-refused-untyped", errors.New("Get \"https://10.0.0.1:6443/apis\": dial tcp 10.0.0.1:6443: connect: connection refused"), true},
		{"unexpected-eof-untyped", errors.New("unexpected EOF"), true},
		{"genuine-parse-error", errors.New("json: cannot unmarshal number into Go value"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sandboxBackendUnavailable(tc.err); got != tc.want {
				t.Fatalf("sandboxBackendUnavailable(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestUnstructuredToSandboxItem_StatusReflection(t *testing.T) {
	creationTime := metav1.Now()
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "sandbox.openova.io/v1",
			"kind":       "Sandbox",
			"metadata": map[string]any{
				"name":              "sandbox-emrah",
				"namespace":         "acme",
				"creationTimestamp": creationTime.Format("2006-01-02T15:04:05Z"),
				"annotations": map[string]any{
					"catalyst.openova.io/sandbox-display-name": "Emrah's box",
				},
			},
			"spec": map[string]any{
				"agentCatalogue": []any{"claude-code"},
				"repos": []any{
					map[string]any{"giteaRepo": "acme/site"},
				},
			},
			"status": map[string]any{
				"phase":       "Ready",
				"sessions":    int64(2),
				"storageUsed": "12Gi",
				"spend30d":    "$4.21",
				"previews": []any{
					map[string]any{
						"prNumber": int64(42),
						"url":      "https://pr-42.preview.example",
						"sha":      "abc1234",
					},
					// missing-URL row → must be dropped
					map[string]any{
						"prNumber": int64(99),
					},
				},
				"conditions": []any{
					map[string]any{
						"type":               "Ready",
						"status":             "True",
						"reason":             "GitopsReconciled",
						"message":            "all good",
						"lastTransitionTime": "2026-05-18T11:00:00Z",
					},
					// missing-type row → must be dropped
					map[string]any{"status": "True"},
				},
			},
		},
	}

	item := unstructuredToSandboxItem(u)

	if item.ID != "sandbox-emrah" {
		t.Errorf("ID: want sandbox-emrah, got %q", item.ID)
	}
	if item.Name != "Emrah's box" {
		t.Errorf("Name: want display-name annotation, got %q", item.Name)
	}
	if item.Agent != "claude-code" {
		t.Errorf("Agent: want claude-code, got %q", item.Agent)
	}
	if item.Repo != "acme/site" {
		t.Errorf("Repo: want acme/site, got %q", item.Repo)
	}
	if item.Phase != "Ready" {
		t.Errorf("Phase: want Ready (raw), got %q", item.Phase)
	}
	if item.Status != "running" {
		t.Errorf("Status (FE projection): want running, got %q", item.Status)
	}
	if item.Sessions != 2 {
		t.Errorf("Sessions: want 2, got %d", item.Sessions)
	}
	if item.StorageUsed != "12Gi" {
		t.Errorf("StorageUsed: want 12Gi, got %q", item.StorageUsed)
	}
	if item.Spend30d != "$4.21" {
		t.Errorf("Spend30d: want $4.21, got %q", item.Spend30d)
	}
	if len(item.Previews) != 1 {
		t.Fatalf("Previews: want 1 valid row (URL-bearing), got %d", len(item.Previews))
	}
	if item.Previews[0].PRNumber != 42 || item.Previews[0].URL != "https://pr-42.preview.example" || item.Previews[0].SHA != "abc1234" {
		t.Errorf("Previews[0]: bad projection: %+v", item.Previews[0])
	}
	if len(item.Conditions) != 1 {
		t.Fatalf("Conditions: want 1 valid row (Type-bearing), got %d", len(item.Conditions))
	}
	c := item.Conditions[0]
	if c.Type != "Ready" || c.Status != "True" || c.Reason != "GitopsReconciled" || c.Message != "all good" || c.LastTransitionTime != "2026-05-18T11:00:00Z" {
		t.Errorf("Conditions[0]: bad projection: %+v", c)
	}
}

func TestUnstructuredToSandboxItem_PhaseProjection(t *testing.T) {
	cases := []struct {
		rawPhase   string
		wantStatus string
		wantPhase  string
	}{
		{"Pending", "pending", "Pending"},
		{"Provisioning", "pending", "Provisioning"},
		{"Ready", "running", "Ready"},
		{"Failed", "failed", "Failed"},
		{"", "pending", ""},        // no phase yet → pending (spinner)
		{"WeirdValue", "unknown", "WeirdValue"},
	}
	for _, tc := range cases {
		u := &unstructured.Unstructured{
			Object: map[string]any{
				"metadata": map[string]any{
					"name": "sandbox-x",
				},
				"status": map[string]any{
					"phase": tc.rawPhase,
				},
			},
		}
		item := unstructuredToSandboxItem(u)
		if item.Status != tc.wantStatus {
			t.Errorf("phase=%q → Status: want %q, got %q", tc.rawPhase, tc.wantStatus, item.Status)
		}
		if item.Phase != tc.wantPhase {
			t.Errorf("phase=%q → Phase (raw): want %q, got %q", tc.rawPhase, tc.wantPhase, item.Phase)
		}
	}
}

func TestUnstructuredToSandboxItem_FailedSurfacesConditions(t *testing.T) {
	// Failed Sandbox with a TokenMintFailed condition — the FE relies
	// on this so it can render "Token mint failed — retrying" inline
	// instead of a generic red pill.
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{"name": "sandbox-tokenfail"},
			"status": map[string]any{
				"phase": "Failed",
				"conditions": []any{
					map[string]any{
						"type":    "Ready",
						"status":  "False",
						"reason":  "TokenMintFailed",
						"message": "newapi bridge unreachable",
					},
				},
			},
		},
	}
	item := unstructuredToSandboxItem(u)
	if item.Status != "failed" || item.Phase != "Failed" {
		t.Errorf("Failed projection wrong: status=%q phase=%q", item.Status, item.Phase)
	}
	if len(item.Conditions) != 1 || item.Conditions[0].Reason != "TokenMintFailed" {
		t.Fatalf("expected TokenMintFailed condition, got %+v", item.Conditions)
	}
}

func TestUnstructuredToSandboxItem_NilInputDoesNotPanic(t *testing.T) {
	item := unstructuredToSandboxItem(nil)
	if item.Previews == nil {
		t.Errorf("nil input: Previews must be empty slice, not nil")
	}
	if item.Conditions == nil {
		t.Errorf("nil input: Conditions must be empty slice, not nil")
	}
}

func TestUnstructuredToSandboxItem_PreviewFloat64Coercion(t *testing.T) {
	// JSON round-trip through the apiserver may serialise int as
	// float64. The projection MUST coerce both forms or the FE sees
	// prNumber=0 for every preview after a SSE refresh.
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{"name": "sandbox-floats"},
			"status": map[string]any{
				"previews": []any{
					map[string]any{
						"prNumber": float64(7),
						"url":      "https://pr-7.preview.example",
					},
				},
			},
		},
	}
	item := unstructuredToSandboxItem(u)
	if len(item.Previews) != 1 || item.Previews[0].PRNumber != 7 {
		t.Errorf("float64→int64 coercion failed: %+v", item.Previews)
	}
}
