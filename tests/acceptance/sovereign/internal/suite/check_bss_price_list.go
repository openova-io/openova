package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Check BSS-G02, One price list. The storefront's anonymous requests: the
// price book GET https://chargeback.<fqdn>/api/v1/public/packages, the plan
// list GET https://marketplace.<fqdn>/api/catalog/plans?product=generic, and
// POST https://marketplace.<fqdn>/api/billing/quote for a cart that holds only
// the cheapest plan without a product: no package_sku.
//
// The price book is asked priceListSamples times, each over a new connection
// and with the storefront's Origin, because the engine's host is served by the
// gateway of every region behind one address: a region whose gateway serves
// the host without the engine behind it answers some of the samples, not all
// (#7909: 6 of 12 answered 502 from the follower region). Every sample must
// answer 200 with at least one package and the CORS header the storefront
// needs. A 404 or an empty document (the state "none") fails: the engine ships
// a default offer (products/chargeback DESIGN.md §22.12), so a Sovereign that
// publishes nothing lost its offer, and its storefront has nothing to sell
// (#7909: a fresh installation passed this check while that was so). Any
// other answer fails.
//
// The quote then passes with 409 "package-required", or with 200 and an
// amount_baisa equal to the price_month of package plan.<slug>. The name of
// the Sovereign is the value sovereignFQDN that step D0.1 of the discovery
// recorded.
const priceListID = "BSS-G02"

// priceListTimeout is the time limit of one request.
const priceListTimeout = 30 * time.Second

// priceListSamples is how often the price book is asked. With two regions
// behind the address, all twelve land on the same region with a probability
// of 2 × 2^-12, about one run in two thousand.
const priceListSamples = 12

type priceListCheck struct {
	transport http.RoundTripper // nil: http.DefaultTransport
}

func init() { RegisterCheck(priceListCheck{}) }

func (priceListCheck) ID() string { return priceListID }

func (c priceListCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	fqdn := sovereignFQDN(cc.Prior)
	if fqdn == "" {
		return errors.New("the run did not learn the name of the Sovereign: step D0.1 recorded no sovereignFQDN")
	}
	transport := c.transport
	if transport == nil {
		// A new connection per request: a kept-alive connection would ask the
		// same region every time.
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DisableKeepAlives = true
		transport = t
	}
	client := &http.Client{Transport: transport, Timeout: priceListTimeout}
	rec.Step(priceListID+".1", "the price book publishes packages on every connection, and a quote without a package is refused or priced from it", func(ev Evidence) error {
		return priceListJudge(ctx, client, fqdn, ev)
	})
	return nil
}

// priceBookDoc is the part of the price book the check reads.
type priceBookDoc struct {
	Packages []struct {
		SKU        string `json:"sku"`
		PriceMonth string `json:"price_month"`
	} `json:"packages"`
}

// priceListDocument asks the price book priceListSamples times and returns
// the first document, or why the price book is not served on every
// connection.
func priceListDocument(ctx context.Context, client *http.Client, fqdn string, ev Evidence) (priceBookDoc, error) {
	docURL := "https://chargeback." + fqdn + "/api/v1/public/packages"
	origin := "https://marketplace." + fqdn
	var first priceBookDoc
	statuses := make([]int, 0, priceListSamples)
	bad := map[string]int{}
	none := 0
	for i := 0; i < priceListSamples; i++ {
		status, body, hdr, err := priceListGet(ctx, client, docURL, origin)
		statuses = append(statuses, status)
		if err != nil {
			// A region that drops or times out the request: one bad sample.
			if _, seen := ev["document_error"]; !seen {
				ev["document_error"] = err.Error()
			}
			bad["a failed request"]++
			continue
		}
		var doc priceBookDoc
		switch {
		case status == http.StatusNotFound:
			none++
			continue
		case status != http.StatusOK:
			bad[fmt.Sprintf("HTTP %d", status)]++
			continue
		case json.Unmarshal(body, &doc) != nil:
			bad["a body that is not a JSON document"]++
			continue
		case len(doc.Packages) == 0:
			none++
			continue
		}
		if allow := hdr.Get("Access-Control-Allow-Origin"); allow != origin && allow != "*" {
			bad["no Access-Control-Allow-Origin for "+origin]++
			continue
		}
		if len(first.Packages) == 0 {
			first = doc
		}
	}
	ev["document_status"], ev["document_statuses"] = statuses[0], statuses
	ev["packages"] = len(first.Packages)
	ok := priceListSamples - none
	for _, n := range bad {
		ok -= n
	}
	switch {
	case none == priceListSamples:
		ev["state"] = "none"
		return first, fmt.Errorf("GET %s publishes no package on any of %d connections (404 or an empty list): the engine ships a default offer, so this Sovereign lost its offer and its storefront has nothing to sell", docURL, priceListSamples)
	case none > 0 || len(bad) > 0:
		ev["state"] = "partial"
		parts := []string{}
		if none > 0 {
			parts = append(parts, fmt.Sprintf("%d no package", none))
		}
		for _, k := range sortedKeys(bad) {
			parts = append(parts, fmt.Sprintf("%d %s", bad[k], k))
		}
		return first, fmt.Errorf("GET %s over %d new connections: %d answered the packages, %s; every region that serves the host must have the engine behind it", docURL, priceListSamples, ok, strings.Join(parts, ", "))
	}
	ev["state"] = "published"
	return first, nil
}

// priceListJudge reads the price book on every connection, then makes the two
// storefront requests and judges the answer of the quote.
func priceListJudge(ctx context.Context, client *http.Client, fqdn string, ev Evidence) error {
	docURL := "https://chargeback." + fqdn + "/api/v1/public/packages"
	doc, err := priceListDocument(ctx, client, fqdn, ev)
	if err != nil {
		return err
	}

	plansURL := "https://marketplace." + fqdn + "/api/catalog/plans?product=generic"
	status, body, err := priceListCall(ctx, client, http.MethodGet, plansURL, nil)
	if err != nil {
		return err
	}
	var plans []struct {
		ID          string `json:"id"`
		Slug        string `json:"slug"`
		ProductSlug string `json:"product_slug"`
		PriceBaisa  int64  `json:"price_baisa"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &plans) != nil {
		return fmt.Errorf("GET %s: HTTP %d; want 200 with a list of plans", plansURL, status)
	}
	pick := -1
	for i, p := range plans {
		if p.ProductSlug == "" && p.ID != "" && (pick < 0 || p.PriceBaisa < plans[pick].PriceBaisa) {
			pick = i
		}
	}
	if pick < 0 {
		return fmt.Errorf("GET %s: the catalog lists no plan without a product", plansURL)
	}
	plan := plans[pick]
	sku := "plan." + strings.ToLower(plan.Slug)
	ev["plan_id"], ev["package_sku"], ev["catalog_price_baisa"] = plan.ID, sku, plan.PriceBaisa

	listed, want := false, int64(0)
	for _, p := range doc.Packages {
		if p.SKU == sku {
			listed = true
			ev["document_price_month"] = p.PriceMonth
			if want, err = priceListBaisa(p.PriceMonth); err != nil {
				return fmt.Errorf("package %s in %s: %w", sku, docURL, err)
			}
		}
	}

	quoteURL := "https://marketplace." + fqdn + "/api/billing/quote"
	quote := map[string]any{"plan_id": plan.ID, "apps": []string{}, "addons": []string{},
		"topology": "single-region", "overage_mode": "capped"}
	status, body, err = priceListCall(ctx, client, http.MethodPost, quoteURL, quote)
	if err != nil {
		return err
	}
	var answer struct {
		Error       string `json:"error"`
		AmountBaisa int64  `json:"amount_baisa"`
		PriceSource string `json:"price_source"`
	}
	_ = json.Unmarshal(body, &answer)
	ev["quote_status"], ev["quote_error"] = status, answer.Error
	ev["quote_amount_baisa"], ev["quote_price_source"] = answer.AmountBaisa, answer.PriceSource

	switch {
	case status == http.StatusConflict && answer.Error == "package-required":
		return nil
	case status == http.StatusOK && listed && answer.AmountBaisa == want:
		return nil
	case status == http.StatusOK && listed:
		return fmt.Errorf("the quote of plan %s is %d baisa and the price book shows %s for %s", plan.ID, answer.AmountBaisa, ev["document_price_month"], sku)
	case status == http.StatusOK:
		return fmt.Errorf("the price book publishes packages and does not list %s, and the quote of plan %s answered 200 at %d baisa from another list", sku, plan.ID, answer.AmountBaisa)
	default:
		return fmt.Errorf("the price book publishes packages and the quote of plan %s answered HTTP %d %q; want 409 package-required or 200 at the price of %s", plan.ID, status, answer.Error, sku)
	}
}

// priceListGet makes one anonymous GET with the storefront's Origin, as the
// browser does, and reads the answer and its headers.
func priceListGet(ctx context.Context, client *http.Client, url, origin string) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", origin)
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("GET %s: read body: %w", url, err)
	}
	return res.StatusCode, b, res.Header, nil
}

// sortedKeys is the keys of m in order, so a reason reads the same each run.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// priceListCall makes one anonymous request and reads the answer.
func priceListCall(ctx context.Context, client *http.Client, method, url string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: read body: %w", method, url, err)
	}
	return res.StatusCode, b, nil
}

// priceListBaisa reads a money string of the price book ("4.490") as baisa,
// the minor unit at three decimals, exactly.
func priceListBaisa(s string) (int64, error) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	if whole == "" || len(frac) > 3 || strings.Trim(whole+frac, "0123456789") != "" {
		return 0, fmt.Errorf("price %q is not an amount with at most 3 decimals", s)
	}
	n, err := strconv.ParseInt(whole+frac+strings.Repeat("0", 3-len(frac)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("price %q: %w", s, err)
	}
	return n, nil
}
