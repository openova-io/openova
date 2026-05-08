package keycloak

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func secretTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewWithHTTP(srv.URL, "test-realm", "catalyst-api-server", "sa-secret",
		&http.Client{Timeout: 5 * time.Second})
}

func TestGetClientSecret_HappyPath(t *testing.T) {
	client := secretTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/realms/test-realm/clients/uuid-1/client-secret":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"type":"secret","value":"abc-1234567890"}`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	got, err := client.GetClientSecret(context.Background(), "uuid-1")
	if err != nil {
		t.Fatalf("GetClientSecret: %v", err)
	}
	if got.Value != "abc-1234567890" {
		t.Fatalf("expected secret value, got %q", got.Value)
	}
	if got.Type != "secret" {
		t.Fatalf("expected type=secret, got %q", got.Type)
	}
}

func TestGetClientSecret_NotFound(t *testing.T) {
	client := secretTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_, err := client.GetClientSecret(context.Background(), "missing")
	if !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("expected ErrClientNotFound, got %v", err)
	}
}

func TestRotateClientSecret_HappyPath(t *testing.T) {
	var posted bool
	client := secretTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/realms/test-realm/clients/uuid-1/client-secret":
			posted = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"type":"secret","value":"new-rotated-value"}`))
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	})
	got, err := client.RotateClientSecret(context.Background(), "uuid-1")
	if err != nil {
		t.Fatalf("RotateClientSecret: %v", err)
	}
	if !posted {
		t.Fatal("RotateClientSecret must POST")
	}
	if got.Value != "new-rotated-value" {
		t.Fatalf("expected new-rotated-value, got %q", got.Value)
	}
}

func TestRotateClientSecret_NotFound(t *testing.T) {
	client := secretTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/test-realm/protocol/openid-connect/token":
			saTokenHandler(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_, err := client.RotateClientSecret(context.Background(), "missing")
	if !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("expected ErrClientNotFound, got %v", err)
	}
}
