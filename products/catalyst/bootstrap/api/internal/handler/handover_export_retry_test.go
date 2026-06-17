// Tests for the shared handover-export retry policy (#3747).
//
// ROOT CAUSE these tests pin: before #3747 every handover export had its own
// retry loop with a FIXED 5-minute budget. When the Sovereign's `api.<fqdn>`
// backend took longer than 5m to become reachable (gateway programming + LE
// cert + pod readiness + LB health-check), the exports exhausted the budget
// and gave up PERMANENTLY — the tofu-archive seal that arms the cutover never
// landed, so NS5/Pillar-5 could never be walked zero-touch.
//
// What this file proves about postHandoverExportWithRetry:
//
//  1. A connection-level error (backend not yet reachable) is RETRIED, and
//     the call survives PAST the historical 5-minute cliff — i.e. the budget
//     is honoured from the env override, not hard-coded to 5m. A short
//     override budget that the backend out-waits returns GaveUpBudget (the
//     give-up path), NOT a premature give-up while the env budget remains.
//  2. A 4xx is given up on IMMEDIATELY (a genuine payload/auth defect that
//     retrying can never fix) — no retry storm.
//  3. A 5xx is retried then succeeds when the backend flips to 200.
//  4. The success path returns Succeeded on the first hit.
package handler

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// countingDialClient returns an *http.Client whose dials always fail with a
// connection error, plus a counter of how many dial attempts were made. This
// simulates an `api.<fqdn>` backend that is not yet reachable.
func countingDialClient(dials *atomic.Int32) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				dials.Add(1)
				return nil, &net.OpError{Op: "dial", Err: errConnRefusedSentinel{}}
			},
		},
	}
}

// errConnRefusedSentinel is a minimal net error used by countingDialClient.
type errConnRefusedSentinel struct{}

func (errConnRefusedSentinel) Error() string   { return "connection refused (test)" }
func (errConnRefusedSentinel) Timeout() bool   { return false }
func (errConnRefusedSentinel) Temporary() bool { return true }

// TestHandoverExportRetry_ConnErrorRespectsBudgetNotFixed5Min is the core
// #3747 regression: the retry must keep going on connection errors until the
// CONFIGURED budget expires, and return GaveUpBudget (not give up early at a
// hard-coded 5m). We set a short budget so the test is fast, and assert the
// loop retried (>1 dial) and ended on the budget path.
func TestHandoverExportRetry_ConnErrorRespectsBudgetNotFixed5Min(t *testing.T) {
	t.Setenv("CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS", "3")
	t.Setenv("CATALYST_HANDOVER_EXPORT_MAX_BACKOFF_SECONDS", "1")

	var dials atomic.Int32
	client := countingDialClient(&dials)

	var seenConnError atomic.Bool
	start := time.Now()
	outcome := postHandoverExportWithRetry(client, "https://api.unreachable.example/x", []byte(`{}`),
		func(_ int, kind string, _ int, _ error) {
			if kind == "conn-error" {
				seenConnError.Store(true)
			}
		})
	elapsed := time.Since(start)

	if outcome != handoverExportGaveUpBudget {
		t.Fatalf("expected GaveUpBudget on a never-reachable backend, got %v", outcome)
	}
	if !seenConnError.Load() {
		t.Fatalf("expected at least one conn-error attempt to be reported")
	}
	if dials.Load() < 2 {
		t.Fatalf("expected the retry to make >1 dial attempt against the unreachable backend, got %d", dials.Load())
	}
	// The loop must run for ~the budget (3s), proving the budget is honoured
	// from the env override and NOT cut short. (It also proves we are not
	// using the old hard-coded 5m, which would make this test hang.)
	if elapsed < 2*time.Second {
		t.Fatalf("retry loop returned in %s — shorter than the 3s budget, budget not honoured", elapsed)
	}
}

// TestHandoverExportRetry_GivesUpImmediatelyOn4xx proves a genuine defect
// (4xx) is not retried.
func TestHandoverExportRetry_GivesUpImmediatelyOn4xx(t *testing.T) {
	t.Setenv("CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS", "30")

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second, Transport: newRoundTripperToServer(srv)}
	start := time.Now()
	outcome := postHandoverExportWithRetry(client, "https://api.x.example/x", []byte(`{}`), nil)
	elapsed := time.Since(start)

	if outcome != handoverExportGaveUp4xx {
		t.Fatalf("expected GaveUp4xx on a 400, got %v", outcome)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected exactly 1 hit on a 4xx (no retry storm), got %d", hits.Load())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("4xx give-up took %s — should be immediate", elapsed)
	}
}

// TestHandoverExportRetry_Retries5xxThenSucceeds proves a 5xx (backend
// reachable but not ready — e.g. OpenBao token still wiring) is retried until
// the backend flips to 200.
func TestHandoverExportRetry_Retries5xxThenSucceeds(t *testing.T) {
	t.Setenv("CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS", "30")
	t.Setenv("CATALYST_HANDOVER_EXPORT_MAX_BACKOFF_SECONDS", "1")

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// First 2 hits → 503, then 200.
		if hits.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second, Transport: newRoundTripperToServer(srv)}
	outcome := postHandoverExportWithRetry(client, "https://api.x.example/x", []byte(`{}`), nil)

	if outcome != handoverExportSucceeded {
		t.Fatalf("expected Succeeded after the backend flipped 503→200, got %v", outcome)
	}
	if hits.Load() < 3 {
		t.Fatalf("expected ≥3 hits (2×503 then 200), got %d", hits.Load())
	}
}

// TestHandoverExportRetry_SucceedsFirstTry proves the happy path returns
// Succeeded with a single hit (no retry storm).
func TestHandoverExportRetry_SucceedsFirstTry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second, Transport: newRoundTripperToServer(srv)}
	outcome := postHandoverExportWithRetry(client, "https://api.x.example/x", []byte(`{}`), nil)

	if outcome != handoverExportSucceeded {
		t.Fatalf("expected Succeeded on first try, got %v", outcome)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected exactly 1 hit on success, got %d", hits.Load())
	}
}

// TestHandoverExportBudget_DefaultsTo20Min proves the default budget is the
// raised 20m (was 5m) when no override is set, and that the override parses.
func TestHandoverExportBudget_DefaultsTo20Min(t *testing.T) {
	t.Setenv("CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS", "")
	if got := handoverExportBudget(); got != 20*time.Minute {
		t.Fatalf("default handover export budget = %s, want 20m (#3747)", got)
	}
	t.Setenv("CATALYST_HANDOVER_EXPORT_BUDGET_SECONDS", "90")
	if got := handoverExportBudget(); got != 90*time.Second {
		t.Fatalf("override budget = %s, want 90s", got)
	}
}
