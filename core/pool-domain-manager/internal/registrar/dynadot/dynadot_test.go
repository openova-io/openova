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

// ── Glue-record path (issue #900) ───────────────────────────────────────
//
// These tests cover the new register_ns / get_ns / set_ns_ip surface the
// adapter exposes via the registrar.GlueRegistrar interface. Each test
// drives the adapter against an httptest server that asserts the
// expected Dynadot api3.json command + parameters and replies with a
// canned api3.json response.
//
// The flow under test:
//
//   1. GetGlueRecord — probes the host via `command=get_ns` and returns
//      the registered IPv4 (or "" + nil for "not registered").
//   2. RegisterGlueRecord — idempotency-aware: short-circuits on same
//      IP, falls through to set_ns_ip on different IP, register_ns when
//      not yet registered.

// TestGetGlueRecordHappy — Dynadot returns the registered IP for a
// known host. Must be returned as the first non-empty entry of the IP
// array (Dynadot's get_ns response carries IP as a list).
func TestGetGlueRecordHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("command"); got != "get_ns" {
			t.Errorf("command = %q, want get_ns", got)
		}
		if got := r.URL.Query().Get("ns"); got != "ns1.otech103.omani.works" {
			t.Errorf("ns = %q, want ns1.otech103.omani.works", got)
		}
		fmt.Fprintln(w, `{
		  "GetNsResponse": {
		    "ResponseHeader": {"ResponseCode":"0","Status":"success"},
		    "NsInfo": {"NameServer": {"Host":"ns1.otech103.omani.works", "IP":["203.0.113.42"]}}
		  }
		}`)
	})
	got, err := a.GetGlueRecord(context.Background(), "k:s", "ns1.otech103.omani.works")
	if err != nil {
		t.Fatalf("GetGlueRecord err = %v", err)
	}
	if got != "203.0.113.42" {
		t.Fatalf("ip = %q, want 203.0.113.42", got)
	}
}

// TestGetGlueRecordNotRegistered — Dynadot returns an error envelope
// with a "not found"/"not registered" error string. Adapter must
// translate that into ("", nil) so callers can distinguish "missing"
// from "API failed".
func TestGetGlueRecordNotRegistered(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{
		  "GetNsResponse": {"ResponseHeader": {"ResponseCode":"-1","Status":"error","Error":"Name server not found"}}
		}`)
	})
	got, err := a.GetGlueRecord(context.Background(), "k:s", "ns1.otech999.omani.works")
	if err != nil {
		t.Fatalf("expected nil err for not-registered, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty ip for not-registered, got %q", got)
	}
}

// TestGetGlueRecordRateLimited — typed registrar.ErrRateLimited
// passes through GetGlueRecord so the HTTP handler can map to 429.
func TestGetGlueRecordRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := a.GetGlueRecord(context.Background(), "k:s", "ns1.otech103.omani.works")
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

// TestRegisterGlueRecordFreshHost — the host is not registered; adapter
// runs get_ns (returns not-found), then register_ns with the supplied IP.
func TestRegisterGlueRecordFreshHost(t *testing.T) {
	calls := 0
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		cmd := r.URL.Query().Get("command")
		switch calls {
		case 1:
			// Probe — not registered.
			if cmd != "get_ns" {
				t.Errorf("call 1: command = %q, want get_ns", cmd)
			}
			fmt.Fprintln(w, `{"GetNsResponse":{"ResponseHeader":{"ResponseCode":"-1","Status":"error","Error":"Name server not found"}}}`)
		case 2:
			// Register with the supplied IP.
			if cmd != "register_ns" {
				t.Errorf("call 2: command = %q, want register_ns", cmd)
			}
			if got := r.URL.Query().Get("ns"); got != "ns1.otech103.omani.works" {
				t.Errorf("ns = %q", got)
			}
			if got := r.URL.Query().Get("ip0"); got != "203.0.113.42" {
				t.Errorf("ip0 = %q, want 203.0.113.42", got)
			}
			fmt.Fprintln(w, `{"RegisterNsResponse":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`)
		default:
			t.Errorf("unexpected call %d (cmd=%s)", calls, cmd)
		}
	})
	if err := a.RegisterGlueRecord(context.Background(), "k:s", "ns1.otech103.omani.works", "203.0.113.42"); err != nil {
		t.Fatalf("RegisterGlueRecord err = %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 api calls (get_ns + register_ns), got %d", calls)
	}
}

// TestRegisterGlueRecordIdempotent — the host is already registered
// with the same IP. Adapter MUST short-circuit after the get_ns probe
// (no register_ns / set_ns_ip call). This is the property that makes
// SetNameservers retries safe at billed-API tiers.
func TestRegisterGlueRecordIdempotent(t *testing.T) {
	calls := 0
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		cmd := r.URL.Query().Get("command")
		if calls > 1 {
			t.Errorf("expected exactly 1 api call (get_ns), got call %d (cmd=%s)", calls, cmd)
		}
		if cmd != "get_ns" {
			t.Errorf("command = %q, want get_ns", cmd)
		}
		fmt.Fprintln(w, `{
		  "GetNsResponse": {
		    "ResponseHeader": {"ResponseCode":"0","Status":"success"},
		    "NsInfo": {"NameServer": {"Host":"ns1.otech103.omani.works", "IP":["203.0.113.42"]}}
		  }
		}`)
	})
	if err := a.RegisterGlueRecord(context.Background(), "k:s", "ns1.otech103.omani.works", "203.0.113.42"); err != nil {
		t.Fatalf("RegisterGlueRecord err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 api call (idempotent short-circuit), got %d", calls)
	}
}

// TestRegisterGlueRecordIPChanged — the host is already registered but
// with a stale IP. Adapter MUST take the set_ns_ip update-in-place path
// (NOT register_ns, which Dynadot rejects when the host already exists).
func TestRegisterGlueRecordIPChanged(t *testing.T) {
	calls := 0
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		cmd := r.URL.Query().Get("command")
		switch calls {
		case 1:
			if cmd != "get_ns" {
				t.Errorf("call 1: command = %q, want get_ns", cmd)
			}
			fmt.Fprintln(w, `{
			  "GetNsResponse": {
			    "ResponseHeader": {"ResponseCode":"0","Status":"success"},
			    "NsInfo": {"NameServer": {"Host":"ns1.otech103.omani.works", "IP":["198.51.100.7"]}}
			  }
			}`)
		case 2:
			if cmd != "set_ns_ip" {
				t.Errorf("call 2: command = %q, want set_ns_ip", cmd)
			}
			if got := r.URL.Query().Get("ns"); got != "ns1.otech103.omani.works" {
				t.Errorf("ns = %q", got)
			}
			if got := r.URL.Query().Get("ip0"); got != "203.0.113.42" {
				t.Errorf("ip0 = %q, want 203.0.113.42", got)
			}
			fmt.Fprintln(w, `{"SetNsIpResponse":{"ResponseHeader":{"ResponseCode":"0","Status":"success"}}}`)
		default:
			t.Errorf("unexpected call %d (cmd=%s)", calls, cmd)
		}
	})
	if err := a.RegisterGlueRecord(context.Background(), "k:s", "ns1.otech103.omani.works", "203.0.113.42"); err != nil {
		t.Fatalf("RegisterGlueRecord err = %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 api calls (get_ns + set_ns_ip), got %d", calls)
	}
}

// TestRegisterGlueRecordRateLimitedDuringRegister — the get_ns probe
// succeeds (host not registered), but the subsequent register_ns
// returns a 429. The typed error must surface so the HTTP handler can
// map to a 429 response — same behaviour as set_ns rate-limit handling.
func TestRegisterGlueRecordRateLimitedDuringRegister(t *testing.T) {
	calls := 0
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		cmd := r.URL.Query().Get("command")
		switch calls {
		case 1:
			if cmd != "get_ns" {
				t.Errorf("call 1: command = %q, want get_ns", cmd)
			}
			fmt.Fprintln(w, `{"GetNsResponse":{"ResponseHeader":{"ResponseCode":"-1","Status":"error","Error":"Name server not found"}}}`)
		case 2:
			if cmd != "register_ns" {
				t.Errorf("call 2: command = %q, want register_ns", cmd)
			}
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			t.Errorf("unexpected call %d (cmd=%s)", calls, cmd)
		}
	})
	err := a.RegisterGlueRecord(context.Background(), "k:s", "ns1.otech103.omani.works", "203.0.113.42")
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

// TestRegisterGlueRecordValidation — empty host or empty IP both fail
// fast without a network call. Hardens against silently-bad input from
// the HTTP handler / wizard layer.
func TestRegisterGlueRecordValidation(t *testing.T) {
	a := New()
	if err := a.RegisterGlueRecord(context.Background(), "k:s", "", "1.2.3.4"); err == nil {
		t.Fatal("expected error for empty host")
	}
	if err := a.RegisterGlueRecord(context.Background(), "k:s", "ns1.x", ""); err == nil {
		t.Fatal("expected error for empty ip")
	}
}

// TestRegisterGlueRecordBadToken — token-shape parse must fail before
// any network call. Mirrors the existing TestValidateTokenBadFormat.
func TestRegisterGlueRecordBadToken(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		// get_ns probe is allowed (returns not-found) BEFORE the token
		// parse for the set_ns_ip / register_ns path.
		fmt.Fprintln(w, `{"GetNsResponse":{"ResponseHeader":{"ResponseCode":"-1","Status":"error","Error":"Name server not found"}}}`)
	})
	err := a.RegisterGlueRecord(context.Background(), "no-colon", "ns1.x.com", "1.2.3.4")
	if !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

// TestAdapterImplementsGlueRegistrar — compile-time guard that the
// adapter satisfies the optional registrar.GlueRegistrar interface.
// The HTTP handler relies on this via type-assertion (see
// handler/registrar.go SetNS), so a future refactor that drops a
// method must fail here, not silently bypass the glue path live.
func TestAdapterImplementsGlueRegistrar(t *testing.T) {
	var _ registrar.GlueRegistrar = (*Adapter)(nil)
	if _, ok := any(New()).(registrar.GlueRegistrar); !ok {
		t.Fatal("New() does not implement registrar.GlueRegistrar")
	}
}
