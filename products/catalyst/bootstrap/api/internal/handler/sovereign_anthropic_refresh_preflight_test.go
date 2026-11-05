package handler

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// #6317 follow-up — two ways the renewal chain still lost the credential
// AFTER the refresh existed, both measured on live Sovereigns:
//
//  1. The pre-flight only asked "is there an in-cluster identity?". A
//     Sovereign whose credential arrived through chart values
//     (sovereign.anthropic.* -> env) and whose operator never created
//     catalyst-system/sovereign-anthropic-credentials passed that check,
//     ran the exchange, and then the strategic-merge PATCH 404'd — the
//     ROTATED refresh token was gone on the first renewal pass.
//  2. A refresh token the provider had definitively rejected (spent by
//     another refresher, or revoked) was re-sent every 10 minutes for
//     weeks, until the endpoint answered 429 to everything (hw305).
//
// Every assertion below is vacuity-proven in
// scripts/check-anthropic-refresh-vacuity-6317.py (mutations M19 / M20).

func newPreflightHandler() (*Handler, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	h := &Handler{log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	return h, logs
}

// ─── 1. root Secret ABSENT: in-cluster identity, credential from the env ───

func TestRefresh_DoesNotSpendRefreshTokenWhenRootSecretAbsent(t *testing.T) {
	h, logs := newPreflightHandler()

	// An in-cluster identity exists but the root Secret does not: the
	// credential can only have come from the process env (chart values).
	withAnthropicSecretClient(t, fake.NewSimpleClientset(), nil)
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, credWithRemaining("acc-OLD", "rt-PRECIOUS", -1*time.Hour))

	srv := newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshNoDurableStore {
		t.Fatalf("outcome = %q, want %q", got, anthropicRefreshNoDurableStore)
	}
	if srv.callCount() != 0 {
		t.Fatalf("the refresh token was SPENT (%d exchange call(s)) with no root Secret to store the result in — the PATCH write-back cannot create one, so the rotated token would have been lost", srv.callCount())
	}
	out := logs.String()
	if !strings.Contains(out, `"rootSecretAbsent":true`) {
		t.Fatalf("the skip did not name the ABSENT root Secret as the cause.\nlogs: %s", out)
	}
	if !strings.Contains(out, "create secret generic "+anthropicCredentialSecret) {
		t.Fatalf("the skip did not carry the one-kubectl remediation that creates the root Secret.\nlogs: %s", out)
	}
	// Hygiene: the remediation must not carry the credential it tells the
	// operator to copy.
	if strings.Contains(out, "rt-PRECIOUS") || strings.Contains(out, "acc-OLD") {
		t.Fatalf("token material leaked into the log.\nlogs: %s", out)
	}
}

// ─── 2. root Secret read FAILS (not NotFound): unknown ⇒ do not spend ──────

func TestRefresh_TransientRootSecretReadErrorDoesNotSpend(t *testing.T) {
	h, logs := newPreflightHandler()

	cs := fake.NewSimpleClientset(anthropicSecret("", credWithRemaining("acc-OLD", "rt-PRECIOUS", -1*time.Hour)))
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, fmt.Errorf("apiserver: etcdserver: request timed out")
	})
	withAnthropicSecretClient(t, cs, nil)
	// The live read fails too, so the credential reaches the refresh through
	// the env fallback — exactly the path a flapping apiserver produces.
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, credWithRemaining("acc-OLD", "rt-PRECIOUS", -1*time.Hour))

	srv := newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshNoDurableStore {
		t.Fatalf("outcome = %q, want %q — a store whose state is UNKNOWN is not a durable store", got, anthropicRefreshNoDurableStore)
	}
	if srv.callCount() != 0 {
		t.Fatalf("the refresh token was SPENT (%d call(s)) while the root Secret's state was unknown", srv.callCount())
	}
	if strings.Contains(logs.String(), `"rootSecretAbsent":true`) {
		t.Fatalf("a transient read error was reported as ABSENT — that would send the operator to create a Secret that may exist.\nlogs: %s", logs.String())
	}
}

// ─── 3. a DEFINITIVELY rejected refresh token is not re-sent ──────────────

func TestRefresh_SpentRefreshTokenIsNotResent(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-SPENT", -1*time.Hour))
	srv := newOAuthServer(t, 400, func() string { return `{"error":"invalid_grant"}` })

	// First pass: the exchange is attempted, the provider says no, loudly.
	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshExchangeFailed {
		t.Fatalf("first pass outcome = %q, want %q", got, anthropicRefreshExchangeFailed)
	}
	if srv.callCount() != 1 {
		t.Fatalf("first pass made %d exchange call(s), want exactly 1", srv.callCount())
	}
	if !strings.Contains(f.logs.String(), `"definitive":true`) {
		t.Fatalf("a 400 invalid_grant was not classified as DEFINITIVE.\nlogs: %s", f.logs.String())
	}

	// Second pass, same credential: no exchange, a different verdict.
	f.logs.Reset()
	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshTokenSpent {
		t.Fatalf("second pass outcome = %q, want %q", got, anthropicRefreshTokenSpent)
	}
	if srv.callCount() != 1 {
		t.Fatalf("a refresh token the provider already rejected was RE-SENT (%d call(s) total) — this is the retry storm that earned HTTP 429 on hw305", srv.callCount())
	}
	out := f.logs.String()
	if !strings.Contains(out, "NOT RETRIED") || !strings.Contains(out, `"rejectedHTTPStatus":400`) {
		t.Fatalf("the spent-token verdict was not reported loudly with the status that produced it.\nlogs: %s", out)
	}
	if !strings.Contains(out, "DEDICATED claude-code login") {
		t.Fatalf("the remediation does not tell the operator the token must not be shared with another refresher.\nlogs: %s", out)
	}
	if strings.Contains(out, "rt-SPENT") {
		t.Fatalf("token material leaked into the log.\nlogs: %s", out)
	}
	// The stored credential is untouched by either pass.
	if _, creds := f.storedRootSecret(t); oauthField(t, creds, "refreshToken") != "rt-SPENT" {
		t.Fatalf("a failed refresh rewrote the stored credential")
	}
}

// ─── 4. an operator ROTATION clears the memo without a restart ────────────

func TestRefresh_RotatedRootSecretClearsTheSpentMemo(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-SPENT", -1*time.Hour))
	bad := newOAuthServer(t, 400, func() string { return `{"error":"invalid_grant"}` })
	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshExchangeFailed {
		t.Fatalf("setup: outcome = %q, want exchange-failed", got)
	}
	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshTokenSpent {
		t.Fatalf("setup: outcome = %q, want refresh-token-spent", got)
	}
	if bad.callCount() != 1 {
		t.Fatalf("setup: %d call(s), want 1", bad.callCount())
	}

	// The operator rotates the root Secret with a freshly issued credential.
	rotated := anthropicSecret("", credWithRemaining("acc-ROTATED", "rt-FRESH", -1*time.Hour))
	if _, err := f.client.CoreV1().Secrets(anthropicCredentialNamespace).Update(context.Background(), rotated, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("rotate root Secret: %v", err)
	}
	good := newOAuthServer(t, 200, okTokenResponse("acc-NEW", "rt-2", 18000))

	if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshed {
		t.Fatalf("after rotation outcome = %q, want %q — the memo must key on the TOKEN, not on the Sovereign", got, anthropicRefreshed)
	}
	if good.callCount() != 1 {
		t.Fatalf("the rotated token was not exchanged (%d call(s))", good.callCount())
	}
	if good.sentRefreshToken() != "rt-FRESH" {
		t.Fatalf("exchanged %q, want the rotated token", good.sentRefreshToken())
	}
	if _, creds := f.storedRootSecret(t); oauthField(t, creds, "accessToken") != "acc-NEW" {
		t.Fatalf("the renewed credential was not stored after rotation")
	}
}

// ─── 5. a TRANSIENT failure is still retried next pass ─────────────────────

func TestRefresh_TransientExchangeErrorIsRetried(t *testing.T) {
	f := newRefreshFixture(t, "", credWithRemaining("acc-OLD", "rt-1", -1*time.Hour))
	srv := newOAuthServer(t, 503, func() string { return `{"error":"upstream"}` })

	for pass := 1; pass <= 2; pass++ {
		if got := f.h.refreshAnthropicCredential(context.Background()); got != anthropicRefreshExchangeFailed {
			t.Fatalf("pass %d outcome = %q, want %q", pass, got, anthropicRefreshExchangeFailed)
		}
	}
	if srv.callCount() != 2 {
		t.Fatalf("a 503 was treated as definitive: %d call(s) over two passes, want 2 — a provider blip must not strand the credential until rotation", srv.callCount())
	}
	if strings.Contains(f.logs.String(), `"definitive":true`) {
		t.Fatalf("a 503 was classified as DEFINITIVE.\nlogs: %s", f.logs.String())
	}
}
