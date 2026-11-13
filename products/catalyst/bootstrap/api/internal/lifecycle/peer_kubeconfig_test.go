package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parentBootstrapKubeconfig and handoverPeerKubeconfig are two complete
// kubeconfigs of one region that differ in their token only, as the two
// accounts of a region do.
const parentBootstrapKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://203.0.113.20:6443
  name: default
contexts:
- name: default
  context:
    cluster: default
    user: default
current-context: default
users:
- name: default
  user:
    token: parent-bootstrap-token
`

const handoverPeerKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://203.0.113.20:6443
  name: default
contexts:
- name: default
  context:
    cluster: default
    user: default
current-context: default
users:
- name: default
  user:
    token: handover-peer-token
`

func forwarded(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("ForwardBody is not a JSON object of strings: %v: %s", err, body)
	}
	return got
}

// TestPeerKubeconfigPath_IsNoClusterFile proves the peer file sits next to the
// region's kubeconfig under a name that no reader of the directory takes for
// a cluster (those readers accept .yaml, .yml and .kubeconfig).
func TestPeerKubeconfigPath_IsNoClusterFile(t *testing.T) {
	got := PeerKubeconfigPath("/var/lib/catalyst/kubeconfigs", "abc-nbg1-1")
	if got != "/var/lib/catalyst/kubeconfigs/abc-nbg1-1.peer" {
		t.Fatalf("PeerKubeconfigPath = %q", got)
	}
	for _, ext := range []string{".yaml", ".yml", ".kubeconfig"} {
		if strings.HasSuffix(got, ext) {
			t.Fatalf("PeerKubeconfigPath %q ends in %s: a directory reader would load it as a cluster", got, ext)
		}
	}
	if got := NodeIPPath("/d", "abc-nbg1-1"); got != "/d/abc-nbg1-1.nodeip" {
		t.Fatalf("NodeIPPath = %q", got)
	}
}

// TestStorePeerKubeconfig_WritesMode0600 proves the stored document and its
// mode, and that a blank document writes nothing.
func TestStorePeerKubeconfig_WritesMode0600(t *testing.T) {
	dir := t.TempDir()
	if err := StorePeerKubeconfig(dir, "abc-nbg1-1", "\n"+handoverPeerKubeconfig+"\n"); err != nil {
		t.Fatalf("StorePeerKubeconfig: %v", err)
	}
	path := PeerKubeconfigPath(dir, "abc-nbg1-1")
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("peer kubeconfig not stored: %v", err)
	}
	if string(raw) != strings.TrimSpace(handoverPeerKubeconfig) {
		t.Fatalf("stored %q, want the peer kubeconfig without surrounding white space", raw)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", st.Mode().Perm())
	}
	if err := StorePeerKubeconfig(dir, "abc-sin-2", "  \n"); err != nil {
		t.Fatalf("StorePeerKubeconfig of a blank document: %v", err)
	}
	if _, err := os.Stat(PeerKubeconfigPath(dir, "abc-sin-2")); !os.IsNotExist(err) {
		t.Fatalf("a blank peer kubeconfig was written (stat err %v)", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want only the peer file (no temp file left)", len(entries))
	}
}

// TestPeerKubeconfigDefects proves a blank peer kubeconfig has no defect, a
// complete one has none, and an incomplete one is refused with prefixed names.
func TestPeerKubeconfigDefects(t *testing.T) {
	if d := PeerKubeconfigDefects(" \n"); d != nil {
		t.Fatalf("blank: defects %v, want none", d)
	}
	if d := PeerKubeconfigDefects(handoverPeerKubeconfig); len(d) != 0 {
		t.Fatalf("complete: defects %v, want none", d)
	}
	stub := "apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://203.0.113.20:6443\n  name: c\n"
	d := PeerKubeconfigDefects(stub)
	if len(d) == 0 {
		t.Fatal("incomplete: no defect reported")
	}
	for _, x := range d {
		if !strings.HasPrefix(x, "peerKubeconfig.") {
			t.Fatalf("defect %q does not name the peer kubeconfig", x)
		}
	}
}

// TestForwardBody_SendsThePeerKubeconfig is the hand-over rule of SOV-141:
// when the parent holds a region's peer kubeconfig, the body it sends to the
// Sovereign carries that document and not the region's parent-bootstrap
// kubeconfig, together with the region's node address.
func TestForwardBody_SendsThePeerKubeconfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abc-nbg1-1.yaml"), []byte(parentBootstrapKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StorePeerKubeconfig(dir, "abc-nbg1-1", handoverPeerKubeconfig); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(NodeIPPath(dir, "abc-nbg1-1"), []byte("10.0.2.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := forwarded(t, ForwardBody(dir, "abc", "nbg1-1", []byte(parentBootstrapKubeconfig)))
	if got["kubeconfigYaml"] != strings.TrimSpace(handoverPeerKubeconfig) {
		t.Fatalf("kubeconfigYaml = %q, want the handover-peer kubeconfig", got["kubeconfigYaml"])
	}
	if strings.Contains(string(ForwardBody(dir, "abc", "nbg1-1", []byte(parentBootstrapKubeconfig))), "parent-bootstrap-token") {
		t.Fatal("the body carries the parent-bootstrap token")
	}
	want := map[string]string{"deploymentId": "abc", "regionKey": "nbg1-1", "nodeInternalIp": "10.0.2.2"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != 4 {
		t.Fatalf("body has keys %v, want exactly deploymentId, regionKey, kubeconfigYaml, nodeInternalIp", got)
	}
}

// TestForwardBody_RegionWithoutPeerKeepsItsKubeconfig covers a region whose
// post-back carried no peer kubeconfig (a Sovereign provisioned before
// SOV-141): the body carries the stored document, and no node address when
// the parent holds none.
func TestForwardBody_RegionWithoutPeerKeepsItsKubeconfig(t *testing.T) {
	dir := t.TempDir()
	got := forwarded(t, ForwardBody(dir, "abc", "sin-2", []byte(parentBootstrapKubeconfig)))
	if got["kubeconfigYaml"] != parentBootstrapKubeconfig {
		t.Fatalf("kubeconfigYaml = %q, want the stored kubeconfig", got["kubeconfigYaml"])
	}
	if _, ok := got["nodeInternalIp"]; ok || len(got) != 3 {
		t.Fatalf("body has keys %v, want exactly deploymentId, regionKey, kubeconfigYaml", got)
	}
}
