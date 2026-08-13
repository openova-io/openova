package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// #4277 — the seed reconciler must not report a heal it never observed.
//
// THE LIVE DEFECT this file pins. On hw293 the ExternalSecret
// `<org>/agenity-anthropic-token` reads
//
//	SecretSyncedError / Ready=False
//	  error retrieving secret at .data[0], key: catalyst/anthropic/token,
//	  err: Secret does not exist
//
// in every Organization namespace, while its two siblings on the SAME
// ClusterSecretStore (`agenity-mcp-bearer` on catalyst/agenity/<slug>/mcp-bearer
// and `oidc-gate-agenity-<slug>-oidc` on sso/sovereign/…) read
// SecretSynced/Ready=True. Store, auth and controller all work; exactly one
// remote path was never written.
//
// What made that survivable for the life of the cluster is the reconciler's own
// reporting. reconcileGlobalSeed logged
//
//	"[SEED-RECONCILE] OpenBao path absent — self-healing (#4877)"
//
// then called the producer and returned, never looking at the path again. Every
// producer it drives is deliberately non-fatal, so that INFO line was emitted
// identically whether the write landed or the producer did nothing at all —
// and on a Sovereign holding no Anthropic credential the producer does nothing,
// every ten minutes, forever. The gap was not unreported; it was reported as
// progress.
//
// The tests below assert the two halves that make a verdict honest: an outcome
// is only "healed" when a read-back says so, and the not-healed case is LOUD
// and names the remediation. Each carries a control that shares the suspect
// property — the path is ABSENT AT ENTRY in the control too — so a guard that
// simply always fired would fail the control rather than pass everything.
// ─────────────────────────────────────────────────────────────────────────────

// logRecord — one captured slog record, flattened for assertions.
type logRecord struct {
	Level string
	Msg   string
	Attrs map[string]any
}

// capturingLogger returns a logger writing JSON into buf plus a reader that
// parses what has been written so far.
func capturingLogger() (*slog.Logger, func() []logRecord) {
	buf := &bytes.Buffer{}
	lg := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return lg, func() []logRecord {
		var out []logRecord
		sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			rec := logRecord{Attrs: map[string]any{}}
			for k, v := range m {
				switch k {
				case "level":
					rec.Level, _ = v.(string)
				case "msg":
					rec.Msg, _ = v.(string)
				case "time":
				default:
					rec.Attrs[k] = v
				}
			}
			out = append(out, rec)
		}
		return out
	}
}

// text renders a record as one searchable blob (message + every attr value) so
// an assertion cannot pass merely because a key exists — #5639's lesson: assert
// on the VALUE, not the key.
func (r logRecord) text() string {
	var b strings.Builder
	b.WriteString(r.Msg)
	for _, v := range r.Attrs {
		b.WriteString(" ")
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

func recordsAtLevel(recs []logRecord, level string) []logRecord {
	var out []logRecord
	for _, r := range recs {
		if r.Level == level {
			out = append(out, r)
		}
	}
	return out
}

func anyRecordContains(recs []logRecord, needle string) bool {
	for _, r := range recs {
		if strings.Contains(r.text(), needle) {
			return true
		}
	}
	return false
}

// Test_reconcileGlobalSeed_UnhealedGapIsLoudAndNotAnnouncedAsHealed is the fix.
//
// Fixture = the exact hw293 state: the anthropic path is ABSENT and the
// platform holds NO Anthropic credential, so the producer is a no-op. The pass
// must (a) return SeedHealUnhealed, (b) emit an ERROR that names the concrete
// remediation, and (c) never emit a line an operator could read as a completed
// heal.
func Test_reconcileGlobalSeed_UnhealedGapIsLoudAndNotAnnouncedAsHealed(t *testing.T) {
	srv := newRWKVServer(t)
	// The path is absent — nothing seeded into the fake KV.
	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()

	// No platform Anthropic credential — seedAnthropicToken loud-skips and
	// writes nothing. This is the live hw293 condition.
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "")

	got := h.reconcileGlobalSeed(context.Background(),
		"anthropic", anthropicSeedMountPath, anthropicSeedSecretPath,
		[]string{"apiKey", "credentialsJson"},
		func() { _ = h.seedAnthropicToken(context.Background()) },
	)

	if got != SeedHealUnhealed {
		t.Errorf("outcome = %q, want %q — the path is still absent after the producer ran, so the pass must not claim anything else (#4277)", got, SeedHealUnhealed)
	}

	recs := read()

	// (a) The pass must have re-read the path rather than assuming. A verdict
	// without a read-back is a verdict from absent evidence.
	if !anyRecordContains(recs, "self-heal did NOT take") {
		t.Errorf("the reconcile pass never reported the unhealed gap — it ran the producer and returned without verifying, which is exactly the #4277 defect. records=%v", recs)
	}

	// (b) LOUD: the unhealed gap must be ERROR level, not INFO/WARN. This is
	// the property that makes it findable in a Sovereign's log.
	errs := recordsAtLevel(recs, "ERROR")
	if len(errs) == 0 {
		t.Errorf("the unhealed gap produced no ERROR-level record — it is a permanently broken delivery, not a transient. records=%v", recs)
	}

	// (c) The line must name the ACTION, not merely the fault. Assert on the
	// remediation's value, reached through the record text.
	if !anyRecordContains(errs, "CATALYST_ANTHROPIC_CREDENTIALS_JSON") {
		t.Errorf("no ERROR record names the env var that closes the gap; an operator is told something is wrong but not what to do. errors=%v", errs)
	}
	if !anyRecordContains(errs, "SecretSyncedError") {
		t.Errorf("no ERROR record names the downstream consequence (the per-Organization ExternalSecret), so the log never connects to the symptom an operator sees. errors=%v", errs)
	}

	// (d) Nothing may read as a completed heal.
	if anyRecordContains(recs, "self-heal complete") {
		t.Errorf("the pass announced a completed heal while the path is still absent. records=%v", recs)
	}
	// The pre-fix announcement is the specific wording that made this
	// survivable; it must not come back.
	for _, r := range recs {
		if strings.Contains(r.Msg, "absent — self-healing") {
			t.Errorf("the pre-fix announcement %q is back: it states an outcome the pass has not observed (#4277)", r.Msg)
		}
	}

	// (e) And no write can have landed — the producer had nothing to write.
	if w := srv.writes(); len(w) != 0 {
		t.Errorf("expected zero writes from a credential-less producer, got %v", w)
	}
}

// Test_reconcileGlobalSeed_SuccessfulHealNeverErrors is THE CONTROL, and it is
// GREEN BOTH BEFORE AND AFTER the fix by construction.
//
// It shares the suspect property with the unhealed test exactly: the OpenBao
// path is ABSENT AT ENTRY, so the pass takes the identical branch, runs the
// identical producer and reaches the identical new code. The only difference is
// that the platform holds a credential, so the write lands.
//
// Its whole job is to fail if the new loud-error path fires on entering the
// absent branch rather than on the observed outcome. A guard that cannot
// distinguish the two would pass the unhealed test and fail here; a guard that
// always fired would do the same. Passing before the fix (there was no error
// path at all) and after it (there is one, and it stays quiet) is what makes it
// a control rather than a second assertion of the fix.
func Test_reconcileGlobalSeed_SuccessfulHealNeverErrors(t *testing.T) {
	srv := newRWKVServer(t)
	// SAME suspect property as the unhealed test: path absent at entry.
	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()

	// The only difference: a platform credential exists, so the seed writes.
	// #6250: an apiKey alone cannot authenticate an agent, so it is no longer a
	// credential that "exists" for the purposes of a successful seed.
	t.Setenv(anthropicSeedAPIKeyEnv, "test-only-not-a-real-credential")
	t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

	h.reconcileGlobalSeed(context.Background(),
		"anthropic", anthropicSeedMountPath, anthropicSeedSecretPath,
		[]string{"apiKey", "credentialsJson"},
		func() { _ = h.seedAnthropicToken(context.Background()) },
	)

	if errs := recordsAtLevel(read(), "ERROR"); len(errs) != 0 {
		t.Errorf("a SUCCESSFUL self-heal emitted ERROR records — the loud path fires on the absent branch itself rather than on the outcome, so it cannot discriminate: %v", errs)
	}
	writes := srv.writes()
	if len(writes) != 1 || writes[0] != anthropicSeedSecretPath {
		t.Fatalf("expected exactly one write to %q, got %v", anthropicSeedSecretPath, writes)
	}
}

// Test_reconcileGlobalSeed_HealedGapIsVerifiedAndQuiet pins the positive half
// of the fix: a heal that DID take is reported as complete, and that report is
// backed by the read-back rather than by having called the producer.
func Test_reconcileGlobalSeed_HealedGapIsVerifiedAndQuiet(t *testing.T) {
	srv := newRWKVServer(t)
	// SAME suspect property as the unhealed test: path absent at entry.
	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()

	// The only difference: a platform credential exists, so the seed writes.
	// #6250: an apiKey alone cannot authenticate an agent, so it is no longer a
	// credential that "exists" for the purposes of a successful seed.
	t.Setenv(anthropicSeedAPIKeyEnv, "test-only-not-a-real-credential")
	t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

	got := h.reconcileGlobalSeed(context.Background(),
		"anthropic", anthropicSeedMountPath, anthropicSeedSecretPath,
		[]string{"apiKey", "credentialsJson"},
		func() { _ = h.seedAnthropicToken(context.Background()) },
	)

	if got != SeedHealHealed {
		t.Errorf("outcome = %q, want %q — the write landed and the read-back proves it", got, SeedHealHealed)
	}

	recs := read()
	if errs := recordsAtLevel(recs, "ERROR"); len(errs) != 0 {
		t.Errorf("a SUCCESSFUL self-heal emitted ERROR records — the loud path fires on the absent branch itself rather than on the outcome, so it cannot discriminate: %v", errs)
	}
	if !anyRecordContains(recs, "self-heal complete") {
		t.Errorf("a verified heal was never reported as complete. records=%v", recs)
	}

	// The heal is real, not just claimed.
	writes := srv.writes()
	if len(writes) != 1 || writes[0] != anthropicSeedSecretPath {
		t.Fatalf("expected exactly one write to %q, got %v", anthropicSeedSecretPath, writes)
	}
}

// Test_reconcileGlobalSeed_HealthyPathIsSilent is the second control, on the
// other discriminator: a path that was ALREADY healthy must produce no writes
// and no log records at all. A reconciler that narrated healthy passes would
// bury the loud line the first test asserts.
func Test_reconcileGlobalSeed_HealthyPathIsSilent(t *testing.T) {
	srv := newRWKVServer(t)
	srv.seedPresent(anthropicSeedSecretPath, map[string]any{
		"apiKey":          "test-only-not-a-real-credential",
		"credentialsJson": "",
	})
	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "")

	got := h.reconcileGlobalSeed(context.Background(),
		"anthropic", anthropicSeedMountPath, anthropicSeedSecretPath,
		[]string{"apiKey", "credentialsJson"},
		func() { _ = h.seedAnthropicToken(context.Background()) },
	)

	if got != SeedHealHealthy {
		t.Errorf("outcome = %q, want %q", got, SeedHealHealthy)
	}
	if recs := read(); len(recs) != 0 {
		t.Errorf("a healthy path must be silent (no churn, no noise), got %v", recs)
	}
	if w := srv.writes(); len(w) != 0 {
		t.Errorf("a healthy path must never be re-written, got %v", w)
	}
}

// Test_seedAnthropicToken_CredentialGapIsErrorAtProvisionTime covers the
// PROVISION-TIME half of the same defect, at the producer itself.
//
// runOrganizationPipeline (organization_provisioning.go, Step 1) calls
// seedAnthropicToken on every Org-create and, by design, does not fail the
// pipeline on a credential gap. Raising the severity inside the producer is
// what makes the gap loud at provision time for EVERY caller — the Org-create
// pipeline, the catalyst-api startup pass and the periodic reconciler — without
// any call site having to remember to inspect the returned outcome.
//
// A permanently unwritten path is not a warning: it leaves every Organization's
// agenity-anthropic-token ExternalSecret SecretSyncedError with no path to
// recovery that does not involve a human supplying a credential.
func Test_seedAnthropicToken_CredentialGapIsErrorAtProvisionTime(t *testing.T) {
	srv := newRWKVServer(t)
	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()
	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "")

	if out := h.seedAnthropicToken(context.Background()); out != AnthropicSeedOutcomeSkippedNoEnv {
		t.Fatalf("outcome = %q, want %q", out, AnthropicSeedOutcomeSkippedNoEnv)
	}

	recs := read()
	errs := recordsAtLevel(recs, "ERROR")
	if len(errs) == 0 {
		t.Errorf("the credential gap was not raised at ERROR — at WARN it reads as a transient the platform will retry past, which is how it survived on a live Sovereign for the life of the cluster (#4277). records=%v", recs)
	}
	if !anyRecordContains(errs, "CATALYST_ANTHROPIC_CREDENTIALS_JSON") {
		t.Errorf("the provision-time gap does not name the env var that closes it. errors=%v", errs)
	}
	if !anyRecordContains(errs, "SecretSyncedError") {
		t.Errorf("the provision-time gap does not name the downstream symptom an operator will actually see. errors=%v", errs)
	}
}

// Test_seedAnthropicToken_SeededPathIsNotAnError is THE CONTROL for the test
// above and shares its suspect property — the same producer, the same call, the
// same absent OpenBao path at entry. Only the credential differs.
//
// It pins that raising the severity did not turn the producer into something
// that shouts on every invocation: a Sovereign that HAS a credential must run
// this path on every Org-create with no error at all.
func Test_seedAnthropicToken_SeededPathIsNotAnError(t *testing.T) {
	srv := newRWKVServer(t)
	lg, read := capturingLogger()
	h := &Handler{log: lg}
	h.openbao = srv.client()
	// #6250: an apiKey alone cannot authenticate an agent, so it is no longer a
	// credential that "exists" for the purposes of a successful seed.
	t.Setenv(anthropicSeedAPIKeyEnv, "test-only-not-a-real-credential")
	t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

	if out := h.seedAnthropicToken(context.Background()); out != AnthropicSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q", out, AnthropicSeedOutcomeSeeded)
	}
	if errs := recordsAtLevel(read(), "ERROR"); len(errs) != 0 {
		t.Errorf("a successful seed logged ERROR records — the severity bump fires on invocation rather than on the gap: %v", errs)
	}
}
