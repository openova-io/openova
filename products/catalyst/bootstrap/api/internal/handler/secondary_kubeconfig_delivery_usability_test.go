// secondary_kubeconfig_delivery_usability_test.go — the DELIVERY leg must
// forward a kubeconfig that can produce a client, not the first non-empty
// bytes it happens to observe.
//
// THE MEASUREMENT (hw293, dep a0077ba47e3720e5, region me-east-215-b-1)
// ---------------------------------------------------------------------
// The mothership PVC holds a COMPLETE 2959-byte region-B kubeconfig. The only
// document ever POSTed to the Sovereign was a 95-byte credential-less shell —
// `apiVersion`, `kind: Config`, one `clusters[]` entry with a `server:` URL,
// and then nothing, ending mid-token on `  name: c` with no trailing newline.
// No contexts, no users, no CA data.
//
// Both facts are true at once because delivery reads the file ONCE, and reads
// it too early. waitForSecondaryKubeconfig's entire content contract was:
//
//	raw, err := os.ReadFile(path)
//	if err == nil && len(raw) > 0 { return raw, true }
//
// The secondary control plane's cloud-init PUTs its kubeconfig to the
// mothership, which persists it atomically (writeFileAtomic0600). A first PUT
// carrying a partial document therefore lands as a COMPLETE, atomic file of 95
// bytes — atomicity guarantees the reader never sees a torn write, it does not
// guarantee the bytes describe a cluster. The export goroutine polls, sees
// non-empty, and forwards. When the retry PUT later replaces the file with the
// full 2959-byte document, nothing re-reads it: the export is one-shot per
// handover, and postSecondaryKubeconfigWithRetry retries the TRANSPORT with
// the same captured body — it never re-reads the FILE. So both of the
// Sovereign's 422s (#6054) carried the identical stale stub.
//
// The downstream cost is region B having no credential at all: the
// organization-controller reaches a secondary region through exactly that
// kubeconfig, so the per-Org `console-https-<slug>` / `console-http-<slug>`
// pair is never written there, while one shared console EIP round-robins both
// regions' envoy — a share of every customer TLS connection to
// `console.<slug>.<parent>` reaches a region with no listener for that SNI and
// resets.
//
// WHAT IS ASSERTED HERE
// ---------------------
// Case 1 is the defect. The other three are controls, each answering the
// opposite way, so the fix cannot pass by having simply made the wait stricter
// for everyone:
//
//   - CONTROL A (vacuity): a usable kubeconfig present from the start returns
//     promptly and unchanged. A gate that cannot pass is as useless as one
//     that cannot fail.
//   - CONTROL B (per-region): one region's unusable document must not hold up
//     or corrupt another's. A fix that lands only for the first region
//     reproduces the failure on the other side.
//   - CONTROL C (not byte length): a document 9x LARGER than the stub but
//     still credential-less is refused, while a SMALLER complete one is
//     accepted. The constraint is usability; size is not evidence.
//
// Refs #6015 #6054 #6104.
package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The stub and its control are the measured artefacts already pinned by the
// #6054 completeness tests in this package — hw293StubKubeconfig (the 95-byte
// document lifted off the chroot PVC, trailing newline deliberately absent)
// and completeKubeconfigSameCluster (the same cluster block plus only the
// three sections the stub lacks). Reusing them keeps ONE copy of the evidence
// and means the delivery leg and the persistence gate are proven against
// byte-identical inputs.
const completeRegionKubeconfig = completeKubeconfigSameCluster

// paddedCredentiallessKubeconfig is far LARGER than the stub and just as
// unusable — the control that stops any future "looks big enough" heuristic.
var paddedCredentiallessKubeconfig = "apiVersion: v1\nkind: Config\n" +
	strings.Repeat("# padding so this document dwarfs the 95-byte stub\n", 12) +
	"clusters:\n- cluster:\n    server: https://10.0.0.1:6443\n  name: r\n"

// writeKubeconfigAtomic mirrors the mothership's own writeFileAtomic0600: the
// reader never observes a torn write, so every fixture below is a COMPLETE
// file whose CONTENT is the only variable.
func writeKubeconfigAtomic(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp kubeconfig: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename kubeconfig into place: %v", err)
	}
}

// TestWaitForSecondaryKubeconfig_ForwardsUsableNotMerelyPresent_6015 is the
// hw293 reproduction: the stub lands first, the complete document replaces it
// shortly after, and delivery must carry the one that can build a client.
func TestWaitForSecondaryKubeconfig_ForwardsUsableNotMerelyPresent_6015(t *testing.T) {
	h := newExportTestHandler(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a0077ba47e3720e5-me-east-215-b-1.yaml")

	// The first PUT: a credential-less shell, atomically complete on disk.
	writeKubeconfigAtomic(t, path, hw293StubKubeconfig)

	// The retry PUT the mothership actually received, landing while the
	// export is polling — the 2959-byte document in the live measurement.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		writeKubeconfigAtomic(t, path, completeRegionKubeconfig)
	}()
	defer func() { <-done }()

	raw, ok := h.waitForSecondaryKubeconfig(path, 5*time.Second, 10*time.Millisecond, "a0077ba47e3720e5", "me-east-215-b-1")
	if !ok {
		t.Fatalf("delivery gave up while a usable kubeconfig was landing")
	}
	if defects := secondaryKubeconfigDefects(string(raw)); len(defects) > 0 {
		t.Fatalf("delivery forwarded a %d-byte document that cannot produce a client (missing: %s) — "+
			"the wait returned on the FIRST non-empty read, so the complete kubeconfig that replaced it "+
			"milliseconds later was never read. The export is one-shot, so region B stays credential-less "+
			"and its per-Org console listeners are never written.",
			len(raw), strings.Join(defects, ","))
	}
}

// TestWaitForSecondaryKubeconfig_UsableReturnsPromptly_6015 is CONTROL A, the
// vacuity arm: the gate must still PASS, and fast, on a healthy delivery.
func TestWaitForSecondaryKubeconfig_UsableReturnsPromptly_6015(t *testing.T) {
	h := newExportTestHandler(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "dep1-region-b.yaml")
	writeKubeconfigAtomic(t, path, completeRegionKubeconfig)

	start := time.Now()
	raw, ok := h.waitForSecondaryKubeconfig(path, 5*time.Second, 50*time.Millisecond, "dep1", "region-b")
	elapsed := time.Since(start)

	if !ok {
		t.Fatalf("a complete kubeconfig was not accepted — the gate cannot pass, which is as broken as one that cannot fail")
	}
	if string(raw) != completeRegionKubeconfig {
		t.Fatalf("delivery altered the kubeconfig bytes it forwarded")
	}
	if elapsed > time.Second {
		t.Fatalf("a healthy delivery waited %s; the gate must not add latency to the common path", elapsed)
	}
}

// TestWaitForSecondaryKubeconfig_IsPerRegion_6015 is CONTROL B. The export
// fans out one goroutine per region; a region whose document never becomes
// usable must fail on its OWN terms without touching its peer.
func TestWaitForSecondaryKubeconfig_IsPerRegion_6015(t *testing.T) {
	h := newExportTestHandler(t)
	dir := t.TempDir()

	goodPath := filepath.Join(dir, "dep1-region-a.yaml")
	badPath := filepath.Join(dir, "dep1-region-b.yaml")
	writeKubeconfigAtomic(t, goodPath, completeRegionKubeconfig)
	writeKubeconfigAtomic(t, badPath, hw293StubKubeconfig)

	goodRaw, goodOK := h.waitForSecondaryKubeconfig(goodPath, 300*time.Millisecond, 10*time.Millisecond, "dep1", "region-a")
	if !goodOK || string(goodRaw) != completeRegionKubeconfig {
		t.Fatalf("the healthy region was held back by its peer's unusable document (ok=%v)", goodOK)
	}

	badRaw, badOK := h.waitForSecondaryKubeconfig(badPath, 300*time.Millisecond, 10*time.Millisecond, "dep1", "region-b")
	if badOK {
		t.Fatalf("a permanently credential-less region reported delivery success with %d bytes — "+
			"forwarding it writes an unusable document over the region's slot", len(badRaw))
	}
	if badRaw != nil {
		t.Fatalf("a refused delivery must hand back no bytes at all, got %d", len(badRaw))
	}
}

// TestWaitForSecondaryKubeconfig_RejectsOnUsabilityNotLength_6015 is CONTROL C.
// The larger document loses and the smaller one wins, so no future reader can
// mistake this gate for a size threshold.
func TestWaitForSecondaryKubeconfig_RejectsOnUsabilityNotLength_6015(t *testing.T) {
	if len(paddedCredentiallessKubeconfig) <= len(completeRegionKubeconfig) {
		t.Fatalf("fixture precondition: the credential-less document (%d bytes) must be LARGER than the complete one (%d bytes)",
			len(paddedCredentiallessKubeconfig), len(completeRegionKubeconfig))
	}

	h := newExportTestHandler(t)
	dir := t.TempDir()

	bigPath := filepath.Join(dir, "dep1-region-big.yaml")
	writeKubeconfigAtomic(t, bigPath, paddedCredentiallessKubeconfig)
	if _, ok := h.waitForSecondaryKubeconfig(bigPath, 200*time.Millisecond, 10*time.Millisecond, "dep1", "region-big"); ok {
		t.Fatalf("a %d-byte credential-less document was accepted — the gate is keying on size, not on whether the bytes build a client",
			len(paddedCredentiallessKubeconfig))
	}

	smallPath := filepath.Join(dir, "dep1-region-small.yaml")
	writeKubeconfigAtomic(t, smallPath, completeRegionKubeconfig)
	if _, ok := h.waitForSecondaryKubeconfig(smallPath, 200*time.Millisecond, 10*time.Millisecond, "dep1", "region-small"); !ok {
		t.Fatalf("the smaller COMPLETE kubeconfig (%d bytes) was refused", len(completeRegionKubeconfig))
	}
}
