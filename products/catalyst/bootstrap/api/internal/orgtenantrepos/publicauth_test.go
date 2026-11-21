package orgtenantrepos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

func pullSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: PublicAuthSecretName, Namespace: TrustSecretNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       data,
	}
}

// PublicAuth reads the Secret step 06 strips: held before Phase 3b (cloud-init's
// ghcr.io entry next to Phase 0's registry entry), not held after it.
func TestPublicAuthReadsTheStrip(t *testing.T) {
	ctx := context.Background()
	before := `{"auths":{"ghcr.io":{"auth":"eDp5"},"harbor.openova.io":{"auth":"eDp5"},"registry.hw.test":{"auth":"eDp5"}}}`
	after := `{"auths":{"registry.hw.test":{"auth":"eDp5"}}}`
	for _, tc := range []struct {
		name    string
		objs    []runtime.Object
		held    bool
		wantErr bool
	}{
		{"before the strip", []runtime.Object{pullSecret(map[string][]byte{corev1.DockerConfigJsonKey: []byte(before)})}, true, false},
		{"after the strip", []runtime.Object{pullSecret(map[string][]byte{corev1.DockerConfigJsonKey: []byte(after)})}, false, false},
		{"no Secret", nil, false, false},
		{"no dockerconfigjson", []runtime.Object{pullSecret(map[string][]byte{"other": []byte("x")})}, false, false},
		{"unparseable", []runtime.Object{pullSecret(map[string][]byte{corev1.DockerConfigJsonKey: []byte("{")})}, false, true},
	} {
		held, err := PublicAuth(ctx, kfake.NewSimpleClientset(tc.objs...))
		if held != tc.held || (err != nil) != tc.wantErr {
			t.Errorf("%s: PublicAuth = %v, %v; want %v, error %v", tc.name, held, err, tc.held, tc.wantErr)
		}
	}
	core := kfake.NewSimpleClientset()
	core.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	if _, err := PublicAuth(ctx, core); err == nil {
		t.Error("a failed read must return its error")
	}
}

// The Secret PublicAuth reads is the one the blocks name and step 06 strips,
// and the strip removes the public catalog's host: the test reads the cutover
// chart's values file in this checkout.
func TestPublicAuthIsTheSecretStep06Strips(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean("../../../../../../platform/self-sovereign-cutover/chart/values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Harbor struct {
			GHCRSecretRef struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"ghcrSecretRef"`
			MothershipAuthsToStrip []string `json:"mothershipAuthsToStrip"`
		} `json:"harbor"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	ref := values.Harbor.GHCRSecretRef
	if ref.Name != PublicAuthSecretName || ref.Namespace != TrustSecretNamespace {
		t.Errorf("chart harbor.ghcrSecretRef = %s/%s, PublicAuth reads %s/%s: change both", ref.Namespace, ref.Name, TrustSecretNamespace, PublicAuthSecretName)
	}
	stripped := false
	for _, h := range values.Harbor.MothershipAuthsToStrip {
		stripped = stripped || h == PublicCatalogHost()
	}
	if !stripped {
		t.Errorf("chart harbor.mothershipAuthsToStrip %v does not strip %s: OwnSourceUsable reads that strip", values.Harbor.MothershipAuthsToStrip, PublicCatalogHost())
	}
	if PublicCatalogHost() != "ghcr.io" {
		t.Errorf("PublicCatalogHost = %q", PublicCatalogHost())
	}
}
