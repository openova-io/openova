package newapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsureUser_Created201(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/users" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Idempotency-Key") != "uuid-1" {
			t.Errorf("missing idempotency key: %q", r.Header.Get("X-Idempotency-Key"))
		}
		var body CreateUserRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ExternalID != "uuid-1" {
			t.Errorf("external_id = %q", body.ExternalID)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"user_id":"newapi-1","api_key":"sk-test-key","created_at":"2026-05-04T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.URL, "tok", srv.Client())
	got, err := c.EnsureUser(context.Background(), CreateUserRequest{
		ExternalID: "uuid-1", Email: "a@b.example", TenantID: "t-1", Tier: "default",
	})
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if got.UserID != "newapi-1" || got.APIKey != "sk-test-key" {
		t.Errorf("response = %+v", got)
	}
}

func TestEnsureUser_Conflict409_FetchesExisting(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			return
		}
		// GET ?external_id=uuid-1
		if r.URL.Query().Get("external_id") != "uuid-1" {
			t.Errorf("external_id query missing: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user_id":"existing","api_key":"sk-existing"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithHTTP(srv.URL, "tok", srv.Client())
	got, err := c.EnsureUser(context.Background(), CreateUserRequest{
		ExternalID: "uuid-1", Email: "a@b.example", TenantID: "t-1",
	})
	if err != nil {
		t.Fatalf("EnsureUser conflict: %v", err)
	}
	if got.APIKey != "sk-existing" {
		t.Errorf("expected existing api_key, got %q", got.APIKey)
	}
}

func TestEnsureUser_Conflict_ListShape(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			return
		}
		// Server returns a JSON array — also acceptable.
		_, _ = w.Write([]byte(`[{"user_id":"u-2","api_key":"sk-2"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithHTTP(srv.URL, "tok", srv.Client())
	got, err := c.EnsureUser(context.Background(), CreateUserRequest{ExternalID: "uuid-1"})
	if err != nil {
		t.Fatalf("EnsureUser list-shape: %v", err)
	}
	if got.UserID != "u-2" {
		t.Errorf("got %+v", got)
	}
}

func TestEnsureUser_5xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.URL, "tok", srv.Client())
	_, err := c.EnsureUser(context.Background(), CreateUserRequest{ExternalID: "uuid-1"})
	if err == nil {
		t.Errorf("expected error on 502")
	}
	if err != nil && !strings.Contains(err.Error(), "502") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestEnsureUser_RejectsEmptyExternalID(t *testing.T) {
	c := New("http://localhost", "tok")
	_, err := c.EnsureUser(context.Background(), CreateUserRequest{})
	if err == nil {
		t.Errorf("expected error on empty external_id")
	}
}

func TestDisableUser_Idempotent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/users/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/disable") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound) // already gone
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithHTTP(srv.URL, "tok", srv.Client())
	if err := c.DisableUser(context.Background(), "u-1"); err != nil {
		t.Errorf("DisableUser 404 should be nil: %v", err)
	}
	if err := c.DisableUser(context.Background(), ""); err != nil {
		t.Errorf("DisableUser empty should be nil: %v", err)
	}
}
