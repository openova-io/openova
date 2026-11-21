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
		ObjectMeta: metav1.ObjectMeta{Name: PullSecretName, Namespace: TrustSecretNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       data,
	}
}

// PullAuth reads the entry step 06's Phase 0 adds: absent before Phase 0
// (cloud-init's ghcr.io and harbor.openova.io only), present after it, and
// still present after the Phase 3b strip removed ghcr.io.
func TestPullAuthReadsPhase0(t *testing.T) {
	ctx := context.Background()
	const host = "registry.hw.test"
	beforePhase0 := `{"auths":{"ghcr.io":{"auth":"eDp5"},"harbor.openova.io":{"auth":"eDp5"}}}`
	afterPhase0 := `{"auths":{"ghcr.io":{"auth":"eDp5"},"harbor.openova.io":{"auth":"eDp5"},"registry.hw.test":{"auth":"eDp5"}}}`
	afterStrip := `{"auths":{"registry.hw.test":{"auth":"eDp5"}}}`
	for _, tc := range []struct {
		name    string
		objs    []runtime.Object
		held    bool
		wantErr bool
	}{
		{"before Phase 0", []runtime.Object{pullSecret(map[string][]byte{corev1.DockerConfigJsonKey: []byte(beforePhase0)})}, false, false},
		{"after Phase 0, before the strip", []runtime.Object{pullSecret(map[string][]byte{corev1.DockerConfigJsonKey: []byte(afterPhase0)})}, true, false},
		{"after the strip", []runtime.Object{pullSecret(map[string][]byte{corev1.DockerConfigJsonKey: []byte(afterStrip)})}, true, false},
		{"no Secret", nil, false, false},
		{"no dockerconfigjson", []runtime.Object{pullSecret(map[string][]byte{"other": []byte("x")})}, false, false},
		{"unparseable", []runtime.Object{pullSecret(map[string][]byte{corev1.DockerConfigJsonKey: []byte("{")})}, false, true},
	} {
		held, err := PullAuth(ctx, kfake.NewSimpleClientset(tc.objs...), host)
		if held != tc.held || (err != nil) != tc.wantErr {
			t.Errorf("%s: PullAuth = %v, %v; want %v, error %v", tc.name, held, err, tc.held, tc.wantErr)
		}
	}
	core := kfake.NewSimpleClientset()
	core.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	if _, err := PullAuth(ctx, core, host); err == nil {
		t.Error("a failed read must return its error")
	}
}

// The host looked up is the host of the url the blocks get on the own source.
func TestOwnRegistryHost(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"SOVEREIGN_FQDN": "hw.test"}, "registry.hw.test"},
		{map[string]string{"CATALYST_OTECH_FQDN": "hw.test"}, "registry.hw.test"},
		{map[string]string{"SOVEREIGN_FQDN": "hw.test", "CATALYST_LOCAL_REGISTRY_URL": "harbor.local.test/openova-io/"}, "harbor.local.test"},
		{nil, ""},
	} {
		if got := OwnRegistryHost(envOf(tc.env)); got != tc.want {
			t.Errorf("%v: OwnRegistryHost = %q, want %q", tc.env, got, tc.want)
		}
	}
}

// The Secret PullAuth reads is the one the blocks name and step 06's Phase 0
// merges into: the test reads the cutover chart's values file in this checkout.
func TestPullAuthIsTheSecretStep06MergesInto(t *testing.T) {
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
		} `json:"harbor"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	ref := values.Harbor.GHCRSecretRef
	if ref.Name != PullSecretName || ref.Namespace != TrustSecretNamespace {
		t.Errorf("chart harbor.ghcrSecretRef = %s/%s, PullAuth reads %s/%s: change both", ref.Namespace, ref.Name, TrustSecretNamespace, PullSecretName)
	}
}
