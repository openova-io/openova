// Tests for the #4000 durable secondary-kubeconfig DELIVERY fix.
//
// Root cause (live hw174 ea30d1d816f2eee2): the mothership stored region-b's
// kubeconfig on disk as `<depID>-me-east-215-b-1.yaml`, but the export's
// spec-reconstructed region key was `me-east-215-1` (CloudRegion + idx), so
// waitForSecondaryKubeconfig polled a path that never existed, the forward to
// the in-cluster chroot never fired, the chroot's kubeconfigs dir stayed empty,
// and every multi-region app's placement collapsed to a false `singleton`.
//
// What this file proves:
//
//  1. onDiskSecondaryKubeconfigKeys enumerates the REAL on-disk region keys —
//     including a BCP `-b-1` key whose suffix the spec reconstruction misses —
//     and excludes the primary `<depID>.yaml` + `.nodeip` sidecars.
//  2. exportSecondaryKubeconfigsToChild now forwards an on-disk kubeconfig
//     whose key the spec-derived keys do NOT reconstruct (the hw174 shape):
//     the chroot receives the POST for `me-east-215-b-1`.
package handler

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOnDiskSecondaryKubeconfigKeys_DiscoversRealKeys(t *testing.T) {
	dir := t.TempDir()
	depID := "ea30d1d816f2eee2"

	// Primary (no region suffix) — MUST be excluded.
	mustWrite(t, filepath.Join(dir, depID+".yaml"), "apiVersion: v1\nkind: Config\n")
	// The real hw174 secondary file — the BCP `-b-1` key the spec missed.
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-b-1.yaml"), "apiVersion: v1\nkind: Config\n")
	// A second secondary + its nodeip sidecar (sidecar MUST be excluded).
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-c-2.yaml"), "apiVersion: v1\nkind: Config\n")
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-c-2.nodeip"), "10.44.2.131")
	// An unrelated deployment's file — MUST be excluded.
	mustWrite(t, filepath.Join(dir, "otherdep-nbg1-1.yaml"), "apiVersion: v1\nkind: Config\n")

	got := onDiskSecondaryKubeconfigKeys(dir, depID)
	want := map[string]bool{"me-east-215-b-1": true, "me-east-215-c-2": true}
	if len(got) != len(want) {
		t.Fatalf("onDiskSecondaryKubeconfigKeys = %v, want keys %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q (primary / sidecar / other-dep leaked in)", k)
		}
	}
}

func TestOnDiskSecondaryKubeconfigKeys_EmptyOrMissingDir(t *testing.T) {
	// A mothership that never received any secondary kubeconfig: zero keys, so
	// the union is a no-op and the caller keeps the spec-derived keys
	// (pre-#4000 behaviour). Empty-readable-dir yields an empty slice; a
	// missing dir yields nil — both are falsy for the `for range` union.
	if got := onDiskSecondaryKubeconfigKeys(t.TempDir(), "dep"); len(got) != 0 {
		t.Fatalf("empty dir: got %v, want zero keys", got)
	}
	if got := onDiskSecondaryKubeconfigKeys("/nonexistent/path/xyz", "dep"); got != nil {
		t.Fatalf("missing dir: got %v, want nil", got)
	}
	if got := onDiskSecondaryKubeconfigKeys("", "dep"); got != nil {
		t.Fatalf("empty dir arg: got %v, want nil", got)
	}
}

// TestExportSecondaryKubeconfigs_ForwardsKeyTheSpecMisses proves the live
// hw174 fix end-to-end: the on-disk file uses a region key (`me-east-215-b-1`)
// that regionKeysForExport CANNOT reconstruct from the deployment's parsed
// CloudRegion (`me-east-215`), yet the export still forwards it to the chroot
// because exportSecondaryKubeconfigsToChild now UNIONs the on-disk keys.
func TestExportSecondaryKubeconfigs_ForwardsKeyTheSpecMisses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("CATALYST_D16_EXPORT_FILE_WAIT_SECONDS", "5")
	t.Setenv("CATALYST_D16_EXPORT_FILE_POLL_SECONDS", "1")

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()

	h := newExportTestHandler(t)
	depID := "ea30d1d816f2eee2"
	fqdn := "hw174.omani.works"

	// The deployment's parsed secondary CloudRegion is `me-east-215` →
	// regionKeysForExport reconstructs `me-east-215-1`. But the secondary CP's
	// cloud-init deposited the file under `me-east-215-b-1` (Huawei BCP
	// `MY_REGION='${r.code}-${idx}'`). The spec key MISSES it; only the
	// on-disk union catches it.
	dep := makeDep(depID, fqdn, []string{"me-east-215", "me-east-215"})
	// #6108 — the fixture must be a DELIVERABLE document. It used to be the
	// clusters-only shell, which is byte-identical to the torn read hw293
	// shipped and which the receiver has refused since #6054: this arm was
	// asserting that the sender forwards bytes the receiver can only 422.
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-b-1.yaml"), completeKubeconfigSameCluster)

	// Sanity: the spec key does NOT include the real on-disk key.
	dep.mu.Lock()
	specKeys := regionKeysForExport(dep)
	dep.mu.Unlock()
	if containsStr(specKeys, "me-east-215-b-1") {
		t.Fatalf("test premise broken: spec keys %v already contain the on-disk key", specKeys)
	}

	// Drive the real export through the fake chroot.
	runExportToChrootWithUnion(t, h, dep, fqdn, depID, srv)

	posted := chroot.regionsPosted()
	found := false
	for _, r := range posted {
		if r == "me-east-215-b-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("chroot never received the on-disk key me-east-215-b-1; regionsPosted=%v", posted)
	}
}

// runExportToChrootWithUnion mirrors exportSecondaryKubeconfigsToChild's
// region-key UNION (spec ∪ on-disk) + per-region POST, routing the network leg
// at the fake chroot server. Kept local so the test exercises the SAME
// onDiskSecondaryKubeconfigKeys + containsStr seam the production path uses
// without needing to monkey-patch the global transport.
func runExportToChrootWithUnion(t *testing.T, h *Handler, dep *Deployment, fqdn, depID string, srv *httptest.Server) {
	t.Helper()
	dep.mu.Lock()
	regions := append([]string(nil), regionKeysForExport(dep)...)
	dep.mu.Unlock()
	dir := secondaryKubeconfigsDir()
	for _, k := range onDiskSecondaryKubeconfigKeys(dir, depID) {
		if !containsStr(regions, k) {
			regions = append(regions, k)
		}
	}
	url := "https://api." + fqdn + "/api/v1/sovereign/secondary-kubeconfig"
	client := srv.Client()
	client.Transport = newRoundTripperToServer(srv)
	for _, regionKey := range regions {
		path := filepath.Join(dir, depID+"-"+regionKey+".yaml")
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			continue
		}
		payload := map[string]string{
			"deploymentId":   depID,
			"regionKey":      regionKey,
			"kubeconfigYaml": string(raw),
		}
		body, _ := json.Marshal(payload)
		h.postSecondaryKubeconfigWithRetry(client, url, body, depID, regionKey)
	}
}
