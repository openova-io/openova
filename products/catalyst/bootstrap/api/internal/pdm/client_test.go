// Tests for the PDM client retry primitives. These cover the
// regressions caught live on otech41+ where Phase-0 ran longer than
// PDM's 10-minute reservation TTL and Commit returned 404 ("pool
// allocation not found"), causing console.<sub>.<pool> to never
// resolve until an operator hand-seeded zone records.
//
// CommitWithRetry must:
//
//   - Treat 404 / 410 / 403 as "Reserve again, then retry Commit"
//     (TTL elapsed or token rotated).
//   - Treat 5xx / network errors as transient — exponential backoff,
//     up to MaxAttempts.
//   - Surface the final error verbatim on exhaustion so the caller
//     can populate Deployment.Error with a human-actionable message.
//   - Honour ctx cancellation between backoff sleeps.
package pdm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestCommitWithRetry_404_ThenSuccess models the canonical otech41+
// failure mode: first /commit returns 404 because the reservation
// row was swept (TTL elapsed during Phase-0 tofu apply); the retry
// loop must re-Reserve, get a fresh token, and Commit successfully
// on the second attempt — without operator intervention.
func TestCommitWithRetry_404_ThenSuccess(t *testing.T) {
	var commits int32
	var reserves int32
	var lastCommitToken string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commit"):
			n := atomic.AddInt32(&commits, 1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			lastCommitToken = body["reservationToken"]
			if n == 1 {
				// First attempt — TTL has expired, sweeper deleted
				// the row. PDM returns 404.
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"not-found"}`))
				return
			}
			// Subsequent attempts succeed.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"state":"active"}`))
		case strings.HasSuffix(r.URL.Path, "/reserve"):
			atomic.AddInt32(&reserves, 1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Reservation{
				PoolDomain:       "omani.works",
				Subdomain:        "otech-test",
				State:            "reserved",
				ReservationToken: "fresh-token-after-rereserve",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	var newTokenSeen string

	err := c.CommitWithRetry(
		context.Background(),
		"omani.works",
		CommitInput{
			Subdomain:        "otech-test",
			ReservationToken: "stale-token-from-original-reserve",
			SovereignFQDN:    "otech-test.omani.works",
			LoadBalancerIP:   "1.2.3.4",
		},
		func(ctx context.Context) (*Reservation, error) {
			return c.Reserve(ctx, "omani.works", "otech-test", "test")
		},
		func(token string) {
			newTokenSeen = token
		},
		CommitRetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	)
	if err != nil {
		t.Fatalf("CommitWithRetry: %v", err)
	}
	if got := atomic.LoadInt32(&commits); got != 2 {
		t.Errorf("commits=%d, want 2 (1 fail + 1 success)", got)
	}
	if got := atomic.LoadInt32(&reserves); got != 1 {
		t.Errorf("reserves=%d, want 1 (single re-Reserve after 404)", got)
	}
	if newTokenSeen != "fresh-token-after-rereserve" {
		t.Errorf("onRereserve callback received token=%q, want fresh-token-after-rereserve", newTokenSeen)
	}
	if lastCommitToken != "fresh-token-after-rereserve" {
		t.Errorf("second Commit sent token=%q, want fresh-token-after-rereserve (the retry must use the NEW token)", lastCommitToken)
	}
}

// TestCommitWithRetry_410Gone_TriggersRereserve covers the alternate
// PDM failure mode where the row is still present but expires_at has
// elapsed before the sweeper ran — PDM returns 410 Gone instead of
// 404. CommitWithRetry must treat it identically to 404.
func TestCommitWithRetry_410Gone_TriggersRereserve(t *testing.T) {
	var commits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commit"):
			if atomic.AddInt32(&commits, 1) == 1 {
				w.WriteHeader(http.StatusGone)
				return
			}
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/reserve"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Reservation{ReservationToken: "fresh"})
		}
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	called := false
	err := c.CommitWithRetry(
		context.Background(),
		"omani.works",
		CommitInput{Subdomain: "otech", ReservationToken: "stale", LoadBalancerIP: "1.1.1.1"},
		func(ctx context.Context) (*Reservation, error) { called = true; return c.Reserve(ctx, "omani.works", "otech", "x") },
		nil,
		CommitRetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	)
	if err != nil {
		t.Fatalf("CommitWithRetry: %v", err)
	}
	if !called {
		t.Errorf("410 Gone should trigger re-Reserve")
	}
}

// TestCommitWithRetry_5xx_BackoffThenExhaustion covers the transient
// PDM-down case: every attempt returns 500 and the retry loop must
// exhaust MaxAttempts before returning a wrapped error containing
// "retry exhausted" so the caller's dep.Error message is actionable.
func TestCommitWithRetry_5xx_BackoffThenExhaustion(t *testing.T) {
	var commits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&commits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"db down"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.CommitWithRetry(
		context.Background(),
		"omani.works",
		CommitInput{Subdomain: "otech", ReservationToken: "ok", LoadBalancerIP: "1.2.3.4"},
		nil, // no reserve closure — 5xx never triggers re-Reserve
		nil,
		CommitRetryConfig{MaxAttempts: 4, InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond},
	)
	if err == nil {
		t.Fatal("CommitWithRetry should fail on persistent 5xx")
	}
	if !strings.Contains(err.Error(), "retry exhausted") {
		t.Errorf("err=%q should contain 'retry exhausted'", err.Error())
	}
	if !strings.Contains(err.Error(), "4 attempts") {
		t.Errorf("err=%q should mention attempt count (4)", err.Error())
	}
	if got := atomic.LoadInt32(&commits); got != 4 {
		t.Errorf("commits=%d, want 4 (MaxAttempts)", got)
	}
}

// TestCommitWithRetry_5xx_RecoversBeforeExhaustion verifies that the
// loop returns success the moment PDM recovers — it does not over-
// retry once it sees a 200.
func TestCommitWithRetry_5xx_RecoversBeforeExhaustion(t *testing.T) {
	var commits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&commits, 1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.CommitWithRetry(
		context.Background(),
		"omani.works",
		CommitInput{Subdomain: "otech", ReservationToken: "ok", LoadBalancerIP: "1.2.3.4"},
		nil,
		nil,
		CommitRetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	)
	if err != nil {
		t.Fatalf("CommitWithRetry: %v", err)
	}
	if got := atomic.LoadInt32(&commits); got != 3 {
		t.Errorf("commits=%d, want 3 (2 5xx + 1 success, no over-retry)", got)
	}
}

// TestCommitWithRetry_403_TokenMismatch_TriggersRereserve covers the
// case where a parallel /reserve replaced the row with a different
// token between the original Reserve and the Commit. Same recovery
// (Reserve again) as 404/410.
func TestCommitWithRetry_403_TokenMismatch_TriggersRereserve(t *testing.T) {
	var commits int32
	var reserves int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commit"):
			if atomic.AddInt32(&commits, 1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/reserve"):
			atomic.AddInt32(&reserves, 1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Reservation{ReservationToken: "fresh"})
		}
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.CommitWithRetry(
		context.Background(),
		"omani.works",
		CommitInput{Subdomain: "otech", ReservationToken: "stale", LoadBalancerIP: "1.1.1.1"},
		func(ctx context.Context) (*Reservation, error) {
			return c.Reserve(ctx, "omani.works", "otech", "x")
		},
		nil,
		CommitRetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	)
	if err != nil {
		t.Fatalf("CommitWithRetry: %v", err)
	}
	if got := atomic.LoadInt32(&reserves); got != 1 {
		t.Errorf("reserves=%d, want 1 (re-Reserve after 403)", got)
	}
}

// TestCommitWithRetry_ContextCancellation_StopsBackoff verifies that
// a cancelled context unblocks the backoff sleep and returns
// immediately, instead of waiting MaxBackoff before noticing.
func TestCommitWithRetry_ContextCancellation_StopsBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := c.CommitWithRetry(
		ctx,
		"omani.works",
		CommitInput{Subdomain: "otech", ReservationToken: "ok"},
		nil,
		nil,
		// 1s/2s/4s backoff would normally take >= 7s — the cancel
		// must unblock far sooner.
		CommitRetryConfig{MaxAttempts: 5, InitialBackoff: time.Second, MaxBackoff: 4 * time.Second},
	)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancel error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("CommitWithRetry honoured backoff after cancel: elapsed=%v (want < 500ms)", elapsed)
	}
}

// TestCommitWithRetry_NoReserveClosure_404Fails ensures that a caller
// passing nil reserve closure does not silently retry forever — a
// 404 with no recovery primitive is a programming error and surfaces
// as a clear error message.
func TestCommitWithRetry_NoReserveClosure_404Fails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.CommitWithRetry(
		context.Background(),
		"omani.works",
		CommitInput{Subdomain: "otech", ReservationToken: "ok"},
		nil, // <- the bug: caller forgot to pass a reserve closure
		nil,
		CommitRetryConfig{MaxAttempts: 3},
	)
	if err == nil {
		t.Fatal("expected error when reserve closure is nil and PDM returns 404")
	}
	if !strings.Contains(err.Error(), "no reserve closure provided") {
		t.Errorf("err=%q should mention missing reserve closure", err.Error())
	}
}

// TestClient_BasicAuth_AppliedFromEnv verifies the PDM client decorates
// every outbound HTTP request with BasicAuth credentials read from
// CATALYST_PDM_BASIC_AUTH_USER / _PASS — the same env-var seam that
// pdmFlipNS uses in handler/parent_domains.go. Catalyst-Zero (contabo)
// proves the issue: POOL_DOMAIN_MANAGER_URL=https://pool.openova.io is
// gated by Traefik basicAuth Middleware, so unauthenticated POSTs to
// /api/v1/pool/{domain}/reserve return 401 — surfacing as
// "pdm-unavailable: pdm reserve status 401: 401 Unauthorized" in the
// catalyst-api deployment-create handler. Without this fix, fresh
// otechN provisions are blocked at the wizard's first PDM hop.
//
// The test sets the env vars, hits a fake PDM that asserts the
// Authorization header arrived, and asserts every Client method
// (Check, Reserve, Commit, Release) propagates the credentials.
func TestClient_BasicAuth_AppliedFromEnv(t *testing.T) {
	t.Setenv("CATALYST_PDM_BASIC_AUTH_USER", "operator")
	t.Setenv("CATALYST_PDM_BASIC_AUTH_PASS", "supersecret")

	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		// Reply with whichever shape matches the path so the call
		// completes happily and we exercise the success branch.
		switch {
		case strings.HasSuffix(r.URL.Path, "/check"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"available":true,"fqdn":"otech.omani.works"}`))
		case strings.HasSuffix(r.URL.Path, "/reserve"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"poolDomain":"omani.works","subdomain":"otech","state":"reserved","reservedAt":"now","expiresAt":"later","reservationToken":"tok","createdBy":"test"}`))
		case strings.HasSuffix(r.URL.Path, "/commit"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"committed":true}`))
		case strings.HasSuffix(r.URL.Path, "/release"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"released":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)

	// Every method must carry the same Authorization header.
	cases := []struct {
		name string
		fn   func() error
	}{
		{
			name: "Check",
			fn: func() error {
				_, err := c.Check(context.Background(), "omani.works", "otech")
				return err
			},
		},
		{
			name: "Reserve",
			fn: func() error {
				_, err := c.Reserve(context.Background(), "omani.works", "otech", "test")
				return err
			},
		},
		{
			name: "Commit",
			fn: func() error {
				return c.Commit(context.Background(), "omani.works", CommitInput{
					Subdomain:        "otech",
					ReservationToken: "tok",
					SovereignFQDN:    "otech.omani.works",
					LoadBalancerIP:   "1.2.3.4",
				})
			},
		},
		{
			name: "Release",
			fn: func() error {
				return c.Release(context.Background(), "omani.works", "otech")
			},
		},
	}

	const wantAuth = "Basic b3BlcmF0b3I6c3VwZXJzZWNyZXQ=" // base64("operator:supersecret")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seenAuth = ""
			if err := tc.fn(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if seenAuth != wantAuth {
				t.Errorf("%s: Authorization header = %q, want %q",
					tc.name, seenAuth, wantAuth)
			}
		})
	}
}

// TestClient_BasicAuth_OmittedWhenEnvUnset verifies the client does
// NOT add Authorization when neither env var is set — preserving the
// in-cluster Service path where catalyst-api dials the PDM Service
// directly and Traefik basicAuth is not in front of the request.
func TestClient_BasicAuth_OmittedWhenEnvUnset(t *testing.T) {
	t.Setenv("CATALYST_PDM_BASIC_AUTH_USER", "")
	t.Setenv("CATALYST_PDM_BASIC_AUTH_PASS", "")

	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"available":true}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	if _, err := c.Check(context.Background(), "omani.works", "otech"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if seenAuth != "" {
		t.Errorf("expected no Authorization header, got %q", seenAuth)
	}
}

// ── ReleaseWithRetry (Refs #3728) ───────────────────────────────────────
//
// The wipe path's pool-subdomain release was best-effort and non-retried;
// a single transient PDM failure orphaned the `active` allocation row,
// which never self-heals (the sweeper only reclaims expired *reserved*
// rows) → permanent 409 on re-fire of the same subdomain. ReleaseWithRetry
// gives the decommission path the same resilience Commit already has.

// TestReleaseWithRetry_5xx_ThenSuccess proves a transient PDM 5xx on the
// first DELETE /release does NOT strand the subdomain — the loop backs off
// and the second attempt frees the slot. This is the exact hw155/hw156
// failure window: a PDM blip during wipe left the row, forcing a fresh
// subdomain each re-fire.
func TestReleaseWithRetry_5xx_ThenSuccess(t *testing.T) {
	var releases int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&releases, 1)
		if !strings.HasSuffix(r.URL.Path, "/release") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"db down"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"freed":{"state":"active"}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.ReleaseWithRetry(context.Background(), "omani.works", "hw155",
		CommitRetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("ReleaseWithRetry should recover after a transient 5xx: %v", err)
	}
	if got := atomic.LoadInt32(&releases); got != 2 {
		t.Errorf("releases=%d, want 2 (1 5xx + 1 success)", got)
	}
}

// TestReleaseWithRetry_404_IsSuccess proves a 404 (row already gone) is
// treated as success on the FIRST attempt — release is idempotent, and a
// freed slot is exactly the post-condition the caller wants. It must NOT
// burn the whole retry budget on an already-released row.
func TestReleaseWithRetry_404_IsSuccess(t *testing.T) {
	var releases int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&releases, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not-found"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.ReleaseWithRetry(context.Background(), "omani.works", "hw155",
		CommitRetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("ReleaseWithRetry: 404 must be success (idempotent), got %v", err)
	}
	if got := atomic.LoadInt32(&releases); got != 1 {
		t.Errorf("releases=%d, want 1 (404 returns immediately, no retry)", got)
	}
}

// TestReleaseWithRetry_5xx_Exhaustion proves a persistently-down PDM
// surfaces a clear 'retry exhausted' error after MaxAttempts so the wipe
// handler can RETAIN the deployment record for manual recovery rather than
// silently forgetting an orphaned allocation.
func TestReleaseWithRetry_5xx_Exhaustion(t *testing.T) {
	var releases int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&releases, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	err := c.ReleaseWithRetry(context.Background(), "omani.works", "hw155",
		CommitRetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if err == nil {
		t.Fatal("ReleaseWithRetry should fail on persistent 5xx")
	}
	if !strings.Contains(err.Error(), "retry exhausted") {
		t.Errorf("err=%q should contain 'retry exhausted'", err.Error())
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("err=%q should mention attempt count (3)", err.Error())
	}
	if got := atomic.LoadInt32(&releases); got != 3 {
		t.Errorf("releases=%d, want 3 (MaxAttempts)", got)
	}
}

// TestReleaseWithRetry_ContextCancellation_StopsBackoff proves a cancelled
// context (e.g. the 90s background-context timeout elapsing) stops the
// backoff loop promptly with a wrapped error rather than spinning.
func TestReleaseWithRetry_ContextCancellation_StopsBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	c := New(srv.URL)

	// Cancel almost immediately so the first inter-attempt sleep aborts.
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	err := c.ReleaseWithRetry(ctx, "omani.works", "hw155",
		CommitRetryConfig{MaxAttempts: 50, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})
	if err == nil {
		t.Fatal("ReleaseWithRetry should return an error when ctx is cancelled mid-backoff")
	}
	if !strings.Contains(err.Error(), "cancelled") && !strings.Contains(err.Error(), "context") {
		t.Errorf("err=%q should reflect context cancellation", err.Error())
	}
}
