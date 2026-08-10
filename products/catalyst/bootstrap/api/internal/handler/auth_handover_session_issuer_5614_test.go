package handler

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
)

// UAT row 96 — "Handover URL → lands /dashboard already signed-in as the
// owner, no login form."
//
// #5614 fixed the INBOUND half of /auth/handover: which `iss` values the
// endpoint will ACCEPT on a handover token (acceptedHandoverIssuers, a set,
// not a single value). It did not touch the OUTBOUND half. The session JWT
// this same handler mints and hands back to the browser carried a raw
// literal:
//
//	"iss": "https://console.openova.io"
//
// at auth_handover.go, while every OTHER locally-minted session on the same
// pod — PIN-verify (auth.go), Org handover (org_handover.go), the MCP bearer
// seed (sovereign_mcp_bearer_seed.go) — resolved pinIssuer() from
// CATALYST_PIN_ISSUER. So on a cut-over Sovereign the handover-authenticated
// OWNER held the one session on the whole pod stamped with a foreign issuer.
//
// That is load-bearing, not cosmetic: bp-openova-mcp rejects any bearer whose
// `iss` is not its expected issuer
// (products/openova-mcp/internal/identity/identity.go:291-295), so the Pillar-4
// MCP attach refused precisely the principal that proves Sovereign ownership.
//
// Measured on hw292 (dep 1c56518035a83e03, cc=true, read-only kubectl):
//
//	CATALYST_PIN_ISSUER=https://console.hw292.omani.works
//	CATALYST_HANDOVER_JWT_ISSUER=https://console.hw292.omani.works
//
// — both env vars pivoted by cutover step-07, and this literal did not move.
//
// The assertion is on the VALUE, not on the key's presence: a missing-key
// check would pass against the defect, since the defective code stamped an
// `iss` too — just the wrong one.
func TestHandoverSessionClaims_IssuerFollowsTheSovereign_UAT96(t *testing.T) {
	const sovereignIssuer = "https://console.hw292.omani.works"

	// Both mint paths on one pod must agree. Assert them together so a
	// future edit that fixes one and forgets the other fails here.
	t.Setenv("CATALYST_PIN_ISSUER", sovereignIssuer)

	if got := pinIssuer(); got != sovereignIssuer {
		t.Fatalf("precondition: pinIssuer()=%q, want %q — the rest of this test is meaningless without it", got, sovereignIssuer)
	}

	signer := newTestSigner(t)

	// The handover-derived session, minted exactly as AuthHandover mints it.
	handoverSession := mintHandoverSessionForTest(t, signer, "emrah.baysal@openova.io")
	// The PIN-derived session on the same pod, for comparison.
	pinSession := mintOrgScopedMCPBearerForTest(t, signer)

	handoverIss := issuerOf(t, signer, handoverSession)
	pinIss := issuerOf(t, signer, pinSession)

	if handoverIss != sovereignIssuer {
		t.Errorf("handover session iss=%q, want %q (the Sovereign's own console). "+
			"A cut-over Sovereign that signs a session with its own key must not label it as issued by the mothership.",
			handoverIss, sovereignIssuer)
	}
	if handoverIss != pinIss {
		t.Errorf("one pod, two issuers: handover session iss=%q but PIN-path session iss=%q — "+
			"every session this process signs with one key must carry one issuer",
			handoverIss, pinIss)
	}
	if handoverIss == handoverjwt.MothershipIssuer() {
		t.Errorf("handover session iss is still the mothership origin %q on a Sovereign whose "+
			"CATALYST_PIN_ISSUER is %q — this is the #5614 hardcode, one leg further down the same request path",
			handoverjwt.MothershipIssuer(), sovereignIssuer)
	}
}

// CONTROL — the same assertion must FAIL to fire when the override is
// absent. A pre-cutover Sovereign leaves CATALYST_PIN_ISSUER unset, and the
// stamped issuer must then be the mothership origin, byte-unchanged from
// pre-fix behaviour. Without this control the test above would also pass
// against an implementation that hardcoded the Sovereign string.
func TestHandoverSessionClaims_PreCutoverIssuerIsUnchanged_UAT96(t *testing.T) {
	t.Setenv("CATALYST_PIN_ISSUER", "")
	if err := os.Unsetenv("CATALYST_PIN_ISSUER"); err != nil {
		t.Fatalf("unset CATALYST_PIN_ISSUER: %v", err)
	}

	signer := newTestSigner(t)
	iss := issuerOf(t, signer, mintHandoverSessionForTest(t, signer, "emrah.baysal@openova.io"))

	if want := handoverjwt.MothershipIssuer(); iss != want {
		t.Errorf("env-unset: handover session iss=%q, want %q — a pre-cutover Sovereign's session shape must not change", iss, want)
	}
	if strings.TrimSpace(iss) == "" {
		t.Errorf("handover session carries no issuer at all; an un-issuer'd session is worse than a wrong one")
	}
}

// mintHandoverSessionForTest signs the claim set that the HANDLER builds —
// buildHandoverSessionClaims, the function AuthHandover step 6 calls. It does
// NOT rebuild the claims locally.
//
// The first version of this test did rebuild them, and both behavioural tests
// then passed against the un-fixed handler: the helper read pinIssuer() itself,
// so it could never observe the literal in auth_handover.go. That is the
// "tested a surface that cannot fail" shape. Calling the handler's own
// constructor is what makes the assertion able to fail.
//
// AuthHandover cannot be driven end-to-end here because step 5 hard-fails on
// EnsureUser without a live Keycloak; the constructor is the closest seam that
// is still the handler's real code.
func mintHandoverSessionForTest(t *testing.T, signer *handoverjwt.Signer, email string) string {
	t.Helper()
	claims := buildHandoverSessionClaims(&authHandoverClaims{
		Email:         email,
		EmailVerified: true,
		Role:          "sovereign-admin",
		SovereignFQDN: "hw292.omani.works",
		DeploymentID:  "1c56518035a83e03",
	}, "kc-uid-under-test", 8*time.Hour)
	raw, err := signer.SignCustomClaims(claims)
	if err != nil {
		t.Fatalf("sign handover session: %v", err)
	}
	return raw
}

func mintOrgScopedMCPBearerForTest(t *testing.T, signer *handoverjwt.Signer) string {
	t.Helper()
	raw, err := mintOrgScopedMCPBearer(signer, "acme", "owner@acme.omani.homes")
	if err != nil {
		t.Fatalf("mint org-scoped bearer: %v", err)
	}
	return raw
}

func issuerOf(t *testing.T, signer *handoverjwt.Signer, raw string) string {
	t.Helper()
	pub, err := signer.PublicRSAKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	claims := jwt.MapClaims{}
	if _, perr := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return pub, nil },
		jwt.WithValidMethods([]string{"RS256"})); perr != nil {
		t.Fatalf("parse minted session: %v", perr)
	}
	iss, _ := claims["iss"].(string)
	return iss
}

// TestHandoverSessionIssuer_SourceHasNoLiteral pins the fix at its call site.
//
// The behavioural tests above already fail against the literal (verified by
// reverting the one line: TestHandoverSessionClaims_IssuerFollowsTheSovereign_UAT96
// reports iss="https://console.openova.io" want "https://console.hw292.omani.works").
// This adds the cheap structural half so a future refactor that reintroduces
// ANY hardcoded mothership issuer at the mint site is named directly rather
// than surfacing as a claim-value mismatch. The vacuity guard is the last
// assertion: the file must still contain AuthHandover, so a check that merely
// greps for absence of the literal cannot be satisfied by an empty, renamed or
// missing file.
func TestHandoverSessionIssuer_SourceHasNoLiteral(t *testing.T) {
	src, err := os.ReadFile("auth_handover.go")
	if err != nil {
		t.Fatalf("read auth_handover.go: %v", err)
	}
	text := string(src)

	const mintSite = `"iss":            pinIssuer(),`
	if !strings.Contains(text, mintSite) {
		t.Errorf("auth_handover.go no longer stamps the session issuer via pinIssuer(); want the line %s", mintSite)
	}
	const literalMint = `"iss":            "https://console.openova.io",`
	if strings.Contains(text, literalMint) {
		t.Errorf("auth_handover.go stamps the mothership literal directly (%s) — that is the row-96 defect", literalMint)
	}
	// Vacuity guard: the file is the right file and was actually read.
	if !strings.Contains(text, "func (h *Handler) AuthHandover(") {
		t.Fatalf("auth_handover.go does not contain AuthHandover — this test read the wrong file and its other assertions mean nothing")
	}
}
