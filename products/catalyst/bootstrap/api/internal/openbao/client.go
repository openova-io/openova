// Package openbao is a minimal HTTP client for OpenBao (Vault-API-compatible)
// KV-v2 secret writes. Catalyst-Zero never speaks to OpenBao directly — only
// the new Sovereign's catalyst-api does, when it receives a handover archive
// (see internal/handler/handover.go).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (no bespoke cloud-API calls): OpenBao
// is part of OpenOva's bootstrap-kit and runs in-cluster on every Sovereign
// (`bp-openbao` HelmRelease). It IS the canonical secret-store seam for the
// platform; this package is the one tiny client layer between catalyst-api
// and that seam, and is named `openbao` so future expansion to KV reads,
// transit-engine encrypt/decrypt, etc. has a home.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (no hardcoded URLs): the address +
// token are read at construct-time from the caller's choice of source —
// production wires CATALYST_OPENBAO_ADDR + CATALYST_OPENBAO_TOKEN env
// vars; tests inject a httptest.Server URL.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene): the token is
// held only on the Client struct and forwarded as the X-Vault-Token request
// header. It never lands in logs — Client.PutKVv2 logs only {mountPath,
// secretPath, status} on completion.
package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a thin wrapper around the OpenBao HTTP API.
type Client struct {
	// Addr is the OpenBao base address, e.g. https://openbao.openbao.svc:8200.
	// No trailing slash required — the client trims defensively.
	Addr string

	// Token is the Vault-API auth token. Forwarded as the X-Vault-Token
	// header on every request. Treated as a credential per
	// docs/INVIOLABLE-PRINCIPLES.md #10 — never logged.
	Token string

	// HTTP is the underlying http.Client. Construct via New for a sensible
	// default (Timeout=15s); tests inject a httptest-backed client.
	HTTP *http.Client
}

// New returns a Client with a 15s HTTP timeout. The caller is responsible
// for sourcing the token from a Kubernetes ServiceAccount projected token
// (production wiring) or a test fixture.
func New(addr, token string) *Client {
	return &Client{
		Addr:  addr,
		Token: token,
		HTTP:  &http.Client{Timeout: 15 * time.Second},
	}
}

// PutKVv2 writes a single key/value blob to a KV-v2 secrets engine
// mount. The KV-v2 API path is:
//
//	POST <addr>/v1/<mountPath>/data/<secretPath>
//
// with body shape:
//
//	{"data": {<key>: <value>, ...}}
//
// `mountPath` defaults to "secret" (Vault's standard mount). `secretPath`
// is the logical path within the mount, e.g. "catalyst/tofu-phase0-archive".
// `data` is the key/value payload — values must already be marshalable to
// JSON. Binary blobs should be base64-encoded by the caller.
//
// Returns nil on 200/204; otherwise wraps the upstream error with the
// status code so the handler can surface a clear diagnostic.
func (c *Client) PutKVv2(ctx context.Context, mountPath, secretPath string, data map[string]any) error {
	if c == nil {
		return fmt.Errorf("openbao: client is nil")
	}
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("openbao: address is required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("openbao: token is required")
	}
	mountPath = strings.Trim(strings.TrimSpace(mountPath), "/")
	if mountPath == "" {
		mountPath = "secret"
	}
	secretPath = strings.Trim(strings.TrimSpace(secretPath), "/")
	if secretPath == "" {
		return fmt.Errorf("openbao: secret path is required")
	}

	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return fmt.Errorf("openbao: marshal body: %w", err)
	}

	url := fmt.Sprintf("%s/v1/%s/data/%s",
		strings.TrimRight(c.Addr, "/"),
		mountPath,
		secretPath,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("openbao: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", c.Token)

	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("openbao: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	// Drain a small slice of the body for context — never include the
	// token, never include the secret payload (the failure may have come
	// before the body was even parsed by the server).
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	return fmt.Errorf("openbao: write %s/data/%s: status %d: %s",
		mountPath, secretPath, resp.StatusCode, strings.TrimSpace(string(respBody)),
	)
}

// GetKVv2 reads a single KV-v2 secret blob. The KV-v2 API path is:
//
//	GET <addr>/v1/<mountPath>/data/<secretPath>
//
// response shape:
//
//	{"data": {"data": {<key>: <value>, ...}, "metadata": {...}}}
//
// Returns the `data.data` map verbatim — the caller picks the key it
// needs. Returns `ErrSecretNotFound` when OpenBao replies 404 so the
// caller can distinguish "no per-Org token yet" from transport error
// (G117.3b: per-Org Gitea robot token lookup at
// `kv/data/org/<slug>/iac-bot-token` must return a stable not-found
// signal so NewProductionGiteaIaCWriter can fall back to the global env
// only on truly-missing — not on transport hiccups).
//
// Refs #2765 (G117.3b writer-side).
func (c *Client) GetKVv2(ctx context.Context, mountPath, secretPath string) (map[string]any, error) {
	if c == nil {
		return nil, fmt.Errorf("openbao: client is nil")
	}
	if strings.TrimSpace(c.Addr) == "" {
		return nil, fmt.Errorf("openbao: address is required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, fmt.Errorf("openbao: token is required")
	}
	mountPath = strings.Trim(strings.TrimSpace(mountPath), "/")
	if mountPath == "" {
		mountPath = "secret"
	}
	secretPath = strings.Trim(strings.TrimSpace(secretPath), "/")
	if secretPath == "" {
		return nil, fmt.Errorf("openbao: secret path is required")
	}

	url := fmt.Sprintf("%s/v1/%s/data/%s",
		strings.TrimRight(c.Addr, "/"),
		mountPath,
		secretPath,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("openbao: build request: %w", err)
	}
	req.Header.Set("X-Vault-Token", c.Token)

	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openbao: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrSecretNotFound
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return nil, fmt.Errorf("openbao: read %s/data/%s: status %d: %s",
			mountPath, secretPath, resp.StatusCode, strings.TrimSpace(string(respBody)),
		)
	}

	var envelope struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("openbao: decode body: %w", err)
	}
	if envelope.Data.Data == nil {
		// 200 with an empty body — treat as not-found so the caller's
		// fallback path triggers (rather than handing back a nil map
		// that would NPE downstream).
		return nil, ErrSecretNotFound
	}
	return envelope.Data.Data, nil
}

// ErrSecretNotFound — sentinel returned by GetKVv2 on 404 or empty body
// so callers can distinguish "no per-Org token yet" from transport
// errors. errors.Is(err, ErrSecretNotFound) is the canonical check.
var ErrSecretNotFound = fmt.Errorf("openbao: secret not found")
