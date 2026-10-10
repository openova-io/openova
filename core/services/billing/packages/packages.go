// Package packages is the billing service's client for the Catalyst BSS price
// book — the document bp-chargeback publishes, public and sessionless, at
//
//	GET <CHARGEBACK_PUBLIC_URL>/api/v1/public/packages
//
// #6971. The SME marketplace sells packages S / M / L / XL whose features are
// Included, an Optional add-on, or Not offered per package. That matrix and
// every price on it are OWNED by BSS. The storefront renders the comparison
// table from this document and sends the chosen `package_sku` plus the ticked
// `addon.*` SKUs on POST /billing/checkout; this package reads the SAME
// document and prices the order from it, so the number on the table and the
// number on the receipt come from one source.
//
// Money in the document is a string at the currency's minor unit ("9.000" for
// OMR, three decimals). It is parsed with integer arithmetic into minor units
// (baisa) — no float ever touches a price.
package packages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Path is the public route on the chargeback host that publishes the matrix.
const Path = "/api/v1/public/packages"

// DefaultTTL is how long one fetched document is served before the next
// request re-reads it. Prices change by the day, not by the second; a minute
// keeps a checkout burst from hammering BSS while bounding staleness.
const DefaultTTL = 60 * time.Second

// AddonSKUPrefix marks an order add-on identifier as a BSS SKU (priced from
// the feature cell of the chosen package) rather than a catalog add-on id
// (priced from /catalog/addons as before).
const AddonSKUPrefix = "addon."

// Cell states, as the contract spells them.
const (
	StateIncluded   = "included"
	StateOptional   = "optional"
	StateNotOffered = "not_offered"
)

// ErrUnavailable is returned when the price book cannot be read: no base URL,
// the request failed, a non-200 answer, a body that is not the contract, or a
// currency billing cannot settle. Callers answer 503 — an order is never
// priced from a different list because this one was unreachable.
var ErrUnavailable = errors.New("prices unavailable")

// IsAddonSKU reports whether an order add-on identifier is a BSS add-on SKU.
func IsAddonSKU(id string) bool { return strings.HasPrefix(id, AddonSKUPrefix) }

// ---------------------------------------------------------------------------
// Money — exact minor units.
// ---------------------------------------------------------------------------

// Decimals is the number of fractional digits the supported currency carries.
// Billing settles in OMR (baisa, 1/1000 OMR) and refuses anything else, the
// same rule the Stripe webhook applies to invoice currencies.
const (
	Currency = "OMR"
	Decimals = 3
)

// MinorUnits parses a money string at the currency's minor unit into integer
// minor units, exactly: "9.000" → 9000, "1.5" → 1500, "12" → 12000 (with
// Decimals = 3). More fractional digits than the currency has, a sign, an
// exponent, or anything but digits and one point is an error — a price that
// cannot be represented in minor units is not a price billing may charge.
func MinorUnits(s string, decimals int) (int64, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, fmt.Errorf("money: empty amount")
	}
	whole, frac := raw, ""
	if i := strings.IndexByte(raw, '.'); i >= 0 {
		whole, frac = raw[:i], raw[i+1:]
		if strings.IndexByte(frac, '.') >= 0 {
			return 0, fmt.Errorf("money: %q has more than one decimal point", s)
		}
		if frac == "" {
			return 0, fmt.Errorf("money: %q ends in a decimal point", s)
		}
	}
	if whole == "" {
		return 0, fmt.Errorf("money: %q has no whole part", s)
	}
	if !digitsOnly(whole) || !digitsOnly(frac) {
		return 0, fmt.Errorf("money: %q is not an unsigned decimal", s)
	}
	if len(frac) > decimals {
		return 0, fmt.Errorf("money: %q has more than %d decimals", s, decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	digits := strings.TrimLeft(whole+frac, "0")
	if digits == "" {
		return 0, nil
	}
	if len(digits) > 18 {
		return 0, fmt.Errorf("money: %q is out of range", s)
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("money: %q: %w", s, err)
	}
	return n, nil
}

func digitsOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// FormatMinor renders minor units back as the contract's money string
// ("10500" → "10.500" with Decimals = 3).
func FormatMinor(n int64, decimals int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) <= decimals {
		s = strings.Repeat("0", decimals-len(s)+1) + s
	}
	out := s[:len(s)-decimals] + "." + s[len(s)-decimals:]
	if neg {
		out = "-" + out
	}
	return out
}

// ---------------------------------------------------------------------------
// The document.
// ---------------------------------------------------------------------------

// Document is the parsed price book: the contract with money already in
// minor units.
type Document struct {
	Currency   string
	PriceBook  string
	PricesAsOf string
	Packages   []Package
	Features   []Feature
}

// Package is one column of the table.
type Package struct {
	SKU        string
	Name       string
	PriceMinor int64
}

// Feature is one row of the table; Cells is keyed by package SKU.
type Feature struct {
	Key  string
	Name string
	Kind string
	// Levels is the ordered label list of a `kind: level` feature (0.1.61,
	// DESIGN.md §22.1): a cell's Level indexes into it.
	Levels []string
	Cells  map[string]Cell
}

// Cell is how one feature is sold on one package.
type Cell struct {
	State        string
	AddonSKU     string
	PriceMinor   int64
	IncludedFrom string
	// Level is the index into Feature.Levels on a level feature; nil when the
	// document does not carry one (a v1 document, or a non-level feature).
	Level *int
}

// wire mirrors the JSON contract one-for-one; Parse converts it.
type wire struct {
	Currency   string `json:"currency"`
	PriceBook  string `json:"price_book"`
	PricesAsOf string `json:"prices_as_of"`
	Packages   []struct {
		SKU        string `json:"sku"`
		Name       string `json:"name"`
		PriceMonth string `json:"price_month"`
	} `json:"packages"`
	Features []struct {
		Key    string   `json:"key"`
		Name   string   `json:"name"`
		Kind   string   `json:"kind"`
		Levels []string `json:"levels"`
		Cells  map[string]struct {
			State        string `json:"state"`
			AddonSKU     string `json:"addon_sku"`
			PriceMonth   string `json:"price_month"`
			IncludedFrom string `json:"included_from"`
			Level        *int   `json:"level"`
		} `json:"cells"`
	} `json:"features"`
}

// Parse validates a GET /api/v1/public/packages body and converts its money
// to minor units. A body that is not the contract — no packages, a package
// without a price, an unknown cell state, an optional cell with no SKU or
// price, a currency billing cannot settle — is an error, never a partial
// document.
func Parse(body []byte) (*Document, error) {
	var w wire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("price book: body is not JSON: %w", err)
	}
	currency := strings.ToUpper(strings.TrimSpace(w.Currency))
	if currency == "" {
		currency = Currency
	}
	if currency != Currency {
		return nil, fmt.Errorf("price book: currency %q is not %s", w.Currency, Currency)
	}
	if len(w.Packages) == 0 {
		return nil, fmt.Errorf("price book: publishes no packages")
	}
	doc := &Document{Currency: currency, PriceBook: w.PriceBook, PricesAsOf: w.PricesAsOf}
	seen := make(map[string]bool, len(w.Packages))
	for _, p := range w.Packages {
		if p.SKU == "" || p.Name == "" {
			return nil, fmt.Errorf("price book: a package has no sku or name")
		}
		if seen[p.SKU] {
			return nil, fmt.Errorf("price book: package %q is listed twice", p.SKU)
		}
		seen[p.SKU] = true
		price, err := MinorUnits(p.PriceMonth, Decimals)
		if err != nil {
			return nil, fmt.Errorf("price book: package %q: %w", p.SKU, err)
		}
		doc.Packages = append(doc.Packages, Package{SKU: p.SKU, Name: p.Name, PriceMinor: price})
	}
	for _, f := range w.Features {
		if f.Key == "" {
			return nil, fmt.Errorf("price book: a feature has no key")
		}
		feat := Feature{Key: f.Key, Name: f.Name, Kind: f.Kind, Cells: make(map[string]Cell, len(f.Cells)), Levels: f.Levels}
		if feat.Name == "" {
			feat.Name = f.Key
		}
		for sku, c := range f.Cells {
			cell := Cell{State: c.State, AddonSKU: c.AddonSKU, IncludedFrom: c.IncludedFrom, Level: c.Level}
			switch c.State {
			case StateIncluded, StateNotOffered:
			case StateOptional:
				if c.AddonSKU == "" {
					return nil, fmt.Errorf("price book: feature %q on %q is optional but names no addon_sku", f.Key, sku)
				}
				price, err := MinorUnits(c.PriceMonth, Decimals)
				if err != nil {
					return nil, fmt.Errorf("price book: feature %q on %q: %w", f.Key, sku, err)
				}
				cell.PriceMinor = price
			default:
				return nil, fmt.Errorf("price book: feature %q on %q has unknown state %q", f.Key, sku, c.State)
			}
			feat.Cells[sku] = cell
		}
		doc.Features = append(doc.Features, feat)
	}
	return doc, nil
}

// PriceSource is the provenance stamp an order priced from this document
// carries: "bss:<price_book>@<prices_as_of>". It is what the later
// reconciliation with BSS matches on.
func (d *Document) PriceSource() string {
	return fmt.Sprintf("bss:%s@%s", d.PriceBook, d.PricesAsOf)
}

// Package returns the package with the given SKU.
func (d *Document) Package(sku string) (Package, bool) {
	for _, p := range d.Packages {
		if p.SKU == sku {
			return p, true
		}
	}
	return Package{}, false
}

// featureForAddon finds the feature that sells addonSKU on ANY package. That
// is how "not offered on this package" is told apart from "not in the price
// book at all".
func (d *Document) featureForAddon(addonSKU string) (Feature, bool) {
	for _, f := range d.Features {
		for _, c := range f.Cells {
			if c.AddonSKU == addonSKU {
				return f, true
			}
		}
	}
	return Feature{}, false
}

// ---------------------------------------------------------------------------
// Pricing.
// ---------------------------------------------------------------------------

// Line is one add-on on a priced order.
type Line struct {
	SKU        string
	Name       string
	PriceMinor int64
	// Redundant is set when the chosen package already includes the feature
	// the add-on sells: the line is kept at 0 so the receipt shows what the
	// customer ticked, and it is never an error.
	Redundant bool
}

// Quote is an order priced from the document.
type Quote struct {
	Currency    string
	PriceSource string
	PackageSKU  string
	PackageName string
	PlanMinor   int64
	Lines       []Line
}

// Total is the plan plus every non-redundant line.
func (q *Quote) Total() int64 {
	t := q.PlanMinor
	for _, l := range q.Lines {
		t += l.PriceMinor
	}
	return t
}

// RefusedError says why an order cannot be priced from this document. It is
// the customer's mistake or a stale table, not an outage: callers answer 422
// and the message names the add-on and the package.
type RefusedError struct {
	PackageSKU string
	AddonSKU   string
	msg        string
}

func (e *RefusedError) Error() string { return e.msg }

// Refused builds a RefusedError from outside the package (the handler refuses
// a topology the package does not carry with the same error the add-on path
// uses, so the same 422 mapping applies).
func Refused(packageSKU, addonSKU, msg string) *RefusedError {
	return &RefusedError{PackageSKU: packageSKU, AddonSKU: addonSKU, msg: msg}
}

// Price prices package packageSKU with the given BSS add-on SKUs.
//
//   - the package must be in the document;
//   - an add-on whose cell on this package is `optional` is priced from that
//     cell;
//   - `included` → 0 and the line is marked redundant;
//   - `not_offered`, a cell the package does not have, or a SKU no feature
//     sells anywhere → *RefusedError naming the add-on and the package.
//
// Identifiers that are not BSS SKUs (see IsAddonSKU) are the caller's to
// price from the catalog; passing one here is a programming error and is
// refused like an unknown SKU.
func (d *Document) Price(packageSKU string, addonSKUs []string) (*Quote, error) {
	pkg, ok := d.Package(packageSKU)
	if !ok {
		return nil, &RefusedError{PackageSKU: packageSKU,
			msg: fmt.Sprintf("package %q is not in the price book", packageSKU)}
	}
	q := &Quote{
		Currency:    d.Currency,
		PriceSource: d.PriceSource(),
		PackageSKU:  pkg.SKU,
		PackageName: pkg.Name,
		PlanMinor:   pkg.PriceMinor,
		Lines:       []Line{},
	}
	for _, sku := range addonSKUs {
		feat, known := d.featureForAddon(sku)
		if !known {
			return nil, &RefusedError{PackageSKU: packageSKU, AddonSKU: sku,
				msg: fmt.Sprintf("add-on %q is not in the price book (package %q)", sku, packageSKU)}
		}
		cell, has := feat.Cells[packageSKU]
		switch {
		case has && cell.State == StateOptional && cell.AddonSKU == sku:
			q.Lines = append(q.Lines, Line{SKU: sku, Name: feat.Name, PriceMinor: cell.PriceMinor})
		case has && cell.State == StateIncluded:
			q.Lines = append(q.Lines, Line{SKU: sku, Name: feat.Name, PriceMinor: 0, Redundant: true})
		default:
			return nil, &RefusedError{PackageSKU: packageSKU, AddonSKU: sku,
				msg: fmt.Sprintf("add-on %q (%s) is not offered on package %q (%s)", sku, feat.Name, packageSKU, pkg.Name)}
		}
	}
	return q, nil
}

// ---------------------------------------------------------------------------
// The client — one fetch per TTL.
// ---------------------------------------------------------------------------

// Client reads the price book from the chargeback base URL and serves it from
// a short cache. The zero value / an empty BaseURL is "not configured":
// Configured() is false and Get returns ErrUnavailable.
type Client struct {
	// BaseURL is the chargeback host, e.g.
	// http://chargeback.chargeback.svc.cluster.local:8080 (the Sovereign
	// placement's in-cluster Service, slot 13f) — Path is appended.
	BaseURL string
	HTTP    *http.Client
	TTL     time.Duration

	// now is the clock; tests move it.
	now func() time.Time

	mu      sync.Mutex
	doc     *Document
	fetched time.Time
}

// NewClient builds a client for baseURL. A nil httpClient gets a 5 s timeout
// — a slow price book must not hold a checkout open indefinitely.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		HTTP:    httpClient,
		TTL:     DefaultTTL,
		now:     time.Now,
	}
}

// Configured reports whether a base URL is set. A nil *Client is not configured.
func (c *Client) Configured() bool { return c != nil && c.BaseURL != "" }

// URL is the full document URL.
func (c *Client) URL() string { return c.BaseURL + Path }

// Get returns the current document, re-reading it from BSS once the cached
// copy is older than TTL. Every failure is wrapped in ErrUnavailable with the
// cause; a cached copy past its TTL is NOT served on failure, so a dead BSS
// is seen within one TTL instead of being papered over indefinitely.
func (c *Client) Get(ctx context.Context) (*Document, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("%w: price book URL is not configured", ErrUnavailable)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()()
	if c.doc != nil && now.Sub(c.fetched) < c.ttl() {
		return c.doc, nil
	}
	doc, err := c.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	c.doc, c.fetched = doc, now
	return doc, nil
}

func (c *Client) fetch(ctx context.Context) (*Document, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", c.URL(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", c.URL(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("GET %s: read body: %w", c.URL(), err)
	}
	return Parse(body)
}

func (c *Client) ttl() time.Duration {
	if c.TTL <= 0 {
		return DefaultTTL
	}
	return c.TTL
}

func (c *Client) clock() func() time.Time {
	if c.now == nil {
		return time.Now
	}
	return c.now
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

// DRTopologyKey is the key of the resilience level feature that says which
// disaster-recovery topology a package carries (0.1.61 seeder / DESIGN.md
// §22.7): levels "single region" and "active-passive".
const DRTopologyKey = "dr_topology"

// DRActivePassive is the level label of the two-region topology.
const DRActivePassive = "active-passive"

// DRTopology returns the DR level label a package carries, and whether the
// document says anything about it at all. A v1 document (no dr_topology
// feature, or no level on the cell) reports false and billing keeps its own
// surcharge; a v2 document is the authority — the package either includes the
// topology or does not offer it, and no surcharge exists beside the package.
func (d *Document) DRTopology(packageSKU string) (string, bool) {
	for _, f := range d.Features {
		if f.Key != DRTopologyKey || f.Kind != "level" {
			continue
		}
		c, has := f.Cells[packageSKU]
		if !has || c.Level == nil || *c.Level < 0 || *c.Level >= len(f.Levels) {
			return "", false
		}
		return f.Levels[*c.Level], true
	}
	return "", false
}
