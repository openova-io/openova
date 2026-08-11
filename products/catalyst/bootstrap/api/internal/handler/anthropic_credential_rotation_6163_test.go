package handler

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// #6163 — the Anthropic credential the seed writes must come from the
// operator-rotatable Secret, LIVE, not from the process env snapshot.
//
// THE DEFECT
//
//	CATALYST_ANTHROPIC_API_KEY / CATALYST_ANTHROPIC_CREDENTIALS_JSON are
//	secretKeyRef env vars on the catalyst-api Deployment, and a container's
//	environment is materialised ONCE at container start. seedAnthropicToken
//	read them with os.Getenv on every pass, so the ten-minute self-heal loop
//	re-wrote, forever, the credential this process booted with.
//
//	The seeded value is a claudeAiOauth pair whose accessToken lives for
//	HOURS. When it expires the operator edits
//	catalyst-system/sovereign-anthropic-credentials — and nothing changed,
//	because the running process could not see the edit. Rotation only took
//	effect on a catalyst-api roll, which nothing triggers. That is an outage
//	on a timer, and it is what the "seed reconciler runs every 10 minutes"
//	surface could never fix: a loop that re-applies a stale snapshot is not
//	a self-heal, it is a self-repeat.
//
// CREDENTIAL HYGIENE (docs/PRINCIPLES.md #10): every fixture below is an
// obviously-fake literal. No real key, token or OAuth blob appears in this
// file, and no assertion prints credential material — only byte lengths and
// outcome classes.

// Deliberately non-credential-shaped fixtures. "ROTATED" vs "STALE" is the
// whole experiment: the test asserts WHICH ONE reached OpenBao.
const (
	fake6163StaleCredsJSON   = `{"claudeAiOauth":{"accessToken":"FAKE-STALE-BOOT-SNAPSHOT","refreshToken":"FAKE","expiresAt":1,"scopes":["user:inference"]}}`
	fake6163RotatedCredsJSON = `{"claudeAiOauth":{"accessToken":"FAKE-ROTATED-BY-OPERATOR","refreshToken":"FAKE","expiresAt":2,"scopes":["user:inference"]}}`
	fake6163StaleAPIKey      = "FAKE-STALE-BOOT-SNAPSHOT-KEY"
	fake6163RotatedAPIKey    = "FAKE-ROTATED-BY-OPERATOR-KEY"
)

// newSeedHandler6163 wires a Handler against the capture-KV fake OpenBao and,
// optionally, a fake apiserver holding the operator Secret.
func newSeedHandler6163(t *testing.T, secret *corev1.Secret) (*Handler, *captureKVServer) {
	t.Helper()
	srv := newCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}
	var cs *k8sfake.Clientset
	if secret != nil {
		cs = k8sfake.NewSimpleClientset(secret)
	} else {
		cs = k8sfake.NewSimpleClientset()
	}
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: cs}, nil
	})
	// Pin the namespace so the test never depends on the CI runner having (or
	// not having) a ServiceAccount namespace file.
	t.Setenv(envAnthropicCredentialNamespace, "catalyst-system")
	return h, srv
}

func operatorSecret6163(apiKey, credsJSON string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sovereignAnthropicSecretName,
			Namespace: "catalyst-system",
		},
		Data: map[string][]byte{
			sovereignAnthropicSecretAPIKeyKey:    []byte(apiKey),
			sovereignAnthropicSecretCredsJSONKey: []byte(credsJSON),
		},
	}
}

// Test_6163_RotatedSecretWinsOverBootTimeEnv is the red-before test.
//
// The process env holds what this Pod booted with; the operator has since
// rotated the Secret. The seed must write the ROTATED credential.
func Test_6163_RotatedSecretWinsOverBootTimeEnv(t *testing.T) {
	h, srv := newSeedHandler6163(t, operatorSecret6163(fake6163RotatedAPIKey, fake6163RotatedCredsJSON))

	// The boot-time snapshot — what the container's env still holds.
	t.Setenv(anthropicSeedAPIKeyEnv, fake6163StaleAPIKey)
	t.Setenv(anthropicSeedCredentialsJSONEnv, fake6163StaleCredsJSON)

	// VACUITY: the two credentials must actually differ, or "the right one
	// was written" is unfalsifiable.
	if fake6163StaleCredsJSON == fake6163RotatedCredsJSON || fake6163StaleAPIKey == fake6163RotatedAPIKey {
		t.Fatal("vacuity: the stale and rotated fixtures are identical — this test could not distinguish them")
	}

	if out := h.seedAnthropicToken(context.Background()); out != AnthropicSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q", out, AnthropicSeedOutcomeSeeded)
	}

	// VACUITY: the fake must have actually received a write.
	got, ok := srv.dataField("credentialsJson")
	if !ok {
		t.Fatal("vacuity: OpenBao fake recorded no credentialsJson field — the assertion below would pass on an empty write")
	}

	if got == fake6163StaleCredsJSON {
		t.Fatalf("seed wrote the BOOT-TIME env snapshot, not the rotated operator Secret.\n"+
			"secretKeyRef env is materialised once at container start, so re-reading os.Getenv every pass "+
			"re-applies a credential the operator already replaced — rotation then needs a catalyst-api roll, "+
			"and an expiring OAuth token re-creates the outage on a timer (#6163).\n"+
			"wrote %d bytes matching the STALE fixture; wanted the %d-byte ROTATED one.",
			len(got), len(fake6163RotatedCredsJSON))
	}
	if got != fake6163RotatedCredsJSON {
		t.Fatalf("seed wrote neither fixture: %d bytes (stale=%d rotated=%d)",
			len(got), len(fake6163StaleCredsJSON), len(fake6163RotatedCredsJSON))
	}
	if key, _ := srv.dataField("apiKey"); key != fake6163RotatedAPIKey {
		t.Fatalf("apiKey came from the wrong seam: got %d bytes, want the %d-byte rotated value",
			len(key), len(fake6163RotatedAPIKey))
	}
}

// Test_6163_ResolvePrefersSecretReportsSource asserts the log-facing source
// class, so an operator reading "credentialSource=process-env" on a Sovereign
// that HAS the Secret knows the live read failed rather than guessing.
func Test_6163_ResolvePrefersSecretReportsSource(t *testing.T) {
	h, _ := newSeedHandler6163(t, operatorSecret6163(fake6163RotatedAPIKey, fake6163RotatedCredsJSON))
	t.Setenv(anthropicSeedAPIKeyEnv, fake6163StaleAPIKey)
	t.Setenv(anthropicSeedCredentialsJSONEnv, fake6163StaleCredsJSON)

	_, creds, src := h.resolveAnthropicCredential(context.Background())
	if src != anthropicCredentialFromSecret {
		t.Fatalf("credential source = %q, want %q — the live operator Secret must win over the boot-time env",
			src, anthropicCredentialFromSecret)
	}
	if creds != fake6163RotatedCredsJSON {
		t.Fatalf("resolved credential is not the rotated one (%d bytes)", len(creds))
	}
}

// ── CONTROL 1 ───────────────────────────────────────────────────────────────
// Same function, same OpenBao fake, same env — the ONLY difference is that no
// operator Secret exists. This is the pre-#6163 world and every Sovereign that
// has not adopted the Secret seam, so it must behave EXACTLY as before: seed
// from the env. Green before AND after the fix; if the change had simply
// rewired everything to the Secret, this goes red.
func Test_6163_CONTROL_NoOperatorSecretStillSeedsFromEnv(t *testing.T) {
	h, srv := newSeedHandler6163(t, nil)
	t.Setenv(anthropicSeedAPIKeyEnv, fake6163StaleAPIKey)
	t.Setenv(anthropicSeedCredentialsJSONEnv, fake6163StaleCredsJSON)

	if out := h.seedAnthropicToken(context.Background()); out != AnthropicSeedOutcomeSeeded {
		t.Fatalf("CONTROL: outcome = %q, want %q — a Sovereign with no operator Secret must still seed from the env",
			out, AnthropicSeedOutcomeSeeded)
	}
	got, ok := srv.dataField("credentialsJson")
	if !ok || got != fake6163StaleCredsJSON {
		t.Fatalf("CONTROL: env-sourced seed regressed — wrote %d bytes, want the %d-byte env value",
			len(got), len(fake6163StaleCredsJSON))
	}
	if _, _, src := h.resolveAnthropicCredential(context.Background()); src != anthropicCredentialFromEnv {
		t.Fatalf("CONTROL: source = %q, want %q", src, anthropicCredentialFromEnv)
	}
}

// ── CONTROL 2 ───────────────────────────────────────────────────────────────
// An operator Secret that EXISTS but is empty must not be read as a credential.
// This is the empty-seed trap the agenity README warns about: an empty Secret
// is presence without usability, and preferring it over a working env value
// would break a Sovereign that was fine. Green before and after.
func Test_6163_CONTROL_EmptyOperatorSecretFallsBackToEnv(t *testing.T) {
	h, srv := newSeedHandler6163(t, operatorSecret6163("", "   "))
	t.Setenv(anthropicSeedAPIKeyEnv, fake6163StaleAPIKey)
	t.Setenv(anthropicSeedCredentialsJSONEnv, fake6163StaleCredsJSON)

	if out := h.seedAnthropicToken(context.Background()); out != AnthropicSeedOutcomeSeeded {
		t.Fatalf("CONTROL: outcome = %q, want %q", out, AnthropicSeedOutcomeSeeded)
	}
	got, _ := srv.dataField("credentialsJson")
	if got != fake6163StaleCredsJSON {
		t.Fatalf("CONTROL: a hollow operator Secret was preferred over a usable env credential — that is the empty-seed trap (%d bytes written)", len(got))
	}
}

// ── CONTROL 3 ───────────────────────────────────────────────────────────────
// No Secret, no env: the founder-supplied gap. Must still be the LOUD skip
// (#4277), never a silent seed of empty bytes. Green before and after.
func Test_6163_CONTROL_NoCredentialAnywhereStaysLoudSkip(t *testing.T) {
	h, _ := newSeedHandler6163(t, nil)
	lg, records := capturingLogger()
	h.log = lg

	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "")

	if out := h.seedAnthropicToken(context.Background()); out != AnthropicSeedOutcomeSkippedNoEnv {
		t.Fatalf("CONTROL: outcome = %q, want %q — no credential anywhere must be a loud skip, not a seed",
			out, AnthropicSeedOutcomeSkippedNoEnv)
	}
	errs := recordsAtLevel(records(), "ERROR")
	if !anyRecordContains(errs, "anthropic seed SKIPPED") {
		t.Fatalf("CONTROL: the founder-supplied gap stopped being reported at ERROR. Records: %+v", records())
	}
	// The remediation must now name BOTH seams: an operator whose actual fix is
	// "edit the Secret" was previously only ever told about the env var.
	if !anyRecordContains(errs, sovereignAnthropicSecretName) {
		t.Fatalf("CONTROL: the loud line does not name the operator-rotatable Secret %q, so it does not tell the operator where to act. Records: %+v",
			sovereignAnthropicSecretName, errs)
	}
}

// ── VACUITY GUARD ───────────────────────────────────────────────────────────
// The whole suite rests on the fake apiserver actually serving the Secret. If
// the fixture were unreadable, Test_6163_RotatedSecretWinsOverBootTimeEnv
// would fail for a plumbing reason rather than the reason it claims, and the
// CONTROLs would pass for the wrong reason. Prove the read works standalone.
func Test_6163_VACUITY_FakeSecretIsActuallyReadable(t *testing.T) {
	h, _ := newSeedHandler6163(t, operatorSecret6163(fake6163RotatedAPIKey, fake6163RotatedCredsJSON))
	key, creds, err := h.readAnthropicCredentialSecret(context.Background())
	if err != nil {
		t.Fatalf("vacuity: the fake operator Secret is not readable (%v) — every assertion in this file would be measuring the env fallback instead", err)
	}
	if key != fake6163RotatedAPIKey || creds != fake6163RotatedCredsJSON {
		t.Fatalf("vacuity: the fake Secret returned unexpected content (%d/%d bytes)", len(key), len(creds))
	}
	// And prove the negative case is distinguishable: no Secret must error.
	hEmpty, _ := newSeedHandler6163(t, nil)
	if _, _, err := hEmpty.readAnthropicCredentialSecret(context.Background()); err == nil {
		t.Fatal("vacuity: reading a NON-EXISTENT operator Secret returned no error — the reader cannot distinguish present from absent, so CONTROL 1 proves nothing")
	}
}
