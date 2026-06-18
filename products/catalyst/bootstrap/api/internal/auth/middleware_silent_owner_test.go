package auth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ownerEmail is the canonical Sovereign owner used across the
// silent-owner tests — mirrors the OPERATOR_EMAIL the orchestrator
// stamps from deployment.request.OrgEmail.
const ownerEmail = "emrah.baysal@openova.io"

// downstreamProbe is a terminal handler that records the Claims the
// RequireSession middleware injected (via context) plus the identity
// headers, and writes 200. Used to assert the silent-owner path actually
// reaches the protected handler with owner authority.
type downstreamProbe struct {
	reached     bool
	claims      *Claims
	headerEmail string
	headerSub   string
}

func (d *downstreamProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.reached = true
	d.claims = ClaimsFromContext(r.Context())
	d.headerEmail = r.Header.Get("X-User-Email")
	d.headerSub = r.Header.Get("X-User-Sub")
	w.WriteHeader(http.StatusOK)
}

// armedConfig returns a non-nil *Config so RequireSession runs its full
// body (a nil cfg is a transparent passthrough). No JWKS / Keycloak is
// wired because the silent-owner no-token path never calls ValidateToken.
func armedConfig() *Config {
	return &Config{Realm: "sovereign"}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestSilentOwner_NoSession_FlagOn_InjectsOwner is the North-Star #3
// acceptance at the middleware layer: a request with NO session cookie,
// on a Sovereign (OPERATOR_EMAIL set) with the flag ON, resolves to the
// owner and reaches the downstream handler with tier=owner instead of a
// 401.
func TestSilentOwner_NoSession_FlagOn_InjectsOwner(t *testing.T) {
	t.Setenv(silentOwnerEnvFlag, "true")
	t.Setenv("OPERATOR_EMAIL", ownerEmail)

	probe := &downstreamProbe{}
	h := RequireSession(armedConfig(), quietLogger())(probe)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil) // no cookie
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (owner injected), got %d body=%s", rr.Code, rr.Body.String())
	}
	if !probe.reached {
		t.Fatal("downstream handler was not reached — silent owner did not inject")
	}
	if probe.claims == nil {
		t.Fatal("no Claims injected into context")
	}
	if probe.claims.Email != ownerEmail {
		t.Fatalf("claims email = %q, want %q", probe.claims.Email, ownerEmail)
	}
	if probe.claims.Tier != sovereignOperatorTier {
		t.Fatalf("claims tier = %q, want %q", probe.claims.Tier, sovereignOperatorTier)
	}
	if !probe.claims.HasRealmRole(sovereignOperatorRoleName) {
		t.Fatalf("claims missing %q role: %+v", sovereignOperatorRoleName, probe.claims.RealmAccess.Roles)
	}
	if probe.headerEmail != ownerEmail {
		t.Fatalf("X-User-Email = %q, want %q", probe.headerEmail, ownerEmail)
	}
	if probe.headerSub != ownerEmail {
		t.Fatalf("X-User-Sub = %q, want %q", probe.headerSub, ownerEmail)
	}
}

// TestSilentOwner_NoSession_FlagOff_401 — the default. With the flag
// unset, a no-session request 401s exactly as before; the downstream
// handler is never reached.
func TestSilentOwner_NoSession_FlagOff_401(t *testing.T) {
	// silentOwnerEnvFlag intentionally unset.
	t.Setenv("OPERATOR_EMAIL", ownerEmail)

	probe := &downstreamProbe{}
	h := RequireSession(armedConfig(), quietLogger())(probe)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with flag off, got %d", rr.Code)
	}
	if probe.reached {
		t.Fatal("downstream handler reached with flag OFF — silent owner must be default-off")
	}
}

// TestSilentOwner_FlagOn_NoOperatorEmail_401 — the flag is set but
// OPERATOR_EMAIL is empty (mothership / a non-Sovereign deployment). The
// front door must NOT synthesize an owner there; 401 stands.
func TestSilentOwner_FlagOn_NoOperatorEmail_401(t *testing.T) {
	t.Setenv(silentOwnerEnvFlag, "true")
	t.Setenv("OPERATOR_EMAIL", "")

	probe := &downstreamProbe{}
	h := RequireSession(armedConfig(), quietLogger())(probe)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when OPERATOR_EMAIL empty, got %d", rr.Code)
	}
	if probe.reached {
		t.Fatal("owner synthesized without OPERATOR_EMAIL — must require Sovereign mode")
	}
}

// TestSilentOwner_FlagNonTrueValues_401 — only the literal "true"
// (case-insensitive) opts in. "1", "false", "yes" and garbage must all
// leave the gate closed.
func TestSilentOwner_FlagNonTrueValues_401(t *testing.T) {
	t.Setenv("OPERATOR_EMAIL", ownerEmail)
	for _, v := range []string{"1", "false", "yes", "on", "TRUE-ish", "  ", "owner"} {
		t.Setenv(silentOwnerEnvFlag, v)
		probe := &downstreamProbe{}
		h := RequireSession(armedConfig(), quietLogger())(probe)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("flag=%q: expected 401, got %d", v, rr.Code)
		}
		if probe.reached {
			t.Fatalf("flag=%q: owner injected for non-\"true\" value", v)
		}
	}
}

// TestSilentOwner_FlagTrueCaseInsensitive — "TRUE", "True", "tRuE" all
// opt in (EqualFold).
func TestSilentOwner_FlagTrueCaseInsensitive(t *testing.T) {
	t.Setenv("OPERATOR_EMAIL", ownerEmail)
	for _, v := range []string{"true", "TRUE", "True", "tRuE", " true "} {
		t.Setenv(silentOwnerEnvFlag, v)
		probe := &downstreamProbe{}
		h := RequireSession(armedConfig(), quietLogger())(probe)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || !probe.reached {
			t.Fatalf("flag=%q: expected 200+reached, got %d reached=%v", v, rr.Code, probe.reached)
		}
	}
}

// TestSilentOwner_NilConfig_StillPassthrough — a nil cfg keeps the
// historical transparent-passthrough behaviour (CI without Keycloak),
// independent of the flag. The silent-owner branch lives inside the
// armed (cfg != nil) path, so this only asserts we didn't regress the
// passthrough.
func TestSilentOwner_NilConfig_StillPassthrough(t *testing.T) {
	t.Setenv(silentOwnerEnvFlag, "true")
	t.Setenv("OPERATOR_EMAIL", ownerEmail)

	probe := &downstreamProbe{}
	h := RequireSession(nil, quietLogger())(probe)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	h.ServeHTTP(rr, req)

	if !probe.reached {
		t.Fatal("nil cfg must be a transparent passthrough")
	}
	// No claims are injected by the passthrough path.
	if probe.claims != nil {
		t.Fatalf("passthrough should not inject claims, got %+v", probe.claims)
	}
}

// TestSilentOwnerClaims_Identity unit-tests the constructor directly:
// the owner identity shape (email/sub/tier/roles/verified) is exactly
// what whoami needs to report tier=owner.
func TestSilentOwnerClaims_Identity(t *testing.T) {
	t.Setenv(silentOwnerEnvFlag, "true")
	t.Setenv("OPERATOR_EMAIL", "  EMRAH.Baysal@OpenOva.io  ") // exercise trim + lowercase

	c := silentOwnerClaims()
	if c == nil {
		t.Fatal("expected non-nil Claims")
	}
	if c.Email != ownerEmail {
		t.Fatalf("email = %q, want %q (trimmed+lowercased)", c.Email, ownerEmail)
	}
	if c.Sub != ownerEmail {
		t.Fatalf("sub = %q, want %q", c.Sub, ownerEmail)
	}
	if !c.EmailVerified {
		t.Fatal("EmailVerified must be true")
	}
	if c.Tier != "owner" {
		t.Fatalf("tier = %q, want owner", c.Tier)
	}
	for _, want := range []string{"catalyst-owner", "catalyst-admin", "sovereign-admins"} {
		if !c.HasRealmRole(want) {
			t.Fatalf("missing realm role %q: %+v", want, c.RealmAccess.Roles)
		}
	}
}

// TestSilentOwnerClaims_NilCases — the two no-op conditions return nil.
func TestSilentOwnerClaims_NilCases(t *testing.T) {
	t.Run("flag off", func(t *testing.T) {
		t.Setenv(silentOwnerEnvFlag, "false")
		t.Setenv("OPERATOR_EMAIL", ownerEmail)
		if c := silentOwnerClaims(); c != nil {
			t.Fatalf("flag off should be nil, got %+v", c)
		}
	})
	t.Run("no operator email", func(t *testing.T) {
		t.Setenv(silentOwnerEnvFlag, "true")
		t.Setenv("OPERATOR_EMAIL", "")
		if c := silentOwnerClaims(); c != nil {
			t.Fatalf("no operator email should be nil, got %+v", c)
		}
	})
}

// TestSilentOwnerClaims_ReturnsFreshSlice guards against a shared-backing
// aliasing bug: mutating one returned Claims' role slice must not leak
// into the next.
func TestSilentOwnerClaims_ReturnsFreshSlice(t *testing.T) {
	t.Setenv(silentOwnerEnvFlag, "true")
	t.Setenv("OPERATOR_EMAIL", ownerEmail)

	a := silentOwnerClaims()
	a.RealmAccess.Roles[0] = "MUTATED"
	b := silentOwnerClaims()
	if b.RealmAccess.Roles[0] == "MUTATED" {
		t.Fatal("returned role slice aliases the package-level template — must be a copy")
	}
}

// TestSilentOwner_OPTIONSStillPassthrough — CORS pre-flights must not be
// intercepted by the silent-owner branch (the OPTIONS short-circuit runs
// first). The downstream handler sees the OPTIONS with no claims.
func TestSilentOwner_OPTIONSStillPassthrough(t *testing.T) {
	t.Setenv(silentOwnerEnvFlag, "true")
	t.Setenv("OPERATOR_EMAIL", ownerEmail)

	probe := &downstreamProbe{}
	h := RequireSession(armedConfig(), quietLogger())(probe)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/whoami", nil)
	h.ServeHTTP(rr, req)

	if !probe.reached {
		t.Fatal("OPTIONS pre-flight must pass through to CORS handler")
	}
	if probe.claims != nil {
		t.Fatalf("OPTIONS path must not inject owner claims, got %+v", probe.claims)
	}
}

// assertJSON is a tiny helper so a future whoami-shape assertion can
// decode the body without pulling the handler package into this test.
func assertJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, string(body))
	}
	return m
}

var _ = assertJSON // reserved for downstream wire-shape assertions
