// secondary_kubeconfig_forward_client_race_6058_test.go — #6058.
//
// `secondaryKubeconfigForwardClient` is a plain package-level var. It is
// WRITTEN by withChrootForwardClient's t.Cleanup restore
// (secondary_kubeconfig_coverage_6015_test.go) and READ by
// reforwardSecondaryKubeconfigsToChild, which runs on the detached
// runSecondaryKubeconfigDelivery goroutine the #6015 tests spawn. Nothing
// synchronises the two.
//
// The #6015 tests signal that goroutine to stop (dep.Status = "wiped") but
// never observe it stopping, so the restore can land while the loop is
// mid-tick. That reds the required `test` gate intermittently — twice
// caught in CI on branches whose diffs touch neither participant:
//
//	PR #6053, run 31419094457:
//	  WARNING: DATA RACE
//	  Write ... withChrootForwardClient.func2()
//	      secondary_kubeconfig_coverage_6015_test.go:600
//	  Previous read ... reforwardSecondaryKubeconfigsToChild()
//	      deployment_handover_export.go:503
//	  --- FAIL: TestSecondaryKubeconfigDelivery_RunsOnFailedDeployment_6015
//	      race detected during execution of test
//
//	PR #6051, run 31425591597: the same, plus
//	  --- FAIL: TestMarkPhase1Done_FailedOutcomeStillStartsDelivery_6015
//
// The failure is timing-dependent, so it does not reproduce from the #6015
// tests alone — it needs the full package under -race and load. This guard
// makes it DETERMINISTIC by exercising the same write/read pair directly:
// it fails on every run under -race before the fix, and passes after.
//
// Fixing by "waiting longer" in the #6015 test was tried and reverted on
// #6053: each loop tick POSTs once per region through
// postSecondaryKubeconfigWithRetry, so a bounded wait turns a rare flake
// into a deterministic timeout. The seam itself has to be safe to read
// while another goroutine swaps it, which is what this guards.
package handler

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestSecondaryKubeconfigForwardClientSeam_IsRaceFree drives the exact
// write/read pair from the CI stack trace concurrently. Under -race this
// FAILS on the unsynchronised package var and PASSES once the seam is
// accessed through its mutex-guarded helpers.
func TestSecondaryKubeconfigForwardClientSeam_IsRaceFree(t *testing.T) {
	restore := swapSecondaryKubeconfigForwardClient(func() *http.Client {
		return &http.Client{Timeout: time.Second}
	})
	t.Cleanup(restore)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// The READER — stands in for reforwardSecondaryKubeconfigsToChild's
	// `client := secondaryKubeconfigForwardClient()` on the detached
	// delivery goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if c := currentSecondaryKubeconfigForwardClient(); c == nil {
				t.Error("forward-client seam resolved to a nil *http.Client")
				return
			}
		}
	}()

	// The WRITER — stands in for withChrootForwardClient's cleanup restore
	// firing while that goroutine is still mid-tick.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			undo := swapSecondaryKubeconfigForwardClient(func() *http.Client {
				return &http.Client{Timeout: 2 * time.Second}
			})
			undo()
		}
	}()

	// Let the writer finish, then stop the reader.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	time.Sleep(150 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("seam race guard did not converge")
	}
}

// TestSecondaryKubeconfigForwardClientSeam_SwapRestoresPrevious is the
// CONTROL: the swap helper must still behave like the plain assignment it
// replaces, or every #6015 test that relies on the seam silently stops
// binding to its httptest server and starts hitting the real network.
func TestSecondaryKubeconfigForwardClientSeam_SwapRestoresPrevious(t *testing.T) {
	marker := &http.Client{Timeout: 4321 * time.Millisecond}

	before := currentSecondaryKubeconfigForwardClient()
	restore := swapSecondaryKubeconfigForwardClient(func() *http.Client { return marker })

	if got := currentSecondaryKubeconfigForwardClient(); got != marker {
		t.Fatalf("swap did not take effect: the seam still resolves to %p, want the injected %p", got, marker)
	}

	restore()
	after := currentSecondaryKubeconfigForwardClient()
	if after == marker {
		t.Fatalf("restore did not undo the swap — the injected client is still live")
	}
	if after.Timeout != before.Timeout {
		t.Fatalf("restore returned a different client shape: timeout %v, want %v", after.Timeout, before.Timeout)
	}
}
