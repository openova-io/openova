package godaddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

func newTestAdapter(t *testing.T, h http.HandlerFunc) (*Adapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := New()
	a.BaseURL = srv.URL
	return a, srv
}

func TestValidateTokenHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "sso-key key:secret" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/v1/domains" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `[{"domain":"example.com","status":"ACTIVE"}]`)
	})
	if err := a.ValidateToken(context.Background(), "key:secret", "example.com"); err != nil {
		t.Fatalf("ValidateToken err = %v", err)
	}
}

func TestValidateTokenBadFormat(t *testing.T) {
	a := New()
	if err := a.ValidateToken(context.Background(), "no-colon", "x.com"); !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenUnauthorized(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":"INVALID_API_KEY","message":"invalid api key"}`)
	})
	err := a.ValidateToken(context.Background(), "key:secret", "x.com")
	if !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenDomainNotInAccount(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"domain":"other.com","status":"ACTIVE"}]`)
	})
	err := a.ValidateToken(context.Background(), "key:secret", "missing.com")
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.ValidateToken(context.Background(), "key:secret", "x.com")
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetNameserversHappy(t *testing.T) {
	patched := false
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/domains/example.com" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		json.Unmarshal(body, &got)
		ns, _ := got["nameServers"].([]any)
		if len(ns) != 2 {
			t.Errorf("nameServers = %v", got)
		}
		patched = true
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	})
	err := a.SetNameservers(context.Background(), "key:secret", "example.com",
		[]string{"ns1.openova.io", "ns2.openova.io"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !patched {
		t.Fatal("PATCH not invoked")
	}
}

func TestSetNameserversEmptyList(t *testing.T) {
	a := New()
	if err := a.SetNameservers(context.Background(), "key:secret", "x.com", nil); err == nil {
		t.Fatal("want error")
	}
}

func TestSetNameserversBadDomain(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"code":"NOT_FOUND","message":"Domain not found"}`)
	})
	err := a.SetNameservers(context.Background(), "key:secret", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetNameserversRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.SetNameservers(context.Background(), "key:secret", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetNameserversHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"domain":"example.com","nameServers":["ns1.openova.io","ns2.openova.io"]}`)
	})
	got, err := a.GetNameservers(context.Background(), "key:secret", "example.com")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(got, ",") != "ns1.openova.io,ns2.openova.io" {
		t.Fatalf("got = %v", got)
	}
}

func TestNameAndOTE(t *testing.T) {
	if New().Name() != "godaddy" {
		t.Fatal("Name != godaddy")
	}
	ote := NewOTE()
	if !strings.Contains(ote.BaseURL, "ote-godaddy") {
		t.Fatalf("OTE URL = %q", ote.BaseURL)
	}
}
