// peer_kubeconfig_sov141_test.go — SOV-141: each peer region's handover-peer
// kubeconfig is stored by the parent and is what the parent gives the
// Sovereign's primary region, in place of the region's parent-bootstrap
// kubeconfig. The tests drive the REAL receiver, the REAL hand-over export and
// the REAL redelivery pass, through the forward-client seam.

package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The parent-bootstrap kubeconfig of the region is completeKubeconfigSameCluster
// (token fake-token); its handover-peer kubeconfig is
// completeKubeconfigOtherCluster (token fake-token-two).
const sov141DepID = "c0ffee0123456789"

func sov141Seed(t *testing.T, dir string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, sov141DepID+"-nbg1-1.yaml"), completeKubeconfigSameCluster)
	mustWrite(t, filepath.Join(dir, sov141DepID+"-nbg1-1.peer"), completeKubeconfigOtherCluster)
}

// sov141AssertPeerOnly fails unless the chroot received exactly one body,
// for region nbg1-1, carrying the handover-peer kubeconfig and no part of the
// parent-bootstrap kubeconfig.
func sov141AssertPeerOnly(t *testing.T, chroot *fakeChrootServer) {
	t.Helper()
	chroot.mu.Lock()
	defer chroot.mu.Unlock()
	if len(chroot.received) != 1 {
		t.Fatalf("chroot received %d bodies, want 1", len(chroot.received))
	}
	got := chroot.received[0]
	if got["regionKey"] != "nbg1-1" {
		t.Fatalf("regionKey = %q, want nbg1-1", got["regionKey"])
	}
	if got["kubeconfigYaml"] != completeKubeconfigOtherCluster {
		t.Fatalf("kubeconfigYaml = %q, want the handover-peer kubeconfig", got["kubeconfigYaml"])
	}
	if strings.Contains(got["kubeconfigYaml"], "token: fake-token\n") {
		t.Fatal("the parent-bootstrap kubeconfig was forwarded")
	}
}

// TestSecondaryKubeconfig_StoresThePeerKubeconfig_SOV141: a post-back that
// carries peerKubeconfig lands the region's kubeconfig at <clusterID>.yaml as
// before and the peer kubeconfig at <clusterID>.peer, mode 0600.
func TestSecondaryKubeconfig_StoresThePeerKubeconfig_SOV141(t *testing.T) {
	dir := t.TempDir()
	h := newCompletenessTestHandler(t, dir)
	rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   sov141DepID,
		"regionKey":      "nbg1-1",
		"kubeconfigYaml": completeKubeconfigSameCluster,
		"peerKubeconfig": completeKubeconfigOtherCluster,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	own, err := os.ReadFile(filepath.Clean(filepath.Join(dir, sov141DepID+"-nbg1-1.yaml")))
	if err != nil || string(own) != strings.TrimSpace(completeKubeconfigSameCluster) {
		t.Fatalf("region kubeconfig = %q (err %v), want the parent-bootstrap kubeconfig", own, err)
	}
	peerPath := filepath.Join(dir, sov141DepID+"-nbg1-1.peer")
	peer, err := os.ReadFile(filepath.Clean(peerPath))
	if err != nil || string(peer) != strings.TrimSpace(completeKubeconfigOtherCluster) {
		t.Fatalf("peer kubeconfig = %q (err %v), want the handover-peer kubeconfig", peer, err)
	}
	if st, err := os.Stat(peerPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("peer kubeconfig mode wrong (err %v)", err)
	}
}

// TestSecondaryKubeconfig_RefusesAnIncompletePeerKubeconfig_SOV141: a peer
// kubeconfig that cannot build a client is refused with 422 before anything is
// written, and the answer names the peer kubeconfig.
func TestSecondaryKubeconfig_RefusesAnIncompletePeerKubeconfig_SOV141(t *testing.T) {
	dir := t.TempDir()
	h := newCompletenessTestHandler(t, dir)
	rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   sov141DepID,
		"regionKey":      "nbg1-1",
		"kubeconfigYaml": completeKubeconfigSameCluster,
		"peerKubeconfig": hw293StubKubeconfig,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "peerKubeconfig.") {
		t.Fatalf("the 422 does not name the peer kubeconfig: %s", rec.Body.String())
	}
	for _, name := range []string{sov141DepID + "-nbg1-1.yaml", sov141DepID + "-nbg1-1.peer"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was written although the post-back was refused (stat err %v)", name, err)
		}
	}
}

// TestExportSecondaryKubeconfigs_SendsThePeerKubeconfig_SOV141 drives the REAL
// one-shot hand-over export.
func TestExportSecondaryKubeconfigs_SendsThePeerKubeconfig_SOV141(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("CATALYST_D16_EXPORT_FILE_WAIT_SECONDS", "5")
	t.Setenv("CATALYST_D16_EXPORT_FILE_POLL_SECONDS", "1")
	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()
	withChrootForwardClient(t, srv)
	sov141Seed(t, dir)

	h := newExportTestHandler(t)
	fqdn := "t41.omani.works"
	dep := makeDep(sov141DepID, fqdn, []string{"fsn1", "nbg1"})
	h.exportSecondaryKubeconfigsToChild(dep, fqdn, sov141DepID)

	sov141AssertPeerOnly(t, chroot)
}

// TestReforwardSecondaryKubeconfigs_SendsThePeerKubeconfig_SOV141 drives the
// REAL redelivery pass, which must send the same document as the export.
func TestReforwardSecondaryKubeconfigs_SendsThePeerKubeconfig_SOV141(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()
	withChrootForwardClient(t, srv)
	sov141Seed(t, dir)

	h := newExportTestHandler(t)
	dep := makeDep(sov141DepID, "t41.omani.works", []string{"fsn1", "nbg1"})
	h.reforwardSecondaryKubeconfigsToChild(dep)

	sov141AssertPeerOnly(t, chroot)
}
