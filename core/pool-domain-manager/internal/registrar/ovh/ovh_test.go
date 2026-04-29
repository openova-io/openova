package ovh

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// frozenTime fixes the clock so signatures are deterministic.
const frozenTS = int64(1700000000)

func newTestAdapter(t *testing.T, h http.HandlerFunc) (*Adapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := New()
	a.BaseURL = srv.URL + "/1.0"
	a.nowFn = func() int64 { return frozenTS }
	return a, srv
}

// expectedSig recomputes the signature the adapter would send. The test
// uses this to assert the signature math is correct.
func expectedSig(appSecret, consumerKey, method, fullURL, body, ts string) string {
	h := sha1.New()
	io.WriteString(h, appSecret+"+"+consumerKey+"+"+method+"+"+fullURL+"+"+body+"+"+ts)
	return "$1$" + hex.EncodeToString(h.Sum(nil))
}

func TestValidateTokenHappy(t *testing.T) {
	a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		// Auth headers must all be present.
		for _, k := range []string{"X-Ovh-Application", "X-Ovh-Consumer", "X-Ovh-Timestamp", "X-Ovh-Signature"} {
			if r.Header.Get(k) == "" {
				t.Errorf("missing header %s", k)
			}
		}
		if r.URL.Path != "/1.0/domain" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `["example.com","other.com"]`)
	})
	// Verify signature math.
	wantSig := expectedSig("sec", "ck", http.MethodGet, srv.URL+"/1.0/domain", "", "1700000000")
	a.HTTP.Transport = &assertHeaderTransport{t: t, key: "X-Ovh-Signature", want: wantSig, base: http.DefaultTransport}

	if err := a.ValidateToken(context.Background(), "ak:sec:ck", "example.com"); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// assertHeaderTransport asserts a request header equals an expected value.
type assertHeaderTransport struct {
	t    *testing.T
	key  string
	want string
	base http.RoundTripper
}

func (a *assertHeaderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if got := r.Header.Get(a.key); got != a.want {
		a.t.Errorf("header %s = %q want %q", a.key, got, a.want)
	}
	return a.base.RoundTrip(r)
}

func TestValidateTokenBadFormat(t *testing.T) {
	a := New()
	if err := a.ValidateToken(context.Background(), "two:parts", "x.com"); !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenUnauthorized(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"errorCode":"INVALID_CREDENTIAL","message":"This credential is not valid"}`)
	})
	err := a.ValidateToken(context.Background(), "ak:sec:ck", "example.com")
	if !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenDomainNotInAccount(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `["other.com"]`)
	})
	err := a.ValidateToken(context.Background(), "ak:sec:ck", "missing.com")
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.ValidateToken(context.Background(), "ak:sec:ck", "x.com")
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetNameserversHappy(t *testing.T) {
	posted := false
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/1.0/domain/example.com/nameServers/update" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		json.Unmarshal(body, &got)
		ns, _ := got["nameServers"].([]any)
		if len(ns) != 2 {
			t.Errorf("nameServers count = %d body=%s", len(ns), string(body))
		}
		first, _ := ns[0].(map[string]any)
		if first["host"] != "ns1.openova.io" {
			t.Errorf("first host = %v", first["host"])
		}
		posted = true
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":42,"status":"todo"}`)
	})
	err := a.SetNameservers(context.Background(), "ak:sec:ck", "example.com",
		[]string{"ns1.openova.io", "ns2.openova.io"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !posted {
		t.Fatal("POST not invoked")
	}
}

func TestSetNameserversEmpty(t *testing.T) {
	a := New()
	if err := a.SetNameservers(context.Background(), "ak:sec:ck", "x.com", nil); err == nil {
		t.Fatal("want error for empty NS")
	}
}

func TestSetNameserversBadDomain(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"errorCode":"NOT_FOUND","message":"Domain not found"}`)
	})
	err := a.SetNameservers(context.Background(), "ak:sec:ck", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetNameserversRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.SetNameservers(context.Background(), "ak:sec:ck", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetNameserversHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nameServer"):
			io.WriteString(w, `[1,2]`)
		case strings.HasSuffix(r.URL.Path, "/nameServer/1"):
			io.WriteString(w, `{"id":1,"host":"ns1.openova.io","type":"external"}`)
		case strings.HasSuffix(r.URL.Path, "/nameServer/2"):
			io.WriteString(w, `{"id":2,"host":"ns2.openova.io","type":"external"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	got, err := a.GetNameservers(context.Background(), "ak:sec:ck", "example.com")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(got, ",") != "ns1.openova.io,ns2.openova.io" {
		t.Fatalf("got = %v", got)
	}
}

func TestSignatureMath(t *testing.T) {
	// The signature is sha1(secret+"+"+ck+"+"+method+"+"+url+"+"+body+"+"+ts).
	got := expectedSig("S", "C", "GET", "https://x/1.0/domain", "", "1700000000")
	if !strings.HasPrefix(got, "$1$") || len(got) != 3+40 {
		t.Fatalf("sig shape wrong: %q", got)
	}
}

func TestNameAndDefaults(t *testing.T) {
	a := New()
	if a.Name() != "ovh" {
		t.Fatalf("Name = %q", a.Name())
	}
	if !strings.HasPrefix(a.BaseURL, "https://eu.api.ovh.com") {
		t.Fatalf("BaseURL = %q", a.BaseURL)
	}
}
