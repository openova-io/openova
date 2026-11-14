// sandbox_secrets.go — real handlers for sandbox.secrets.* (per-Sandbox
// Secret store).
//
// Scope (architecture.md §3): agents shipping apps from inside a
// Sandbox frequently need to keep API keys, signing secrets, third-party
// credentials. Rather than expose a generic k8s.write.* Secret API
// (which would require fine-grained Sandbox-namespace RBAC + a
// namespace + name dance for every call), Sandbox provides a single
// canonical Secret per Sandbox owner — `sandbox-<owner-uid>-secrets` —
// that the agent can read/write key-by-key:
//
//   - sandbox.secrets.read  {key}        → reads key from the Secret
//   - sandbox.secrets.write {key, value} → upserts key into the Secret
//
// The agent CANNOT pass a `namespace` or `secret_name` argument. The
// Secret lives in env.SandboxNamespace, named exclusively by
// `sandbox-<env.OwnerUID>-secrets`. K8s' at-rest Secret encryption
// (kube-apiserver `--encryption-provider-config`) handles encryption.
//
// Authoring rules + scope guarantees:
//
//   1. The Secret is auto-created on first write — empty Sandbox has no
//      Secret. read on a missing Secret returns a structured 404-like
//      response rather than erroring (so the agent can branch on
//      "never set" vs "set but mismatched").
//
//   2. Every read NEVER echoes the value into the JSON-RPC response
//      body when the caller didn't supply a key (i.e. we don't support
//      a "list" or "dump" variant — those are deliberate omissions).
//
//   3. The Secret is gated by the same managed-by label every other
//      sandbox.*.* writer uses (`openova.io/managed-by=
//      openova-sandbox-mcp`). A pre-existing Secret without the label
//      cannot be written through this tool (defence in depth — the
//      sandbox-controller's `sandbox-tokens` Secret happens to live in
//      the same ns and we MUST not mutate it).
//
//   4. Keys are restricted to `[A-Za-z0-9_.-]{1,253}` — same as k8s
//      Secret data-key validation. The agent cannot smuggle a
//      `../../../foo` path through the key.
//
// Auth: same shape as sandbox.db.* / sandbox.auth.* — claims.OrgID must
// match env.OrgID, RequiredCapability = "sandbox.secrets".
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// secretsGVR — core/v1 Secret. Same shape as cnpgClusterGVR but the
// core group is empty-string by convention; we centralise the value
// here so a future API-group rename is one line.
var secretsGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

// secretKeyRE matches RFC-1123-ish key names. Same alphabet k8s itself
// accepts for Secret/ConfigMap data keys.
var secretKeyRE = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,253}$`)

// sandboxSecretsName returns the canonical Secret name for the
// Sandbox bound to env. Format: `sandbox-<owner-uid>-secrets`.
// Returns "" when env.OwnerUID is empty (controller didn't wire
// SANDBOX_OWNER_UID).
func sandboxSecretsName(env *Env) string {
	uid := strings.ToLower(strings.TrimSpace(env.OwnerUID))
	if uid == "" {
		return ""
	}
	return fmt.Sprintf("sandbox-%s-secrets", uid)
}

// requireSandboxSecretsScope is the canonical gate every
// sandbox.secrets.* handler runs first.
func requireSandboxSecretsScope(ctx context.Context) (*Env, string, string, error) {
	env, ns, err := requireSandboxNS(ctx)
	if err != nil {
		return nil, "", "", err
	}
	name := sandboxSecretsName(env)
	if name == "" {
		return nil, "", "", errors.New("sandbox.secrets: env missing SANDBOX_OWNER_UID (controller didn't wire owner UID)")
	}
	return env, ns, name, nil
}

// validateSecretKey rejects keys that would clash with k8s' Secret
// data-key validation OR that could collide with controller-managed
// keys (`placeholder` from the sandbox-controller's stub Secret).
func validateSecretKey(key string) error {
	if key == "" {
		return errors.New("`key` is required")
	}
	if !secretKeyRE.MatchString(key) {
		return fmt.Errorf("`key` must match %s", secretKeyRE.String())
	}
	return nil
}

// buildSecretObject renders an empty Sandbox-managed Secret. Used by
// sandbox.secrets.write when the Secret doesn't exist yet — the
// first-write path auto-creates rather than 404'ing.
func buildSecretObject(env *Env, ns, name string) *unstructured.Unstructured {
	labels := map[string]any{
		cnpgManagedByLabel: cnpgManagedByValue,
	}
	if env.SandboxID != "" {
		labels[cnpgSandboxIDLabel] = env.SandboxID
	}
	if env.OwnerUID != "" {
		labels["openova.io/sandbox-owner"] = env.OwnerUID
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      name,
				"namespace": ns,
				"labels":    labels,
				"annotations": map[string]any{
					"openova.io/created-by": "sandbox.secrets.write",
				},
			},
			"type": "Opaque",
			"data": map[string]any{},
		},
	}
}

// assertManagedBy verifies the existing Secret carries the
// sandbox-mcp managed-by label. Refusing to mutate a Secret we did NOT
// create stops sandbox.secrets.write from clobbering the
// controller-injected `sandbox-tokens` Secret (or a future per-Sandbox
// app Secret an operator might author by hand).
func assertManagedBy(obj *unstructured.Unstructured, name string) error {
	if obj.GetLabels()[cnpgManagedByLabel] != cnpgManagedByValue {
		return fmt.Errorf("sandbox.secrets: refusing to mutate Secret %s — missing %s=%s label (not Sandbox-managed)",
			name, cnpgManagedByLabel, cnpgManagedByValue)
	}
	return nil
}

// --- sandbox.secrets.read ----------------------------------------------

type sandboxSecretsReadArgs struct {
	Key string `json:"key"`
}

func sandboxSecretsRead(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxSecretsReadArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.secrets.read: invalid arguments: %w", err)
	}
	if err := validateSecretKey(args.Key); err != nil {
		return nil, fmt.Errorf("sandbox.secrets.read: %w", err)
	}
	env, ns, name, err := requireSandboxSecretsScope(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	obj, err := client.Resource(secretsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// "Never set" is a different signal than "set but no such
			// key". Surface a clean envelope with status="NotFound".
			return map[string]any{
				"status":    "NotFound",
				"key":       args.Key,
				"namespace": ns,
				"secret":    name,
				"note":      "Sandbox secret store does not exist yet. Write any key to create it.",
			}, nil
		}
		return nil, fmt.Errorf("sandbox.secrets.read: GET Secret %s/%s: %w", ns, name, err)
	}
	if err := assertManagedBy(obj, name); err != nil {
		return nil, err
	}
	data, _, _ := unstructured.NestedMap(obj.Object, "data")
	enc, ok := data[args.Key].(string)
	if !ok || enc == "" {
		return map[string]any{
			"status":    "KeyNotFound",
			"key":       args.Key,
			"namespace": ns,
			"secret":    name,
		}, nil
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(enc)
	if decodeErr != nil {
		return nil, fmt.Errorf("sandbox.secrets.read: decode key %q: %w", args.Key, decodeErr)
	}
	_ = env // env returned for symmetry; not used in the body below.
	return map[string]any{
		"status":    "Found",
		"key":       args.Key,
		"value":     string(decoded),
		"namespace": ns,
		"secret":    name,
	}, nil
}

func schemaSandboxSecretsRead() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"key"},
		"properties": map[string]any{
			"key": map[string]any{
				"type":        "string",
				"description": "Secret data key (RFC-1123-ish: alnum + `._-`, 1-253 chars).",
				"pattern":     secretKeyRE.String(),
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.secrets.write ---------------------------------------------

type sandboxSecretsWriteArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func sandboxSecretsWrite(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxSecretsWriteArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.secrets.write: invalid arguments: %w", err)
	}
	if err := validateSecretKey(args.Key); err != nil {
		return nil, fmt.Errorf("sandbox.secrets.write: %w", err)
	}
	// Value may be empty string (caller explicitly wants to set the
	// key to ""), but NOT unset — for that the caller uses a future
	// sandbox.secrets.delete.
	env, ns, name, err := requireSandboxSecretsScope(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	encodedValue := base64.StdEncoding.EncodeToString([]byte(args.Value))

	existing, getErr := client.Resource(secretsGVR).Namespace(ns).
		Get(ctx, name, metav1.GetOptions{})
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("sandbox.secrets.write: GET Secret %s/%s: %w", ns, name, getErr)
	}
	if apierrors.IsNotFound(getErr) {
		// Create-with-key path. Build a fresh Sandbox-managed Secret
		// carrying the single supplied key.
		obj := buildSecretObject(env, ns, name)
		data, _ := obj.Object["data"].(map[string]any)
		data[args.Key] = encodedValue
		obj.Object["data"] = data
		created, err := client.Resource(secretsGVR).Namespace(ns).
			Create(ctx, obj, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("sandbox.secrets.write: CREATE Secret %s/%s: %w", ns, name, err)
		}
		return map[string]any{
			"status":          "Created",
			"key":             args.Key,
			"namespace":       ns,
			"secret":          name,
			"resourceVersion": created.GetResourceVersion(),
			"note":            "Secret store auto-created on first write.",
		}, nil
	}
	// Upsert path. Refuse to write through a non-Sandbox Secret.
	if err := assertManagedBy(existing, name); err != nil {
		return nil, err
	}
	data, _, _ := unstructured.NestedMap(existing.Object, "data")
	if data == nil {
		data = map[string]any{}
	}
	previouslyPresent := false
	if _, ok := data[args.Key]; ok {
		previouslyPresent = true
	}
	data[args.Key] = encodedValue
	if err := unstructured.SetNestedMap(existing.Object, data, "data"); err != nil {
		return nil, fmt.Errorf("sandbox.secrets.write: set nested data: %w", err)
	}
	updated, err := client.Resource(secretsGVR).Namespace(ns).
		Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox.secrets.write: UPDATE Secret %s/%s: %w", ns, name, err)
	}
	status := "Updated"
	if !previouslyPresent {
		status = "Added"
	}
	return map[string]any{
		"status":          status,
		"key":             args.Key,
		"namespace":       ns,
		"secret":          name,
		"resourceVersion": updated.GetResourceVersion(),
	}, nil
}

func schemaSandboxSecretsWrite() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"key", "value"},
		"properties": map[string]any{
			"key": map[string]any{
				"type":        "string",
				"description": "Secret data key (RFC-1123-ish: alnum + `._-`, 1-253 chars).",
				"pattern":     secretKeyRE.String(),
			},
			"value": map[string]any{
				"type":        "string",
				"description": "Value to store. Stored base64-encoded in the K8s Secret (encryption-at-rest via kube-apiserver encryption provider).",
			},
		},
		"additionalProperties": false,
	}
}
