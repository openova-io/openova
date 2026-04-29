// Tests for subdomains.go — the catalyst-api side of the PDM contract.
// These cover three architectural invariants:
//
//   1. Managed pools NEVER call net.LookupHost. The DNS-wildcard parking
//      record at omani.works (which previously made every subdomain
//      resolve to 185.53.179.128) cannot cause a false positive when
//      the pool is in DYNADOT_MANAGED_DOMAINS — PDM is the single source
//      of truth.
//   2. BYO domains use net.LookupHost — the customer's nameserver is
//      authoritative; PDM doesn't manage their zone.
//   3. The PDM client is consulted exactly once per managed-pool request,
//      with the response surfaced verbatim.
//
// These guarantees are what prevent the regression #163 was opened for.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
)

// fakePDM is a stub pdmClient that records every call. We assert against the
// recorded calls to prove the behaviour the architecture requires.
type fakePDM struct {
	checks   []checkCall
	check    func(ctx context.Context, pool, sub string) (*pdm.CheckResult, error)
	reserves []reserveCall
	reserve  func(ctx context.Context, pool, sub, by string) (*pdm.Reservation, error)
	commits  []pdm.CommitInput
	commit   func(ctx context.Context, pool string, in pdm.CommitInput) error
	releases []releaseCall
	release  func(ctx context.Context, pool, sub string) error
}

type checkCall struct{ pool, sub string }
type reserveCall struct{ pool, sub, by string }
type releaseCall struct{ pool, sub string }

func (f *fakePDM) Check(ctx context.Context, pool, sub string) (*pdm.CheckResult, error) {
	f.checks = append(f.checks, checkCall{pool, sub})
	if f.check != nil {
		return f.check(ctx, pool, sub)
	}
	return &pdm.CheckResult{Available: true, FQDN: sub + "." + pool}, nil
}

func (f *fakePDM) Reserve(ctx context.Context, pool, sub, by string) (*pdm.Reservation, error) {
	f.reserves = append(f.reserves, reserveCall{pool, sub, by})
	if f.reserve != nil {
		return f.reserve(ctx, pool, sub, by)
	}
	return &pdm.Reservation{
		PoolDomain: pool, Subdomain: sub, State: "reserved",
		ReservationToken: "00000000-0000-0000-0000-000000000000",
	}, nil
}

func (f *fakePDM) Commit(ctx context.Context, pool string, in pdm.CommitInput) error {
	f.commits = append(f.commits, in)
	if f.commit != nil {
		return f.commit(ctx, pool, in)
	}
	return nil
}

func (f *fakePDM) Release(ctx context.Context, pool, sub string) error {
	f.releases = append(f.releases, releaseCall{pool, sub})
	if f.release != nil {
		return f.release(ctx, pool, sub)
	}
	return nil
}

func decodeResp(t *testing.T, body io.Reader) SubdomainCheckResponse {
	t.Helper()
	var got SubdomainCheckResponse
	raw, _ := io.ReadAll(body)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, string(raw))
	}
	return got
}

func makeRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/subdomains/check", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestCheckSubdomain_ManagedPoolDelegatesToPDM(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works,openova.io")
	pdm.ResetManagedDomains()

	fake := &fakePDM{
		check: func(ctx context.Context, pool, sub string) (*pdm.CheckResult, error) {
			return &pdm.CheckResult{Available: true, FQDN: sub + "." + pool}, nil
		},
	}
	h := NewWithPDM(slog.Default(), fake)

	w := httptest.NewRecorder()
	h.CheckSubdomain(w, makeRequest(`{"subdomain":"dadasg4543sdfs","poolDomain":"omani.works"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeResp(t, w.Body)
	if !got.Available {
		t.Errorf("Available=false body=%+v", got)
	}
	if len(fake.checks) != 1 || fake.checks[0].pool != "omani.works" || fake.checks[0].sub != "dadasg4543sdfs" {
		t.Errorf("expected single PDM check call, got %+v", fake.checks)
	}
}

// The architectural invariant: even if the customer happens to type a
// random string that "would" resolve via the omani.works wildcard, PDM
// (which has no DNS dependency) returns Available=true — i.e. the
// wildcard parking record is NEVER consulted on the managed-pool path.
func TestCheckSubdomain_WildcardParkingIsIgnored(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	pdm.ResetManagedDomains()

	fake := &fakePDM{
		check: func(ctx context.Context, pool, sub string) (*pdm.CheckResult, error) {
			// PDM has nothing in its allocation table — it returns
			// Available=true regardless of what DNS says.
			return &pdm.CheckResult{Available: true, FQDN: sub + "." + pool}, nil
		},
	}
	h := NewWithPDM(slog.Default(), fake)

	for _, sub := range []string{"foo", "dadasg4543sdfs", "totally-random-name"} {
		w := httptest.NewRecorder()
		h.CheckSubdomain(w, makeRequest(`{"subdomain":"`+sub+`","poolDomain":"omani.works"}`))
		got := decodeResp(t, w.Body)
		if !got.Available {
			t.Errorf("sub=%s: Available=false (wildcard regression!): %+v", sub, got)
		}
	}
	// Exactly one check per call — PDM is consulted, DNS is not.
	if len(fake.checks) != 3 {
		t.Errorf("expected 3 PDM checks, got %d", len(fake.checks))
	}
}

func TestCheckSubdomain_ManagedPoolPDMConflict(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	pdm.ResetManagedDomains()

	fake := &fakePDM{
		check: func(ctx context.Context, pool, sub string) (*pdm.CheckResult, error) {
			return &pdm.CheckResult{
				Available: false,
				Reason:    "active-state",
				Detail:    "this subdomain is already taken by a live Sovereign — pick a different name",
				FQDN:      sub + "." + pool,
			}, nil
		},
	}
	h := NewWithPDM(slog.Default(), fake)

	w := httptest.NewRecorder()
	h.CheckSubdomain(w, makeRequest(`{"subdomain":"omantel","poolDomain":"omani.works"}`))
	got := decodeResp(t, w.Body)
	if got.Available {
		t.Fatalf("expected unavailable, got %+v", got)
	}
	if got.Reason != "active-state" {
		t.Errorf("Reason=%s want active-state", got.Reason)
	}
}

func TestCheckSubdomain_BYOFallsBackToDNS(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	// Pick a domain that is guaranteed to be in DNS — example.com always
	// resolves. The handler should call LookupHost and surface "exists".
	w := httptest.NewRecorder()
	h.CheckSubdomain(w, makeRequest(`{"subdomain":"www","poolDomain":"example.com"}`))
	got := decodeResp(t, w.Body)
	if got.Available {
		t.Errorf("www.example.com should resolve and be unavailable: %+v", got)
	}
	if len(fake.checks) != 0 {
		t.Errorf("BYO path must NOT consult PDM; got %d checks", len(fake.checks))
	}
}

func TestCheckSubdomain_BYONXDomainAvailable(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	// A guaranteed-NXDOMAIN under example.com (RFC 6761).
	w := httptest.NewRecorder()
	h.CheckSubdomain(w, makeRequest(`{"subdomain":"this-name-must-not-resolve-1234567","poolDomain":"example.com"}`))
	got := decodeResp(t, w.Body)
	if !got.Available {
		t.Errorf("BYO NXDOMAIN should be available, got %+v", got)
	}
}

func TestCheckSubdomain_InvalidLabel(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	pdm.ResetManagedDomains()

	fake := &fakePDM{}
	h := NewWithPDM(slog.Default(), fake)

	w := httptest.NewRecorder()
	h.CheckSubdomain(w, makeRequest(`{"subdomain":"-bad-","poolDomain":"omani.works"}`))
	got := decodeResp(t, w.Body)
	if got.Available {
		t.Errorf("invalid label should be unavailable")
	}
	if got.Reason != "invalid-format" {
		t.Errorf("Reason=%s want invalid-format", got.Reason)
	}
	if len(fake.checks) != 0 {
		t.Errorf("invalid label must short-circuit before PDM is called")
	}
}

func TestCheckSubdomain_PDMUnavailable(t *testing.T) {
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	pdm.ResetManagedDomains()

	fake := &fakePDM{
		check: func(ctx context.Context, pool, sub string) (*pdm.CheckResult, error) {
			return nil, errors.New("connection refused")
		},
	}
	h := NewWithPDM(slog.Default(), fake)

	w := httptest.NewRecorder()
	h.CheckSubdomain(w, makeRequest(`{"subdomain":"omantel","poolDomain":"omani.works"}`))
	got := decodeResp(t, w.Body)
	if got.Available {
		t.Errorf("PDM unavailable must NOT be reported as Available=true")
	}
	if got.Reason != "pdm-unavailable" {
		t.Errorf("Reason=%s want pdm-unavailable", got.Reason)
	}
}
