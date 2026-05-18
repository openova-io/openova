package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestSandboxPreviewCreate_InputValidation covers the input-validation
// guard rails before any RBAC / API hop. The handler runs WITHOUT a
// live API server because it never gets past validation.
func TestSandboxPreviewCreate_InputValidation(t *testing.T) {
	cases := map[string]struct {
		args    string
		wantErr string
	}{
		"missing pr_number":  {`{"image":"ghcr.io/x:v1"}`, "must be a positive"},
		"zero pr_number":     {`{"pr_number":0,"image":"ghcr.io/x:v1"}`, "must be a positive"},
		"negative pr_number": {`{"pr_number":-3,"image":"ghcr.io/x:v1"}`, "must be a positive"},
		"huge pr_number":     {`{"pr_number":99999999,"image":"ghcr.io/x:v1"}`, "suspiciously large"},
		"missing image":      {`{"pr_number":42}`, "must match"},
		"image with space":   {`{"pr_number":42,"image":"ghcr.io/x v1"}`, "must match"},
		"valid pr+image":     {`{"pr_number":42,"image":"ghcr.io/x:v1"}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := WithEnv(context.Background(), &Env{
				SandboxNamespace: "sandbox-uid-1",
				SovereignFQDN:    "acme.openova.io",
				SandboxID:        "emrah",
			})
			_, err := sandboxPreviewCreate(ctx, json.RawMessage(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			// Valid path: the call MUST not surface any of the validation
			// errors.
			if err == nil {
				return
			}
			for _, bad := range []string{"must be a positive", "suspiciously large", "must match"} {
				if strings.Contains(err.Error(), bad) {
					t.Errorf("unexpected validation error for valid input: %v", err)
				}
			}
		})
	}
}

// TestPreviewAppSlug_Derivation covers the canonical app-slug rules.
func TestPreviewAppSlug_Derivation(t *testing.T) {
	cases := []struct {
		name    string
		env     *Env
		repo    string
		want    string
		wantErr bool
	}{
		{"repo basename", &Env{SandboxID: "emrah"}, "acme/eventforge", "eventforge", false},
		{"bare repo name", &Env{SandboxID: "emrah"}, "eventforge", "eventforge", false},
		{"falls back to SandboxID", &Env{SandboxID: "emrah"}, "", "emrah", false},
		{"sandbox id lowercased", &Env{SandboxID: "Emrah"}, "", "emrah", false},
		{"repo uppercase lowercased", &Env{SandboxID: "emrah"}, "ACME/MyApp", "myapp", false},
		{"no repo no sandbox", &Env{}, "", "", true},
		{"repo with bad chars", &Env{SandboxID: "emrah"}, "acme/my_app", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := previewAppSlug(tc.env, tc.repo)
			if tc.wantErr {
				if err == nil {
					t.Errorf("got=%q want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if got != tc.want {
				t.Errorf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

// TestPreviewHostname_Shape asserts the canonical `pr-<n>.<app>.sandbox.<sov>` shape.
func TestPreviewHostname_Shape(t *testing.T) {
	env := &Env{SovereignFQDN: "acme.openova.io"}
	got := previewHostname(env, "eventforge", 1483)
	want := "pr-1483.eventforge.sandbox.acme.openova.io"
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
}

// TestBuildPreviewDeployment_Shape covers the rendered Deployment so
// the agent / Gateway pickup contract doesn't drift.
func TestBuildPreviewDeployment_Shape(t *testing.T) {
	env := &Env{
		SandboxID:        "emrah",
		SandboxNamespace: "sandbox-uid-1",
		SovereignFQDN:    "acme.openova.io",
	}
	dep := buildPreviewDeployment(env, 42, "eventforge", "ghcr.io/x:v1", "acme/eventforge")
	if got := dep.GetAPIVersion(); got != "apps/v1" {
		t.Errorf("apiVersion=%q want apps/v1", got)
	}
	if got := dep.GetKind(); got != "Deployment" {
		t.Errorf("kind=%q want Deployment", got)
	}
	if got := dep.GetName(); got != "preview-pr-42" {
		t.Errorf("name=%q want preview-pr-42", got)
	}
	if got := dep.GetNamespace(); got != "sandbox-uid-1" {
		t.Errorf("namespace=%q want sandbox-uid-1", got)
	}
	labels := dep.GetLabels()
	if labels[cnpgManagedByLabel] != cnpgManagedByValue {
		t.Errorf("managed-by label missing: %v", labels)
	}
	if labels[previewPRLabel] != "42" {
		t.Errorf("preview-pr label=%q want 42", labels[previewPRLabel])
	}
	if labels[previewAppLabel] != "eventforge" {
		t.Errorf("preview-app label=%q want eventforge", labels[previewAppLabel])
	}
	if labels[previewSandboxID] != "emrah" {
		t.Errorf("sandbox-id label=%q want emrah", labels[previewSandboxID])
	}
	if labels[previewRepoLabel] != "acme-eventforge" {
		t.Errorf("repo label=%q want acme-eventforge", labels[previewRepoLabel])
	}
	annots := dep.GetAnnotations()
	if annots[previewImageAnnot] != "ghcr.io/x:v1" {
		t.Errorf("image annotation=%q want ghcr.io/x:v1", annots[previewImageAnnot])
	}
	spec := dep.Object["spec"].(map[string]any)
	if spec["replicas"].(int64) != 1 {
		t.Errorf("replicas=%v want 1", spec["replicas"])
	}
	selector := spec["selector"].(map[string]any)
	matchLabels := selector["matchLabels"].(map[string]any)
	if matchLabels[previewPRLabel] != "42" {
		t.Errorf("matchLabels[%s]=%v want 42", previewPRLabel, matchLabels[previewPRLabel])
	}
	tmpl := spec["template"].(map[string]any)
	tmplSpec := tmpl["spec"].(map[string]any)
	containers := tmplSpec["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("containers count=%d want 1", len(containers))
	}
	c0 := containers[0].(map[string]any)
	if c0["image"] != "ghcr.io/x:v1" {
		t.Errorf("container image=%v want ghcr.io/x:v1", c0["image"])
	}
	if c0["name"] != "app" {
		t.Errorf("container name=%v want app", c0["name"])
	}
}

// TestBuildPreviewService_Shape covers the Service CR contract.
func TestBuildPreviewService_Shape(t *testing.T) {
	env := &Env{SandboxNamespace: "sandbox-uid-1", SandboxID: "emrah"}
	svc := buildPreviewService(env, 42, "eventforge", "acme/eventforge")
	if got := svc.GetKind(); got != "Service" {
		t.Errorf("kind=%q want Service", got)
	}
	if got := svc.GetName(); got != "preview-pr-42" {
		t.Errorf("name=%q want preview-pr-42", got)
	}
	spec := svc.Object["spec"].(map[string]any)
	if spec["type"] != "ClusterIP" {
		t.Errorf("type=%v want ClusterIP", spec["type"])
	}
	selector := spec["selector"].(map[string]any)
	if selector[previewPRLabel] != "42" {
		t.Errorf("selector[%s]=%v want 42", previewPRLabel, selector[previewPRLabel])
	}
	ports := spec["ports"].([]any)
	if len(ports) != 1 {
		t.Fatalf("ports count=%d want 1", len(ports))
	}
	port0 := ports[0].(map[string]any)
	if port0["port"].(int64) != 80 {
		t.Errorf("port=%v want 80", port0["port"])
	}
	if port0["targetPort"].(int64) != int64(previewPort) {
		t.Errorf("targetPort=%v want %d", port0["targetPort"], previewPort)
	}
}

// TestBuildPreviewHTTPRoute_Shape covers the Gateway attachment.
func TestBuildPreviewHTTPRoute_Shape(t *testing.T) {
	env := &Env{
		SandboxNamespace: "sandbox-uid-1",
		SovereignFQDN:    "acme.openova.io",
		SandboxID:        "emrah",
	}
	route := buildPreviewHTTPRoute(env, 42, "eventforge", "acme/eventforge")
	if got := route.GetAPIVersion(); got != "gateway.networking.k8s.io/v1" {
		t.Errorf("apiVersion=%q want gateway.networking.k8s.io/v1", got)
	}
	if got := route.GetKind(); got != "HTTPRoute" {
		t.Errorf("kind=%q want HTTPRoute", got)
	}
	spec := route.Object["spec"].(map[string]any)
	parents := spec["parentRefs"].([]any)
	if len(parents) != 1 {
		t.Fatalf("parentRefs count=%d want 1", len(parents))
	}
	p0 := parents[0].(map[string]any)
	if p0["name"] != "cilium-gateway" || p0["namespace"] != "kube-system" {
		t.Errorf("parentRef=%+v want cilium-gateway/kube-system", p0)
	}
	hostnames := spec["hostnames"].([]any)
	if len(hostnames) != 1 || hostnames[0] != "pr-42.eventforge.sandbox.acme.openova.io" {
		t.Errorf("hostnames=%v want [pr-42.eventforge.sandbox.acme.openova.io]", hostnames)
	}
	rules := spec["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("rules count=%d want 1", len(rules))
	}
	rule0 := rules[0].(map[string]any)
	backends := rule0["backendRefs"].([]any)
	b0 := backends[0].(map[string]any)
	if b0["name"] != "preview-pr-42" || b0["port"].(int64) != 80 {
		t.Errorf("backendRef=%+v want preview-pr-42:80", b0)
	}
}

// TestRequirePreviewScope_Guards covers the missing-FQDN + missing-NS
// failure modes so the agent gets a clean error instead of a 500.
func TestRequirePreviewScope_Guards(t *testing.T) {
	t.Run("no env", func(t *testing.T) {
		_, _, err := requirePreviewScope(context.Background())
		if err == nil || !strings.Contains(err.Error(), "server env missing") {
			t.Errorf("err = %v, want missing env", err)
		}
	})
	t.Run("missing FQDN", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-uid-1"})
		_, _, err := requirePreviewScope(ctx)
		if err == nil || !strings.Contains(err.Error(), "SOVEREIGN_FQDN") {
			t.Errorf("err = %v, want SOVEREIGN_FQDN", err)
		}
	})
	t.Run("env populated", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{
			SandboxNamespace: "sandbox-uid-1",
			SovereignFQDN:    "acme.openova.io",
		})
		_, ns, err := requirePreviewScope(ctx)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if ns != "sandbox-uid-1" {
			t.Errorf("ns=%q want sandbox-uid-1", ns)
		}
	})
}

// TestSandboxPreviewCapabilityGate confirms the registry rejects
// sandbox.preview.* calls whose claims lack the `sandbox.preview`
// capability.
func TestSandboxPreviewCapabilityGate(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:            "acme",
		JWTSecret:        []byte("x"),
		SandboxNamespace: "sandbox-uid-1",
		SovereignFQDN:    "acme.openova.io",
		SandboxID:        "emrah",
	})
	for _, name := range []string{
		"sandbox.preview.create", "sandbox.preview.list", "sandbox.preview.status",
		"sandbox.preview.teardown", "sandbox.preview.rebuild",
	} {
		t.Run(name, func(t *testing.T) {
			cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.session"}}
			args := json.RawMessage(`{"pr_number":42,"image":"ghcr.io/x:v1"}`)
			_, err := r.Call(context.Background(), name, args, CallOpts{Claims: cl})
			if err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("missing cap: err = %v, want forbidden", err)
			}
		})
	}
}

// TestSandboxPreviewRegistered makes sure every Wave-12 tool name is
// registered AND carries the canonical RequiredCapability.
func TestSandboxPreviewRegistered(t *testing.T) {
	r := NewRegistry(&Env{})
	expected := []string{
		"sandbox.preview.create",
		"sandbox.preview.list",
		"sandbox.preview.status",
		"sandbox.preview.teardown",
		"sandbox.preview.rebuild",
	}
	seen := map[string]bool{}
	for _, tool := range r.List() {
		if strings.HasPrefix(tool.Name, "sandbox.preview.") {
			seen[tool.Name] = true
			if tool.RequiredCapability != "sandbox.preview" {
				t.Errorf("%s RequiredCapability=%q want sandbox.preview", tool.Name, tool.RequiredCapability)
			}
			if tool.Handler == nil {
				t.Errorf("%s has nil Handler — still a stub", tool.Name)
			}
		}
	}
	for _, want := range expected {
		if !seen[want] {
			t.Errorf("tool %q not registered", want)
		}
	}
}

// TestPreviewSelectorLabel covers the canonical list/teardown selector.
func TestPreviewSelectorLabel(t *testing.T) {
	if got := previewSelectorLabel(0); got != cnpgManagedByLabel+"="+cnpgManagedByValue+","+previewPRLabel {
		t.Errorf("all-list selector=%q", got)
	}
	if got := previewSelectorLabel(42); got != cnpgManagedByLabel+"="+cnpgManagedByValue+","+previewPRLabel+"=42" {
		t.Errorf("per-PR selector=%q", got)
	}
}

// TestPreviewResourceName covers the canonical name shape used by D/S/R.
func TestPreviewResourceName(t *testing.T) {
	if got := previewResourceName(42); got != "preview-pr-42" {
		t.Errorf("got=%q want=preview-pr-42", got)
	}
}
