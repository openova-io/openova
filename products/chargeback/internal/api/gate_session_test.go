package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// setupGateAPI builds a handler whose trusted forward-auth header is `hdr`
// ("" = the shipped default, i.e. the feature off).
func setupGateAPI(t *testing.T, hdr string) (http.Handler, *store.Store) {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{3}, 32))
	var logbuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	cfg := config.Config{
		PublicURL:      "https://chargeback.t99.omani.works",
		Profile:        "sovereign",
		OperatorEmails: []string{opEmail},
	}
	if hdr != "" {
		cfg.TrustedForwardAuthHeader = http.CanonicalHeaderKey(hdr)
	}
	h := New(Deps{
		Store: st, Keys: keys, Mail: &recMail{}, Verifier: &fakeVerifier{},
		Config: cfg, Metrics: metrics.New(), Version: "test",
	})
	return h, st
}

func getWithHeader(t *testing.T, h http.Handler, path, hdr, val string) int {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if hdr != "" {
		r.Header.Set(hdr, val)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// A user the Sovereign SSO already authenticated must not be asked to sign in
// again — that second prompt is the whole defect in #6841.
func TestGateIdentitySignsInWithoutASecondPrompt(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Auth-Request-Email")

	if got := getWithHeader(t, h, "/api/v1/auth/me", "X-Auth-Request-Email", opEmail); got != 200 {
		t.Fatalf("operator identity from the gate: got %d, want 200 (a signed-in user was asked to sign in again)", got)
	}
	if got := getWithHeader(t, h, "/api/v1/customers", "X-Auth-Request-Email", opEmail); got != 200 {
		t.Fatalf("operator API via gate identity: got %d, want 200", got)
	}
}

// THE security property. With the feature off (the shipped default) the header
// must be inert, so a deployment that has not opted in cannot be spoofed by
// anyone who can reach the app directly.
func TestGateHeaderIsInertWhenNotConfigured(t *testing.T) {
	h, _ := setupGateAPI(t, "") // default: TRUSTED_FORWARD_AUTH_HEADER unset

	if got := getWithHeader(t, h, "/api/v1/customers", "X-Auth-Request-Email", opEmail); got != 401 {
		t.Fatalf("spoofed header on an unconfigured deployment: got %d, want 401 — the header must be ignored entirely", got)
	}
	// And the operator's own address is the strongest spoof to try.
	if got := getWithHeader(t, h, "/api/v1/auth/me", "X-Auth-Request-Email", opEmail); got == 200 {
		t.Fatal("unconfigured deployment accepted a forwarded identity — any caller could assume the operator role")
	}
}

// A different header name must not be honoured: only the configured one.
func TestOnlyTheConfiguredHeaderIsTrusted(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Auth-Request-Email")

	if got := getWithHeader(t, h, "/api/v1/customers", "X-Forwarded-Email", opEmail); got != 401 {
		t.Fatalf("a header other than the configured one was trusted: got %d, want 401", got)
	}
}

// An identity the gate verified but which holds no grant here must read as
// unauthenticated, not as an invented account.
func TestGateIdentityWithoutAGrantIsNotAdmitted(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Auth-Request-Email")

	if got := getWithHeader(t, h, "/api/v1/customers", "X-Auth-Request-Email", "stranger@example.com"); got != 401 {
		t.Fatalf("ungranted gate identity: got %d, want 401", got)
	}
}

// Junk in the header must never produce a session.
func TestGateHeaderRejectsNonEmailValues(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Auth-Request-Email")

	for _, v := range []string{"", "   ", "not-an-email", "admin"} {
		if got := getWithHeader(t, h, "/api/v1/customers", "X-Auth-Request-Email", v); got != 401 {
			t.Fatalf("header value %q: got %d, want 401", v, got)
		}
	}
}
