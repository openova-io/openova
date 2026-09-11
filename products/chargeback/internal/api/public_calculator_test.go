package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The token bucket: a minute's budget up front, one token per interval after
// that, Retry-After rounded up to whole seconds, and a fresh key gets its own
// bucket.
func TestIPLimiterBudgetAndRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	l := newIPLimiter(6, func() time.Time { return now })
	for i := 0; i < 6; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("request %d within the budget refused", i+1)
		}
	}
	ok, wait := l.allow("a")
	if ok || wait != 10*time.Second {
		t.Fatalf("over budget: ok=%v wait=%v, want 10s (one token per 10s)", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("another key has its own budget")
	}
	now = now.Add(10 * time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("one interval later a token is back")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("only one token came back")
	}
	now = now.Add(time.Hour)
	for i := 0; i < 6; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("after an hour the burst is whole again (request %d)", i+1)
		}
	}
	// Idle buckets are swept once the map is large.
	for i := 0; i < limiterSweepAt; i++ {
		l.allow(strings.Repeat("k", 3) + string(rune('a'+i%26)) + time.Duration(i).String())
	}
	now = now.Add(2 * limiterIdleTTL)
	l.allow("fresh")
	if len(l.buckets) > 2 {
		t.Fatalf("sweep left %d buckets", len(l.buckets))
	}
}

// The address the budget is charged to: the peer when it is public; behind a
// private peer (the gateway) the LAST X-Forwarded-For hop — the one the
// gateway appended — never the first, which a caller writes itself.
func TestClientIPTrustsOnlyTheGatewayHop(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if ip := clientIP(req); ip != "203.0.113.9" {
		t.Fatalf("public peer must win over the header: %s", ip)
	}
	req.RemoteAddr = "10.244.1.7:4444"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.7")
	if ip := clientIP(req); ip != "198.51.100.7" {
		t.Fatalf("last hop behind a private peer: %s", ip)
	}
	req.Header.Del("X-Forwarded-For")
	req.Header.Set("X-Real-Ip", "198.51.100.8")
	if ip := clientIP(req); ip != "198.51.100.8" {
		t.Fatalf("X-Real-Ip fallback: %s", ip)
	}
	req.Header.Del("X-Real-Ip")
	if ip := clientIP(req); ip != "10.244.1.7" {
		t.Fatalf("no header: the peer: %s", ip)
	}
	if h := clientHash("198.51.100.8"); len(h) != 32 || h == "198.51.100.8" {
		t.Fatalf("hash = %q", h)
	}
}

// Every refusal names the line and the rule; defaults are 1 unit, 730 hours,
// 1 month; a plan becomes its plan.<slug> SKU; flexi is refused with the
// pay-per-use meters named.
func TestParseEstimateLines(t *testing.T) {
	items := map[string]store.PriceItem{
		"ecs.s6.large.2": {SKU: "ecs.s6.large.2", Unit: "instance-hour", UnitPrice: "0.1"},
		"plan.m":         {SKU: "plan.m", Unit: "plan-hour", UnitPrice: "0.01232877"},
	}
	dec := func(s string) *store.Decimal { d := store.Decimal(s); return &d }
	num := func(n int) *int { return &n }
	lines, plans, msg := parseEstimateLines([]estimateLineBody{{SKU: "ecs.s6.large.2"}, {SKU: " ecs.s6.large.2 ", Quantity: dec("2.5"), HoursPerMonth: dec("100")}, {Plan: "M", Months: num(3), Quantity: dec("2")}}, items)
	if msg != "" {
		t.Fatal(msg)
	}
	if lines[0].Quantity != "1" || lines[0].Hours != "730" || lines[0].Months != 1 || plans[0] != "" {
		t.Fatalf("defaults = %+v", lines[0])
	}
	if lines[1].Quantity != "2.5" || lines[1].Hours != "100" || lines[1].SKU != "ecs.s6.large.2" {
		t.Fatalf("explicit = %+v", lines[1])
	}
	if lines[2].SKU != "plan.m" || lines[2].Months != 3 || lines[2].Quantity != "2" || lines[2].Hours != "730" || plans[2] != "m" {
		t.Fatalf("plan = %+v %q", lines[2], plans[2])
	}
	cases := map[string][]estimateLineBody{
		"at least one line":            {},
		"line 1: sku or plan":          {{}},
		"give sku or plan, not both":   {{SKU: "ecs.s6.large.2", Plan: "m"}},
		`unknown sku "ecs.nope"`:       {{SKU: "ecs.nope"}},
		`unknown plan "xxl"`:           {{Plan: "xxl"}},
		"pay per use":                  {{Plan: "flexi"}},
		"hours_per_month does not":     {{Plan: "m", HoursPerMonth: dec("100")}},
		"months must be between":       {{Plan: "m", Months: num(13)}},
		"months applies to a plan":     {{SKU: "ecs.s6.large.2", Months: num(2)}},
		"hours_per_month must be more": {{SKU: "ecs.s6.large.2", HoursPerMonth: dec("745")}},
		"quantity must be more":        {{SKU: "ecs.s6.large.2", Quantity: dec("0")}},
		"line 2: quantity":             {{SKU: "ecs.s6.large.2"}, {SKU: "ecs.s6.large.2", Quantity: dec("1000000001")}},
	}
	for want, in := range cases {
		if _, _, msg := parseEstimateLines(in, items); !strings.Contains(msg, want) {
			t.Fatalf("%v: message %q lacks %q", in, msg, want)
		}
	}
	many := make([]estimateLineBody, estimateMaxLines+1)
	for i := range many {
		many[i] = estimateLineBody{SKU: "ecs.s6.large.2"}
	}
	if _, _, msg := parseEstimateLines(many, items); !strings.Contains(msg, "at most 200 lines") {
		t.Fatalf("201 lines: %q", msg)
	}
}

// The estimate page may be framed only by the configured origins, and only
// it; the API and the console stay DENY. Origins gate CORS the same way.
func TestFrameHeadersAndOrigins(t *testing.T) {
	deny := &Handler{Deps: Deps{Config: config.Config{}}}
	allow := &Handler{Deps: Deps{Config: config.Config{PublicCalculatorOrigins: []string{"https://www.omantel.om"}}}}
	for _, tc := range []struct {
		h    *Handler
		path string
		want string
	}{
		{deny, "/estimate", "X-Frame-Options: DENY"},
		{deny, "/overview", "X-Frame-Options: DENY"},
		{allow, "/estimate", "Content-Security-Policy: frame-ancestors 'self' https://www.omantel.om"},
		{allow, "/estimate/abc", "Content-Security-Policy: frame-ancestors 'self' https://www.omantel.om"},
		{allow, "/estimates", "X-Frame-Options: DENY"},
		{allow, "/api/v1/public/catalog", "X-Frame-Options: DENY"},
		{allow, "/overview", "X-Frame-Options: DENY"},
	} {
		rec := httptest.NewRecorder()
		tc.h.frameHeaders(rec, httptest.NewRequest("GET", tc.path, nil))
		k, v, _ := strings.Cut(tc.want, ": ")
		if got := rec.Header().Get(k); got != v {
			t.Fatalf("%s: %s = %q, want %q (headers %v)", tc.path, k, got, v, rec.Header())
		}
		if k == "Content-Security-Policy" && rec.Header().Get("X-Frame-Options") != "" {
			t.Fatalf("%s: DENY must not accompany frame-ancestors", tc.path)
		}
	}
	if deny.originAllowed("https://www.omantel.om") || !allow.originAllowed("HTTPS://WWW.OMANTEL.OM") || allow.originAllowed("https://evil.example") || allow.originAllowed("") {
		t.Fatal("origin allow-list")
	}
	any := &Handler{Deps: Deps{Config: config.Config{PublicCalculatorOrigins: []string{"*"}}}}
	if !any.originAllowed("https://anything.example") {
		t.Fatal("* allows any origin")
	}
	if !isPublicPath("/api/v1/public/catalog") || isPublicPath("/api/v1/publicity") || isPublicPath("/api/v1/pricebooks") {
		t.Fatal("public path prefix")
	}
	if serviceOf("ecs.s6.large.2") != "ecs" || serviceOf("eip.bandwidth_mbps") != "eip" || serviceOf("eip-bw") != "eip" || serviceOf("k8s.vcpu") != "k8s" || serviceOf("plan.m") != "plan" || serviceOf("obs") != "obs" {
		t.Fatal("serviceOf")
	}
	_ = http.StatusOK
}
