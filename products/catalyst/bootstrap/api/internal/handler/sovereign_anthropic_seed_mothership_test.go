package handler

// sovereign_anthropic_seed_mothership_test.go — #4277 / #4111.
//
// THE SUBJECT IS THE CALL SITE. seedSovereignAnthropicCredentials is a
// helper nobody reaches by hand; what has to hold is that a real
// `PUT /api/v1/deployments/{id}/kubeconfig` — the cloud-init postback
// that every fresh Sovereign performs exactly once — lands the Secret on
// the new cluster. So every behavioural case below drives
// h.PutKubeconfig and then inspects the fake Sovereign's apiserver.
// A test that called the helper directly would keep passing if the call
// in kubeconfig.go were deleted, which is precisely the way this seam
// went missing in the first place.
//
// CREDENTIAL SAFETY: every value here is obviously synthetic. This repo
// is public; no fixture may be a plausible credential.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	fakek8s "k8s.io/client-go/kubernetes/fake"
)

// Obviously-fake fixture values. Never a real key shape that could be
// mistaken for one during a secret scan.
const (
	fakeAnthropicAPIKey = "sk-ant-FAKE-DO-NOT-USE-test-fixture-only"
	fakeAccessToken     = "FAKE-ACCESS-TOKEN-NOT-A-CREDENTIAL"
	fakeRefreshToken    = "FAKE-REFRESH-TOKEN-NOT-A-CREDENTIAL"
)

// fakeClaudeOAuthBlob renders a claudeAiOauth document whose accessToken
// expires `in` from now. A negative duration yields an EXPIRED blob.
//
// expiresAt is computed, never a literal. A hardcoded epoch is how a
// fixture silently becomes expired and then starts asserting that an
// expired credential is a good one — the exact defect #6234 fixed.
func fakeClaudeOAuthBlob(t *testing.T, in time.Duration) string {
	t.Helper()
	blob := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  fakeAccessToken,
			"refreshToken": fakeRefreshToken,
			"expiresAt":    time.Now().Add(in).UnixMilli(),
			"scopes":       []string{"user:inference"},
		},
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		t.Fatalf("marshal fake oauth blob: %v", err)
	}
	return string(raw)
}

// withMothershipAnthropicEnv sets the mothership-side credential env for
// one test.
func withMothershipAnthropicEnv(t *testing.T, apiKey, credsJSON string) {
	t.Helper()
	t.Setenv(anthropicSeedAPIKeyEnv, apiKey)
	t.Setenv(anthropicSeedCredentialsJSONEnv, credsJSON)
}

// putKubeconfigWithFakeSovereign drives the real PUT handler with a fake
// Sovereign apiserver injected, and returns that fake so the test can
// inspect what the seed wrote. `seed` pre-populates the fake (used by
// the idempotence case).
func putKubeconfigWithFakeSovereign(t *testing.T, query string, seed ...*corev1.Secret) kubernetes.Interface {
	t.Helper()
	h, _, id, bearer := makePutFixture(t, "phase1-watching")

	objs := make([]runtime.Object, 0, len(seed))
	for _, s := range seed {
		objs = append(objs, s)
	}
	fake := fakek8s.NewSimpleClientset(objs...)
	h.SetSovereignAnthropicSeedClientFactory(func(string) (kubernetes.Interface, error) {
		return fake, nil
	})
	// The SMTP seed shares this hot path; give it a sink of its own so
	// its own env-driven behaviour never colours this test's fake.
	h.SetSovereignSMTPSeedClientFactory(func(string) (kubernetes.Interface, error) {
		return fakek8s.NewSimpleClientset(), nil
	})

	r := putReq(t, id, bearer, []byte(validKubeconfigYAML))
	if query != "" {
		r.URL.RawQuery = query
	}
	w := httptest.NewRecorder()
	h.PutKubeconfig(w, r)
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("PUT kubeconfig: status = %d, body = %s", w.Code, w.Body.String())
	}
	return fake
}

// getSeededSecret returns the seeded Secret, or (nil, false).
func getSeededSecret(t *testing.T, cli kubernetes.Interface) (*corev1.Secret, bool) {
	t.Helper()
	sec, err := cli.CoreV1().Secrets(anthropicCredentialNamespace).
		Get(t.Context(), anthropicCredentialSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get seeded Secret: %v", err)
	}
	return sec, true
}

// ── the gap this closes ─────────────────────────────────────────────

// TestPutKubeconfig_SeedsAnthropicCredentialOnTheNewSovereign — THE
// call-site pin.
//
// Before this seam existed, catalyst-system/sovereign-anthropic-
// credentials had two readers and no writer: it appeared on a Sovereign
// only when a human ran kubectl there. This asserts the cloud-init
// postback now creates it, with the two keys the chart lookup and the
// live reader both index.
func TestPutKubeconfig_SeedsAnthropicCredentialOnTheNewSovereign(t *testing.T) {
	withMothershipAnthropicEnv(t, fakeAnthropicAPIKey, fakeClaudeOAuthBlob(t, 12*time.Hour))

	fake := putKubeconfigWithFakeSovereign(t, "")

	sec, ok := getSeededSecret(t, fake)
	if !ok {
		t.Fatal("no Secret on the new Sovereign — the kubeconfig postback did not seed the Anthropic credential")
	}
	if got := string(sec.Data[anthropicCredentialAPIKeyKey]); got != fakeAnthropicAPIKey {
		t.Errorf("data[%s] = %q, want the mothership's apiKey", anthropicCredentialAPIKeyKey, got)
	}
	// credentialsJson is the load-bearing half — claude-code
	// authenticates from the OAuth blob, not from apiKey. A seed that
	// wrote only apiKey would satisfy a careless assertion and leave
	// every workspace unable to authenticate.
	if got := string(sec.Data[anthropicCredentialJSONKey]); got == "" {
		t.Errorf("data[%s] is EMPTY — the OAuth blob is the channel claude-code actually uses", anthropicCredentialJSONKey)
	}
	if sec.Labels["catalyst.openova.io/seed"] != "anthropic-credentials" {
		t.Errorf("seed label = %q, want anthropic-credentials", sec.Labels["catalyst.openova.io/seed"])
	}
}

// ── controls ────────────────────────────────────────────────────────

// TestPutKubeconfig_NoMothershipCredentialSeedsNothing — CONTROL
// sharing the "the postback ran" property.
//
// The founder gate is real: when the mothership holds no credential
// there is nothing to propagate, and the seed must not write an empty
// Secret that inspects as populated. Provisioning must also continue —
// the PUT still succeeds (asserted inside the helper).
func TestPutKubeconfig_NoMothershipCredentialSeedsNothing(t *testing.T) {
	withMothershipAnthropicEnv(t, "", "")

	fake := putKubeconfigWithFakeSovereign(t, "")

	if sec, ok := getSeededSecret(t, fake); ok {
		t.Fatalf("a Secret was created with no mothership credential to put in it: keys = %v", anthropicSeedSecretKeys(sec))
	}
}

// TestPutKubeconfig_ExpiredMothershipCredentialSeedsNothing — CONTROL
// sharing the "a credential IS configured" property.
//
// This is the control the absent-case cannot provide. An implementation
// that merely checked "is either env var non-empty" would pass the
// happy path and the absent path and still ship an expired OAuth blob to
// every new Sovereign, where it inspects as populated and fails at first
// use. The seed classifies with the same predicate the Sovereign-side
// seam uses (#6245), so this must be refused.
func TestPutKubeconfig_ExpiredMothershipCredentialSeedsNothing(t *testing.T) {
	withMothershipAnthropicEnv(t, fakeAnthropicAPIKey, fakeClaudeOAuthBlob(t, -3*time.Hour))

	fake := putKubeconfigWithFakeSovereign(t, "")

	if _, ok := getSeededSecret(t, fake); ok {
		t.Fatal("an EXPIRED credential was seeded onto the new Sovereign — it inspects as populated and fails at first use")
	}
}

// TestPutKubeconfig_KeyOnlyMothershipCredentialSeedsNothing — CONTROL
// for the other unusable class. A bare apiKey with no OAuth blob is the
// shape claude-code rejects outright.
func TestPutKubeconfig_KeyOnlyMothershipCredentialSeedsNothing(t *testing.T) {
	withMothershipAnthropicEnv(t, fakeAnthropicAPIKey, "")

	fake := putKubeconfigWithFakeSovereign(t, "")

	if _, ok := getSeededSecret(t, fake); ok {
		t.Fatal("a key-only credential was seeded — claude-code authenticates from credentialsJson, not from apiKey")
	}
}

// TestPutKubeconfig_ExistingSecretIsNeverOverwritten — an operator who
// already placed their own credential on this Sovereign keeps their
// bytes. The seed closes the FIRST-write gap only; rotation stays the
// operator's edit plus the #6163 live read.
func TestPutKubeconfig_ExistingSecretIsNeverOverwritten(t *testing.T) {
	withMothershipAnthropicEnv(t, fakeAnthropicAPIKey, fakeClaudeOAuthBlob(t, 12*time.Hour))

	operatorBytes := "OPERATOR-SUPPLIED-FAKE-VALUE-DO-NOT-REPLACE"
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      anthropicCredentialSecret,
			Namespace: anthropicCredentialNamespace,
		},
		Data: map[string][]byte{
			anthropicCredentialAPIKeyKey: []byte(operatorBytes),
			anthropicCredentialJSONKey:   []byte(operatorBytes),
		},
	}

	fake := putKubeconfigWithFakeSovereign(t, "", existing)

	sec, ok := getSeededSecret(t, fake)
	if !ok {
		t.Fatal("the pre-existing Secret disappeared")
	}
	if got := string(sec.Data[anthropicCredentialAPIKeyKey]); got != operatorBytes {
		t.Errorf("data[%s] = %q — the seed overwrote operator-supplied bytes", anthropicCredentialAPIKeyKey, got)
	}
}

// TestPutKubeconfig_SecondaryRegionDoesNotSeed — the seed belongs to the
// primary control plane's postback only, exactly like the SMTP seed it
// sits beside. A secondary-region PUT just deposits its kubeconfig.
func TestPutKubeconfig_SecondaryRegionDoesNotSeed(t *testing.T) {
	withMothershipAnthropicEnv(t, fakeAnthropicAPIKey, fakeClaudeOAuthBlob(t, 12*time.Hour))

	fake := putKubeconfigWithFakeSovereign(t, "region=fsn1")

	if _, ok := getSeededSecret(t, fake); ok {
		t.Fatal("a secondary-region postback seeded the credential — the seed must fire once, on the primary CP")
	}
}

// ── outcome classification ──────────────────────────────────────────

// TestSeedSovereignAnthropicCredentials_OutcomeMatrix pins the outcome
// each input class produces, since the SSE message the wizard renders is
// selected from it. Driven through the helper because the outcome value
// is not observable from the HTTP response.
func TestSeedSovereignAnthropicCredentials_OutcomeMatrix(t *testing.T) {
	cases := []struct {
		name      string
		apiKey    string
		credsJSON func(*testing.T) string
		want      SovereignAnthropicSeedOutcome
	}{
		{"absent", "", func(*testing.T) string { return "" }, SovereignAnthropicSeedOutcomeSkippedNoEnv},
		{"key-only", fakeAnthropicAPIKey, func(*testing.T) string { return "" }, SovereignAnthropicSeedOutcomeSkippedUnusable},
		{"malformed", fakeAnthropicAPIKey, func(*testing.T) string { return "{\"nope\":true}" }, SovereignAnthropicSeedOutcomeSkippedUnusable},
		{"expired", fakeAnthropicAPIKey, func(t *testing.T) string { return fakeClaudeOAuthBlob(t, -time.Hour) }, SovereignAnthropicSeedOutcomeSkippedUnusable},
		{"valid", fakeAnthropicAPIKey, func(t *testing.T) string { return fakeClaudeOAuthBlob(t, 6*time.Hour) }, SovereignAnthropicSeedOutcomeCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withMothershipAnthropicEnv(t, tc.apiKey, tc.credsJSON(t))
			h := NewWithPDM(silentLogger(), &fakePDM{})
			h.SetSovereignAnthropicSeedClientFactory(func(string) (kubernetes.Interface, error) {
				return fakek8s.NewSimpleClientset(), nil
			})
			dep := &Deployment{ID: "dep-" + tc.name}

			got := h.seedSovereignAnthropicCredentials(t.Context(), dep, validKubeconfigYAML)
			if got != tc.want {
				t.Errorf("outcome = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSeedSovereignAnthropicCredentials_ClientFailure — a kubeconfig the
// factory cannot turn into a client is reported as such, never as a
// successful seed.
func TestSeedSovereignAnthropicCredentials_ClientFailure(t *testing.T) {
	withMothershipAnthropicEnv(t, fakeAnthropicAPIKey, fakeClaudeOAuthBlob(t, 6*time.Hour))
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetSovereignAnthropicSeedClientFactory(func(string) (kubernetes.Interface, error) {
		return nil, fmt.Errorf("synthetic kubeconfig parse failure")
	})

	got := h.seedSovereignAnthropicCredentials(t.Context(), &Deployment{ID: "dep-clientfail"}, "not-a-kubeconfig")
	if got != SovereignAnthropicSeedOutcomeClientFailure {
		t.Errorf("outcome = %q, want %q", got, SovereignAnthropicSeedOutcomeClientFailure)
	}
}

// anthropicSeedSecretKeys renders a Secret's key set for failure messages. Keys only
// — a failing test must never print a credential.
func anthropicSeedSecretKeys(sec *corev1.Secret) []string {
	out := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		out = append(out, k)
	}
	return out
}
