// Regression guard for orgCatalogClient.SetPublished — the upstream
// Organization catalog at core/services/catalog/handlers/handlers.go:303-313
// (SetAppPublished) reads the new published state via the
// `?value=true|false` query param ONLY. Any future regression that
// reverts SetPublished to send a JSON body would re-introduce the
// C4-012 / TBD-C4-fup symptom on t22 cov-bench v3 (400 with
// "value query param must be 'true' or 'false'", catalyst-api proxy
// shipped JSON body the upstream ignored). This test pins the wire
// shape so the bug can't silently come back.
package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSetPublished_SendsQueryParamNotBody(t *testing.T) {
	cases := []struct {
		name      string
		published bool
		wantValue string
	}{
		{"true", true, "true"},
		{"false", false, "false"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotQuery, gotAuth, gotBody string
			var gotContentType string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotQuery = r.URL.RawQuery
				gotAuth = r.Header.Get("Authorization")
				gotContentType = r.Header.Get("Content-Type")
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			c := &orgCatalogClient{
				baseURL: srv.URL,
				http:    &http.Client{Timeout: 2 * time.Second},
			}
			status, err := c.SetPublished(context.Background(), "wordpress-tenant", tc.published, "test-bearer")
			if err != nil {
				t.Fatalf("SetPublished: %v", err)
			}
			if status != http.StatusOK {
				t.Fatalf("status: got %d want 200", status)
			}
			if gotMethod != http.MethodPatch {
				t.Errorf("method: got %s want PATCH", gotMethod)
			}
			if gotPath != "/catalog/admin/apps/wordpress-tenant/publish" {
				t.Errorf("path: got %q want /catalog/admin/apps/wordpress-tenant/publish", gotPath)
			}
			// PIN: the query MUST carry value=<bool>. If a future refactor
			// reverts to a JSON body, this assertion fires immediately.
			wantQuery := "value=" + tc.wantValue
			if gotQuery != wantQuery {
				t.Errorf("query: got %q want %q (regression: PATCH body→query translation lost — C4-012/TBD-C4-fup symptom)", gotQuery, wantQuery)
			}
			// PIN: the body MUST be empty. The upstream Organization catalog
			// SetAppPublished doesn't decode a body — but a non-empty
			// body with the wrong content-type would still tell us the
			// caller is shipping the wrong wire shape.
			if strings.TrimSpace(gotBody) != "" {
				t.Errorf("body: got %q want empty (regression: published bool must travel as query, not JSON)", gotBody)
			}
			// Content-Type should NOT be set to application/json — the
			// upstream rejects unknown content types on some paths.
			if strings.Contains(strings.ToLower(gotContentType), "application/json") {
				t.Errorf("content-type: got %q want non-JSON (no body shipped)", gotContentType)
			}
			if gotAuth != "Bearer test-bearer" {
				t.Errorf("auth: got %q want %q", gotAuth, "Bearer test-bearer")
			}
		})
	}
}

func TestSetPublished_EmptyBearerSkipsAuthHeader(t *testing.T) {
	var sawAuthHeader atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuthHeader.Store(true)
		}
		// Upstream returns 401 when no auth — surface verbatim.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &orgCatalogClient{
		baseURL: srv.URL,
		http:    &http.Client{Timeout: 2 * time.Second},
	}
	status, err := c.SetPublished(context.Background(), "x", true, "")
	if err != nil {
		t.Fatalf("SetPublished: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401 (upstream surface verbatim)", status)
	}
	if sawAuthHeader.Load() {
		t.Error("Authorization header sent despite empty bearer — caller must omit, not stamp 'Bearer '")
	}
}

func TestSetPublished_EmptySlugRejected(t *testing.T) {
	c := &orgCatalogClient{baseURL: "http://unused", http: http.DefaultClient}
	if _, err := c.SetPublished(context.Background(), "  ", true, "x"); err == nil {
		t.Fatal("expected error on empty slug; got nil")
	}
}
