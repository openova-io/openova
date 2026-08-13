package handler

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// #6250 — the Org-create → workspace-pod credential seam owes its operator
// THREE distinguishable answers, and shipped two: ABSENT and VALID were
// distinct, INVALID took VALID's verdict.
//
// Everything here drives a REAL CALL SITE — seedAnthropicToken (the producer
// runOrganizationPipeline fires) and runSeedReconcilePass (the ten-minute loop)
// — never the classifier in isolation. A classifier that returns the right
// enum while the producer still writes "seeded" over it is the exact defect
// being fixed, and a helper-only test cannot see it.
//
// CONTROLS are load-bearing throughout: every "unusable credential is refused"
// case is paired with a valid credential that must still seed. Without them
// "refuses an expired credential" is indistinguishable from "refuses every
// credential", which would be a worse bug than the one being fixed.
//
// No real credential appears in this file. Every token is an obvious
// FAKE-prefixed placeholder.

// fakeAnthropicCredentialsJSON builds a claudeAiOauth blob whose accessToken
// expires `until` from now — negative for an already-expired one. Shaped like
// the real seeded document (the fields anthropic_credential_class.go parses),
// with placeholder values that are not credentials.
func fakeAnthropicCredentialsJSON(until time.Duration) string {
	return fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"FAKE-not-a-real-access-token","refreshToken":"FAKE-not-a-real-refresh-token","expiresAt":%d,"scopes":["user:inference"]}}`,
		time.Now().Add(until).UnixMilli())
}

// validAnthropicCredentialsJSON / expiredAnthropicCredentialsJSON — the two
// fixtures every case in this package builds on. 6h ahead and 45h behind; the
// 45h mirrors the live omantel.biz precedent recorded in the bp-agenity
// statefulset template.
func validAnthropicCredentialsJSON() string   { return fakeAnthropicCredentialsJSON(6 * time.Hour) }
func expiredAnthropicCredentialsJSON() string { return fakeAnthropicCredentialsJSON(-45 * time.Hour) }

// ── Producer: a credential that cannot authenticate is never "seeded" ───────

func Test_seedAnthropicToken_UnusableCredentialIsNotReportedAsSeeded(t *testing.T) {
	cases := []struct {
		name        string
		apiKey      string
		credsJSON   string
		wantClass   string
		wantInError string
	}{
		{
			// The shape that stranded R19: an apiKey is set, credentialsJson is
			// empty, the path is written and read back as healthy — while
			// claude-code, which authenticates from the OAuth blob, cannot spawn
			// at all and the init container exits non-zero (#6163 FREEZE 2).
			name:        "key-only",
			apiKey:      "FAKE-not-a-real-api-key",
			credsJSON:   "",
			wantClass:   "key-only",
			wantInError: "credentialsJson field is EMPTY",
		},
		{
			// #4111 records this exact operator error: the bare OAuth token
			// pasted into the credentialsJson field instead of the document.
			name:        "malformed-bare-token",
			apiKey:      "",
			credsJSON:   "FAKE-not-a-real-oauth-token",
			wantClass:   "malformed",
			wantInError: "not a claudeAiOauth document",
		},
		{
			name:        "malformed-json-without-accessToken",
			apiKey:      "",
			credsJSON:   `{"claudeAiOauth":{"refreshToken":"FAKE-not-a-real-refresh-token"}}`,
			wantClass:   "malformed",
			wantInError: "not a claudeAiOauth document",
		},
		{
			// The steady state of a credential nobody rotated: the accessToken
			// lives hours and nothing in this platform refreshes it.
			name:        "expired",
			apiKey:      "FAKE-not-a-real-api-key",
			credsJSON:   expiredAnthropicCredentialsJSON(),
			wantClass:   "expired",
			wantInError: "ROTATE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRWKVServer(t)
			lg, read := capturingLogger()
			h := &Handler{log: lg}
			h.openbao = srv.client()
			t.Setenv(anthropicSeedAPIKeyEnv, tc.apiKey)
			t.Setenv(anthropicSeedCredentialsJSONEnv, tc.credsJSON)

			got := h.seedAnthropicToken(context.Background())

			if got == AnthropicSeedOutcomeSeeded {
				t.Fatalf("a %s credential was reported as %q — INVALID took VALID's verdict, which is the whole defect (#6250)",
					tc.wantClass, got)
			}
			if got != AnthropicSeedOutcomeUnusableSeeded {
				t.Fatalf("outcome = %q, want %q", got, AnthropicSeedOutcomeUnusableSeeded)
			}

			// The value IS written (so the per-Org ExternalSecret syncs and the
			// workspace init container can render the precise verdict from the
			// real bytes) — but never under a green label.
			if w := srv.writes(); len(w) != 1 || w[0] != anthropicSeedSecretPath {
				t.Fatalf("expected exactly one write to %q, got %v", anthropicSeedSecretPath, w)
			}

			errs := recordsAtLevel(read(), "ERROR")
			if len(errs) == 0 {
				t.Fatalf("a credential that cannot authenticate produced no ERROR record — the Sovereign-level signal stays green while every workspace refuses to start")
			}
			// The class must be NAMED. "something is wrong" costs the operator
			// the whole diagnosis; the three causes have three remediations.
			if !anyRecordContains(errs, tc.wantClass) {
				t.Errorf("no ERROR record names the credential class %q. records=%v", tc.wantClass, errs)
			}
			if !anyRecordContains(errs, tc.wantInError) {
				t.Errorf("no ERROR record carries the %s remediation (%q). records=%v", tc.wantClass, tc.wantInError, errs)
			}
		})
	}
}

// Test_seedAnthropicToken_ValidCredentialStillSeeds is THE CONTROL for the test
// above, and it shares the suspect property exactly: same producer, same absent
// OpenBao path at entry, same write. The ONLY difference is that the credential
// can actually authenticate.
//
// Its job is to fail if the new refusal fires on "a credential is configured"
// rather than on "the configured credential cannot work". A guard that refused
// everything would pass every case above and fail here.
func Test_seedAnthropicToken_ValidCredentialStillSeeds(t *testing.T) {
	srv := newRWKVServer(t)
	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()
	t.Setenv(anthropicSeedAPIKeyEnv, "FAKE-not-a-real-api-key")
	t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

	if got := h.seedAnthropicToken(context.Background()); got != AnthropicSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q — a usable credential must still seed", got, AnthropicSeedOutcomeSeeded)
	}
	if errs := recordsAtLevel(read(), "ERROR"); len(errs) != 0 {
		t.Errorf("a usable credential emitted ERROR records — the refusal cannot discriminate valid from invalid: %v", errs)
	}
	if w := srv.writes(); len(w) != 1 || w[0] != anthropicSeedSecretPath {
		t.Fatalf("expected exactly one write to %q, got %v", anthropicSeedSecretPath, w)
	}
}

// Test_seedAnthropicToken_NoExpiresAtIsValid is the second control: a blob with
// no expiresAt is a non-expiring credential, not a defect. It mirrors the
// bp-agenity init container's NOEXP arm, so the two layers cannot disagree
// about the same bytes.
func Test_seedAnthropicToken_NoExpiresAtIsValid(t *testing.T) {
	srv := newRWKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, `{"claudeAiOauth":{"accessToken":"FAKE-not-a-real-access-token"}}`)

	if got := h.seedAnthropicToken(context.Background()); got != AnthropicSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q — no expiresAt means non-expiring, not broken", got, AnthropicSeedOutcomeSeeded)
	}
}

// Test_seedAnthropicToken_AbsentStaysDistinctFromUnusable pins the third leg of
// the three-way split. Absence has its own outcome and its own remediation
// ("supply one"), and adding the unusable class must not swallow it.
func Test_seedAnthropicToken_AbsentStaysDistinctFromUnusable(t *testing.T) {
	srv := newRWKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "")

	got := h.seedAnthropicToken(context.Background())
	if got != AnthropicSeedOutcomeSkippedNoEnv {
		t.Fatalf("outcome = %q, want %q — absent must not be folded into unusable", got, AnthropicSeedOutcomeSkippedNoEnv)
	}
	if w := srv.writes(); len(w) != 0 {
		t.Fatalf("an absent credential must write NOTHING (the ESO empty-seed trap), got %v", w)
	}
}

// ── Producer: a bad rotation must not demote a Sovereign that works ─────────

func Test_seedAnthropicToken_UnusableSourceDoesNotClobberUsableStored(t *testing.T) {
	srv := newRWKVServer(t)
	// The Sovereign works today: the stored path holds a usable credential.
	srv.seedPresent(anthropicSeedSecretPath, map[string]any{
		"apiKey":          "FAKE-not-a-real-api-key",
		"credentialsJson": validAnthropicCredentialsJSON(),
	})
	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()
	// …and the operator fat-fingers the rotation.
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "FAKE-not-a-real-oauth-token")

	got := h.seedAnthropicToken(context.Background())
	if got != AnthropicSeedOutcomeUnusableWithheld {
		t.Fatalf("outcome = %q, want %q", got, AnthropicSeedOutcomeUnusableWithheld)
	}
	if w := srv.writes(); len(w) != 0 {
		t.Fatalf("a working credential was overwritten with an unusable one: writes=%v", w)
	}
	if errs := recordsAtLevel(read(), "ERROR"); len(errs) == 0 || !anyRecordContains(errs, "WITHHELD") {
		t.Errorf("withholding the write was not reported. records=%v", errs)
	}
}

// Test_seedAnthropicToken_UsableSourceOverwritesUsableStored is the control for
// the withhold: rotation of a GOOD credential onto a good one must still land,
// otherwise the guard has frozen the path instead of protecting it.
func Test_seedAnthropicToken_UsableSourceOverwritesUsableStored(t *testing.T) {
	srv := newRWKVServer(t)
	srv.seedPresent(anthropicSeedSecretPath, map[string]any{
		"apiKey":          "FAKE-not-a-real-api-key",
		"credentialsJson": validAnthropicCredentialsJSON(),
	})
	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

	if got := h.seedAnthropicToken(context.Background()); got != AnthropicSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q — a good rotation must still write", got, AnthropicSeedOutcomeSeeded)
	}
	if w := srv.writes(); len(w) != 1 {
		t.Fatalf("expected the rotation to write once, got %v", w)
	}
}

// Test_seedAnthropicToken_UnusableSourceOverwritesUnusableStored is the second
// control on the withhold: there is nothing to protect, so the write proceeds
// and the workspace pod gets the real bytes to diagnose from.
func Test_seedAnthropicToken_UnusableSourceOverwritesUnusableStored(t *testing.T) {
	srv := newRWKVServer(t)
	srv.seedPresent(anthropicSeedSecretPath, map[string]any{
		"apiKey":          "FAKE-not-a-real-api-key",
		"credentialsJson": expiredAnthropicCredentialsJSON(),
	})
	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, expiredAnthropicCredentialsJSON())

	if got := h.seedAnthropicToken(context.Background()); got != AnthropicSeedOutcomeUnusableSeeded {
		t.Fatalf("outcome = %q, want %q — nothing usable was at risk, so the write proceeds", got, AnthropicSeedOutcomeUnusableSeeded)
	}
	if w := srv.writes(); len(w) != 1 {
		t.Fatalf("expected one write, got %v", w)
	}
}

// ── Call site: the ten-minute loop's HEALTH verdict, not a presence check ───

// Test_runSeedReconcilePass_StoredCredentialThatCannotWorkIsNotHealthy drives
// runSeedReconcilePass — the production call site, with the production props
// and predicate — rather than reconcileGlobalSeed with hand-passed arguments.
// That distinction matters here: the defect WAS the argument list at this call
// site (props were {"apiKey","credentialsJson"} and the presence check is an
// OR, so an apiKey with an empty credentialsJson read healthy), and a test that
// passes its own props could never have seen it.
func Test_runSeedReconcilePass_StoredCredentialThatCannotWorkIsNotHealthy(t *testing.T) {
	cases := []struct {
		name   string
		stored map[string]any
	}{
		{
			name:   "key-only",
			stored: map[string]any{"apiKey": "FAKE-not-a-real-api-key", "credentialsJson": ""},
		},
		{
			name:   "expired",
			stored: map[string]any{"apiKey": "", "credentialsJson": expiredAnthropicCredentialsJSON()},
		},
		{
			name:   "malformed",
			stored: map[string]any{"apiKey": "", "credentialsJson": "FAKE-not-a-real-oauth-token"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRWKVServer(t)
			// newapi present so its leg no-ops and cannot reach the cluster.
			srv.seedPresent("catalyst/newapi/admin-token", map[string]any{"ADMIN_API_TOKEN": "FAKE-not-a-real-token"})
			srv.seedPresent(anthropicSeedSecretPath, tc.stored)

			h := &Handler{log: silentLogger()}
			h.openbao = srv.client()
			// A usable credential IS available to heal with.
			t.Setenv(anthropicSeedAPIKeyEnv, "")
			t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

			h.runSeedReconcilePass(context.Background())

			healed := false
			for _, p := range srv.writes() {
				if p == anthropicSeedSecretPath {
					healed = true
				}
			}
			if !healed {
				t.Fatalf("a stored %s credential read as HEALTHY — the loop went silent over a Sovereign whose every Agenity workspace refuses to start (#6250). writes=%v",
					tc.name, srv.writes())
			}
		})
	}
}

// Test_runSeedReconcilePass_UsableStoredCredentialIsSilent is THE CONTROL for
// the call-site test: same pass, same predicate, same props — the only
// difference is that the stored credential works. It must produce ZERO writes.
//
// Without it, "re-seeds a stored credential that cannot work" is
// indistinguishable from "re-seeds on every tick", which would churn the
// OpenBao value → ExternalSecret → per-Org Secret chain the no-churn rule in
// sovereign_seed_reconciler.go exists to prevent.
func Test_runSeedReconcilePass_UsableStoredCredentialIsSilent(t *testing.T) {
	srv := newRWKVServer(t)
	srv.seedPresent("catalyst/newapi/admin-token", map[string]any{"ADMIN_API_TOKEN": "FAKE-not-a-real-token"})
	srv.seedPresent(anthropicSeedSecretPath, map[string]any{
		"apiKey":          "FAKE-not-a-real-api-key",
		"credentialsJson": validAnthropicCredentialsJSON(),
	})

	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

	h.runSeedReconcilePass(context.Background())

	for _, p := range srv.writes() {
		if p == anthropicSeedSecretPath {
			t.Fatalf("a HEALTHY anthropic path was re-written (churn) — the health predicate cannot tell usable from unusable. writes=%v", srv.writes())
		}
	}
	if anyRecordContains(recordsAtLevel(read(), "ERROR"), "anthropic") {
		t.Errorf("a healthy anthropic path produced an ERROR record — the loud line fires on the leg rather than on the verdict")
	}
}

// Test_runSeedReconcilePass_UnhealableStoredCredentialStaysLoud closes the
// loop the operator actually reads: a stored credential that cannot work AND
// no usable source to heal it with must keep reporting unhealed every tick,
// carrying the rotation remediation. Silence here is what let hw295 sit red.
func Test_runSeedReconcilePass_UnhealableStoredCredentialStaysLoud(t *testing.T) {
	srv := newRWKVServer(t)
	srv.seedPresent("catalyst/newapi/admin-token", map[string]any{"ADMIN_API_TOKEN": "FAKE-not-a-real-token"})
	srv.seedPresent(anthropicSeedSecretPath, map[string]any{
		"apiKey":          "",
		"credentialsJson": expiredAnthropicCredentialsJSON(),
	})

	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()
	// The source is the same expired blob — rotation never happened.
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, expiredAnthropicCredentialsJSON())

	h.runSeedReconcilePass(context.Background())

	errs := recordsAtLevel(read(), "ERROR")
	if !anyRecordContains(errs, "self-heal did NOT take") {
		t.Errorf("an unhealable anthropic path was not reported as unhealed. records=%v", errs)
	}
	if !anyRecordContains(errs, "ROTATE") {
		t.Errorf("the unhealed report never names ROTATE, so the operator is told to CREATE a Secret that already exists. records=%v", errs)
	}
}

// ── The classifier's own boundary cases, on top of the call-site coverage ───

// Test_classifyAnthropicCredential_Boundaries pins the parses that the call
// site cannot vary cheaply: expiresAt carried as a JSON string, and the exact
// now/expiry boundary. These supplement the call-site tests above; they do not
// replace them.
func Test_classifyAnthropicCredential_Boundaries(t *testing.T) {
	future := time.Now().Add(6 * time.Hour).UnixMilli()
	cases := []struct {
		name      string
		apiKey    string
		credsJSON string
		want      anthropicCredentialClass
	}{
		{"expiresAt-as-string", "", fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"FAKE-not-a-real-access-token","expiresAt":"%d"}}`, future), anthropicCredValid},
		{"expiresAt-zero-is-non-expiring", "", `{"claudeAiOauth":{"accessToken":"FAKE-not-a-real-access-token","expiresAt":0}}`, anthropicCredValid},
		{"epoch-seconds-mistaken-for-millis-reads-expired", "", `{"claudeAiOauth":{"accessToken":"FAKE-not-a-real-access-token","expiresAt":1750000000}}`, anthropicCredExpired},
		{"whitespace-only-credentialsJson-is-key-only", "FAKE-not-a-real-api-key", "   ", anthropicCredKeyOnly},
		{"empty-object", "", `{}`, anthropicCredMalformed},
		{"oauth-under-wrong-key", "", `{"anthropicOauth":{"accessToken":"FAKE-not-a-real-access-token"}}`, anthropicCredMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := classifyAnthropicCredential(tc.apiKey, tc.credsJSON)
			if got != tc.want {
				t.Fatalf("class = %q, want %q (detail=%q)", got, tc.want, detail)
			}
			// Credential hygiene (docs/PRINCIPLES.md #10): the detail rides on a
			// log line, so it must never carry the material.
			if detail != "" && (containsAny(detail, "FAKE-not-a-real-access-token", "FAKE-not-a-real-api-key")) {
				t.Errorf("detail leaks credential material: %q", detail)
			}
		})
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if len(n) > 0 && len(s) >= len(n) {
			for i := 0; i+len(n) <= len(s); i++ {
				if s[i:i+len(n)] == n {
					return true
				}
			}
		}
	}
	return false
}
