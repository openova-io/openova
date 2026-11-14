package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestFluxKindToGVR covers the kind → GVR routing for the two
// supported Flux kinds plus the rejection of unknown kinds.
func TestFluxKindToGVR(t *testing.T) {
	cases := map[string]struct {
		kind      string
		wantGroup string
		wantRes   string
		errSub    string
	}{
		"hr lower":      {"helmrelease", "helm.toolkit.fluxcd.io", "helmreleases", ""},
		"hr camel":      {"HelmRelease", "helm.toolkit.fluxcd.io", "helmreleases", ""},
		"ks lower":      {"kustomization", "kustomize.toolkit.fluxcd.io", "kustomizations", ""},
		"ks upper":      {"Kustomization", "kustomize.toolkit.fluxcd.io", "kustomizations", ""},
		"unknown":       {"deployment", "", "", "unknown kind"},
		"empty":         {"", "", "", "unknown kind"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gvr, _, err := fluxKindToGVR(tc.kind)
			if tc.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSub) {
					t.Errorf("err=%v want %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if gvr.Group != tc.wantGroup || gvr.Resource != tc.wantRes {
				t.Errorf("gvr=%+v", gvr)
			}
		})
	}
}

// TestFluxScopedNamespace covers the default-to-Sandbox-namespace
// rule.
func TestFluxScopedNamespace(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		ns, err := fluxScopedNamespace(&Env{SandboxNamespace: "sb"}, "other")
		if err != nil || ns != "other" {
			t.Errorf("ns=%q err=%v", ns, err)
		}
	})
	t.Run("default", func(t *testing.T) {
		ns, err := fluxScopedNamespace(&Env{SandboxNamespace: "sb"}, "")
		if err != nil || ns != "sb" {
			t.Errorf("ns=%q err=%v", ns, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		_, err := fluxScopedNamespace(&Env{}, "")
		if err == nil || !strings.Contains(err.Error(), "SANDBOX_NAMESPACE") {
			t.Errorf("err=%v", err)
		}
	})
}

// TestValidateFluxMutationArgs covers the per-call arg gate.
func TestValidateFluxMutationArgs(t *testing.T) {
	cases := map[string]struct {
		args   fluxMutationArgs
		errSub string
	}{
		"no kind":    {fluxMutationArgs{Name: "x"}, "kind"},
		"bad kind":   {fluxMutationArgs{Kind: "Deployment", Name: "x"}, "must match"},
		"no name":    {fluxMutationArgs{Kind: "HelmRelease"}, "name"},
		"bad name":   {fluxMutationArgs{Kind: "HelmRelease", Name: "Bad-NAME"}, "RFC-1123"},
		"happy hr":   {fluxMutationArgs{Kind: "HelmRelease", Name: "my-app"}, ""},
		"happy ks":   {fluxMutationArgs{Kind: "Kustomization", Name: "my-app-0"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateFluxMutationArgs(&tc.args)
			if tc.errSub == "" {
				if err != nil {
					t.Errorf("err=%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("err=%v want %q", err, tc.errSub)
			}
		})
	}
}

// TestExtractReadyCondition covers the condition-extraction helper
// over the canonical Flux status shape.
func TestExtractReadyCondition(t *testing.T) {
	t.Run("ready true", func(t *testing.T) {
		obj := map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded", "message": "ok"},
				},
			},
		}
		st, r, m := extractReadyCondition(obj)
		if st != "True" || r != "ReconciliationSucceeded" || m != "ok" {
			t.Errorf("st=%q r=%q m=%q", st, r, m)
		}
	})
	t.Run("ready false", func(t *testing.T) {
		obj := map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "False", "reason": "UpgradeFailed", "message": "values invalid"},
				},
			},
		}
		st, r, _ := extractReadyCondition(obj)
		if st != "False" || r != "UpgradeFailed" {
			t.Errorf("st=%q r=%q", st, r)
		}
	})
	t.Run("no conditions", func(t *testing.T) {
		st, _, _ := extractReadyCondition(map[string]any{})
		if st != "Unknown" {
			t.Errorf("st=%q want Unknown", st)
		}
	})
	t.Run("other conditions only", func(t *testing.T) {
		obj := map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Released", "status": "True"},
				},
			},
		}
		st, _, _ := extractReadyCondition(obj)
		if st != "Unknown" {
			t.Errorf("st=%q want Unknown", st)
		}
	})
}

// TestSummariseFluxResource covers the per-row distillation logic
// against a realistic HelmRelease shape.
func TestSummariseFluxResource(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":      "my-app",
			"namespace": "sandbox-u1",
		},
		"spec": map[string]any{
			"suspend": true,
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True", "reason": "Ok"},
			},
			"lastAppliedRevision":   "1.2.3",
			"lastHandledReconcileAt": "2026-05-18T12:00:00Z",
		},
	}}
	s := summariseFluxResource("HelmRelease", obj)
	if s.Kind != "HelmRelease" || s.Name != "my-app" || s.Namespace != "sandbox-u1" {
		t.Errorf("identity=%+v", s)
	}
	if s.Ready != "True" || s.Suspended != true || s.Revision != "1.2.3" {
		t.Errorf("body=%+v", s)
	}
}

// TestFluxStatus_NoNamespacesInScope ensures the tool errors early when
// neither env.SandboxNamespace nor caller-supplied namespaces is set.
func TestFluxStatus_NoNamespacesInScope(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{})
	_, err := fluxStatus(ctx, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "no namespaces in scope") {
		t.Errorf("err=%v", err)
	}
}

// TestFluxMutation_Validation covers the validation surface common to
// reconcile / suspend / resume.
func TestFluxMutation_Validation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sb"})
	for _, fn := range []HandlerFunc{fluxReconcile, fluxSuspend, fluxResume} {
		_, err := fn(ctx, json.RawMessage(`{}`))
		if err == nil || !strings.Contains(err.Error(), "kind") {
			t.Errorf("err=%v want kind", err)
		}
		_, err = fn(ctx, json.RawMessage(`{"kind":"HelmRelease"}`))
		if err == nil || !strings.Contains(err.Error(), "name") {
			t.Errorf("err=%v want name", err)
		}
	}
}

// TestFluxCapabilityGate confirms the registry rejects flux.* calls
// whose claims lack the `flux` capability.
func TestFluxCapabilityGate(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:            "acme",
		JWTSecret:        []byte("x"),
		SandboxNamespace: "sandbox-u1",
	})
	for _, name := range []string{"flux.status", "flux.reconcile", "flux.suspend", "flux.resume"} {
		t.Run(name, func(t *testing.T) {
			cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.db"}}
			args := json.RawMessage(`{"kind":"HelmRelease","name":"x"}`)
			_, err := r.Call(context.Background(), name, args, CallOpts{Claims: cl})
			if err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("missing cap: err=%v want forbidden", err)
			}
		})
	}
}
