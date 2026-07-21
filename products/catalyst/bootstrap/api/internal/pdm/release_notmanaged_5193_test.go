// release_notmanaged_5193_test.go — #5193 (item 4) regression coverage.
//
// A wipe of a record whose SovereignDomainMode is "pool" but whose pool
// domain is NOT one OpenOva's pool-domain-manager actually manages (a BYO
// / customer-owned domain, or a Sovereign FQDN whose parent zone was
// never enrolled) makes PDM answer the release with 422 "pool domain <x>
// is not managed by OpenOva". There is no allocation row to free, so a
// 422 is the SAME terminal, non-retryable post-condition as a 404: the
// slot is already free.
//
// Pre-fix, Release mapped 422 to a generic `fmt.Errorf("pdm release
// status 422: …")`, which ReleaseWithRetry did NOT recognise as
// success — so it burned all MaxAttempts retries against a permanent
// error, then returned "retry exhausted". In the wipe handler that set
// pdmReleaseFailed=true, which SKIPS deleting the on-disk deployment
// record, stranding a non-managed-domain wipe as a partially-cleaned
// record forever. This file locks the fix: 422 → ErrNotManaged →
// treated as idempotent success with NO retry.
package pdm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestRelease_422_MapsToErrNotManaged proves the single-shot Release maps
// a 422 Unprocessable Entity to the typed ErrNotManaged sentinel (not a
// generic status error), so callers can branch on it with errors.Is.
func TestRelease_422_MapsToErrNotManaged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"pool domain omantel.biz is not managed by OpenOva"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.Release(context.Background(), "omantel.biz", "hw276")
	if !errors.Is(err, ErrNotManaged) {
		t.Fatalf("Release on a 422 must return ErrNotManaged, got %v", err)
	}
}

// TestReleaseWithRetry_422_IsSuccessNoRetry is the headline #5193 item-4
// assertion: a 422 (non-managed pool domain) is idempotent success and
// must NOT consume any retry attempt — exactly like a 404. Reverting the
// fix (dropping the ErrNotManaged case from either Release's status
// switch or ReleaseWithRetry's success guard) fails this test twice: the
// returned error becomes non-nil AND the call count jumps to MaxAttempts.
func TestReleaseWithRetry_422_IsSuccessNoRetry(t *testing.T) {
	var releases int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&releases, 1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"pool domain omantel.biz is not managed by OpenOva"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.ReleaseWithRetry(context.Background(), "omantel.biz", "hw276",
		CommitRetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("ReleaseWithRetry: a 422 non-managed domain must be idempotent success, got %v", err)
	}
	if got := atomic.LoadInt32(&releases); got != 1 {
		t.Errorf("releases=%d, want 1 (422 is terminal + non-retryable — it must return immediately, never burn the retry budget)", got)
	}
}
