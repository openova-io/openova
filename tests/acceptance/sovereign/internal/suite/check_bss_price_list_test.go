package suite

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// priceListSovereign answers as the hosts of sov.test do: the price book on
// chargeback, the plan deck and the quote on marketplace. It keeps every
// request as "<host> <method> <path>". The price book answers docStatus, or,
// when docStatuses is set, one status after the other in turn, as two regions
// behind one address do; it sends the CORS header the storefront needs unless
// noCORS is set.
type priceListSovereign struct {
	t                      *testing.T
	docStatus, quoteStatus int
	docStatuses            []int
	noCORS                 bool
	doc, quote             string
	seen                   []string
	origins                []string
	docAsked               int
	quoteBody              map[string]any
}

const priceListPlans = `[
  {"id":"sb-free","slug":"sandbox-free","product_slug":"sandbox","price_baisa":0},
  {"id":"id-m","slug":"m","price_baisa":4490},
  {"id":"id-s","slug":"s","price_baisa":2490}]`

const priceListDoc = `{"currency":"OMR","packages":[{"sku":"plan.s","price_month":"2.490"},{"sku":"plan.m","price_month":"4.490"}]}`

func (f *priceListSovereign) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.seen = append(f.seen, r.Host+" "+r.Method+" "+r.URL.RequestURI())
	switch r.Host + " " + r.URL.Path {
	case "chargeback.sov.test /api/v1/public/packages":
		f.origins = append(f.origins, r.Header.Get("Origin"))
		status := f.docStatus
		if len(f.docStatuses) > 0 {
			status = f.docStatuses[f.docAsked%len(f.docStatuses)]
		}
		f.docAsked++
		if !f.noCORS && status == http.StatusOK {
			w.Header().Set("Access-Control-Allow-Origin", "https://marketplace.sov.test")
		}
		w.WriteHeader(status)
		switch status {
		case http.StatusOK:
			_, _ = io.WriteString(w, f.doc)
		case http.StatusBadGateway:
			_, _ = io.WriteString(w, "<html>502 Bad Gateway</html>")
		}
	case "marketplace.sov.test /api/catalog/plans":
		_, _ = io.WriteString(w, priceListPlans)
	case "marketplace.sov.test /api/billing/quote":
		if err := json.NewDecoder(r.Body).Decode(&f.quoteBody); err != nil {
			f.t.Errorf("quote body: %v", err)
		}
		w.WriteHeader(f.quoteStatus)
		_, _ = io.WriteString(w, f.quote)
	default:
		w.WriteHeader(http.StatusTeapot)
	}
}

// priceListRun runs BSS-G02 against f, every https host routed to it.
func priceListRun(t *testing.T, f *priceListSovereign, prior ...*Step) *runner {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	transport := priceListRoute(func(r *http.Request) (*http.Response, error) {
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host, out.Host = "http", srv.Listener.Addr().String(), r.URL.Host
		return http.DefaultTransport.RoundTrip(out)
	})
	return runCheck(t, "none", priceListCheck{transport: transport}, &CheckContext{}, prior...)
}

type priceListRoute func(*http.Request) (*http.Response, error)

func (f priceListRoute) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func priceListD01() *Step {
	return &Step{Journey: "D0", ID: "D0.1", Name: "whoami", Status: Pass, Evidence: Evidence{"sovereignFQDN": "sov.test"}}
}

func TestPriceListCheck(t *testing.T) {
	for _, c := range []struct {
		name, doc, quote, fail string
		docStatus, quoteStatus int
	}{
		{"published, refused", priceListDoc, `{"error":"package-required"}`, "", 200, 409},
		{"published, priced from the document", priceListDoc, `{"amount_baisa":2490}`, "", 200, 200},
		// #7909 — the engine ships a default offer: publishing nothing fails,
		// whatever the catalog answers (a fresh installation passed here while
		// its storefront showed an old built-in deck).
		{"none: 404, the catalog answers", "", `{"amount_baisa":2490,"price_source":"catalog"}`, "publishes no package on any of 12 connections", 404, 200},
		{"none: an empty list, the catalog answers", `{"packages":[]}`, `{"amount_baisa":2490}`, "publishes no package on any of 12 connections", 200, 200},
		{"published, priced from another list", priceListDoc, `{"amount_baisa":3000}`, "the quote of plan id-s is 3000 baisa and the price book shows 2.490 for plan.s", 200, 200},
		{"published, the plan is not listed", `{"packages":[{"sku":"plan.m","price_month":"4.490"}]}`, `{"amount_baisa":2490}`, "does not list plan.s", 200, 200},
		{"published, an outage", priceListDoc, `{"error":"prices unavailable"}`, "answered HTTP 503", 200, 503},
		{"published, refused with another reason", priceListDoc, `{"error":"plan not found"}`, "answered HTTP 409", 200, 409},
		{"none, refused", "", `{"error":"package-required"}`, "publishes no package", 404, 409},
		{"the price book cannot be read", "", "", "0 answered the packages, 12 HTTP 500", 500, 200},
		{"the price book is not JSON", "<html>", "", "12 a body that is not a JSON document", 200, 200},
	} {
		f := &priceListSovereign{docStatus: c.docStatus, doc: c.doc, quoteStatus: c.quoteStatus, quote: c.quote}
		r := priceListRun(t, f, priceListD01())
		got := r.run.Checks[0]
		switch {
		case c.fail == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %s %q", c.name, got.Status, got.Reason)
		case c.fail != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.fail)):
			t.Errorf("%s: want FAIL with %q, got %s %q", c.name, c.fail, got.Status, got.Reason)
		}
	}
}

func TestPriceListRequests(t *testing.T) {
	f := &priceListSovereign{docStatus: 200, doc: priceListDoc, quoteStatus: 409, quote: `{"error":"package-required"}`}
	r := priceListRun(t, f, priceListD01())
	var want []string
	for i := 0; i < priceListSamples; i++ {
		want = append(want, "chargeback.sov.test GET /api/v1/public/packages")
	}
	want = append(want,
		"marketplace.sov.test GET /api/catalog/plans?product=generic",
		"marketplace.sov.test POST /api/billing/quote",
	)
	if strings.Join(f.seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(f.seen, "\n"), strings.Join(want, "\n"))
	}
	// The cheapest plan without a product, and the storefront's body for a cart
	// that holds only that plan: no package_sku.
	b, _ := json.Marshal(f.quoteBody)
	if string(b) != `{"addons":[],"apps":[],"overage_mode":"capped","plan_id":"id-s","topology":"single-region"}` {
		t.Errorf("quote body = %s", b)
	}
	ev := r.run.Steps[len(r.run.Steps)-1].Evidence
	if ev["state"] != "published" || ev["document_price_month"] != "2.490" || ev["quote_status"] != 409 || ev["package_sku"] != "plan.s" {
		t.Errorf("evidence = %v", ev)
	}
	// Without the name of the Sovereign the check asks nothing and fails.
	f = &priceListSovereign{}
	r = priceListRun(t, f)
	if got := r.run.Checks[0]; got.Status != Fail || !strings.Contains(got.Reason, "no sovereignFQDN") || len(f.seen) != 0 {
		t.Errorf("without D0.1: %s %q, %d requests", got.Status, got.Reason, len(f.seen))
	}
}

// TestPriceListEveryRegion — #7909. The engine's host is served by the gateway
// of every region behind one address; on a fresh two-region Sovereign the
// follower region's gate had no engine behind it and answered 6 of 12
// requests with its own 502 page, without CORS. One request could pass on the
// other region; twelve over new connections cannot.
func TestPriceListEveryRegion(t *testing.T) {
	for _, c := range []struct {
		name     string
		statuses []int
		noCORS   bool
		fail     string
	}{
		{"both regions publish", []int{200, 200}, false, ""},
		{"the follower region answers 502", []int{200, 502}, false, "6 answered the packages, 6 HTTP 502; every region that serves the host must have the engine behind it"},
		{"the follower region publishes nothing", []int{404, 200}, false, "6 answered the packages, 6 no package"},
		{"one region in twelve", []int{200, 200, 200, 200, 200, 200, 200, 200, 200, 200, 200, 503}, false, "11 answered the packages, 1 HTTP 503"},
		{"the storefront may not read it", []int{200}, true, "12 no Access-Control-Allow-Origin for https://marketplace.sov.test"},
	} {
		f := &priceListSovereign{docStatuses: c.statuses, noCORS: c.noCORS, doc: priceListDoc, quoteStatus: 409, quote: `{"error":"package-required"}`}
		r := priceListRun(t, f, priceListD01())
		got := r.run.Checks[0]
		switch {
		case c.fail == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %s %q", c.name, got.Status, got.Reason)
		case c.fail != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.fail)):
			t.Errorf("%s: want FAIL with %q, got %s %q", c.name, c.fail, got.Status, got.Reason)
		}
		if f.docAsked != priceListSamples {
			t.Errorf("%s: the price book was asked %d times, want %d", c.name, f.docAsked, priceListSamples)
		}
		for _, o := range f.origins {
			if o != "https://marketplace.sov.test" {
				t.Errorf("%s: a sample carried Origin %q, want the storefront's", c.name, o)
			}
		}
	}
}

// An amount is read exactly at three decimals; -1 marks one that is refused.
func TestPriceListBaisa(t *testing.T) {
	for in, want := range map[string]int64{"2.490": 2490, "4.49": 4490, "12": 12000, "0.001": 1, "": -1, "1.2345": -1, "-1.000": -1, "1,5": -1} {
		if got, err := priceListBaisa(in); (err != nil) != (want < 0) || (err == nil && got != want) {
			t.Errorf("priceListBaisa(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
