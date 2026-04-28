// sshkey_test.go — coverage for /api/v1/sshkey/generate (issue #160).
//
// Asserts:
//
//   - Response shape (publicKey + privateKey + fingerprint all present)
//   - Public key parses as a well-formed authorized_keys line, starts with
//     "ssh-ed25519 AAAA", and includes the fqdn-derived comment when one is
//     provided
//   - Private key is a PEM block with the OPENSSH PRIVATE KEY header, and
//     the body decodes back to the canonical openssh-key-v1 magic header
//   - Fingerprint matches the SHA256:<base64-raw> shape that `ssh-keygen -lf`
//     emits
//   - Empty body POST is accepted (defaults comment to "catalyst")
//   - Two consecutive calls produce DIFFERENT keypairs (rules out a stuck
//     RNG / accidentally-deterministic seed source)
//
// The fingerprint-format test is the deterministic check the issue spec
// asks for: regardless of the random keypair, the fingerprint MUST start
// with "SHA256:" and decode to exactly 32 bytes.
package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newSSHKeyTestHandler(t *testing.T) *Handler {
	t.Helper()
	log := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(log)
}

func postSSHKey(t *testing.T, h *Handler, body string) (*httptest.ResponseRecorder, SSHKeyGenerateResponse) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sshkey/generate", reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.GenerateSSHKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp SSHKeyGenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return rec, resp
}

func TestGenerateSSHKey_ResponseShape(t *testing.T) {
	h := newSSHKeyTestHandler(t)
	_, resp := postSSHKey(t, h, `{"fqdn":"omantel.omani.works"}`)

	if resp.PublicKey == "" {
		t.Error("publicKey is empty")
	}
	if resp.PrivateKey == "" {
		t.Error("privateKey is empty")
	}
	if resp.Fingerprint == "" {
		t.Error("fingerprint is empty")
	}
}

func TestGenerateSSHKey_PublicKeyAuthorizedKeysFormat(t *testing.T) {
	h := newSSHKeyTestHandler(t)
	_, resp := postSSHKey(t, h, `{"fqdn":"omantel.omani.works"}`)

	if !strings.HasPrefix(resp.PublicKey, "ssh-ed25519 AAAA") {
		t.Errorf("public key should start with 'ssh-ed25519 AAAA', got %q", resp.PublicKey)
	}
	parts := strings.SplitN(resp.PublicKey, " ", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3-field authorized_keys line, got %d fields", len(parts))
	}
	if parts[0] != "ssh-ed25519" {
		t.Errorf("algorithm field = %q, want ssh-ed25519", parts[0])
	}
	if _, err := base64.StdEncoding.DecodeString(parts[1]); err != nil {
		t.Errorf("middle field is not valid base64: %v", err)
	}
	if parts[2] != "catalyst@omantel.omani.works" {
		t.Errorf("comment field = %q, want catalyst@omantel.omani.works", parts[2])
	}
}

func TestGenerateSSHKey_PublicKeyDefaultComment(t *testing.T) {
	h := newSSHKeyTestHandler(t)
	// Empty body — handler should accept it and default the comment to "catalyst".
	_, resp := postSSHKey(t, h, "")
	parts := strings.SplitN(resp.PublicKey, " ", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3-field authorized_keys line, got %d fields", len(parts))
	}
	if parts[2] != "catalyst" {
		t.Errorf("default comment = %q, want catalyst", parts[2])
	}
}

func TestGenerateSSHKey_PrivateKeyPEMShape(t *testing.T) {
	h := newSSHKeyTestHandler(t)
	_, resp := postSSHKey(t, h, `{"fqdn":"loadtest.openova.io"}`)

	block, rest := pem.Decode([]byte(resp.PrivateKey))
	if block == nil {
		t.Fatal("private key did not decode as PEM")
	}
	if block.Type != "OPENSSH PRIVATE KEY" {
		t.Errorf("PEM type = %q, want OPENSSH PRIVATE KEY", block.Type)
	}
	if len(rest) != 0 && strings.TrimSpace(string(rest)) != "" {
		t.Errorf("trailing data after PEM block: %q", string(rest))
	}

	const magic = "openssh-key-v1\x00"
	if !bytes.HasPrefix(block.Bytes, []byte(magic)) {
		t.Errorf("PEM body does not start with openssh-key-v1 magic")
	}
}

func TestGenerateSSHKey_FingerprintFormat(t *testing.T) {
	h := newSSHKeyTestHandler(t)
	_, resp := postSSHKey(t, h, `{"fqdn":"any.example.com"}`)

	if !strings.HasPrefix(resp.Fingerprint, "SHA256:") {
		t.Errorf("fingerprint should start with SHA256:, got %q", resp.Fingerprint)
	}
	digest := strings.TrimPrefix(resp.Fingerprint, "SHA256:")
	raw, err := base64.RawStdEncoding.DecodeString(digest)
	if err != nil {
		t.Errorf("fingerprint base64 invalid: %v", err)
	}
	if len(raw) != 32 {
		t.Errorf("fingerprint hash length = %d, want 32 (SHA-256)", len(raw))
	}
}

func TestGenerateSSHKey_TwoCallsProduceDifferentKeys(t *testing.T) {
	h := newSSHKeyTestHandler(t)
	_, resp1 := postSSHKey(t, h, `{"fqdn":"a.openova.io"}`)
	_, resp2 := postSSHKey(t, h, `{"fqdn":"a.openova.io"}`)

	if resp1.PublicKey == resp2.PublicKey {
		t.Error("two consecutive generate calls returned the same public key")
	}
	if resp1.PrivateKey == resp2.PrivateKey {
		t.Error("two consecutive generate calls returned the same private key")
	}
	if resp1.Fingerprint == resp2.Fingerprint {
		t.Error("two consecutive generate calls returned the same fingerprint")
	}
}

func TestBuildKeyComment(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "catalyst"},
		{"   ", "catalyst"},
		{"omantel.omani.works", "catalyst@omantel.omani.works"},
		{"  acme-bank.com  ", "catalyst@acme-bank.com"},
	}
	for _, c := range cases {
		if got := buildKeyComment(c.in); got != c.want {
			t.Errorf("buildKeyComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
