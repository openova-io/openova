// openbao_store.go — OpenBao KV-v2 backing for the iacbootstrap
// TokenStore interface.
//
// G117.3b (issue #2765) lands per-Org Gitea robot tokens in OpenBao at
// the canonical path:
//
//	kv/org/<org>/iac-bot-token
//
// with field name `token`. External-Secrets renders that into a
// Kubernetes Secret named `<org>-iac-bot-token` in the Org's namespace
// per ADR-0009 §Robot-account-scope. catalyst-api reads the Secret
// when opening PRs against the per-Org IaC repo.
//
// The implementation is a thin wrapper over the HTTP API:
//
//	GET  /v1/kv/data/org/<org>/iac-bot-token → has-token check
//	POST /v1/kv/data/org/<org>/iac-bot-token → put / rotate
//	DELETE /v1/kv/metadata/org/<org>/iac-bot-token → finalizer cleanup
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the token plaintext is held in
// memory only long enough to be wrapped into the JSON payload + handed
// to the http.Client. No log statement emits the plaintext; the success
// log carries only the path.

package iacbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenBaoConfig is the minimal config the OpenBaoStore needs. Wired
// from organization-controller cmd/main.go via env CATALYST_OPENBAO_ADDR
// + CATALYST_OPENBAO_TOKEN — same convention as products/catalyst/
// bootstrap/api/internal/openbao/client.go uses for catalyst-api.
type OpenBaoConfig struct {
	// Addr is the OpenBao base address (e.g. https://openbao.openbao.svc:8200).
	Addr string

	// Token is the Vault-API auth token (typically a Kubernetes-auth
	// role token; see bp-openbao chart for the role binding).
	Token string

	// MountPath is the KV-v2 mount. Defaults to "kv" — match the
	// bp-openbao chart's `kv` mount name.
	MountPath string

	// HTTPClient is optional; defaults to a 15s-timeout client.
	HTTPClient *http.Client
}

// OpenBaoStore implements TokenStore backed by an OpenBao KV-v2 mount.
type OpenBaoStore struct {
	cfg OpenBaoConfig
}

// NewOpenBaoStore returns a TokenStore that persists per-Org robot
// tokens at `<mount>/data/org/<org>/iac-bot-token`. Pass an empty
// MountPath to default to "kv".
func NewOpenBaoStore(cfg OpenBaoConfig) *OpenBaoStore {
	if cfg.MountPath == "" {
		cfg.MountPath = "kv"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &OpenBaoStore{cfg: cfg}
}

func (s *OpenBaoStore) HasToken(ctx context.Context, org string) (bool, error) {
	if err := s.validate(org); err != nil {
		return false, err
	}
	resp, err := s.do(ctx, http.MethodGet, s.dataPath(org), nil)
	if err != nil {
		return false, fmt.Errorf("openbao-store.HasToken: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// Decode minimally to confirm the `data.data.token` is non-empty —
		// External-Secrets won't render a Secret from a path that holds an
		// empty value, so we report "no token" in that case and let the
		// bootstrap re-mint.
		var body struct {
			Data struct {
				Data map[string]string `json:"data"`
			} `json:"data"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		if err := json.Unmarshal(raw, &body); err != nil {
			return false, fmt.Errorf("openbao-store.HasToken: decode: %w", err)
		}
		return strings.TrimSpace(body.Data.Data["token"]) != "", nil
	case http.StatusNotFound:
		return false, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return false, fmt.Errorf("openbao-store.HasToken: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func (s *OpenBaoStore) PutToken(ctx context.Context, org, plaintext string) error {
	if err := s.validate(org); err != nil {
		return err
	}
	if plaintext == "" {
		return errors.New("openbao-store.PutToken: plaintext is empty")
	}
	payload := map[string]any{
		"data": map[string]string{"token": plaintext},
	}
	body, _ := json.Marshal(payload)

	resp, err := s.do(ctx, http.MethodPost, s.dataPath(org), body)
	if err != nil {
		return fmt.Errorf("openbao-store.PutToken: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	return fmt.Errorf("openbao-store.PutToken: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

func (s *OpenBaoStore) DeleteToken(ctx context.Context, org string) error {
	if err := s.validate(org); err != nil {
		return err
	}
	// Delete via the metadata path so the version history is purged
	// alongside the current value — finalizer semantics demand the path
	// be gone, not soft-deleted.
	resp, err := s.do(ctx, http.MethodDelete, s.metadataPath(org), nil)
	if err != nil {
		return fmt.Errorf("openbao-store.DeleteToken: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return fmt.Errorf("openbao-store.DeleteToken: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func (s *OpenBaoStore) validate(org string) error {
	if s.cfg.Addr == "" {
		return errors.New("openbao-store: Addr is empty")
	}
	if s.cfg.Token == "" {
		return errors.New("openbao-store: Token is empty")
	}
	if org == "" {
		return errors.New("openbao-store: org slug is empty")
	}
	return nil
}

func (s *OpenBaoStore) dataPath(org string) string {
	return fmt.Sprintf("/v1/%s/data/org/%s/iac-bot-token",
		strings.Trim(s.cfg.MountPath, "/"), org)
}

func (s *OpenBaoStore) metadataPath(org string) string {
	return fmt.Sprintf("/v1/%s/metadata/org/%s/iac-bot-token",
		strings.Trim(s.cfg.MountPath, "/"), org)
}

func (s *OpenBaoStore) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	url := strings.TrimRight(s.cfg.Addr, "/") + path
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Vault-Token", s.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.cfg.HTTPClient.Do(req)
}
