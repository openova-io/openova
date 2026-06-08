// Package provisioner — unit tests for the #3126 tofu-init resilience
// layer: retry-with-backoff on transient github/CDN provider-install
// failures, the transient-vs-config error classifier, and the
// TF_PLUGIN_CACHE_DIR wiring in New().
//
// These are white-box tests (package provisioner) so they can drive the
// unexported retryTofuInit / isTransientInitFailure helpers with an
// injected runOnce + a no-op sleep — no real `tofu` binary is exec'd.
package provisioner

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// noopSleep is the injected sleeper so retries don't actually wait
// 5s+15s+45s during the test run.
func noopSleep(time.Duration) {}

// discardEmit drops progress events.
func discardEmit(string, string, string) {}

// TestRetryTofuInit_RetriesTransientThenSucceeds asserts the wrapper
// retries on a transient (CDN 504 / provider-install) failure and
// eventually returns nil once the underlying init succeeds — and that it
// took exactly the expected number of attempts.
func TestRetryTofuInit_RetriesTransientThenSucceeds(t *testing.T) {
	calls := 0
	// Fail the first 2 attempts with a github-release-asset-CDN-shaped
	// error, then succeed on the 3rd.
	runOnce := func() error {
		calls++
		if calls < 3 {
			return errors.New("tofu init failed: exit status 1 | stderr: Error: Failed to install provider\n\nError while installing huaweicloud/huaweicloud v1.62.1: 504 Gateway Timeout retrieving https://release-assets.githubusercontent.com/...")
		}
		return nil
	}

	err := retryTofuInit(context.Background(), runOnce, noopSleep, discardEmit)
	if err != nil {
		t.Fatalf("expected eventual success, got error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected exactly 3 attempts (2 transient failures + 1 success), got %d", calls)
	}
}

// TestRetryTofuInit_DoesNotRetryConfigError asserts a deterministic
// configuration error (NO network/CDN marker) is surfaced immediately
// without consuming the remaining attempts — retrying a bad provider
// constraint would only waste backoff time.
func TestRetryTofuInit_DoesNotRetryConfigError(t *testing.T) {
	calls := 0
	runOnce := func() error {
		calls++
		return errors.New("tofu init failed: exit status 1 | stderr: Error: Invalid provider requirements\n\nProvider \"registry.terraform.io/foo/bar\" is required but the given constraint \">= 99.0\" matches no versions")
	}

	err := retryTofuInit(context.Background(), runOnce, noopSleep, discardEmit)
	if err == nil {
		t.Fatal("expected the config error to be returned, got nil")
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt for a non-transient config error (no retry), got %d", calls)
	}
	if !strings.Contains(err.Error(), "Invalid provider requirements") {
		t.Fatalf("expected the original config error to surface, got: %v", err)
	}
}

// TestRetryTofuInit_GivesUpAfterMaxAttempts asserts that a transient
// failure which never clears stops after tofuInitMaxAttempts and returns
// the last error (it does NOT loop forever).
func TestRetryTofuInit_GivesUpAfterMaxAttempts(t *testing.T) {
	calls := 0
	runOnce := func() error {
		calls++
		return errors.New("Failed to install provider: 504 Gateway Timeout")
	}

	err := retryTofuInit(context.Background(), runOnce, noopSleep, discardEmit)
	if err == nil {
		t.Fatal("expected a terminal error after exhausting retries, got nil")
	}
	if calls != tofuInitMaxAttempts {
		t.Fatalf("expected exactly %d attempts (bounded), got %d", tofuInitMaxAttempts, calls)
	}
}

// TestRetryTofuInit_FirstAttemptSuccessNoRetry asserts the happy path
// (warm plugin cache → init succeeds first try) does exactly one call
// and never sleeps.
func TestRetryTofuInit_FirstAttemptSuccessNoRetry(t *testing.T) {
	calls := 0
	slept := 0
	runOnce := func() error { calls++; return nil }
	sleep := func(time.Duration) { slept++ }

	if err := retryTofuInit(context.Background(), runOnce, sleep, discardEmit); err != nil {
		t.Fatalf("expected first-attempt success, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt on the happy path, got %d", calls)
	}
	if slept != 0 {
		t.Fatalf("expected zero backoff sleeps on the happy path, got %d", slept)
	}
}

// TestRetryTofuInit_StopsOnCancelledContext asserts that once the
// caller's context is cancelled we stop retrying instead of sleeping
// through the remaining backoffs.
func TestRetryTofuInit_StopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	runOnce := func() error {
		calls++
		// Cancel after the first failure so the retry loop sees a done
		// context before the second attempt.
		cancel()
		return errors.New("Failed to install provider: connection reset by peer")
	}

	err := retryTofuInit(ctx, runOnce, noopSleep, discardEmit)
	if err == nil {
		t.Fatal("expected the last transient error to surface after context cancel, got nil")
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt before honouring the cancelled context, got %d", calls)
	}
}

// TestIsTransientInitFailure_Matrix locks the transient-vs-deterministic
// classifier: every network/CDN-class marker must qualify for retry, and
// a pure config error must NOT.
func TestIsTransientInitFailure_Matrix(t *testing.T) {
	transient := []string{
		"Error: Failed to install provider",
		"Failed to query available provider packages",
		"could not query provider registry registry.terraform.io",
		"504 Gateway Timeout",
		"received 502 Bad Gateway from release-assets.githubusercontent.com",
		"503 Service Unavailable",
		"net/http: TLS handshake timeout",
		"read tcp: i/o timeout",
		"connection reset by peer",
		"dial tcp: connect: connection refused",
		"Temporary failure in name resolution",
		"no such host",
		"context deadline exceeded: timeout",
		"unexpected EOF",
		"failed to retrieve: checksum mismatch download interrupted",
		"error fetching checksums",
	}
	for _, s := range transient {
		if !isTransientInitFailure(s) {
			t.Errorf("expected %q to be classified TRANSIENT (retryable), but it was not", s)
		}
	}

	deterministic := []string{
		"Error: Invalid provider requirements",
		"Error: Unsupported argument",
		"Error: Reference to undeclared input variable",
		"Backend configuration changed: a migration is required",
	}
	for _, s := range deterministic {
		if isTransientInitFailure(s) {
			t.Errorf("expected %q to be classified DETERMINISTIC (no retry), but it matched a transient marker", s)
		}
	}
}

// TestNew_DefaultsTofuPluginCacheDir asserts New() defaults the plugin
// cache to the persistent deployments-PVC path when the env is unset.
func TestNew_DefaultsTofuPluginCacheDir(t *testing.T) {
	t.Setenv("CATALYST_TF_PLUGIN_CACHE_DIR", "")
	p := New()
	if got, want := p.TofuPluginCacheDir, "/var/lib/catalyst/tofu-plugin-cache"; got != want {
		t.Fatalf("default TofuPluginCacheDir = %q, want %q", got, want)
	}
}

// TestNew_TofuPluginCacheDirOverride asserts the env var overrides the
// default (docs/PRINCIPLES.md #4 — runtime-configurable, never hardcoded).
func TestNew_TofuPluginCacheDirOverride(t *testing.T) {
	t.Setenv("CATALYST_TF_PLUGIN_CACHE_DIR", "/mnt/iac/plugin-cache")
	p := New()
	if got, want := p.TofuPluginCacheDir, "/mnt/iac/plugin-cache"; got != want {
		t.Fatalf("override TofuPluginCacheDir = %q, want %q", got, want)
	}
}

// TestRunTofu_SetsPluginCacheEnv asserts that runTofu actually exports
// TF_PLUGIN_CACHE_DIR (and creates the directory) when the Provisioner
// carries a cache path. We exercise this by pointing the cache at a temp
// dir and asserting the dir gets created; the `tofu` exec itself fails
// fast (binary absent in CI) which is fine — the MkdirAll side effect is
// what we assert, and it runs before exec.
func TestRunTofu_CreatesPluginCacheDir(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := tmp + "/plugin-cache"
	p := &Provisioner{TofuPluginCacheDir: cacheDir}

	// runTofu will try to exec `tofu` and almost certainly fail (no
	// binary in the unit-test sandbox), but MkdirAll(cacheDir) happens
	// BEFORE the exec, so the directory must exist afterwards regardless
	// of the exec outcome.
	_ = p.runTofu(context.Background(), tmp, []string{"version"}, discardEmit)

	if fi, err := os.Stat(cacheDir); err != nil || !fi.IsDir() {
		t.Fatalf("expected runTofu to create plugin cache dir %q, stat err=%v", cacheDir, err)
	}
}
