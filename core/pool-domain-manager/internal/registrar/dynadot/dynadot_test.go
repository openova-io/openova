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

// ── ValidateToken ────────────────────────────────────────────────────────
//
// All fixtures below use the REAL Dynadot api3.json envelope shape
// (status fields directly under <Command>Response, NO `ResponseHeader`
// wrapper, ResponseCode oscillating between JSON int 0 on success and
// JSON string "-1" on error). Captured live 2026-05-05 against
// omani.works (success) and a non-owned domain (error). Refs #939.

// TestValidateTokenHappy — real Dynadot api3.json `domain_info` success
// envelope: ResponseCode is JSON int 0 at TOP of DomainInfoResponse, not
// nested under a `ResponseHeader` wrapper.
func TestValidateTokenHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("command"); got != "domain_info" {
			t.Errorf("command = %q, want domain_info", got)
		}
		if got := r.URL.Query().Get("domain"); got != "example.com" {
			t.Errorf("domain = %q, want example.com", got)
		}
		w.Write([]byte(`{"DomainInfoResponse":{"ResponseCode":0,"Status":"success","DomainInfo":{"Name":"example.com","Status":"active"}}}`))
	})
	if err := a.ValidateToken(context.Background(), "k:s", "example.com"); err != nil {
		t.Fatalf("ValidateToken err = %v", err)
	}
}

// TestValidateTokenIntCodeNoStatus — Dynadot has been observed to omit
// the Status string field in some success replies, returning only
// `"ResponseCode": 0`. The adapter must accept that as success too;
// otherwise every such response would 500 the caller. The codeIsZero
// helper is what makes this work — it normalises int 0 / string "0" /
// empty-string to "success".
func TestValidateTokenIntCodeNoStatus(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"DomainInfoResponse":{"ResponseCode":0,"DomainInfo":{"Name":"example.com"}}}`))
	})
	if err := a.ValidateToken(context.Background(), "k:s", "example.com"); err != nil {
		t.Fatalf("ValidateToken err = %v (expected success on int-only ResponseCode)", err)
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

// TestValidateTokenAppError — real Dynadot error envelope: ResponseCode
// is the STRING "-1" (not the int 0 success returns), Status="error",
// Error message at TOP of DomainInfoResponse — no ResponseHeader wrapper.
func TestValidateTokenAppError(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"DomainInfoResponse":{"ResponseCode":"-1","Status":"error","Error":"Invalid api key"}}`))
	})
	err := a.ValidateToken(context.Background(), "k:s", "example.com")
	if !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

// TestValidateTokenDomainNotInAccount — captures the live "could not
// find domain in your account" message that Dynadot returns when the
// supplied (key, secret) authenticates but the domain is registered
// elsewhere. Maps to ErrDomainNotInAccount per #939. Captured live
// 2026-05-05 against does-not-exist-12345.works.
func TestValidateTokenDomainNotInAccount(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"DomainInfoResponse":{"ResponseCode":"-1","Status":"error","Error":"could not find domain in your account"}}`))
	})
	err := a.ValidateToken(context.Background(), "k:s", "not-mine.com")
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v, want ErrDomainNotInAccount", err)
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

// ── SetNameservers ───────────────────────────────────────────────────────

// TestSetNameserversHappy — Dynadot api3.json docs sample for set_ns:
//
//	{"SetNsResponse":{"ResponseCode":0,"Status":"success"}}
//
// Status fields live DIRECTLY under SetNsResponse (no ResponseHeader
// wrapper — see #939).
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
		w.Write([]byte(`{"SetNsResponse":{"ResponseCode":0,"Status":"success"}}`))
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

// TestSetNameserversDomainNotFound — real Dynadot error shape: code is
// the STRING "-1" at top of SetNsResponse (no ResponseHeader wrapper).
func TestSetNameserversDomainNotFound(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"SetNsResponse":{"ResponseCode":"-1","Status":"error","Error":"Domain not in your account"}}`))
	})
	err := a.SetNameservers(context.Background(), "k:s", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v, want ErrDomainNotInAccount", err)
	}
}

// TestSetNameserversNeedsGlueRegistration — real-world Dynadot error
// (issue #900) when set_ns references an out-of-bailiwick NS hostname
// that hasn't been pre-registered. The wrapper-bug-fixed adapter must
// now correctly surface this non-success; pre-fix this would have
// silently zeroed out and returned nil, masking the failure.
func TestSetNameserversNeedsGlueRegistration(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"SetNsResponse":{"ResponseCode":"-1","Status":"error","Error":"'ns1.otech103.omani.works' needs to be registered with an ip address before it can be used."}}`))
	})
	err := a.SetNameservers(context.Background(), "k:s", "x.com", []string{"ns1.otech103.omani.works", "ns2.otech103.omani.works"})
	if err == nil {
		t.Fatal("expected error for needs-glue-registration response")
	}
	if !strings.Contains(err.Error(), "needs to be registered") {
		t.Fatalf("err = %v, expected to surface registrar's needs-glue message", err)
	}
}

// ── GetNameservers ───────────────────────────────────────────────────────

// TestGetNameserversHappy — real Dynadot domain_info success shape
// (verified live 2026-05-05 against omani.works): ResponseCode is INT 0
// at TOP of DomainInfoResponse, ServerId+ServerName per NameServers
// entry, no ResponseHeader wrapper.
func TestGetNameserversHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{
		  "DomainInfoResponse": {
		    "ResponseCode": 0,
		    "Status": "success",
		    "DomainInfo": {
		      "NameServerSettings": {
		        "Type": "Name Servers",
		        "NameServers": [
		          {"ServerId":"5262350","ServerName":"ns1.openova.io"},
		          {"ServerId":"5262351","ServerName":"ns2.openova.io"}
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
// All fixtures below use the REAL Dynadot api3.json envelope shape
// (status fields directly under <Command>Response, no ResponseHeader
// wrapper). These tests cover the new register_ns / get_ns / set_ns_ip
// surface the adapter exposes via the registrar.GlueRegistrar interface.
//
// The flow under test:
//
//   1. GetGlueRecord — probes the host via `command=get_ns` and returns
//      the registered IPv4 (or "" + nil for "not registered").
//   2. RegisterGlueRecord — idempotency-aware: short-circuits on same
//      IP, falls through to set_ns_ip on different IP, register_ns when
//      not yet registered.

// TestGetGlueRecordHappy — real Dynadot get_ns success shape: status
// fields directly under GetNsResponse (no ResponseHeader wrapper, see
// #939). ResponseCode is INT 0 on success.
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
		    "ResponseCode": 0,
		    "Status": "success",
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
		// Real Dynadot error shape (string code "-1" at top of
		// GetNsResponse, no ResponseHeader wrapper — see #939).
		fmt.Fprintln(w, `{
		  "GetNsResponse": {"ResponseCode":"-1","Status":"error","Error":"Name server not found"}
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
			// Probe — not registered. Real Dynadot error shape.
			if cmd != "get_ns" {
				t.Errorf("call 1: command = %q, want get_ns", cmd)
			}
			fmt.Fprintln(w, `{"GetNsResponse":{"ResponseCode":"-1","Status":"error","Error":"Name server not found"}}`)
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
			// register_ns success — real Dynadot shape: code+status at
			// top of RegisterNsResponse (no wrapper).
			fmt.Fprintln(w, `{"RegisterNsResponse":{"ResponseCode":0,"Status":"success"}}`)
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
		// Real Dynadot get_ns success — code+status at top of
		// GetNsResponse (no ResponseHeader wrapper, see #939).
		fmt.Fprintln(w, `{
		  "GetNsResponse": {
		    "ResponseCode": 0,
		    "Status": "success",
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
			// Real Dynadot get_ns shape — top-level code+status, no wrapper.
			fmt.Fprintln(w, `{
			  "GetNsResponse": {
			    "ResponseCode": 0,
			    "Status": "success",
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
			// set_ns_ip success — real Dynadot shape: code+status at top
			// of SetNsIpResponse (no wrapper).
			fmt.Fprintln(w, `{"SetNsIpResponse":{"ResponseCode":0,"Status":"success"}}`)
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
			fmt.Fprintln(w, `{"GetNsResponse":{"ResponseCode":"-1","Status":"error","Error":"Name server not found"}}`)
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
		// parse for the set_ns_ip / register_ns path. Real Dynadot shape.
		fmt.Fprintln(w, `{"GetNsResponse":{"ResponseCode":"-1","Status":"error","Error":"Name server not found"}}`)
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
