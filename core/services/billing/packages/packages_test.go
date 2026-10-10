package packages

// Tests for the price-book client (#6971): exact money parsing, the contract
// parser, the four pricing outcomes (optional / included-redundant /
// not-offered / unknown), and the 60 s cache with its fail-closed refresh.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// contractBody is the document in the shape the ticket specifies: S 5.000,
// M 9.000, L 15.000, XL 30.000; backup optional at 1.500 below XL and
// included on XL; dedicated-ip not offered on S.
const contractBody = `{
  "currency": "OMR", "price_book": "OpenOva plans", "prices_as_of": "2026-09-11",
  "packages": [
    {"sku":"plan.s","name":"S","price_month":"5.000","includes":{"vcpu":1,"memory_gb":2,"storage_gb":25,"bandwidth_mbps":50}},
    {"sku":"plan.m","name":"M","price_month":"9.000","includes":{"vcpu":2,"memory_gb":4,"storage_gb":50,"bandwidth_mbps":100}},
    {"sku":"plan.l","name":"L","price_month":"15.000","includes":{"vcpu":4,"memory_gb":8,"storage_gb":100,"bandwidth_mbps":250}},
    {"sku":"plan.xl","name":"XL","price_month":"30.000","includes":{"vcpu":8,"memory_gb":16,"storage_gb":200,"bandwidth_mbps":1000}}
  ],
  "features": [
    {"key":"backup","name":"Backup","kind":"boolean","cells":{
      "plan.s":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
      "plan.m":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
      "plan.l":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
      "plan.xl":{"state":"included"}}},
    {"key":"dedicated-ip","name":"Dedicated IP address","kind":"boolean","cells":{
      "plan.s":{"state":"not_offered"},
      "plan.m":{"state":"optional","addon_sku":"addon.dedicated-ip","price_month":"2.000"},
      "plan.l":{"state":"optional","addon_sku":"addon.dedicated-ip","price_month":"2.000"},
      "plan.xl":{"state":"optional","addon_sku":"addon.dedicated-ip","price_month":"2.000"}}},
    {"key":"bandwidth","name":"Bandwidth","kind":"quantity","unit":"Mbps","cells":{
      "plan.s":{"state":"included","quantity":50},"plan.m":{"state":"included","quantity":100},
      "plan.l":{"state":"included","quantity":250},"plan.xl":{"state":"included","quantity":1000}}}
  ]
}`

func mustParse(t *testing.T) *Document {
	t.Helper()
	doc, err := Parse([]byte(contractBody))
	if err != nil {
		t.Fatalf("Parse(contract): %v", err)
	}
	return doc
}

func TestMinorUnits_ExactDecimal(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"9.000", 9000},
		{"1.500", 1500},
		{"1.5", 1500},
		{"12", 12000},
		{"0.001", 1},
		{"0", 0},
		{"0.000", 0},
		{" 30.000 ", 30000},
		{"1234567.890", 1234567890},
	} {
		got, err := MinorUnits(tc.in, Decimals)
		if err != nil {
			t.Errorf("MinorUnits(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("MinorUnits(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", ".", ".5", "1.", "1.5001", "-1.000", "+1.000", "1e3", "abc", "1,500", "1.2.3", "9999999999999999999"} {
		if got, err := MinorUnits(bad, Decimals); err == nil {
			t.Errorf("MinorUnits(%q) = %d, want error", bad, got)
		}
	}
}

func TestFormatMinor_RoundTrips(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{10500, "10.500"}, {1, "0.001"}, {0, "0.000"}, {9000, "9.000"}, {-1500, "-1.500"},
	} {
		if got := FormatMinor(tc.in, Decimals); got != tc.want {
			t.Errorf("FormatMinor(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParse_Contract(t *testing.T) {
	doc := mustParse(t)
	if doc.Currency != "OMR" || doc.PriceBook != "OpenOva plans" || doc.PricesAsOf != "2026-09-11" {
		t.Fatalf("header: %+v", doc)
	}
	if doc.PriceSource() != "bss:OpenOva plans@2026-09-11" {
		t.Errorf("PriceSource = %q", doc.PriceSource())
	}
	if len(doc.Packages) != 4 || doc.Packages[1].SKU != "plan.m" || doc.Packages[1].PriceMinor != 9000 {
		t.Fatalf("packages: %+v", doc.Packages)
	}
	if len(doc.Features) != 3 {
		t.Fatalf("features: want 3, got %d", len(doc.Features))
	}
	cell := doc.Features[0].Cells["plan.m"]
	if cell.State != StateOptional || cell.AddonSKU != "addon.backup" || cell.PriceMinor != 1500 || cell.IncludedFrom != "plan.xl" {
		t.Errorf("backup on plan.m: %+v", cell)
	}
	if doc.Features[0].Cells["plan.xl"].State != StateIncluded {
		t.Errorf("backup on plan.xl should be included")
	}
}

func TestParse_RefusesNonContract(t *testing.T) {
	for name, body := range map[string]string{
		"not json":         `{`,
		"no packages":      `{"currency":"OMR","packages":[]}`,
		"other currency":   `{"currency":"USD","packages":[{"sku":"plan.s","name":"S","price_month":"5.00"}]}`,
		"package no price": `{"currency":"OMR","packages":[{"sku":"plan.s","name":"S"}]}`,
		"float money":      `{"currency":"OMR","packages":[{"sku":"plan.s","name":"S","price_month":"5.0001"}]}`,
		"unknown state":    `{"currency":"OMR","packages":[{"sku":"plan.s","name":"S","price_month":"5.000"}],"features":[{"key":"x","name":"X","cells":{"plan.s":{"state":"maybe"}}}]}`,
		"optional no sku":  `{"currency":"OMR","packages":[{"sku":"plan.s","name":"S","price_month":"5.000"}],"features":[{"key":"x","name":"X","cells":{"plan.s":{"state":"optional","price_month":"1.000"}}}]}`,
		"duplicate sku":    `{"currency":"OMR","packages":[{"sku":"plan.s","name":"S","price_month":"5.000"},{"sku":"plan.s","name":"S2","price_month":"6.000"}]}`,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: Parse accepted a non-contract body", name)
		}
	}
}

// Plan M + optional backup → 9.000 + 1.500.
func TestPrice_OptionalAddonIsPriced(t *testing.T) {
	q, err := mustParse(t).Price("plan.m", []string{"addon.backup"})
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	if q.PlanMinor != 9000 {
		t.Errorf("plan = %d, want 9000", q.PlanMinor)
	}
	if len(q.Lines) != 1 || q.Lines[0].SKU != "addon.backup" || q.Lines[0].Name != "Backup" || q.Lines[0].PriceMinor != 1500 || q.Lines[0].Redundant {
		t.Fatalf("lines = %+v", q.Lines)
	}
	if q.Total() != 10500 {
		t.Errorf("total = %d, want 10500", q.Total())
	}
	if q.PriceSource != "bss:OpenOva plans@2026-09-11" {
		t.Errorf("price source = %q", q.PriceSource)
	}
}

// Plan XL + backup → 30.000 and the add-on marked redundant, not an error.
func TestPrice_IncludedAddonIsRedundantNotAnError(t *testing.T) {
	q, err := mustParse(t).Price("plan.xl", []string{"addon.backup"})
	if err != nil {
		t.Fatalf("Price: %v", err)
	}
	if q.Total() != 30000 {
		t.Errorf("total = %d, want 30000", q.Total())
	}
	if len(q.Lines) != 1 || !q.Lines[0].Redundant || q.Lines[0].PriceMinor != 0 {
		t.Fatalf("lines = %+v, want one redundant zero line", q.Lines)
	}
}

// Plan S + an add-on that is not_offered on S → refused, naming both.
func TestPrice_NotOfferedIsRefusedNamingBoth(t *testing.T) {
	_, err := mustParse(t).Price("plan.s", []string{"addon.dedicated-ip"})
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want *RefusedError, got %v", err)
	}
	if refused.AddonSKU != "addon.dedicated-ip" || refused.PackageSKU != "plan.s" {
		t.Errorf("refused = %+v", refused)
	}
	for _, want := range []string{"addon.dedicated-ip", "plan.s", "not offered"} {
		if !contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err.Error(), want)
		}
	}
}

func TestPrice_UnknownAddonAndUnknownPackageAreRefused(t *testing.T) {
	doc := mustParse(t)
	var refused *RefusedError
	if _, err := doc.Price("plan.m", []string{"addon.nope"}); !errors.As(err, &refused) || refused.AddonSKU != "addon.nope" {
		t.Errorf("unknown add-on: got %v", err)
	}
	if _, err := doc.Price("plan.xxl", nil); !errors.As(err, &refused) || refused.PackageSKU != "plan.xxl" {
		t.Errorf("unknown package: got %v", err)
	}
	// A refusal happens before any line is priced: the first bad SKU wins
	// even when a good one precedes it.
	if _, err := doc.Price("plan.m", []string{"addon.backup", "addon.nope"}); !errors.As(err, &refused) {
		t.Errorf("mixed list: want refusal, got %v", err)
	}
}

func TestIsAddonSKU(t *testing.T) {
	if !IsAddonSKU("addon.backup") || IsAddonSKU("waf") || IsAddonSKU("plan.m") || IsAddonSKU("") {
		t.Error("IsAddonSKU misclassifies")
	}
}

func TestClient_CachesForTTLAndFailsClosedAfter(t *testing.T) {
	var hits int32
	var status int32 = http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != Path {
			t.Errorf("path = %q, want %q", r.URL.Path, Path)
		}
		if s := atomic.LoadInt32(&status); s != http.StatusOK {
			w.WriteHeader(int(s))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(contractBody))
	}))
	defer srv.Close()

	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	c := NewClient(srv.URL, nil)
	c.now = func() time.Time { return now }
	if !c.Configured() {
		t.Fatal("client with a base URL must be configured")
	}

	ctx := context.Background()
	if _, err := c.Get(ctx); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if _, err := c.Get(ctx); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("two Gets inside the TTL made %d requests, want 1", got)
	}

	// Past the TTL the document is re-read.
	now = now.Add(DefaultTTL + time.Second)
	if _, err := c.Get(ctx); err != nil {
		t.Fatalf("Get after TTL: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("Get after TTL made %d requests in total, want 2", got)
	}

	// Past the TTL with BSS down: the stale copy is NOT served — the caller
	// must see ErrUnavailable, never a price from a list it cannot verify.
	atomic.StoreInt32(&status, http.StatusBadGateway)
	now = now.Add(DefaultTTL + time.Second)
	_, err := c.Get(ctx)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get with BSS down after TTL: want ErrUnavailable, got %v", err)
	}
}

func TestClient_UnconfiguredAndUnreachable(t *testing.T) {
	ctx := context.Background()
	var nilClient *Client
	if nilClient.Configured() {
		t.Error("nil client must not be configured")
	}
	if _, err := nilClient.Get(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil client Get: want ErrUnavailable, got %v", err)
	}
	if _, err := NewClient("", nil).Get(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("empty URL Get: want ErrUnavailable, got %v", err)
	}
	// A closed server — connection refused — is unavailable too.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := NewClient(url, &http.Client{Timeout: time.Second}).Get(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("dead server Get: want ErrUnavailable, got %v", err)
	}
	// A non-contract body is unavailable, not a partial price list.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"currency":"OMR","packages":[]}`))
	}))
	defer bad.Close()
	if _, err := NewClient(bad.URL, nil).Get(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("empty-packages body: want ErrUnavailable, got %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
