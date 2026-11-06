package regions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const usableKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: b
  cluster:
    server: https://10.0.0.2:6443
users:
- name: b
  user:
    token: abc
contexts:
- name: b
  context:
    cluster: b
    user: b
current-context: b
`

func TestSecretResolver_MapsContinuumRegionToRegionKey(t *testing.T) {
	var gotNS, gotName string
	var built string
	want := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	r := &SecretResolver{
		Read: func(_ context.Context, ns, name string) (map[string][]byte, error) {
			gotNS, gotName = ns, name
			return map[string][]byte{
				"me-east-215-b-1.yaml": []byte(usableKubeconfig),
			}, nil
		},
		Build: func(raw []byte) (dynamic.Interface, error) {
			built = string(raw)
			return want, nil
		},
	}
	got, err := r.ClientFor(context.Background(), "hw-me-east-215-b-rtz-prod")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if got != want || built != usableKubeconfig {
		t.Errorf("wrong client built")
	}
	if gotNS != DefaultSecretNamespace || gotName != DefaultSecretName {
		t.Errorf("read %s/%s, want the existing bridge Secret %s/%s", gotNS, gotName, DefaultSecretNamespace, DefaultSecretName)
	}
}

func TestSecretResolver_FailsLoud(t *testing.T) {
	cases := []struct {
		name string
		data map[string][]byte
		err  error
		want string
	}{
		{"secret missing", nil, errors.New("secrets \"cutover-secondary-kubeconfigs\" not found"), "not found"},
		{"no key for region", map[string][]byte{"me-east-215-c-1.yaml": []byte(usableKubeconfig)}, nil, "no kubeconfig for region"},
		{"unusable kubeconfig", map[string][]byte{"me-east-215-b-1.yaml": []byte("apiVersion: v1\nkind: Config\n")}, nil, "unusable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &SecretResolver{
				Read: func(context.Context, string, string) (map[string][]byte, error) { return tc.data, tc.err },
				Build: func([]byte) (dynamic.Interface, error) {
					t.Fatal("must not build a client")
					return nil, nil
				},
			}
			_, err := r.ClientFor(context.Background(), "hw-me-east-215-b-rtz-prod")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want containing %q", err, tc.want)
			}
		})
	}
}
