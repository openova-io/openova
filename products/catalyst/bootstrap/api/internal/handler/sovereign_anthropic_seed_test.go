package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// captureKVServer is a fake OpenBao KV-v2 endpoint that records the last
// PUT path + payload so the test can assert the producer wrote the exact
// `secret/catalyst/anthropic/token` path with the `apiKey` +
// `credentialsJson` fields the agenity ExternalSecret expects.
type captureKVServer struct {
	mu      sync.Mutex
	lastURL string
	lastReq map[string]any
	srv     *httptest.Server
}

func newCaptureKVServer(t *testing.T) *captureKVServer {
	t.Helper()
	c := &captureKVServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		c.mu.Lock()
		c.lastURL = r.URL.Path
		c.lastReq = parsed
		c.mu.Unlock()
		// KV-v2 write success envelope.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"version":1}}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureKVServer) dataField(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastReq == nil {
		return "", false
	}
	data, ok := c.lastReq["data"].(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := data[key].(string)
	return v, ok
}

// Test_seedAnthropicToken_SeedsExpectedPathAndFields verifies the producer
// writes secret/catalyst/anthropic/token with both properties the read-path
// consumes (#4277). The path + field names are the contract with the agenity
// chart's externalsecret-anthropic.yaml — a drift here re-breaks the journey.
func Test_seedAnthropicToken_SeedsExpectedPathAndFields(t *testing.T) {
	srv := newCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}

	t.Setenv(anthropicSeedAPIKeyEnv, "sk-ant-test-key")
	// #6250: this fixture used to carry `"expiresAt":1` — epoch-millisecond 1,
	// i.e. an already-expired credential asserted as a successful seed. The
	// happy path now needs a credential that can actually authenticate.
	t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

	outcome := h.seedAnthropicToken(context.Background())
	if outcome != AnthropicSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q", outcome, AnthropicSeedOutcomeSeeded)
	}

	// KV-v2 PUT path is /v1/<mount>/data/<secretPath>.
	wantPath := "/v1/secret/data/catalyst/anthropic/token"
	srv.mu.Lock()
	gotPath := srv.lastURL
	srv.mu.Unlock()
	if gotPath != wantPath {
		t.Errorf("PUT path = %q, want %q", gotPath, wantPath)
	}

	if v, ok := srv.dataField("apiKey"); !ok || v != "sk-ant-test-key" {
		t.Errorf("apiKey field = %q (present=%v), want sk-ant-test-key", v, ok)
	}
	if v, ok := srv.dataField("credentialsJson"); !ok || v == "" {
		t.Errorf("credentialsJson field missing/empty (present=%v)", ok)
	}
}

// Test_seedAnthropicToken_CredentialsJSONOnly verifies the OAuth-only path
// (no bare api key) still seeds — the agenity OAuth journey supplies only
// credentialsJson.
func Test_seedAnthropicToken_CredentialsJSONOnly(t *testing.T) {
	srv := newCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}

	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, `{"claudeAiOauth":{"accessToken":"oat-x"}}`)

	if outcome := h.seedAnthropicToken(context.Background()); outcome != AnthropicSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q", outcome, AnthropicSeedOutcomeSeeded)
	}
	if v, ok := srv.dataField("credentialsJson"); !ok || v == "" {
		t.Errorf("credentialsJson field missing/empty (present=%v)", ok)
	}
	if v, _ := srv.dataField("apiKey"); v != "" {
		t.Errorf("apiKey field = %q, want empty", v)
	}
}

// Test_seedAnthropicToken_SkipNoEnv verifies the founder-supplied-secret gap:
// both env vars empty ⇒ loud skip, NO OpenBao write (an empty path would
// pin the ESO/reflector empty-seed trap).
func Test_seedAnthropicToken_SkipNoEnv(t *testing.T) {
	srv := newCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}

	t.Setenv(anthropicSeedAPIKeyEnv, "")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "")

	if outcome := h.seedAnthropicToken(context.Background()); outcome != AnthropicSeedOutcomeSkippedNoEnv {
		t.Fatalf("outcome = %q, want %q", outcome, AnthropicSeedOutcomeSkippedNoEnv)
	}
	srv.mu.Lock()
	wrote := srv.lastURL != ""
	srv.mu.Unlock()
	if wrote {
		t.Errorf("expected NO OpenBao write when credential unset, but a write hit %q", srv.lastURL)
	}
}

// Test_seedAnthropicToken_SkipNoOpenBao verifies the Catalyst-Zero
// orchestrator path: nil OpenBao client ⇒ non-fatal skip, no panic.
func Test_seedAnthropicToken_SkipNoOpenBao(t *testing.T) {
	h := &Handler{log: silentLogger()}
	// h.openbao deliberately nil.
	t.Setenv(anthropicSeedAPIKeyEnv, "sk-ant-test-key")
	if outcome := h.seedAnthropicToken(context.Background()); outcome != AnthropicSeedOutcomeSkippedNoBao {
		t.Fatalf("outcome = %q, want %q", outcome, AnthropicSeedOutcomeSkippedNoBao)
	}
}
