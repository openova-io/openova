package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Check BSS-G02, One price list. Three anonymous requests, the storefront's:
// the price book GET https://chargeback.<fqdn>/api/v1/public/packages, the
// plan deck GET https://marketplace.<fqdn>/api/catalog/plans?product=generic,
// and POST https://marketplace.<fqdn>/api/billing/quote for a cart that holds
// only the cheapest plan without a product: no package_sku. The price book
// answering 404, or 200 with no package, is the state "none"; 200 with
// packages is "published"; any other answer fails. In "published" the quote
// passes with 409 "package-required", or with 200 and an amount_baisa equal
// to the price_month of package plan.<slug>. In "none" it passes with 200:
// only then may the catalog answer. The name of the Sovereign is the value
// sovereignFQDN that step D0.1 of the discovery recorded.
const priceListID = "BSS-G02"

// priceListTimeout is the time limit of one request.
const priceListTimeout = 30 * time.Second

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
	client := &http.Client{Transport: c.transport, Timeout: priceListTimeout}
	rec.Step(priceListID+".1", "a quote without a package is refused, or priced from the published price book", func(ev Evidence) error {
		return priceListJudge(ctx, client, fqdn, ev)
	})
	return nil
}

// priceListJudge makes the three requests and judges the answer of the quote.
func priceListJudge(ctx context.Context, client *http.Client, fqdn string, ev Evidence) error {
	docURL := "https://chargeback." + fqdn + "/api/v1/public/packages"
	status, body, err := priceListCall(ctx, client, http.MethodGet, docURL, nil)
	if err != nil {
		return err
	}
	ev["document_status"] = status
	var doc struct {
		Packages []struct {
			SKU        string `json:"sku"`
			PriceMonth string `json:"price_month"`
		} `json:"packages"`
	}
	switch {
	case status == http.StatusNotFound:
	case status != http.StatusOK:
		return fmt.Errorf("GET %s: HTTP %d; want 200 or 404", docURL, status)
	case json.Unmarshal(body, &doc) != nil:
		return fmt.Errorf("GET %s: the body is not a JSON document", docURL)
	}
	state := "none"
	if len(doc.Packages) > 0 {
		state = "published"
	}
	ev["state"], ev["packages"] = state, len(doc.Packages)

	plansURL := "https://marketplace." + fqdn + "/api/catalog/plans?product=generic"
	status, body, err = priceListCall(ctx, client, http.MethodGet, plansURL, nil)
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
	case state == "none" && status == http.StatusOK:
		return nil
	case state == "none":
		return fmt.Errorf("the price book publishes nothing and the quote of plan %s answered HTTP %d; the catalog must answer 200", plan.ID, status)
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
