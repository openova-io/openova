package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestSandboxSecretsName_Format pins the canonical per-Sandbox Secret
// naming so the agent and operator-facing tooling agree on where the
// Secret lives.
func TestSandboxSecretsName_Format(t *testing.T) {
	cases := map[string]struct {
		env  *Env
		want string
	}{
		"happy":   {&Env{OwnerUID: "uid-1"}, "sandbox-uid-1-secrets"},
		"upper":   {&Env{OwnerUID: "UID-1"}, "sandbox-uid-1-secrets"},
		"trimmed": {&Env{OwnerUID: "  uid-1 "}, "sandbox-uid-1-secrets"},
		"missing": {&Env{}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := sandboxSecretsName(tc.env); got != tc.want {
				t.Errorf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

// TestValidateSecretKey covers the per-call key validation gate.
func TestValidateSecretKey(t *testing.T) {
	cases := map[string]string{
		"":          "is required",
		"a":         "",
		"BAR_BAZ":   "",
		"foo.bar":   "",
		"foo/bar":   "must match",
		"../etc":    "must match",
		strings.Repeat("x", 254): "must match",
	}
	for in, want := range cases {
		err := validateSecretKey(in)
		if want == "" {
			if err != nil {
				t.Errorf("%q: err=%v want nil", in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err=%v want %q", in, err, want)
		}
	}
}

// TestRequireSandboxSecretsScope covers the canonical gate every
// sandbox.secrets.* handler runs first.
func TestRequireSandboxSecretsScope(t *testing.T) {
	t.Run("no env", func(t *testing.T) {
		_, _, _, err := requireSandboxSecretsScope(context.Background())
		if err == nil || !strings.Contains(err.Error(), "env missing") {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("no namespace", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{OwnerUID: "u1"})
		_, _, _, err := requireSandboxSecretsScope(ctx)
		if err == nil || !strings.Contains(err.Error(), "SANDBOX_NAMESPACE") {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("no owner uid", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-u1"})
		_, _, _, err := requireSandboxSecretsScope(ctx)
		if err == nil || !strings.Contains(err.Error(), "SANDBOX_OWNER_UID") {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("populated", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-u1", OwnerUID: "u1"})
		env, ns, name, err := requireSandboxSecretsScope(ctx)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if env == nil || ns != "sandbox-u1" || name != "sandbox-u1-secrets" {
			t.Errorf("env=%v ns=%q name=%q", env, ns, name)
		}
	})
}

// TestBuildSecretObject_LabelsAndShape pins the rendered Secret CR's
// shape — labels, type, data: {} (empty), namespace + name.
func TestBuildSecretObject_LabelsAndShape(t *testing.T) {
	env := &Env{SandboxID: "emrah", OwnerUID: "uid-1"}
	obj := buildSecretObject(env, "sandbox-uid-1", "sandbox-uid-1-secrets")
	if obj.GetAPIVersion() != "v1" || obj.GetKind() != "Secret" {
		t.Errorf("api/kind mismatch: %s/%s", obj.GetAPIVersion(), obj.GetKind())
	}
	if obj.GetName() != "sandbox-uid-1-secrets" {
		t.Errorf("name=%q", obj.GetName())
	}
	if obj.GetNamespace() != "sandbox-uid-1" {
		t.Errorf("ns=%q", obj.GetNamespace())
	}
	labels := obj.GetLabels()
	if labels[cnpgManagedByLabel] != cnpgManagedByValue {
		t.Errorf("managed-by missing: %v", labels)
	}
	if labels[cnpgSandboxIDLabel] != "emrah" {
		t.Errorf("sandbox-id label missing: %v", labels)
	}
	if labels["openova.io/sandbox-owner"] != "uid-1" {
		t.Errorf("sandbox-owner label missing: %v", labels)
	}
	if obj.Object["type"] != "Opaque" {
		t.Errorf("type=%v", obj.Object["type"])
	}
	data, _ := obj.Object["data"].(map[string]any)
	if data == nil || len(data) != 0 {
		t.Errorf("data should be empty map, got %v", data)
	}
}

// TestAssertManagedBy enforces the defence-in-depth refusal to mutate
// non-Sandbox Secrets. Stops sandbox.secrets.write from clobbering the
// controller-injected `sandbox-tokens` Secret in the same namespace.
func TestAssertManagedBy(t *testing.T) {
	t.Run("managed", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{cnpgManagedByLabel: cnpgManagedByValue},
			},
		}}
		if err := assertManagedBy(obj, "x"); err != nil {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("unmanaged", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{"openova.io/managed-by": "catalyst"},
			},
		}}
		err := assertManagedBy(obj, "sandbox-tokens")
		if err == nil || !strings.Contains(err.Error(), "not Sandbox-managed") {
			t.Errorf("err=%v want not Sandbox-managed", err)
		}
	})
}

// TestSandboxSecretsRead_KeyValidation covers per-arg key validation
// before any API call.
func TestSandboxSecretsRead_KeyValidation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-u1", OwnerUID: "u1"})
	cases := map[string]string{
		`{}`:               "is required",
		`{"key":""}`:       "is required",
		`{"key":"foo/x"}`:  "must match",
	}
	for args, want := range cases {
		_, err := sandboxSecretsRead(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("args=%s err=%v want %q", args, err, want)
		}
	}
}

// TestSandboxSecretsWrite_KeyValidation covers per-arg key validation.
func TestSandboxSecretsWrite_KeyValidation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-u1", OwnerUID: "u1"})
	cases := map[string]string{
		`{"value":"v"}`:               "is required",
		`{"key":"","value":"v"}`:      "is required",
		`{"key":"foo/x","value":"v"}`: "must match",
	}
	for args, want := range cases {
		_, err := sandboxSecretsWrite(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("args=%s err=%v want %q", args, err, want)
		}
	}
}

// TestSandboxSecretsCapabilityGate confirms the registry rejects
// sandbox.secrets.* calls whose claims lack the `sandbox.secrets` cap.
func TestSandboxSecretsCapabilityGate(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:            "acme",
		JWTSecret:        []byte("x"),
		SandboxNamespace: "sandbox-u1",
		OwnerUID:         "u1",
	})
	for _, name := range []string{"sandbox.secrets.read", "sandbox.secrets.write"} {
		t.Run(name, func(t *testing.T) {
			cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.db"}}
			args := json.RawMessage(`{"key":"FOO","value":"bar"}`)
			_, err := r.Call(context.Background(), name, args, CallOpts{Claims: cl})
			if err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("missing cap: err=%v want forbidden", err)
			}
		})
	}
}

// TestBase64RoundTrip is a sanity check that the encoder/decoder pair
// the handlers use matches the K8s Secret on-the-wire format.
func TestBase64RoundTrip(t *testing.T) {
	v := "hunter2-with-newlines\nand\tspecial=chars!"
	enc := base64.StdEncoding.EncodeToString([]byte(v))
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(dec) != v {
		t.Errorf("roundtrip mismatch: got %q want %q", string(dec), v)
	}
}
