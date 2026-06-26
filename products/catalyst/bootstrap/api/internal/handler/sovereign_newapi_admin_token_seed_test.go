package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// newapiCaptureKVServer is a fake OpenBao KV-v2 endpoint that records the
// last PUT path + payload so the test can assert seedNewapiAdminToken wrote
// the exact `secret/catalyst/newapi/admin-token` path with the
// `ADMIN_API_TOKEN` field the catalyst-newapi-admin-token ExternalSecret
// expects. Named distinctly from the anthropic seed test's captureKVServer
// (same package).
type newapiCaptureKVServer struct {
	mu      sync.Mutex
	lastURL string
	lastReq map[string]any
	srv     *httptest.Server
}

func newNewapiCaptureKVServer(t *testing.T) *newapiCaptureKVServer {
	t.Helper()
	c := &newapiCaptureKVServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		c.mu.Lock()
		c.lastURL = r.URL.Path
		c.lastReq = parsed
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"version":1}}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *newapiCaptureKVServer) dataField(key string) (string, bool) {
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

// Test_seedNewapiAdminToken_SeedsExpectedPathAndField verifies the producer
// writes secret/catalyst/newapi/admin-token with ADMIN_API_TOKEN set to the
// bridge ADMIN_SECRET it read from the in-cluster Secret (#4477). The path +
// field name are the contract with platform/newapi/chart/templates/
// external-secret.yaml — drift here re-breaks unified-rbac's admin auth.
func Test_seedNewapiAdminToken_SeedsExpectedPathAndField(t *testing.T) {
	srv := newNewapiCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}

	const bridgeSecret = "bridge-admin-secret-64chars-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	h.SetNewapiAdminTokenSecretReader(func(_ context.Context, ns, name, key string) (string, bool, error) {
		// Confirm the seam reads the canonical bridge Secret coordinates.
		if ns != newapiBridgeSecretNamespace || name != newapiBridgeSecretName || key != newapiBridgeSecretKey {
			t.Errorf("reader called with (%q,%q,%q), want (%q,%q,%q)", ns, name, key,
				newapiBridgeSecretNamespace, newapiBridgeSecretName, newapiBridgeSecretKey)
		}
		return bridgeSecret, true, nil
	})

	outcome := h.seedNewapiAdminToken(context.Background())
	if outcome != NewapiAdminTokenSeedOutcomeSeeded {
		t.Fatalf("outcome = %q, want %q", outcome, NewapiAdminTokenSeedOutcomeSeeded)
	}

	// KV-v2 PUT path is /v1/<mount>/data/<secretPath>.
	wantPath := "/v1/secret/data/catalyst/newapi/admin-token"
	srv.mu.Lock()
	gotPath := srv.lastURL
	srv.mu.Unlock()
	if gotPath != wantPath {
		t.Errorf("PUT path = %q, want %q", gotPath, wantPath)
	}

	// The seeded value MUST be the bridge ADMIN_SECRET verbatim — a mismatch
	// here would 401 every unified-rbac call against the sandbox-bridge.
	if v, ok := srv.dataField("ADMIN_API_TOKEN"); !ok || v != bridgeSecret {
		t.Errorf("ADMIN_API_TOKEN field = %q (present=%v), want bridge ADMIN_SECRET verbatim", v, ok)
	}
}

// Test_seedNewapiAdminToken_SkipNoBridgeSecret verifies the NewAPI-not-yet-
// installed path: reader reports not-found ⇒ loud skip, NO OpenBao write
// (an empty path would pin the ESO/reflector empty-seed trap).
func Test_seedNewapiAdminToken_SkipNoBridgeSecret(t *testing.T) {
	srv := newNewapiCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}

	h.SetNewapiAdminTokenSecretReader(func(_ context.Context, _, _, _ string) (string, bool, error) {
		return "", false, nil
	})

	if outcome := h.seedNewapiAdminToken(context.Background()); outcome != NewapiAdminTokenSeedOutcomeSkippedNoSecret {
		t.Fatalf("outcome = %q, want %q", outcome, NewapiAdminTokenSeedOutcomeSkippedNoSecret)
	}
	srv.mu.Lock()
	wrote := srv.lastURL != ""
	srv.mu.Unlock()
	if wrote {
		t.Errorf("expected NO OpenBao write when bridge secret absent, but a write hit %q", srv.lastURL)
	}
}

// Test_seedNewapiAdminToken_ClientFailure verifies a real API error from the
// reader (RBAC drift, apiserver unreachable) ⇒ loud skip, NO OpenBao write,
// and the pipeline is NOT failed (outcome is a classification, not an error).
func Test_seedNewapiAdminToken_ClientFailure(t *testing.T) {
	srv := newNewapiCaptureKVServer(t)
	h := &Handler{log: silentLogger()}
	h.openbao = &openbao.Client{Addr: srv.srv.URL, Token: "test-token", HTTP: srv.srv.Client()}

	h.SetNewapiAdminTokenSecretReader(func(_ context.Context, _, _, _ string) (string, bool, error) {
		return "", false, errors.New("forbidden: secrets is forbidden")
	})

	if outcome := h.seedNewapiAdminToken(context.Background()); outcome != NewapiAdminTokenSeedOutcomeClientFailure {
		t.Fatalf("outcome = %q, want %q", outcome, NewapiAdminTokenSeedOutcomeClientFailure)
	}
	srv.mu.Lock()
	wrote := srv.lastURL != ""
	srv.mu.Unlock()
	if wrote {
		t.Errorf("expected NO OpenBao write on reader error, but a write hit %q", srv.lastURL)
	}
}

// Test_seedNewapiAdminToken_SkipNoOpenBao verifies the Catalyst-Zero
// orchestrator path: nil OpenBao client ⇒ non-fatal skip, no panic, reader
// never consulted.
func Test_seedNewapiAdminToken_SkipNoOpenBao(t *testing.T) {
	h := &Handler{log: silentLogger()}
	// h.openbao deliberately nil.
	h.SetNewapiAdminTokenSecretReader(func(_ context.Context, _, _, _ string) (string, bool, error) {
		t.Fatal("reader must not be consulted when OpenBao client is nil")
		return "", false, nil
	})
	if outcome := h.seedNewapiAdminToken(context.Background()); outcome != NewapiAdminTokenSeedOutcomeSkippedNoBao {
		t.Fatalf("outcome = %q, want %q", outcome, NewapiAdminTokenSeedOutcomeSkippedNoBao)
	}
}

// Test_readNewapiBridgeAdminSecret exercises the clientset-bound reader core
// against a fake clientset: present Secret returns the value; missing Secret
// and missing key both return (",false,nil) (the non-fatal skip branch).
func Test_readNewapiBridgeAdminSecret(t *testing.T) {
	ctx := context.Background()

	// (a) present Secret + key.
	core := kfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: newapiBridgeSecretName, Namespace: newapiBridgeSecretNamespace},
		Data:       map[string][]byte{newapiBridgeSecretKey: []byte("the-admin-secret")},
	})
	v, found, err := readNewapiBridgeAdminSecret(ctx, core, newapiBridgeSecretNamespace, newapiBridgeSecretName, newapiBridgeSecretKey)
	if err != nil || !found || v != "the-admin-secret" {
		t.Fatalf("present: got (%q, found=%v, err=%v), want (\"the-admin-secret\", true, nil)", v, found, err)
	}

	// (b) Secret missing entirely ⇒ non-fatal skip.
	empty := kfake.NewSimpleClientset()
	if v, found, err := readNewapiBridgeAdminSecret(ctx, empty, newapiBridgeSecretNamespace, newapiBridgeSecretName, newapiBridgeSecretKey); err != nil || found || v != "" {
		t.Fatalf("missing secret: got (%q, found=%v, err=%v), want (\"\", false, nil)", v, found, err)
	}

	// (c) Secret present but key absent ⇒ non-fatal skip (partial render).
	noKey := kfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: newapiBridgeSecretName, Namespace: newapiBridgeSecretNamespace},
		Data:       map[string][]byte{"SIGNING_KEY": []byte("x")},
	})
	if v, found, err := readNewapiBridgeAdminSecret(ctx, noKey, newapiBridgeSecretNamespace, newapiBridgeSecretName, newapiBridgeSecretKey); err != nil || found || v != "" {
		t.Fatalf("missing key: got (%q, found=%v, err=%v), want (\"\", false, nil)", v, found, err)
	}
}
