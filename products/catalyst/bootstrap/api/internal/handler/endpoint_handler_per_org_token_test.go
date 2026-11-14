// endpoint_handler_per_org_token_test.go — G117.3b (Refs #2765)
// per-Org Gitea robot token isolation coverage.
//
// What we assert:
//
//  1. Two Orgs with distinct OpenBao-seeded tokens resolve to DISTINCT
//     bearer tokens (no cross-Org leak via cached/shared state).
//
//  2. Falling back to the env-var shim happens ONLY when OpenBao
//     returns `ErrSecretNotFound` — transport errors propagate so the
//     caller doesn't silently downgrade to global-token writes after a
//     transient network hiccup.
//
//  3. The looked-up secret PATH is `kv/data/org/<slug>/iac-bot-token`
//     (the ADR-0009 contract) and the required key is `token`.
//
// We exercise the production code path by injecting a Handler with a
// real openbao.Client pointed at a httptest.Server that mimics the
// KV-v2 contract (GET /v1/secret/data/<path> → 200 with the canonical
// envelope shape, or 404 with the not-found body).
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// fakeOpenBao serves a deterministic per-org token table over the
// real KV-v2 HTTP shape. Tracks every request path so cross-Org leak
// tests can assert that Org-A's resolver never asks for Org-B's path.
type fakeOpenBao struct {
	server *httptest.Server
	tokens map[string]string // org → token; missing keys → 404
	// gotPaths records every path the client GET'd, in order.
	gotPaths atomic.Value // []string
}

func newFakeOpenBao(t *testing.T, tokens map[string]string) *fakeOpenBao {
	t.Helper()
	f := &fakeOpenBao{tokens: tokens}
	f.gotPaths.Store([]string{})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/secret/data/", func(w http.ResponseWriter, r *http.Request) {
		// Path shape: /v1/secret/data/org/<slug>/iac-bot-token
		path := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		// Record.
		paths := f.gotPaths.Load().([]string)
		f.gotPaths.Store(append(append([]string{}, paths...), path))

		// Look up by `org/<slug>/iac-bot-token` → token.
		const suffix = "/iac-bot-token"
		if !strings.HasSuffix(path, suffix) || !strings.HasPrefix(path, "org/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(path, "org/"), suffix)
		tok, ok := f.tokens[slug]
		if !ok {
			// KV-v2 404 envelope (mimics OpenBao real response).
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []string{"secret not found"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"data": map[string]any{
					"token": tok,
				},
				"metadata": map[string]any{
					"version": 1,
				},
			},
		})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOpenBao) paths() []string {
	return f.gotPaths.Load().([]string)
}

// newHandlerWithFakeOpenBao constructs a Handler with the openbao
// field set to a real openbao.Client pointing at fakeOpenBao.server.
// Tests use this to exercise resolveGiteaTokenForOrg end-to-end.
func newHandlerWithFakeOpenBao(t *testing.T, f *fakeOpenBao, rootToken string) *Handler {
	t.Helper()
	log := slog.Default()
	h := New(log)
	if f != nil {
		h.SetOpenBao(openbao.New(f.server.URL, rootToken))
	}
	return h
}

// TestResolveGiteaTokenForOrg_CrossOrgIsolation — the central #2765
// acceptance: two Orgs with distinct OpenBao tokens MUST resolve to
// distinct bearer tokens. Org-A's resolver MUST NOT see Org-B's path,
// MUST NOT return Org-B's token, MUST NOT cache shared state.
func TestResolveGiteaTokenForOrg_CrossOrgIsolation(t *testing.T) {
	fake := newFakeOpenBao(t, map[string]string{
		"acme":   "acme-gitea-token-AAA",
		"globex": "globex-gitea-token-BBB",
	})
	h := newHandlerWithFakeOpenBao(t, fake, "test-root-token")
	ctx := context.Background()

	gotA, err := h.resolveGiteaTokenForOrg(ctx, "acme")
	if err != nil {
		t.Fatalf("resolve acme: %v", err)
	}
	gotB, err := h.resolveGiteaTokenForOrg(ctx, "globex")
	if err != nil {
		t.Fatalf("resolve globex: %v", err)
	}

	if gotA != "acme-gitea-token-AAA" {
		t.Errorf("Org-A token = %q, want acme-gitea-token-AAA", gotA)
	}
	if gotB != "globex-gitea-token-BBB" {
		t.Errorf("Org-B token = %q, want globex-gitea-token-BBB", gotB)
	}
	if gotA == gotB {
		t.Errorf("cross-Org leak: Org-A and Org-B resolved to the SAME token (%q)", gotA)
	}

	// Assert path isolation — each org hits its OWN path, never the peer's.
	paths := fake.paths()
	if len(paths) != 2 {
		t.Fatalf("expected exactly 2 OpenBao GETs, got %d (%v)", len(paths), paths)
	}
	if paths[0] != "org/acme/iac-bot-token" {
		t.Errorf("Org-A path = %q, want org/acme/iac-bot-token", paths[0])
	}
	if paths[1] != "org/globex/iac-bot-token" {
		t.Errorf("Org-B path = %q, want org/globex/iac-bot-token", paths[1])
	}
}

// TestResolveGiteaTokenForOrg_FallbackToEnvOnNotFound — when the
// per-Org secret hasn't been seeded yet, fall back to the env-var
// shim. This preserves the single-token bootstrap path for fresh
// Sovereigns whose tools/bootstrap-org-iac-repo.sh has provisioned
// the Gitea side but the OpenBao seed hasn't landed.
func TestResolveGiteaTokenForOrg_FallbackToEnvOnNotFound(t *testing.T) {
	fake := newFakeOpenBao(t, map[string]string{
		// `unseeded` org has no entry → 404 → fallback to env.
	})
	h := newHandlerWithFakeOpenBao(t, fake, "test-root-token")
	t.Setenv("CATALYST_GITEA_TOKEN", "global-fallback-token-XYZ")

	got, err := h.resolveGiteaTokenForOrg(context.Background(), "unseeded")
	if err != nil {
		t.Fatalf("resolve unseeded: %v", err)
	}
	if got != "global-fallback-token-XYZ" {
		t.Errorf("fallback token = %q, want global-fallback-token-XYZ", got)
	}
}

// TestResolveGiteaTokenForOrg_NotFoundAndNoEnv_Fails — when both the
// per-Org secret and the env-var shim are absent the resolver MUST
// error (never return an empty token that would silently auth as
// anonymous against Gitea).
func TestResolveGiteaTokenForOrg_NotFoundAndNoEnv_Fails(t *testing.T) {
	fake := newFakeOpenBao(t, map[string]string{})
	h := newHandlerWithFakeOpenBao(t, fake, "test-root-token")
	t.Setenv("CATALYST_GITEA_TOKEN", "")

	_, err := h.resolveGiteaTokenForOrg(context.Background(), "unseeded")
	if err == nil {
		t.Fatalf("expected error when both OpenBao and env are empty; got nil")
	}
	if !strings.Contains(err.Error(), "kv/data") {
		t.Errorf("error should mention the kv path; got %q", err.Error())
	}
}

// TestResolveGiteaTokenForOrg_TransportErrorPropagates — a real
// transport failure (e.g. OpenBao unreachable) MUST NOT silently
// downgrade to the env-var token. The caller (endpoint handler)
// surfaces the error to the API client; on retry the resolver tries
// again. Anti-pattern guard: silent fallback on transport error would
// turn an OpenBao outage into a permanent Org-A-token-leak risk if
// Org-A's request raced with Org-B's failure window.
func TestResolveGiteaTokenForOrg_TransportErrorPropagates(t *testing.T) {
	// Build an httptest.Server that always 500s — simulates OpenBao
	// unavailable / sealed / token-revoked.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "vault sealed", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	h := New(slog.Default())
	h.SetOpenBao(openbao.New(srv.URL, "test-root-token"))
	t.Setenv("CATALYST_GITEA_TOKEN", "global-fallback-token-XYZ")

	_, err := h.resolveGiteaTokenForOrg(context.Background(), "acme")
	if err == nil {
		t.Fatalf("expected error on OpenBao 503; got nil (silent fallback is a cross-Org leak risk)")
	}
	if errors.Is(err, openbao.ErrSecretNotFound) {
		t.Errorf("503 must NOT be classified as not-found; got ErrSecretNotFound")
	}
}

// TestResolveGiteaTokenForOrg_NoOpenBaoWired_UsesEnv — when openbao
// is unwired (test mode / chroot dev / first-boot before openbao Pod
// ready), the resolver MUST fall through to the env-var shim. This
// preserves the legacy single-Org-Sovereign code path verbatim.
func TestResolveGiteaTokenForOrg_NoOpenBaoWired_UsesEnv(t *testing.T) {
	h := New(slog.Default()) // no SetOpenBao → h.openbao stays nil
	t.Setenv("CATALYST_GITEA_TOKEN", "legacy-shim-token")

	got, err := h.resolveGiteaTokenForOrg(context.Background(), "acme")
	if err != nil {
		t.Fatalf("resolve acme: %v", err)
	}
	if got != "legacy-shim-token" {
		t.Errorf("got %q, want legacy-shim-token", got)
	}
}

// TestResolveGiteaTokenForOrg_EmptyOrgRejected — defence-in-depth: an
// empty org slug MUST be rejected before any KV path is built, so a
// buggy caller can't accidentally hit `kv/data/org//iac-bot-token`
// (which would 404 then fall through to env — still a correctness
// regression but a worse signal).
func TestResolveGiteaTokenForOrg_EmptyOrgRejected(t *testing.T) {
	fake := newFakeOpenBao(t, map[string]string{"": "do-not-leak"})
	h := newHandlerWithFakeOpenBao(t, fake, "test-root-token")
	t.Setenv("CATALYST_GITEA_TOKEN", "global")

	_, err := h.resolveGiteaTokenForOrg(context.Background(), "  ")
	if err == nil {
		t.Fatalf("expected error on empty org slug; got nil")
	}
	if len(fake.paths()) != 0 {
		t.Errorf("empty-org request hit OpenBao (%v); should be rejected before KV lookup", fake.paths())
	}
}
