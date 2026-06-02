package iacbootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeBao records every request the OpenBaoStore makes so tests can
// assert path shape + body shape + idempotency. It serves the minimal
// KV-v2 surface (data GET/POST + metadata DELETE).
type fakeBao struct {
	mu       sync.Mutex
	store    map[string]map[string]string // path → fields
	requests []string
	// Failure-injection knobs.
	failNext int
}

func newFakeBao() *fakeBao { return &fakeBao{store: map[string]map[string]string{}} }

func (b *fakeBao) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.requests = append(b.requests, r.Method+" "+r.URL.Path)
		if b.failNext > 0 {
			b.failNext--
			b.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":["injected"]}`))
			return
		}
		b.mu.Unlock()

		if r.Header.Get("X-Vault-Token") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/data/"):
			b.mu.Lock()
			fields, ok := b.store[r.URL.Path]
			b.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":["not found"]}`))
				return
			}
			out := map[string]any{"data": map[string]any{"data": fields}}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/data/"):
			body, _ := io.ReadAll(r.Body)
			var parsed struct {
				Data map[string]string `json:"data"`
			}
			_ = json.Unmarshal(body, &parsed)
			b.mu.Lock()
			b.store[r.URL.Path] = parsed.Data
			b.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/metadata/"):
			// Translate metadata path back to the data path the store uses.
			dataPath := strings.Replace(r.URL.Path, "/metadata/", "/data/", 1)
			b.mu.Lock()
			delete(b.store, dataPath)
			b.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func TestOpenBaoStore_PutHasDelete_RoundTrip(t *testing.T) {
	bao := newFakeBao()
	srv := httptest.NewServer(bao.handler(t))
	t.Cleanup(srv.Close)

	store := NewOpenBaoStore(OpenBaoConfig{
		Addr:  srv.URL,
		Token: "test-token",
	})
	ctx := context.Background()

	has, err := store.HasToken(ctx, "acme")
	if err != nil {
		t.Fatalf("HasToken (empty): %v", err)
	}
	if has {
		t.Errorf("HasToken: expected false on empty bao, got true")
	}

	if err := store.PutToken(ctx, "acme", "secret-plain"); err != nil {
		t.Fatalf("PutToken: %v", err)
	}

	has, err = store.HasToken(ctx, "acme")
	if err != nil {
		t.Fatalf("HasToken (after put): %v", err)
	}
	if !has {
		t.Errorf("HasToken: expected true after put, got false")
	}

	if err := store.DeleteToken(ctx, "acme"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}

	has, err = store.HasToken(ctx, "acme")
	if err != nil {
		t.Fatalf("HasToken (after delete): %v", err)
	}
	if has {
		t.Errorf("HasToken: expected false after delete, got true")
	}
}

func TestOpenBaoStore_UsesCanonicalPath(t *testing.T) {
	bao := newFakeBao()
	srv := httptest.NewServer(bao.handler(t))
	t.Cleanup(srv.Close)

	store := NewOpenBaoStore(OpenBaoConfig{
		Addr:      srv.URL,
		Token:     "test-token",
		MountPath: "kv",
	})

	_ = store.PutToken(context.Background(), "acme", "secret")
	wantPath := "POST /v1/kv/data/org/acme/iac-bot-token"
	found := false
	for _, r := range bao.requests {
		if r == wantPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected request %q in %v", wantPath, bao.requests)
	}
}

func TestOpenBaoStore_RejectsMissingConfig(t *testing.T) {
	cases := []OpenBaoConfig{
		{Addr: ""},
		{Addr: "http://example", Token: ""},
	}
	for i, c := range cases {
		s := NewOpenBaoStore(c)
		if _, err := s.HasToken(context.Background(), "acme"); err == nil {
			t.Errorf("case %d: HasToken accepted %+v", i, c)
		}
	}
}

func TestOpenBaoStore_RejectsEmptyOrg(t *testing.T) {
	bao := newFakeBao()
	srv := httptest.NewServer(bao.handler(t))
	t.Cleanup(srv.Close)
	s := NewOpenBaoStore(OpenBaoConfig{Addr: srv.URL, Token: "x"})
	if err := s.PutToken(context.Background(), "", "secret"); err == nil {
		t.Errorf("PutToken accepted empty org")
	}
}

func TestOpenBaoStore_RejectsEmptyPlaintext(t *testing.T) {
	bao := newFakeBao()
	srv := httptest.NewServer(bao.handler(t))
	t.Cleanup(srv.Close)
	s := NewOpenBaoStore(OpenBaoConfig{Addr: srv.URL, Token: "x"})
	if err := s.PutToken(context.Background(), "acme", ""); err == nil {
		t.Errorf("PutToken accepted empty plaintext")
	}
}

func TestOpenBaoStore_PropagatesUpstreamError(t *testing.T) {
	bao := newFakeBao()
	bao.failNext = 99 // every request fails
	srv := httptest.NewServer(bao.handler(t))
	t.Cleanup(srv.Close)
	s := NewOpenBaoStore(OpenBaoConfig{Addr: srv.URL, Token: "x"})
	if err := s.PutToken(context.Background(), "acme", "secret"); err == nil {
		t.Errorf("PutToken should surface upstream 500")
	}
}
