// secondary_kubeconfig_torn_read_6108_test.go — the SENDER half of the hw293
// credential-less region-B kubeconfig.
//
// #6054 closed the receiver: `HandleSovereignSecondaryKubeconfig` now refuses a
// document that cannot produce a client, so an unusable one can no longer be
// PERSISTED through that door. Nothing stopped one being PRODUCED or SHIPPED.
// Three rungs carried the predicate #6054 removed, or the write that creates
// what it catches:
//
//	waitForSecondaryKubeconfig            `err == nil && len(raw) > 0`   → PR #6112
//	reforwardSecondaryKubeconfigsToChild  `err != nil || len(raw) == 0`  → here
//	the store itself                       plain os.WriteFile            → here
//
// `os.WriteFile` is O_WRONLY|O_CREATE|O_TRUNC followed by a write, so between
// the truncate and the end of the write the file on disk IS a strict prefix of
// the new document — and the readers of that path are a poller built to race it
// plus the k8scache rescan. That is the rung nobody else is holding, so it is
// proven here THROUGH the receiver rather than through the helper it calls.
//
// Measured on hw293 (dep a0077ba47e3720e5, region me-east-215-b-1): the chroot
// held 95 bytes ending mid-token on `  name: c`; the mothership's copy of the
// same region was 2959 bytes and complete. `k8scache: rescan — AddCluster
// failed … no configuration has been provided` repeated every 30s for 12h,
// still live at 2026-08-11T00:59:36Z on catalyst-api 0b0143b.
//
// Every arm below is RED against the pre-fix sender and green after. Each one
// is paired with a CONTROL that shares the suspect property — same code path,
// same fixture family, same call — so a green arm cannot be produced by a gate
// that simply refuses everything, nor by a race the harness is too slow to see.
//
// Refs #6108, #6015, #6054, #6112.

package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// Rung 2 — the redelivery loop must not ship a torn read.
// ---------------------------------------------------------------------------

// TestReforwardSecondaryKubeconfigs_RefusesTornRead_6108 drives the REAL
// level-triggered redelivery loop against a chroot that records what it is sent.
//
// Pre-fix the loop's only content check is `len(raw) == 0`, so the 95-byte stub
// is POSTed on every 5-minute pass forever. Post-#6054 the receiver answers 422
// each time, so the pair converges on shipping a document neither end can use
// while the log says only `child 4xx (giving up)` with a bare status number.
//
// The CONTROL is the same loop, same directory, same server, one region key
// apart: the region whose file is COMPLETE must still be delivered. A guard
// that refused both would pass the negative arm and break delivery.
func TestReforwardSecondaryKubeconfigs_RefusesTornRead_6108(t *testing.T) {
	depID := "a0077ba47e3720e5"
	fqdn := "hw293.omantel.biz"
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()
	withChrootForwardClient(t, srv)

	// Region B holds the measured torn read; region C holds a complete document.
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-b-1.yaml"), hw293StubKubeconfig)
	mustWrite(t, filepath.Join(dir, depID+"-me-east-215-c-2.yaml"), completeKubeconfigSameCluster)

	h := newExportTestHandler(t)
	dep := makeDep(depID, fqdn, []string{"me-east-215-a", "me-east-215-b", "me-east-215-c"})

	h.reforwardSecondaryKubeconfigsToChild(dep)

	posted := chroot.regionsPosted()
	if containsStr(posted, "me-east-215-b-1") {
		t.Fatalf("#6108: the redelivery loop shipped a %d-byte torn read the receiver can only 422 "+
			"(missing %v); regionsPosted=%v",
			len(hw293StubKubeconfig), secondaryKubeconfigDefects(hw293StubKubeconfig), posted)
	}
	if !containsStr(posted, "me-east-215-c-2") {
		t.Fatalf("control: the region with a COMPLETE kubeconfig was not delivered — the gate refuses everything; regionsPosted=%v", posted)
	}
}

// CONTROL: once the torn read is REPLACED by the complete document, the very
// next pass must deliver it. Pins that the refusal is level-triggered and not a
// latch — a region must be able to recover without a catalyst-api restart.
func TestReforwardSecondaryKubeconfigs_DeliversAfterTornReadIsReplaced_6108(t *testing.T) {
	depID := "a0077ba47e3720e5"
	fqdn := "hw293.omantel.biz"
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()
	withChrootForwardClient(t, srv)

	path := filepath.Join(dir, depID+"-me-east-215-b-1.yaml")
	mustWrite(t, path, hw293StubKubeconfig)

	h := newExportTestHandler(t)
	dep := makeDep(depID, fqdn, []string{"me-east-215-a", "me-east-215-b"})

	h.reforwardSecondaryKubeconfigsToChild(dep)
	if posted := chroot.regionsPosted(); len(posted) != 0 {
		t.Fatalf("#6108: pass 1 shipped the torn read; regionsPosted=%v", posted)
	}

	mustWrite(t, path, completeKubeconfigSameCluster)
	h.reforwardSecondaryKubeconfigsToChild(dep)
	if posted := chroot.regionsPosted(); !containsStr(posted, "me-east-215-b-1") {
		t.Fatalf("#6108: the refusal latched — a repaired file was still not delivered; regionsPosted=%v", posted)
	}
}

// ---------------------------------------------------------------------------
// Rung 3 — the store must not be able to PRODUCE a prefix.
// ---------------------------------------------------------------------------

// TestSecondaryKubeconfigStore_ConcurrentReaderNeverSeesAPrefix_6108 is the
// atomicity proof, driven through the REAL receiver — the site that wrote the
// hw293 file — not through the helper it calls. It states the property the
// poller depends on: a reader that spins on the destination path while a
// delivery is being persisted observes either the PREVIOUS complete document or
// the NEW one, never a prefix of either.
//
// Pre-fix HandleSovereignSecondaryKubeconfig persists with os.WriteFile, whose
// O_TRUNC makes every in-flight write observable as a strict prefix. Post-fix it
// goes through the atomic store (temp + fsync + rename); rename(2) within a
// directory is atomic, so a prefix cannot be observed at all.
//
// The payload is large enough that the write cannot complete inside a single
// scheduler slice; the reader samples continuously. Any observation that is a
// strict prefix of either document, or is neither, is a torn read.
func TestSecondaryKubeconfigStore_ConcurrentReaderNeverSeesAPrefix_6108(t *testing.T) {
	dir := t.TempDir()
	h := newCompletenessTestHandler(t, dir)
	path := filepath.Join(dir, "a0077ba47e3720e5-me-east-215-b-1.yaml")

	// Two complete, DISTINCT documents. Both are usable kubeconfigs, so the
	// only thing under test is whether a reader can catch the store mid-write.
	prev := bulkKubeconfig("prev", 1<<20)
	next := bulkKubeconfig("next", 1<<20)
	mustWrite(t, path, prev)

	var stop atomic.Bool
	torn := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			raw, err := os.ReadFile(path)
			if err != nil {
				// ENOENT is itself a torn observation: the destination must
				// never disappear while a complete document is already there.
				select {
				case torn <- "read error: " + err.Error():
				default:
				}
				return
			}
			s := string(raw)
			if s == prev || s == next {
				continue
			}
			select {
			case torn <- describeTornRead(s, prev, next):
			default:
			}
			return
		}
	}()

	for i := 0; i < 20; i++ {
		for _, doc := range []string{next, prev} {
			rec := postSecondaryKubeconfig(t, h, map[string]string{
				"deploymentId":   "a0077ba47e3720e5",
				"regionKey":      "me-east-215-b-1",
				"kubeconfigYaml": doc,
			})
			if rec.Code != http.StatusCreated {
				stop.Store(true)
				<-done
				t.Fatalf("delivery POST status = %d, want 201; body = %s", rec.Code, rec.Body.String())
			}
		}
	}
	stop.Store(true)
	<-done

	select {
	case what := <-torn:
		t.Fatalf("#6108: a concurrent reader observed a TORN document — the kubeconfig store is not atomic: %s", what)
	default:
	}
}

// CONTROL for rung 3, and the vacuity arm: the same reader/writer shape driven
// through the NON-atomic primitive the pre-fix sites used MUST tear. Without
// this, a green rung 3 could mean the payload was too small, the reader too
// slow, or the loop too short to catch anything — a test that cannot fail.
func TestSecondaryKubeconfigStore_NonAtomicWriteDoesTear_6108(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control-nonatomic.yaml")

	prev := bulkKubeconfig("prev", 1<<20)
	next := bulkKubeconfig("next", 1<<20)
	mustWrite(t, path, prev)

	var stop atomic.Bool
	torn := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			s := string(raw)
			if s == prev || s == next {
				continue
			}
			select {
			case torn <- struct{}{}:
			default:
			}
			return
		}
	}()

	saw := false
	for i := 0; i < 200 && !saw; i++ {
		_ = os.WriteFile(path, []byte(next), 0o600)
		_ = os.WriteFile(path, []byte(prev), 0o600)
		select {
		case <-torn:
			saw = true
		default:
		}
	}
	stop.Store(true)
	<-done
	select {
	case <-torn:
		saw = true
	default:
	}

	if !saw {
		t.Skip("control could not catch os.WriteFile mid-write on this host — rung 3 is unproven here, not passing")
	}
}

// bulkKubeconfig builds a COMPLETE kubeconfig of at least approxBytes, padded
// with a comment block so the two fixtures differ in content but not in shape.
// The padding is a YAML comment, so the document still parses and still
// produces a client — the reader arms above are about torn bytes, not validity.
//
// The server host is a closed loopback port on purpose: the receiver calls
// k8sCache.AddCluster on every accepted delivery, and this arm accepts twenty
// of them. Pointing at a routable public address makes each registration sit in
// a dial timeout, which turns a byte-level race test into a network test.
func bulkKubeconfig(tag string, approxBytes int) string {
	var b strings.Builder
	b.WriteString(strings.Replace(
		completeKubeconfigSameCluster,
		"server: https://212.72.24.6:6443",
		"server: https://127.0.0.1:1",
		1,
	))
	b.WriteString("# padding-" + tag + "\n")
	line := "# " + strings.Repeat(tag+"-", 30) + "\n"
	for b.Len() < approxBytes {
		b.WriteString(line)
	}
	// Trimmed, because the receiver persists strings.TrimSpace(body) — an
	// untrimmed fixture lands one byte shorter than it was sent and every
	// observation of it is a strict prefix of the fixture, which reads exactly
	// like a torn write. The arm must fail on tearing, not on trimming.
	return strings.TrimSpace(b.String())
}

// describeTornRead reports the observation in bytes without echoing document
// contents into CI output.
func describeTornRead(got, prev, next string) string {
	switch {
	case strings.HasPrefix(next, got):
		return "strict prefix of the incoming document"
	case strings.HasPrefix(prev, got):
		return "strict prefix of the previous document"
	default:
		return "neither document"
	}
}
