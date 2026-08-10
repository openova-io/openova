// secondary_kubeconfig_forward_client.go — #6058.
//
// `secondaryKubeconfigForwardClient` (deployment_handover_export.go) is the
// test seam that lets the #6015 suite drive the REAL
// reforwardSecondaryKubeconfigsToChild / runSecondaryKubeconfigDelivery
// against an httptest server instead of re-implementing the production loop.
// Keeping that seam is right — a test that re-implements the loop cannot fail
// on a defect in the loop.
//
// What was wrong is that it was a PLAIN package var, written by one goroutine
// and read by another with nothing in between:
//
//	WRITE  withChrootForwardClient's t.Cleanup restore, on the test goroutine
//	READ   reforwardSecondaryKubeconfigsToChild's
//	       `client := secondaryKubeconfigForwardClient()`, on the detached
//	       runSecondaryKubeconfigDelivery goroutine the #6015 tests spawn
//
// The #6015 tests ask that goroutine to stop (dep.Status = "wiped") but never
// observe it stopping, so the restore can land mid-tick. Caught by the
// required `test` gate on two branches whose diffs touch neither participant
// (PR #6053 run 31419094457; PR #6051 run 31425591597, which also took
// TestMarkPhase1Done_FailedOutcomeStillStartsDelivery_6015 down with it).
//
// Waiting longer in the test was tried on #6053 and reverted: each tick POSTs
// once per region through postSecondaryKubeconfigWithRetry, so a bounded wait
// converts a rare flake into a deterministic timeout. The seam itself has to
// be safe to read while another goroutine swaps it — which is what these two
// helpers provide, at the cost of one uncontended RLock per delivery tick
// (the loop runs every 5 minutes in production).
//
// Every access to the var now goes through here.
package handler

import (
	"net/http"
	"sync"
)

// secondaryKubeconfigForwardClientMu guards secondaryKubeconfigForwardClient.
// RWMutex rather than atomic.Value because the seam holds a func value and
// tests swap it to a closure over their httptest server.
var secondaryKubeconfigForwardClientMu sync.RWMutex

// currentSecondaryKubeconfigForwardClient resolves the seam and builds a
// client. This is the ONLY read path; reforwardSecondaryKubeconfigsToChild
// calls it once per pass.
func currentSecondaryKubeconfigForwardClient() *http.Client {
	secondaryKubeconfigForwardClientMu.RLock()
	build := secondaryKubeconfigForwardClient
	secondaryKubeconfigForwardClientMu.RUnlock()
	return build()
}

// swapSecondaryKubeconfigForwardClient installs a new builder and returns the
// restore func. This is the ONLY write path — tests must use it rather than
// assigning the var, so the write is ordered against a concurrent read.
//
// The returned restore is safe to call from a t.Cleanup while the delivery
// goroutine is still running.
func swapSecondaryKubeconfigForwardClient(build func() *http.Client) func() {
	secondaryKubeconfigForwardClientMu.Lock()
	prev := secondaryKubeconfigForwardClient
	secondaryKubeconfigForwardClient = build
	secondaryKubeconfigForwardClientMu.Unlock()
	return func() {
		secondaryKubeconfigForwardClientMu.Lock()
		secondaryKubeconfigForwardClient = prev
		secondaryKubeconfigForwardClientMu.Unlock()
	}
}
