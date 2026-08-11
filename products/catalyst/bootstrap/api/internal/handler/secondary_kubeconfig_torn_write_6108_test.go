// secondary_kubeconfig_torn_write_6108_test.go — refusing to READ a torn
// document and refusing to CREATE one are different guarantees.
//
// #6112 closed the reader half: waitForSecondaryKubeconfig now waits for a
// document that passes the receiving end's own usability contract instead of
// returning the first non-empty bytes. Its godoc names the two ways an unusable
// document can be sitting at one of these paths when a reader fires — a
// truncated upload, or a TORN WRITE, "HandleSovereignSecondaryKubeconfig writes
// the same `<depID>-<regionKey>.yaml` names into this very directory with a
// plain os.WriteFile, which truncates before it writes" — and says plainly that
// its fix does not depend on which one produced the hw293 artefact.
//
// This file closes the second one at its source, and the one reader #6112 did
// not reach.
//
// WHY THE READER FIX IS NOT SUFFICIENT ON ITS OWN. os.WriteFile is O_TRUNC
// followed by a write, so between those two the file on disk IS a prefix of the
// new content. A guard inside catalyst-api's own pollers protects catalyst-api's
// own pollers. It does nothing for the readers this package does not own and
// cannot gate: LoadClustersFromDir at startup, buildRegionSlots, the placement
// resolver, orgConsoleTLSPoolRegions, and any operator or Job reading the PVC.
// Every one of them can still land inside the truncate window. Atomic
// replacement removes the window itself, so a torn document cannot be observed
// by anyone rather than being declined by two callers.
//
// THE MEASURED ARTEFACT, for the record this file is pinned to. hw293 (dep
// a0077ba47e3720e5) carried `a0077ba47e3720e5-me-east-215-b-1.yaml` at 95 bytes
// — `apiVersion`, `kind`, one `clusters[]` entry, then nothing, ending mid-token
// on `  name: c` with no trailing newline. The mothership's copy of the SAME
// region was 2959 bytes and carried all six kubeconfig top-level keys
// (`clusters`, `contexts`, `current-context`, `kind`, `preferences`, `users`).
// A complete source and a prefix at the destination.
//
// Downstream, measured read-only 2026-08-11T00:57Z: clientcmd.RESTConfigFromKubeConfig
// fails on the shell, orgConsoleTLSTargets puts me-east-215-b-1 in `unreached`,
// region B holds ZERO per-Org console listeners against region A's ten, and the
// shared console EIP round-robins a share of every customer TLS connection onto
// the region that cannot answer it.
//
// Refs #6108 · #6112 (the reader half) · #6054 (the receiver gate) · #5246.

package handler

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// completeKubeconfigOtherCluster is a SECOND complete document, distinguishable
// from completeKubeconfigSameCluster byte-for-byte, so the atomicity test can
// tell "the reader saw the old document" from "the reader saw the new one".
const completeKubeconfigOtherCluster = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://10.0.0.9:6443
  name: d
contexts:
- name: d
  context:
    cluster: d
    user: d
current-context: d
users:
- name: d
  user:
    token: fake-token-two
`

// TestSecondaryKubeconfigPersist_IsAtomicForAConcurrentReader_6108 states the
// torn-write property directly rather than trying to win a race.
//
// A reader that has the file OPEN when a rewrite lands is the deterministic
// stand-in for a reader that is mid-read when a rewrite lands: both hold a
// handle on the inode that was current at open time.
//
//   - O_TRUNC-in-place (os.WriteFile) mutates THAT inode, so the holder's next
//     read yields the new bytes, or a short read, or nothing — never a coherent
//     document.
//   - temp+rename swaps a DIFFERENT inode into the name, so the holder keeps
//     reading the complete previous document through to its end.
//
// The property asserted is the one that matters in production, and it is
// stronger than "our pollers decline bad documents": a reader concurrent with a
// write observes a COMPLETE document, whichever one it is, whether or not that
// reader knows to check.
func TestSecondaryKubeconfigPersist_IsAtomicForAConcurrentReader_6108(t *testing.T) {
	dir := t.TempDir()
	const clusterID = "a0077ba47e3720e5-me-east-215-b-1"

	path, err := persistSecondaryKubeconfig(dir, clusterID, completeKubeconfigSameCluster)
	if err != nil {
		t.Fatalf("seed persist failed: %v", err)
	}

	// The concurrent reader, holding the inode that is current right now.
	reader, err := os.Open(path)
	if err != nil {
		t.Fatalf("open for concurrent read: %v", err)
	}
	defer func() { _ = reader.Close() }()

	// The rewrite lands while that handle is open — the production shape when
	// the mothership re-delivers a region whose kubeconfig rotated, or when the
	// #4000 self-heal rewrites the server host under a running scan.
	if _, err := persistSecondaryKubeconfig(dir, clusterID, completeKubeconfigOtherCluster); err != nil {
		t.Fatalf("rewrite persist failed: %v", err)
	}

	buf, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read through the pre-opened handle: %v", err)
	}
	got := string(buf)

	if got != completeKubeconfigSameCluster {
		if got == completeKubeconfigOtherCluster {
			t.Fatal("#6108: the rewrite mutated the inode the concurrent reader holds — " +
				"os.WriteFile truncates in place, so a reader that is mid-read when a delivery " +
				"lands gets a PREFIX of the new document. That is the 95-byte shell measured on " +
				"hw293. Persist through writeFileAtomic0600 (temp+rename) instead.")
		}
		t.Fatalf("#6108: the concurrent reader observed neither complete document — it observed a TORN one.\n"+
			"got %d bytes: %q\nwant the complete previous document (%d bytes)",
			len(got), got, len(completeKubeconfigSameCluster))
	}

	// CONTROL — vacuity guard. The assertion above would also hold if persist
	// were a no-op, so prove the rewrite really landed on the NAME.
	fresh, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fresh read of the path: %v", err)
	}
	if string(fresh) != completeKubeconfigOtherCluster {
		t.Fatalf("CONTROL FAILED: the rewrite did not land on the path, so the assertion above proved nothing.\n"+
			"path holds %d bytes, want the second document (%d bytes)",
			len(fresh), len(completeKubeconfigOtherCluster))
	}
}

// TestSecondaryKubeconfigPersist_LeavesNoPhantomRegionOnDisk_6108 is the
// control that makes the atomicity fix admissible.
//
// Temp+rename puts a SECOND file in the kubeconfigs directory for the duration
// of the write. That directory is scanned BY NAME to decide which regions
// exist: onDiskSecondaryKubeconfigKeys and orgConsoleTLSPoolRegions both derive
// the region set from filenames. A temp file those scanners matched would
// invent a region with no cluster behind it — trading a torn read for a phantom
// region, which is the same class of defect pointed the other way, and which on
// this Sovereign would put a nonexistent region into the console ELB pool's
// expected set.
func TestSecondaryKubeconfigPersist_LeavesNoPhantomRegionOnDisk_6108(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	const depID = "a0077ba47e3720e5"

	mustWrite(t, filepath.Join(dir, depID+".yaml"), completeKubeconfigSameCluster)
	if _, err := persistSecondaryKubeconfig(dir, depID+"-me-east-215-b-1", completeKubeconfigSameCluster); err != nil {
		t.Fatalf("persist failed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var leftovers []string
	for _, e := range entries {
		n := e.Name()
		if n == depID+".yaml" || n == depID+"-me-east-215-b-1.yaml" {
			continue
		}
		leftovers = append(leftovers, n)
	}
	if len(leftovers) != 0 {
		t.Fatalf("#6108: persist left temp artefacts behind in the kubeconfigs dir: %v", leftovers)
	}

	keys := onDiskSecondaryKubeconfigKeys(dir, depID)
	if len(keys) != 1 || keys[0] != "me-east-215-b-1" {
		t.Fatalf("#6108: the region scan sees %v, want exactly [me-east-215-b-1] — "+
			"a temp file must never be counted as a region", keys)
	}
	if pool := orgConsoleTLSPoolRegions(depID); len(pool) != 1 {
		t.Fatalf("#6108: the console ELB pool region set is %v, want exactly one region", pool)
	}
}

// TestReforwardSecondaryKubeconfigs_DoesNotShipATornRead_6108 pins the SECOND
// sender — the #6015 level-triggered redelivery loop.
//
// #6112 gave waitForSecondaryKubeconfig the usability contract; this loop reads
// the same directory and POSTs to the same receiver and was left on
// `len(raw) > 0`. That split matters more than it looks: the poller runs ONCE
// per handover, while this loop runs for the lifetime of the Sovereign, so the
// unguarded path is the one that runs essentially always.
//
// Two regions on disk, one torn and one complete, so the assertion and its
// control run through the SAME loop, the SAME directory scan and the SAME POST
// path in a single pass. The complete region proves the loop still delivers;
// the torn one proves it no longer ships a shell. A loop that simply stopped
// forwarding — a worse defect than the one it replaces — fails the control.
func TestReforwardSecondaryKubeconfigs_DoesNotShipATornRead_6108(t *testing.T) {
	const depID = "a0077ba47e3720e5"
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()
	withChrootForwardClient(t, srv)

	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-b-1.yaml"), hw293StubKubeconfig)
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-c-1.yaml"), completeKubeconfigSameCluster)

	h := newExportTestHandler(t)
	dep := makeDep(depID, "hw293.omantel.biz",
		[]string{"me-east-215-a", "me-east-215-b", "me-east-215-c"})

	h.reforwardSecondaryKubeconfigsToChild(dep)

	posted := chroot.regionsPosted()
	for _, r := range posted {
		if r == "me-east-215-b-1" {
			t.Fatalf("#6108: the redelivery loop shipped the %d-byte torn document to the chroot. "+
				"The receiver's gate (#6054) 422s it, a 4xx ends the retry policy, and the region "+
				"stays credential-less while every on-disk reader still counts it delivered.\n"+
				"regionsPosted=%v", len(hw293StubKubeconfig), posted)
		}
	}

	// CONTROL — the loop must still deliver the region whose document IS usable,
	// with the right bytes. Same loop, same pass, same directory.
	var sawComplete bool
	for _, body := range chrootBodies(chroot) {
		if body["regionKey"] != "me-east-215-c-1" {
			continue
		}
		sawComplete = true
		if body["kubeconfigYaml"] != completeKubeconfigSameCluster {
			t.Fatalf("CONTROL FAILED: the complete region was delivered with the wrong bytes (%d)",
				len(body["kubeconfigYaml"]))
		}
	}
	if !sawComplete {
		t.Fatalf("CONTROL FAILED: the loop delivered NOTHING — the change stopped forwarding instead of "+
			"stopping torn forwarding, which is a worse defect than the one it replaces.\nregionsPosted=%v",
			posted)
	}
}

// chrootBodies exposes every payload the fake chroot received, so a test can
// assert on the BYTES delivered rather than only on which regions were named.
func chrootBodies(s *fakeChrootServer) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]string, 0, len(s.received))
	out = append(out, s.received...)
	return out
}
