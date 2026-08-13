package handler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// #6163 FREEZE 1. seedAnthropicToken read its credential with os.Getenv, and a
// secretKeyRef env var is materialised ONCE at container start — so the
// ten-minute self-heal loop re-applied the boot-time snapshot forever and an
// operator's rotation never took effect without a pod roll.

func withAnthropicSecretClient(t *testing.T, c kubernetes.Interface, err error) {
	t.Helper()
	prev := anthropicSecretClientFor
	anthropicSecretClientFor = func() (kubernetes.Interface, error) { return c, err }
	t.Cleanup(func() { anthropicSecretClientFor = prev })
}

func anthropicSecret(apiKey, credsJSON string) *corev1.Secret {
	d := map[string][]byte{}
	if apiKey != "" {
		d[anthropicCredentialAPIKeyKey] = []byte(apiKey)
	}
	if credsJSON != "" {
		d[anthropicCredentialJSONKey] = []byte(credsJSON)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      anthropicCredentialSecret,
			Namespace: anthropicCredentialNamespace,
		},
		Data: d,
	}
}

// THE BUG: a rotated Secret must be visible without a pod roll.
func TestAnthropicCredential_RotationIsVisibleLive_6163(t *testing.T) {
	sec := anthropicSecret("sk-ant-ROTATED", `{"claudeAiOauth":{"accessToken":"fresh"}}`)
	withAnthropicSecretClient(t, fake.NewSimpleClientset(sec), nil)

	key, creds := anthropicCredentialFromSecret()
	if key != "sk-ant-ROTATED" {
		t.Fatalf("live apiKey not read — rotation would still need a pod roll; got %q", key)
	}
	if creds != `{"claudeAiOauth":{"accessToken":"fresh"}}` {
		t.Fatalf("live credentialsJson not read; got %q", creds)
	}
}

// FALLBACK MUST HOLD: no in-cluster identity => ("",""), so the caller keeps its
// env-based behaviour. This is what stops the fix regressing installs that work.
func TestAnthropicCredential_NoClientFallsBack_6163(t *testing.T) {
	withAnthropicSecretClient(t, nil, errNoInCluster{})
	if k, c := anthropicCredentialFromSecret(); k != "" || c != "" {
		t.Fatalf("no-client path must yield empty so env wins; got %q/%q", k, c)
	}
}

// Secret absent entirely — same fallback, no error surfaced.
func TestAnthropicCredential_SecretAbsentFallsBack_6163(t *testing.T) {
	withAnthropicSecretClient(t, fake.NewSimpleClientset(), nil)
	if k, c := anthropicCredentialFromSecret(); k != "" || c != "" {
		t.Fatalf("absent Secret must yield empty so env wins; got %q/%q", k, c)
	}
}

// VALUE not KEY: a present-but-empty key is a gap, not a credential.
func TestAnthropicCredential_EmptyValueIsNotACredential_6163(t *testing.T) {
	sec := anthropicSecret("", "")
	sec.Data[anthropicCredentialAPIKeyKey] = []byte("   ")
	sec.Data[anthropicCredentialJSONKey] = []byte("")
	withAnthropicSecretClient(t, fake.NewSimpleClientset(sec), nil)
	if k, c := anthropicCredentialFromSecret(); k != "" || c != "" {
		t.Fatalf("whitespace/empty values must not count as a credential; got %q/%q", k, c)
	}
}

// CONTROL — the reader discriminates rather than always answering the same way:
// two different Secrets yield two different answers.
func TestAnthropicCredential_Control_Discriminates_6163(t *testing.T) {
	withAnthropicSecretClient(t, fake.NewSimpleClientset(anthropicSecret("A", "")), nil)
	a, _ := anthropicCredentialFromSecret()
	withAnthropicSecretClient(t, fake.NewSimpleClientset(anthropicSecret("B", "")), nil)
	b, _ := anthropicCredentialFromSecret()
	if a != "A" || b != "B" {
		t.Fatalf("control failed — reader is not discriminating: %q vs %q", a, b)
	}
}

type errNoInCluster struct{}

func (errNoInCluster) Error() string { return "no in-cluster identity" }
