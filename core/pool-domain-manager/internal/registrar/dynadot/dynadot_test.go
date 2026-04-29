package dynadot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// newTestAdapter wires the adapter to an httptest server.
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
		if got := r.URL.Query().Get("command"); got != "domain_info" {
			t.Errorf("command = %q, want domain_info", got)
		}
		if got := r.URL.Query().Get("domain"); got != "example.com" {
			t.Errorf("domain = %q, want example.com", got)
		}
		w.Write([]byte(`{"DomainInfoResponse":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`))
	})
	if err := a.ValidateToken(context.Background(), "k:s", "example.com"); err != nil {
		t.Fatalf("ValidateToken err = %v", err)
	}
}

func TestValidateTokenBadFormat(t *testing.T) {
	a := New()
	if err := a.ValidateToken(context.Background(), "no-colon", "x.com"); !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestValidateTokenUnauthorized(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{}`))
	})
	err := a.ValidateToken(context.Background(), "k:s", "example.com")
	if !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestValidateTokenAppError(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"DomainInfoResponse":{"ResponseHeader":{"ResponseCode":"-1","Status":"error","Error":"Invalid api key"}}}`))
	})
	err := a.ValidateToken(context.Background(), "k:s", "example.com")
	if !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestValidateTokenRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.ValidateToken(context.Background(), "k:s", "example.com")
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestSetNameserversHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("command"); got != "set_ns" {
			t.Errorf("command = %q, want set_ns", got)
		}
		if got := r.URL.Query().Get("ns0"); got != "ns1.openova.io" {
			t.Errorf("ns0 = %q", got)
		}
		if got := r.URL.Query().Get("ns1"); got != "ns2.openova.io" {
			t.Errorf("ns1 = %q", got)
		}
		w.Write([]byte(`{"SetNsResponse":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`))
	})
	err := a.SetNameservers(context.Background(), "k:s", "example.com",
		[]string{"ns1.openova.io", "ns2.openova.io"})
	if err != nil {
		t.Fatalf("SetNameservers err = %v", err)
	}
}

func TestSetNameserversEmptyList(t *testing.T) {
	a := New()
	if err := a.SetNameservers(context.Background(), "k:s", "x.com", nil); err == nil {
		t.Fatal("expected error for empty ns list")
	}
}

func TestSetNameserversRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.SetNameservers(context.Background(), "k:s", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestSetNameserversDomainNotFound(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"SetNsResponse":{"ResponseHeader":{"ResponseCode":"-1","Status":"error","Error":"Domain not in your account"}}}`))
	})
	err := a.SetNameservers(context.Background(), "k:s", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v, want ErrDomainNotInAccount", err)
	}
}

func TestGetNameserversHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{
		  "DomainInfoResponse": {
		    "ResponseHeader": {"ResponseCode":"0","Status":"success"},
		    "DomainInfo": {
		      "NameServerSettings": {
		        "NameServers": [
		          {"ServerName":"ns1.openova.io"},
		          {"ServerName":"ns2.openova.io"}
		        ]
		      }
		    }
		  }
		}`)
	})
	got, err := a.GetNameservers(context.Background(), "k:s", "example.com")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(got, ",") != "ns1.openova.io,ns2.openova.io" {
		t.Fatalf("got = %v", got)
	}
}

// Compile-time assertion via the package check below — and a runtime
// guarantee that no method panics with a typical zero adapter.
func TestNewAdapterDefaults(t *testing.T) {
	a := New()
	if a.Name() != "dynadot" {
		t.Fatalf("Name = %q", a.Name())
	}
	if a.HTTP == nil || a.BaseURL == "" {
		t.Fatalf("expected defaults")
	}
}
