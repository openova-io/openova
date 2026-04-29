package namecheap

import (
	"context"
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

const okBalance = `<?xml version="1.0" encoding="UTF-8"?>
<ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response">
  <Errors/>
  <RequestedCommand>namecheap.users.getbalances</RequestedCommand>
  <CommandResponse Type="namecheap.users.getBalances">
    <UserGetBalancesResult Currency="USD" AvailableBalance="0.00"/>
  </CommandResponse>
  <Server>WEB1</Server>
</ApiResponse>`

func TestValidateTokenHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		// All required auth params present.
		q := r.URL.Query()
		for _, k := range []string{"ApiUser", "ApiKey", "UserName", "ClientIp", "Command"} {
			if q.Get(k) == "" {
				t.Errorf("missing %s in query", k)
			}
		}
		switch q.Get("Command") {
		case "namecheap.users.getBalances":
			io.WriteString(w, okBalance)
		case "namecheap.domains.getList":
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response">
  <Errors/>
  <CommandResponse Type="namecheap.domains.getList">
    <DomainGetListResult>
      <Domain ID="1" Name="example.com" User="testuser"/>
    </DomainGetListResult>
    <Paging><TotalItems>1</TotalItems><CurrentPage>1</CurrentPage><PageSize>100</PageSize></Paging>
  </CommandResponse>
</ApiResponse>`)
		}
	})
	if err := a.ValidateToken(context.Background(), "user:key:user:1.2.3.4", "example.com"); err != nil {
		t.Fatalf("ValidateToken err = %v", err)
	}
}

func TestValidateTokenBadFormat(t *testing.T) {
	a := New()
	if err := a.ValidateToken(context.Background(), "only-one-part", "x.com"); !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenAuthError(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<?xml version="1.0"?>
<ApiResponse Status="ERROR">
  <Errors><Error Number="1010100">Invalid ApiUser</Error></Errors>
</ApiResponse>`)
	})
	err := a.ValidateToken(context.Background(), "u:k:u:1.2.3.4", "example.com")
	if !errors.Is(err, registrar.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.ValidateToken(context.Background(), "u:k:u:1.2.3.4", "x.com")
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTokenDomainNotInAccount(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("Command") {
		case "namecheap.users.getBalances":
			io.WriteString(w, okBalance)
		case "namecheap.domains.getList":
			io.WriteString(w, `<?xml version="1.0"?>
<ApiResponse Status="OK">
  <Errors/>
  <CommandResponse Type="namecheap.domains.getList">
    <DomainGetListResult>
      <Domain ID="1" Name="other.com"/>
    </DomainGetListResult>
    <Paging><TotalItems>1</TotalItems><CurrentPage>1</CurrentPage><PageSize>100</PageSize></Paging>
  </CommandResponse>
</ApiResponse>`)
		}
	})
	err := a.ValidateToken(context.Background(), "u:k:u:1.2.3.4", "missing.com")
	if !errors.Is(err, registrar.ErrDomainNotInAccount) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetNameserversHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("Command") != "namecheap.domains.dns.setCustom" {
			t.Errorf("Command = %q", q.Get("Command"))
		}
		if q.Get("SLD") != "example" || q.Get("TLD") != "com" {
			t.Errorf("SLD/TLD = %q/%q", q.Get("SLD"), q.Get("TLD"))
		}
		if q.Get("Nameservers") != "ns1.openova.io,ns2.openova.io" {
			t.Errorf("Nameservers = %q", q.Get("Nameservers"))
		}
		io.WriteString(w, `<?xml version="1.0"?>
<ApiResponse Status="OK">
  <Errors/>
  <CommandResponse><DomainDNSSetCustomResult Domain="example.com" Updated="true"/></CommandResponse>
</ApiResponse>`)
	})
	err := a.SetNameservers(context.Background(), "u:k:u:1.2.3.4", "example.com",
		[]string{"ns1.openova.io", "ns2.openova.io"})
	if err != nil {
		t.Fatalf("SetNameservers err = %v", err)
	}
}

func TestSetNameserversBadDomain(t *testing.T) {
	a := New()
	err := a.SetNameservers(context.Background(), "u:k:u:1.2.3.4", "no-tld", []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "invalid domain") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetNameserversRateLimited(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := a.SetNameservers(context.Background(), "u:k:u:1.2.3.4", "x.com", []string{"a", "b"})
	if !errors.Is(err, registrar.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetNameserversHappy(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<?xml version="1.0"?>
<ApiResponse Status="OK">
  <Errors/>
  <CommandResponse>
    <DomainDNSGetListResult Domain="example.com" IsUsingOurDNS="false">
      <Nameserver>ns1.openova.io</Nameserver>
      <Nameserver>ns2.openova.io</Nameserver>
    </DomainDNSGetListResult>
  </CommandResponse>
</ApiResponse>`)
	})
	got, err := a.GetNameservers(context.Background(), "u:k:u:1.2.3.4", "example.com")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(got, ",") != "ns1.openova.io,ns2.openova.io" {
		t.Fatalf("got = %v", got)
	}
}

func TestSplitDomain(t *testing.T) {
	cases := []struct {
		in       string
		sld, tld string
		err      bool
	}{
		{"example.com", "example", "com", false},
		{"acme.co.uk", "acme", "co.uk", false},
		{"weird", "", "", true},
		{".invalid", "", "", true},
		{"trailing.", "", "", true},
	}
	for _, c := range cases {
		s, t1, e := splitDomain(c.in)
		if c.err {
			if e == nil {
				t.Errorf("splitDomain(%q) want err", c.in)
			}
			continue
		}
		if s != c.sld || t1 != c.tld {
			t.Errorf("splitDomain(%q) = %q,%q want %q,%q", c.in, s, t1, c.sld, c.tld)
		}
	}
}

func TestParseToken4Part(t *testing.T) {
	c, err := parseToken("a:b:c:1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if c.APIUser != "a" || c.APIKey != "b" || c.UserName != "c" || c.ClientIP != "1.2.3.4" {
		t.Fatalf("got %+v", c)
	}
}

func TestParseToken3Part(t *testing.T) {
	c, err := parseToken("a:b:1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if c.UserName != "a" || c.ClientIP != "1.2.3.4" {
		t.Fatalf("got %+v", c)
	}
}

func TestNameAndSandbox(t *testing.T) {
	if New().Name() != "namecheap" {
		t.Fatal("Name() != namecheap")
	}
	sb := NewSandbox()
	if !strings.Contains(sb.BaseURL, "sandbox") {
		t.Fatalf("sandbox URL = %q", sb.BaseURL)
	}
}
