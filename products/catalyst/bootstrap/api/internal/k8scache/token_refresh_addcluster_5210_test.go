// token_refresh_addcluster_5210_test.go — #5210 file-backed path regression.
//
// On a chroot the LOCAL region informer client is NOT the InClusterConfig
// self-register (that is de-duped away once <kubeconfigsDir>/<id>.yaml exists);
// it is the FILE-backed AddCluster path loading the materialized primary
// kubeconfig. clustermesh_primary_kubeconfig.go now materializes that kubeconfig
// with a `user.tokenFile:` reference (not a one-shot inline `token:`), which
// clientcmd maps to rest.Config.BearerTokenFile. AddCluster must (a) keep that
// BearerTokenFile on the captured exec config so the projected token keeps
// refreshing, and (b) route the informer clients through the same
// force-refresh-on-401 transport the self-register path uses. Before the #5210
// fix the materialized kubeconfig baked a static token (empty BearerTokenFile)
// and every reflector 401-looped forever once kubelet rotated the bound token.
package k8scache

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTokenFileKubeconfig writes a projected-token file plus a kubeconfig that
// references it via user.tokenFile (the shape clustermesh_primary_kubeconfig.go
// now materializes). Returns the kubeconfig path and the token-file path.
func writeTokenFileKubeconfig(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("bound-sa-token-A"), 0o600); err != nil {
		t.Fatalf("seed token file: %v", err)
	}
	kc := "apiVersion: v1\n" +
		"kind: Config\n" +
		"clusters:\n" +
		"- cluster:\n" +
		"    server: https://10.255.0.1:6443\n" +
		"    insecure-skip-tls-verify: true\n" +
		"  name: local\n" +
		"contexts:\n" +
		"- context:\n" +
		"    cluster: local\n" +
		"    user: local\n" +
		"  name: local\n" +
		"current-context: local\n" +
		"users:\n" +
		"- name: local\n" +
		"  user:\n" +
		"    tokenFile: " + tokenFile + "\n"
	kcPath := filepath.Join(dir, "local.yaml")
	if err := os.WriteFile(kcPath, []byte(kc), 0o600); err != nil {
		t.Fatalf("seed kubeconfig: %v", err)
	}
	return kcPath, tokenFile
}

// TestAddCluster_TokenFileKubeconfig_StaysRefreshing proves the file-backed
// local-cluster path preserves the refreshing token reference. NewFactory calls
// AddCluster during construction (before Start), so the informers are built but
// never dial — the assertion inspects the captured rest.Config only.
func TestAddCluster_TokenFileKubeconfig_StaysRefreshing(t *testing.T) {
	kcPath, tokenFile := writeTokenFileKubeconfig(t)

	f, err := NewFactory(Config{
		Logger:   quietLogger(),
		Clusters: []ClusterRef{{ID: "local", KubeconfigPath: kcPath}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()

	// The cluster must have registered (a parse failure would have made
	// NewFactory skip it — e.g. if BearerTokenFile had been dropped and the
	// tokenFile could not be resolved).
	if !containsClusterID(f.Clusters(), "local") {
		t.Fatalf("Clusters() = %v, want to contain \"local\" — tokenFile kubeconfig failed to register", f.Clusters())
	}

	// The captured exec rest.Config (RestConfigFor, consumed by the SPDY exec
	// dialer) MUST retain BearerTokenFile so its auth also refreshes with the
	// rotated projected token instead of freezing on a one-shot value.
	rc, err := f.RestConfigFor("local")
	if err != nil {
		t.Fatalf("RestConfigFor(local): %v", err)
	}
	if rc.BearerTokenFile != tokenFile {
		t.Errorf("captured exec BearerTokenFile = %q, want %q — a tokenFile kubeconfig must stay refreshing (#5210)", rc.BearerTokenFile, tokenFile)
	}
}
