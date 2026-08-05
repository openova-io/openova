// auth_handover_issuer_set_test.go — the ACCEPTED-ISSUER SET guard for
// GET /auth/handover (#5614).
//
// # Why this file exists, and why the negative rows are the important ones
//
// The handover verifier decides whether to establish a sovereign-admin session
// from a token's `iss` claim. Any change here moves an authn boundary, so a test
// that only proves "the token we wanted now works" is worthless: a fix that
// simply WIDENS acceptance is indistinguishable from an authn HOLE unless the
// same table also pins what must still be REFUSED. Rows 5-8 below are that pin.
// Delete them and this file stops being a guard and becomes a rubber stamp.
//
// # The three legs that must all keep working
//
// catalyst-api both MINTS and VERIFIES handover tokens, and there are three
// distinct mint paths with two different `iss` values:
//
//   - Leg A — the Sovereign's own console mints a handover for itself
//     (deployments/{id}/mint-handover-token). Post-cutover its signer issuer is
//     handoverjwt.DefaultIssuer() == CATALYST_HANDOVER_JWT_ISSUER ==
//     https://console.<own-fqdn>. This is the leg #5614 reported broken.
//   - Leg B — the MOTHERSHIP (Catalyst-Zero, which leaves
//     CATALYST_HANDOVER_JWT_ISSUER unset) mints the post-provisioning handover
//     with iss=https://console.openova.io, aud=https://console.<sov-fqdn>, and
//     the SOVEREIGN redeems it here (handoverjwt package doc; signed with the
//     mothership key mounted by cloud-init).
//   - Leg C — the "Enter org" support session (#3378 B2, org_enter_org.go).
//     Minted AND redeemed by the same Sovereign pod, historically stamping the
//     mothership literal as `iss`.
//
// A post-cutover Sovereign has CATALYST_HANDOVER_JWT_ISSUER set, so a verifier
// that accepts EXACTLY ONE issuer can serve Leg A or Legs B+C — never all three.
// That is why the expected issuer is a SET {configured, mothership}, not a
// single value.
//
// # Falsifiability (run both directions before trusting this file)
//
//   - Against the pre-#5614 tree (`const expectedIss = mothership`): row 1 fails.
//   - Against the #5681 tree (`expectedIss := handoverjwt.DefaultIssuer()`, a
//     single-value compare): rows 2 and 3b fail — that tree swapped which leg is
//     broken rather than fixing it.
//   - Against a naive "accept anything" fix: rows 5-8 fail.
//
// Only a fix that accepts the two-element set passes every row.
package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
)

// TestAuthHandover_AcceptedIssuerSet_5614 is the table-driven authn boundary for
// the handover `iss` claim.
func TestAuthHandover_AcceptedIssuerSet_5614(t *testing.T) {
	const (
		sovIssuer      = "https://console.hw292.omani.works"
		mothership     = "https://console.openova.io"
		attackerIssuer = "https://console.evil.example.com"
	)

	cases := []struct {
		name string
		// envIssuer is CATALYST_HANDOVER_JWT_ISSUER. setEnv=false leaves it
		// unset (Catalyst-Zero / a pre-cutover Sovereign).
		setEnv    bool
		envIssuer string
		// tokenIss is the `iss` claim stamped into the presented token.
		tokenIss string
		// wantAccept: true → 302 to /dashboard; false → 401 "invalid issuer".
		wantAccept bool
		why        string
	}{
		{
			name: "1_own_issuer_on_cutover_sovereign_ACCEPTED",
			// #5614 itself: hw292 (cc=true) minted a handover for ITSELF and
			// its own verifier 401'd it. FAILS on the pre-#5614 hardcode.
			setEnv: true, envIssuer: sovIssuer, tokenIss: sovIssuer,
			wantAccept: true,
			why:        "Leg A — a Sovereign must accept the token it minted itself",
		},
		{
			name: "2_mothership_issuer_on_cutover_sovereign_ACCEPTED",
			// Legs B and C. FAILS on the #5681 single-value compare, which
			// narrowed the accepted set to {configured} and so rejected both
			// the mothership post-provisioning handover and the Enter-org
			// support session on every cut-over Sovereign.
			setEnv: true, envIssuer: sovIssuer, tokenIss: mothership,
			wantAccept: true,
			why:        "Legs B+C — mothership handover + Enter-org must survive cutover",
		},
		{
			name: "3a_mothership_issuer_unconfigured_ACCEPTED",
			// Catalyst-Zero and every pre-cutover Sovereign: env unset, so the
			// accepted set is {mothership} alone. Byte-unchanged behaviour.
			setEnv: false, tokenIss: mothership,
			wantAccept: true,
			why:        "Catalyst-Zero unchanged when the override env is absent",
		},
		{
			name: "3b_own_issuer_unconfigured_REJECTED",
			// FAIL-CLOSED. An unconfigured pod must NOT infer a per-Sovereign
			// issuer from the token. If this row ever flips to accept, the
			// verifier has started trusting the attacker-controlled claim to
			// define its own expectation.
			setEnv: false, tokenIss: sovIssuer,
			wantAccept: false,
			why:        "unset env must not silently trust a per-Sovereign issuer",
		},
		{
			name: "4_empty_env_attacker_issuer_REJECTED",
			// Explicitly-empty env must fall back to {mothership}, never to a
			// wildcard. This is the row that stops "configured issuer empty"
			// from becoming "accept anything".
			setEnv: true, envIssuer: "", tokenIss: attackerIssuer,
			wantAccept: false,
			why:        "empty configured issuer is NOT a wildcard",
		},
		{
			name: "5_attacker_issuer_on_cutover_sovereign_REJECTED",
			setEnv: true, envIssuer: sovIssuer, tokenIss: attackerIssuer,
			wantAccept: false,
			why:        "a foreign issuer stays foreign after the widening",
		},
		{
			name: "6_attacker_issuer_unconfigured_REJECTED",
			setEnv: false, tokenIss: attackerIssuer,
			wantAccept: false,
			why:        "the pre-existing negative must not regress",
		},
		{
			name: "7_empty_token_issuer_REJECTED",
			// jwt/v5 GetIssuer() returns ("", nil) for a missing claim, so a
			// membership test written with a nil-guard alone would let an
			// issuer-less token through if the accepted set ever contained "".
			setEnv: true, envIssuer: sovIssuer, tokenIss: "",
			wantAccept: false,
			why:        "a token with no iss claim must never authenticate",
		},
		{
			name: "8_empty_token_issuer_unconfigured_REJECTED",
			setEnv: false, tokenIss: "",
			wantAccept: false,
			why:        "same, on an unconfigured pod",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// t.Setenv forbids parallel subtests — keep these sequential.
			if tc.setEnv {
				t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", tc.envIssuer)
			} else {
				// Guarantee the override is genuinely ABSENT (not merely
				// empty) even if the ambient environment set it. t.Setenv
				// registers the restore-on-cleanup, so the following
				// Unsetenv is undone when the subtest ends.
				t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "")
				if err := os.Unsetenv("CATALYST_HANDOVER_JWT_ISSUER"); err != nil {
					t.Fatalf("unset override: %v", err)
				}
			}

			// Fresh handler per row: the jti store is single-use, so reusing
			// one handler across rows would turn a genuine accept into a
			// replay 401 and silently weaken the table.
			h, privKey, _ := testHandoverSetup(t)

			c := validClaims("sov.test")
			c.Issuer = tc.tokenIss
			c.ID = fmt.Sprintf("jti-issuer-set-%d", i)
			tok := signClaims(t, privKey, c)

			req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
			w := httptest.NewRecorder()
			h.AuthHandover(w, req)

			if tc.wantAccept {
				if w.Code != http.StatusFound {
					t.Fatalf("%s\nenv=%q token.iss=%q: got %d %q, want 302 (accepted)",
						tc.why, tc.envIssuer, tc.tokenIss, w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s\nAUTHN HOLE: env=%q token.iss=%q was ACCEPTED (%d) but must be refused",
					tc.why, tc.envIssuer, tc.tokenIss, w.Code)
			}
			if body := w.Body.String(); !strings.Contains(body, "invalid issuer") {
				t.Fatalf("%s\nenv=%q token.iss=%q: rejected with %q, want the issuer check to be the thing that refused it",
					tc.why, tc.envIssuer, tc.tokenIss, body)
			}
		})
	}
}

// TestAcceptedHandoverIssuers_NeverEmpty pins the resolver itself: the accepted
// set must never be empty (an empty set with a `len(set)==0 → allow` bug reads
// as "accept anything") and must never contain the empty string.
func TestAcceptedHandoverIssuers_NeverEmpty(t *testing.T) {
	for _, env := range []string{"", "   ", "https://console.hw292.omani.works"} {
		t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", env)
		got := acceptedHandoverIssuers()
		if len(got) == 0 {
			t.Fatalf("env=%q: accepted issuer set is EMPTY — fail-open risk", env)
		}
		for _, v := range got {
			if v == "" {
				t.Fatalf("env=%q: accepted set contains the empty string %v — an iss-less token would authenticate", env, got)
			}
		}
		// The mothership origin is the invariant member: it is what keeps
		// Legs B and C alive on a cut-over Sovereign.
		found := false
		for _, v := range got {
			if v == handoverjwt.MothershipIssuer() {
				found = true
			}
		}
		if !found {
			t.Fatalf("env=%q: accepted set %v drops the mothership issuer — Legs B+C break", env, got)
		}
	}
}
