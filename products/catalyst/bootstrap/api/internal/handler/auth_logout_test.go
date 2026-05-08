package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// atomicInt64 is a tiny wrapper so the test file doesn't pull
// sync/atomic.Int64 into the call sites (Go 1.19+) — same shape, no
// reflection, race-detector friendly.
type atomicInt64 struct{ v atomic.Int64 }

func (a *atomicInt64) add(delta int64) int64 { return a.v.Add(delta) }
func (a *atomicInt64) load() int64           { return a.v.Load() }

// TestHandleAuthLogout_ClearCookieWireShape asserts the wire shape of
// the Set-Cookie header emitted by /auth/session DELETE matches the
// contract enforced in TC-R-066 + TC-R-095:
//
//   - One Set-Cookie per cookie (catalyst_session, catalyst_refresh).
//   - Each line carries the literal token "Max-Age=-1" (Go's
//     http.SetCookie collapses negative MaxAge to "Max-Age=0", which
//     would fail the substring match).
//   - SameSite=Lax (matches the Lax cookie set on /pin/verify so the
//     browser actually deletes it; Strict-vs-Lax mismatch creates a
//     ghost cookie that fails to clear).
//   - Path=/ + Secure + HttpOnly mirroring the issue path.
//   - Domain attribute appears IFF CATALYST_SESSION_COOKIE_DOMAIN is
//     set (so local dev / CI without a domain stays host-only, same
//     as the issue path).
func TestHandleAuthLogout_ClearCookieWireShape(t *testing.T) {
	prev := os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN")
	t.Cleanup(func() { os.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", prev) })
	os.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "console.example.test")

	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandleAuthLogout(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}

	cookies := w.Result().Header.Values("Set-Cookie")
	if len(cookies) != 2 {
		t.Fatalf("Set-Cookie count: got %d want 2 — %v", len(cookies), cookies)
	}

	// Build a per-cookie checklist so any missing attribute on either
	// line surfaces the exact assertion that broke.
	var sessionLine, refreshLine string
	for _, line := range cookies {
		switch {
		case strings.HasPrefix(line, "catalyst_session=;"):
			sessionLine = line
		case strings.HasPrefix(line, "catalyst_refresh=;"):
			refreshLine = line
		}
	}
	if sessionLine == "" {
		t.Fatalf("catalyst_session clear-cookie missing — cookies=%v", cookies)
	}
	if refreshLine == "" {
		t.Fatalf("catalyst_refresh clear-cookie missing — cookies=%v", cookies)
	}

	for label, line := range map[string]string{
		"catalyst_session": sessionLine,
		"catalyst_refresh": refreshLine,
	} {
		// TC-R-095 + TC-R-066 substring contract.
		for _, want := range []string{
			"Max-Age=-1",
			"Path=/",
			"Domain=console.example.test",
			"Secure",
			"HttpOnly",
			"SameSite=Lax",
		} {
			if !strings.Contains(line, want) {
				t.Errorf("%s clear-cookie missing %q in %q", label, want, line)
			}
		}
		// SameSite=Strict on the clear would fail to delete the Lax
		// cookie set at /pin/verify (cookie attributes must match for
		// browsers to honour deletion).
		if strings.Contains(line, "SameSite=Strict") {
			t.Errorf("%s clear-cookie must not be Strict — got %q", label, line)
		}
	}
}

// TestHandleAuthLogout_NoDomainWhenEnvUnset asserts the local-dev
// path: when CATALYST_SESSION_COOKIE_DOMAIN is unset, the clear
// cookie must NOT include a Domain attribute (host-only), matching
// the shape /pin/verify uses in that environment.
func TestHandleAuthLogout_NoDomainWhenEnvUnset(t *testing.T) {
	prev := os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN")
	t.Cleanup(func() { os.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", prev) })
	os.Unsetenv("CATALYST_SESSION_COOKIE_DOMAIN")

	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandleAuthLogout(w, req)

	for _, line := range w.Result().Header.Values("Set-Cookie") {
		if strings.Contains(line, "Domain=") {
			t.Errorf("Domain attr leaked when env is unset: %q", line)
		}
		if !strings.Contains(line, "Max-Age=-1") {
			t.Errorf("clear-cookie missing Max-Age=-1: %q", line)
		}
	}
}

// TestHandleAuthLogout_NoSecureOverHTTP asserts the local-dev / plain
// HTTP path: when neither r.TLS nor X-Forwarded-Proto=https is set,
// the Secure attribute must NOT be emitted (or browsers will refuse
// to honour the cookie on the same plain-HTTP origin).
func TestHandleAuthLogout_NoSecureOverHTTP(t *testing.T) {
	prev := os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN")
	t.Cleanup(func() { os.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", prev) })
	os.Unsetenv("CATALYST_SESSION_COOKIE_DOMAIN")

	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	// no X-Forwarded-Proto, no r.TLS
	w := httptest.NewRecorder()
	h.HandleAuthLogout(w, req)

	for _, line := range w.Result().Header.Values("Set-Cookie") {
		if strings.Contains(line, "; Secure") {
			t.Errorf("Secure attr leaked over plain HTTP: %q", line)
		}
	}
}

// TestPinIssue_ConcurrentRapidFireRateLimit asserts the TC-R-089
// contract: 3 concurrent /pin/issue calls for the same email return
// EXACTLY one 200 + two 429, NEVER a 5xx. The pre-fix path called
// EnsureUser before the rate-limit check, so concurrent callers all
// raced Keycloak; the loser of that race surfaced as a 502
// user-provisioning-failed.
func TestPinIssue_ConcurrentRapidFireRateLimit(t *testing.T) {
	h := testPinSetup(t)
	defer withSendPinEmail(noopSendPin)()

	// Stub Keycloak panics if EnsureUser is called more than once for
	// the same email — the rate-limit-before-EnsureUser ordering
	// guarantees only the winner reaches Keycloak.
	calls := &countingKCClient{}
	h.openovaKC = calls

	const N = 3
	codes := make(chan int, N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
				strings.NewReader(`{"email":"concurrent@example.test"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.HandlePinIssue(w, req)
			codes <- w.Code
		}()
	}
	close(start)

	got := map[int]int{}
	for i := 0; i < N; i++ {
		got[<-codes]++
	}
	if got[http.StatusOK] != 1 {
		t.Errorf("200 count: got %d want 1 — distribution %v", got[http.StatusOK], got)
	}
	if got[http.StatusTooManyRequests] != N-1 {
		t.Errorf("429 count: got %d want %d — distribution %v", got[http.StatusTooManyRequests], N-1, got)
	}
	for code, n := range got {
		if code >= 500 {
			t.Errorf("server error code %d appeared %d times — distribution %v", code, n, got)
		}
	}
	// EnsureUser must run at most once: the rate-limit gate fires
	// before EnsureUser so the losers never reach Keycloak.
	if calls.count() > 1 {
		t.Errorf("EnsureUser called %d times under concurrent rate-limit; want ≤1 (rate-limit-before-EnsureUser ordering broken)", calls.count())
	}
}

// TestPinIssue_RateLimitedBeforeEnsureUser asserts the ordering
// directly: when an entry already exists in the cooldown window, a
// follow-up /pin/issue returns 429 WITHOUT calling EnsureUser. This
// is what makes TC-R-089's concurrent path safe.
func TestPinIssue_RateLimitedBeforeEnsureUser(t *testing.T) {
	h := testPinSetup(t)
	defer withSendPinEmail(noopSendPin)()

	calls := &countingKCClient{}
	h.openovaKC = calls

	// Seed an entry to put the email inside the cooldown window.
	h.pinStore.put("op@example.test", "111111", "seed-req")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"op@example.test"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinIssue(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status: got %d want 429 (body: %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got == "" {
		t.Errorf("Retry-After header missing on 429 (TC-R-089 contract)")
	}
	if calls.count() != 0 {
		t.Errorf("EnsureUser called %d times; rate-limit must short-circuit before Keycloak", calls.count())
	}
}

// countingKCClient is a stub keycloakClient that counts EnsureUser
// invocations from concurrent goroutines via atomic.Int64.
type countingKCClient struct {
	calls atomicInt64
}

func (c *countingKCClient) EnsureUser(_ context.Context, _, _ string) (string, error) {
	c.calls.add(1)
	return "user-uuid-001", nil
}

func (c *countingKCClient) ImpersonateToken(_ context.Context, _, _ string) (string, string, int, error) {
	return "", "", 0, errors.New("ImpersonateToken not stubbed")
}

func (c *countingKCClient) count() int { return int(c.calls.load()) }
