package cloudflare

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

// successEnvelope marshals a Cloudflare-shaped success body.
func successEnvelope(result any) string {
	r, _ := json.Marshal(result)
	return `{"success":true,"errors":[],"messages":[],"result":` + string(r) + `}`
}

func errorEnvelope(code int, msg string) string {
	return `{"success":false,"errors":[{"code":` + itoa(code) + `,"message":` + jsonEscape(msg) + `}],"messages":[],"result":null}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestValidateTokenHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		// Bearer token MUST be present.
		if got := r.Header.Get("Authorization"); got != "Bearer t-good" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case r.URL.Path == "/user/tokens/verify":
			io.WriteString(w, successEnvelope(map[string]any{"id": "abc", "status": "active"}))
		case strings.HasPrefix(r.URL.Path, "/zones") && r.URL.Query().Get("name") == "example.com":
			io.WriteString(w, successEnvelope([]map[string]any{{"id": "zone-id-1", "name": "example.com"}}))
		default:
			t.Errorf("unexpected path %s ?%s", r.URL.Path, r.URL.RawQuery)
		}
	})
	if err := a.ValidateToken(context.Background(), "t-good", "example.com"); err != nil {
		t.Fatalf("ValidateToken err = %v", err)
	}
}

func TestValidateTokenEmpty(t *testing.T) {
	a := New()
	if err := a.ValidateToken(context.Background(), "", "x.com"); !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenUnauthorized(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, errorEnvelope(10000, "Authentication error"))
	})
	err := a.ValidateToken(context.Background(), "bad", "x.com")
	if !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestValidateTokenZoneNotInAccount(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user/tokens/verify":
			io.WriteString(w, successEnvelope(map[string]any{"status": "active"}))
		case strings.HasPrefix(r.URL.Path, "/zones"):
			// Empty list.
			io.WriteString(w, successEnvelope([]map[string]any{}))
		}
	})
	err := a.ValidateToken(context.Background(), "t", "missing.com")
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.ValidateToken(context.Background(), "t", "x.com")
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetNameserversHappy(t *testing.T) {
	patched := false
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/zones" || strings.HasPrefix(r.URL.RawQuery, "name="):
			io.WriteString(w, successEnvelope([]map[string]any{{"id": "z1", "name": "example.com"}}))
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/z1":
			body, _ := io.ReadAll(r.Body)
			var got map[string]any
			json.Unmarshal(body, &got)
			ns, ok := got["name_servers"].([]any)
			if !ok || len(ns) != 2 {
				t.Errorf("PATCH body name_servers = %v", got)
			}
			patched = true
			io.WriteString(w, successEnvelope(map[string]any{"id": "z1", "name_servers": ns}))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	err := a.SetNameservers(context.Background(), "t", "example.com",
		[]string{"ns1.openova.io", "ns2.openova.io"})
	if err != nil {
		t.Fatalf("SetNameservers err = %v", err)
	}
	if !patched {
		t.Fatal("PATCH /zones/z1 not invoked")
	}
}

func TestSetNameserversEmptyList(t *testing.T) {
	a := New()
	if err := a.SetNameservers(context.Background(), "t", "x.com", nil); err == nil {
		t.Fatal("want error for empty NS list")
	}
}

func TestSetNameserversBadDomain(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		// Empty zones array.
		io.WriteString(w, successEnvelope([]map[string]any{}))
	})
	err := a.SetNameservers(context.Background(), "t", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetNameserversRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.SetNameservers(context.Background(), "t", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetNameserversHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zones":
			io.WriteString(w, successEnvelope([]map[string]any{{"id": "z1", "name": "example.com"}}))
		case "/zones/z1":
			io.WriteString(w, successEnvelope(map[string]any{
				"id":           "z1",
				"name":         "example.com",
				"name_servers": []string{"ns1.openova.io", "ns2.openova.io"},
			}))
		}
	})
	got, err := a.GetNameservers(context.Background(), "t", "example.com")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(got, ",") != "ns1.openova.io,ns2.openova.io" {
		t.Fatalf("got = %v", got)
	}
}

func TestNameAndDefaults(t *testing.T) {
	a := New()
	if a.Name() != "cloudflare" {
		t.Fatalf("Name = %q", a.Name())
	}
	if a.BaseURL == "" || a.HTTP == nil {
		t.Fatal("expected defaults")
	}
}
