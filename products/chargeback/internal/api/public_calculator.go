package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The public calculator (DESIGN.md §11, EPIC #6867, founder requirement
// 2026-09-11: "we'll provide a cost calculator publicly").
//
// Three unauthenticated routes under /api/v1/public/, the shape of the AWS /
// Azure pricing calculators: a CATALOG (the Sovereign's public list book,
// the plans book and the pay-per-use book, regions, tax rate), a saved
// shareable ESTIMATE priced through rating.PriceEstimate — which is Rate and
// Totals, the functions the statement run calls — and the estimate read back
// by id. No principal is resolved on these routes, no cookie is ever set,
// and nothing here reads a customer: the only data that reaches a caller is
// list prices and what the caller itself submitted. A per-address token
// bucket answers 429 with Retry-After beyond the budget, and the configured
// origins (PUBLIC_CALCULATOR_ORIGINS) are the only ones answered cross-origin
// or allowed to frame /estimate.
//
// The operator side is two routes on the authenticated API: the public
// toggle on a cloud book (rating.manage; a second public book is 409) and
// the read-only Leads list (customers.manage) — the estimates a prospect left
// an address on, which the proposals module will consume.

const (
	publicPathPrefix    = "/api/v1/public/"
	estimateMaxLines    = 200
	estimateMaxBody     = 64 << 10
	estimateMaxHours    = 744 // the longest month
	estimateMaxMonths   = 12
	estimateMaxQuantity = "1000000000"
	estimateMaxRegion   = 64
	catalogNotice       = "List prices. Taxes are shown separately. A negotiated price, a discount or a partner rate is never part of this estimate; contact us for a proposal."
)

// isPublicPath reports whether the request is one of the unauthenticated
// calculator routes: the session middleware is skipped for them.
func isPublicPath(p string) bool { return strings.HasPrefix(p, publicPathPrefix) }

// isEmbeddablePath reports whether the request is the public estimate page,
// the one document that may be framed by a configured origin.
func isEmbeddablePath(p string) bool { return p == "/estimate" || strings.HasPrefix(p, "/estimate/") }

// frameHeaders sets the anti-framing header for a response: DENY everywhere,
// except the public estimate page, which the configured origins may frame
// (the Omantel site's iframe). With no origins configured the page stays
// unframeable, exactly like the rest of the console.
func (h *Handler) frameHeaders(w http.ResponseWriter, r *http.Request) {
	if isEmbeddablePath(r.URL.Path) && len(h.Config.PublicCalculatorOrigins) > 0 {
		ancestors := "'self'"
		for _, o := range h.Config.PublicCalculatorOrigins {
			ancestors += " " + o
		}
		w.Header().Set("Content-Security-Policy", "frame-ancestors "+ancestors)
		return
	}
	w.Header().Set("X-Frame-Options", "DENY")
}

// originAllowed reports whether origin may call the public routes from
// another site. Empty configuration = same origin only; "*" = any.
func (h *Handler) originAllowed(origin string) bool {
	o := strings.ToLower(strings.TrimSpace(origin))
	if o == "" {
		return false
	}
	for _, allowed := range h.Config.PublicCalculatorOrigins {
		if allowed == "*" || allowed == o {
			return true
		}
	}
	return false
}

// publicRoute wraps a calculator handler: CORS for the configured origins
// (a preflight is answered here), then the per-address budget. Nothing
// under it may set a cookie or read the session.
func (h *Handler) publicRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && h.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if ok, wait := h.limiter.allow(clientIP(r)); !ok {
			secs := int(wait / time.Second)
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("too many requests; retry in %d second(s)", secs))
			return
		}
		next(w, r)
	}
}

// clientIP is the address the public budget is charged to. Behind the
// Sovereign gateway the peer is the gateway itself and the caller is the
// LAST X-Forwarded-For hop — the one the gateway appended, which a caller
// cannot forge by prepending its own. Reached directly (a public peer
// address), the peer is the caller and the header is ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer != nil && (peer.IsPrivate() || peer.IsLoopback() || peer.IsUnspecified()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			hops := strings.Split(xff, ",")
			if last := net.ParseIP(strings.TrimSpace(hops[len(hops)-1])); last != nil {
				return last.String()
			}
		}
		if rip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-Ip"))); rip != nil {
			return rip.String()
		}
	}
	if peer != nil {
		return peer.String()
	}
	return host
}

// clientHash is what an estimate records about its caller: a digest of the
// address, never the address.
func clientHash(ip string) string {
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:16])
}

// ---------------------------------------------------------------------------
// catalog
// ---------------------------------------------------------------------------

// catalogSKU is one priced SKU of the public book. Monthly is one unit for
// HoursPerMonth hours, priced through rating.Amount — the figure a cart shows
// before any quantity is typed.
type catalogSKU struct {
	SKU         string        `json:"sku"`
	Service     string        `json:"service"`
	Unit        string        `json:"unit"`
	UnitPrice   store.Decimal `json:"unit_price"`
	Monthly     store.Decimal `json:"monthly"`
	Description string        `json:"description,omitempty"`
}

// catalogPlan is one sized catalog plan as the plans book prices it.
type catalogPlan struct {
	Slug      string        `json:"slug"`
	Name      string        `json:"name"`
	SKU       string        `json:"sku"`
	Unit      string        `json:"unit"`
	UnitPrice store.Decimal `json:"unit_price"`
	Monthly   store.Decimal `json:"monthly"`
	VCPU      int           `json:"vcpu"`
	MemoryGiB int           `json:"memory_gib"`
}

// catalogRate is one pay-per-use platform meter.
type catalogRate struct {
	SKU         string        `json:"sku"`
	Unit        string        `json:"unit"`
	UnitPrice   store.Decimal `json:"unit_price"`
	Monthly     store.Decimal `json:"monthly"`
	Description string        `json:"description,omitempty"`
}

type catalogDoc struct {
	PriceBook     store.EstimateBook `json:"price_book"`
	Currency      string             `json:"currency"`
	TaxRate       store.Decimal      `json:"tax_rate"`
	Regions       []string           `json:"regions"`
	SKUs          []catalogSKU       `json:"skus"`
	Plans         []catalogPlan      `json:"plans"`
	PAYG          []catalogRate      `json:"payg"`
	HoursPerMonth int                `json:"hours_per_month"`
	ListPrices    bool               `json:"list_prices"`
	Notice        string             `json:"notice"`
	GeneratedAt   time.Time          `json:"generated_at"`
}

// calcContext is everything one request prices against: the public book
// (which chooses the currency and the stopped-instance policy), the items of
// the three books merged, and the Sovereign default tax rate. Choosing the
// books is the whole of this file's pricing decision; the numbers come out
// of rating.
type calcContext struct {
	book    store.PriceBook
	plans   *store.PriceBook
	payg    *store.PriceBook
	items   map[string]store.PriceItem
	taxRate store.Decimal
}

func (h *Handler) calcContext(r *http.Request) (*calcContext, error) {
	ctx := r.Context()
	pb, err := h.Store.PublicPriceBook(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := h.Store.GetBillingSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("billing settings: %w", err)
	}
	c := &calcContext{book: pb, items: map[string]store.PriceItem{}, taxRate: settings.TaxRate}
	if strings.TrimSpace(string(c.taxRate)) == "" {
		c.taxRate = store.DefaultTaxRate
	}
	for _, it := range pb.Items {
		c.items[it.SKU] = it
	}
	// The two platform books the Organization sync owns are published with
	// the cloud list — when they exist on this Sovereign and are priced in
	// its currency. An estimate is issued in one currency and nothing here
	// converts money (the same rule a statement follows).
	for _, name := range []string{store.PlanBookName, store.PAYGBookName} {
		b, err := h.Store.GetPriceBookByName(ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.Currency != pb.Currency {
			continue
		}
		if b.Items, err = h.Store.ListPriceItems(ctx, b.ID); err != nil {
			return nil, err
		}
		for _, it := range b.Items {
			if _, taken := c.items[it.SKU]; !taken {
				c.items[it.SKU] = it
			}
		}
		bb := b
		if name == store.PlanBookName {
			c.plans = &bb
		} else {
			c.payg = &bb
		}
	}
	return c, nil
}

// monthlyOf is one unit of item for HoursPerMonth hours, through rating.
func monthlyOf(it store.PriceItem) store.Decimal {
	m, err := rating.Amount(rating.HoursPerMonth, it.UnitPrice)
	if err != nil {
		return ""
	}
	return m
}

// serviceOf is the SKU family — the first token — the console groups by.
func serviceOf(sku string) string {
	s := strings.ToLower(strings.TrimSpace(sku))
	if i := strings.IndexAny(s, ".-_/:"); i > 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "eip") {
		return "eip"
	}
	return s
}

// publicCatalog — GET /api/v1/public/catalog.
func (h *Handler) publicCatalog(w http.ResponseWriter, r *http.Request) {
	c, err := h.calcContext(r)
	if err != nil {
		publicStoreErr(w, err)
		return
	}
	regions, err := h.Store.EstimateRegions(r.Context())
	if err != nil {
		publicStoreErr(w, err)
		return
	}
	doc := catalogDoc{
		PriceBook:     store.EstimateBook{ID: c.book.ID, Name: c.book.Name, UpdatedAt: c.book.UpdatedAt},
		Currency:      c.book.Currency,
		TaxRate:       c.taxRate,
		Regions:       regions,
		SKUs:          []catalogSKU{},
		Plans:         []catalogPlan{},
		PAYG:          []catalogRate{},
		HoursPerMonth: 730,
		ListPrices:    true,
		Notice:        catalogNotice,
		GeneratedAt:   h.Now().UTC(),
	}
	for _, it := range c.book.Items {
		doc.SKUs = append(doc.SKUs, catalogSKU{SKU: it.SKU, Service: serviceOf(it.SKU), Unit: it.Unit, UnitPrice: it.UnitPrice, Monthly: monthlyOf(it), Description: it.Description})
	}
	if c.plans != nil {
		for _, it := range c.plans.Items {
			slug := strings.TrimPrefix(it.SKU, store.PlanSKUPrefix)
			if !strings.HasPrefix(it.SKU, store.PlanSKUPrefix) || !store.PlanBillable(slug) {
				continue
			}
			vcpu, mem, _ := store.PlanShape(slug)
			doc.Plans = append(doc.Plans, catalogPlan{Slug: slug, Name: store.PlanName(slug), SKU: it.SKU, Unit: it.Unit, UnitPrice: it.UnitPrice, Monthly: monthlyOf(it), VCPU: vcpu, MemoryGiB: mem})
		}
	}
	if c.payg != nil {
		for _, it := range c.payg.Items {
			if !store.IsPlatformMeter(it.SKU) {
				continue
			}
			doc.PAYG = append(doc.PAYG, catalogRate{SKU: it.SKU, Unit: it.Unit, UnitPrice: it.UnitPrice, Monthly: monthlyOf(it), Description: it.Description})
		}
	}
	writeJSON(w, http.StatusOK, doc)
}

// publicStoreErr is storeErr for the public routes: the one 404 a caller can
// provoke — no public book — keeps its message; nothing else is described.
func publicStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNoPublicPriceBook) {
		writeErr(w, http.StatusNotFound, "the public price list is not published yet")
		return
	}
	storeErr(w, err)
}

// ---------------------------------------------------------------------------
// estimates
// ---------------------------------------------------------------------------

type estimateLineBody struct {
	SKU           string         `json:"sku"`
	Plan          string         `json:"plan"`
	Quantity      *store.Decimal `json:"quantity"`
	HoursPerMonth *store.Decimal `json:"hours_per_month"`
	Months        *int           `json:"months"`
}

type estimateBody struct {
	Currency     string             `json:"currency"`
	Region       string             `json:"region"`
	Lines        []estimateLineBody `json:"lines"`
	ContactEmail string             `json:"contact_email"`
}

// estimateDoc is a saved estimate on the wire plus the link it is shared by.
type estimateDoc struct {
	store.Estimate
	ShareURL string `json:"share_url,omitempty"`
}

func (h *Handler) estimateDoc(e store.Estimate, public bool) estimateDoc {
	if public {
		e.ContactEmail = ""
	}
	d := estimateDoc{Estimate: e}
	if e.ID != "" {
		d.ShareURL = h.Config.PublicURL + "/estimate/" + e.ID
	}
	return d
}

// parseEstimateLines validates the request into rating lines plus the wire
// metadata of each (plan slug). Every refusal names the line and the rule.
func parseEstimateLines(in []estimateLineBody, items map[string]store.PriceItem) ([]rating.EstimateLine, []string, string) {
	if len(in) == 0 {
		return nil, nil, "at least one line is required"
	}
	if len(in) > estimateMaxLines {
		return nil, nil, fmt.Sprintf("at most %d lines per estimate (%d given)", estimateMaxLines, len(in))
	}
	maxQty, _ := new(big.Rat).SetString(estimateMaxQuantity)
	lines := make([]rating.EstimateLine, 0, len(in))
	plans := make([]string, 0, len(in))
	for i, l := range in {
		at := fmt.Sprintf("line %d", i+1)
		sku := strings.TrimSpace(l.SKU)
		plan := store.NormalizePlanSlug(l.Plan)
		line := rating.EstimateLine{Quantity: "1", Hours: rating.HoursPerMonth, Months: 1}
		switch {
		case plan != "" && sku != "":
			return nil, nil, at + ": give sku or plan, not both"
		case plan != "":
			if !store.ValidPlanSlug(plan) {
				return nil, nil, fmt.Sprintf("%s: unknown plan %q", at, plan)
			}
			if !store.PlanBillable(plan) {
				return nil, nil, fmt.Sprintf("%s: plan %q is pay per use — add the %s lines instead", at, plan, strings.Join(store.PlatformMeterSKUs, " / "))
			}
			if l.HoursPerMonth != nil {
				return nil, nil, at + ": a plan is a whole month; hours_per_month does not apply"
			}
			sku = store.PlanSKU(plan)
			if l.Months != nil {
				if *l.Months < 1 || *l.Months > estimateMaxMonths {
					return nil, nil, fmt.Sprintf("%s: months must be between 1 and %d", at, estimateMaxMonths)
				}
				line.Months = *l.Months
			}
		case sku != "":
			if l.Months != nil && *l.Months != 1 {
				return nil, nil, at + ": months applies to a plan line; an SKU line is one month of hours_per_month hours"
			}
			if l.HoursPerMonth != nil {
				hr, ok := new(big.Rat).SetString(strings.TrimSpace(string(*l.HoursPerMonth)))
				if !ok || hr.Sign() <= 0 || hr.Cmp(big.NewRat(estimateMaxHours, 1)) > 0 {
					return nil, nil, fmt.Sprintf("%s: hours_per_month must be more than 0 and at most %d", at, estimateMaxHours)
				}
				line.Hours = store.Decimal(strings.TrimSpace(string(*l.HoursPerMonth)))
			}
		default:
			return nil, nil, at + ": sku or plan is required"
		}
		if _, known := items[sku]; !known {
			return nil, nil, fmt.Sprintf("%s: unknown sku %q — it is not in the public price list", at, sku)
		}
		if l.Quantity != nil {
			q, ok := new(big.Rat).SetString(strings.TrimSpace(string(*l.Quantity)))
			if !ok || q.Sign() <= 0 || q.Cmp(maxQty) > 0 {
				return nil, nil, fmt.Sprintf("%s: quantity must be more than 0 and at most %s", at, estimateMaxQuantity)
			}
			line.Quantity = store.Decimal(strings.TrimSpace(string(*l.Quantity)))
		}
		line.SKU = sku
		lines = append(lines, line)
		plans = append(plans, plan)
	}
	return lines, plans, ""
}

// publicCreateEstimate — POST /api/v1/public/estimates[?preview=1]. Prices
// the lines through rating.PriceEstimate against the public catalog and
// saves the result as a shareable estimate; with preview=1 it prices and
// answers without saving (the live total of the cart). An address makes it a
// lead.
func (h *Handler) publicCreateEstimate(w http.ResponseWriter, r *http.Request) {
	var in estimateBody
	dec := json.NewDecoder(io.LimitReader(r.Body, estimateMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	c, err := h.calcContext(r)
	if err != nil {
		publicStoreErr(w, err)
		return
	}
	if cur := strings.ToUpper(strings.TrimSpace(in.Currency)); cur != "" && cur != c.book.Currency {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("the public price list is in %s; estimates are not converted to %s", c.book.Currency, cur))
		return
	}
	region := strings.TrimSpace(in.Region)
	if len(region) > estimateMaxRegion || strings.ContainsAny(region, "\r\n\t") {
		writeErr(w, http.StatusBadRequest, "region must be a short label")
		return
	}
	email := normEmail(in.ContactEmail)
	if email != "" && !validEmail(email) {
		writeErr(w, http.StatusBadRequest, "contact_email must be a valid email address")
		return
	}
	lines, plans, msg := parseEstimateLines(in.Lines, c.items)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	res, err := rating.PriceEstimate(c.items, c.book.BillStopped, lines, c.taxRate)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(res.Unknown) > 0 {
		writeErr(w, http.StatusBadRequest, "unknown sku "+strings.Join(res.Unknown, ", "))
		return
	}
	out := make([]store.EstimateLine, 0, len(res.Lines))
	for i, rl := range res.Lines {
		out = append(out, store.EstimateLine{
			SKU: rl.SKU, Plan: plans[i], Description: c.items[rl.SKU].Description, Unit: rl.Unit,
			Quantity: lines[i].Quantity, Hours: lines[i].Hours, Months: lines[i].Months,
			RatedQuantity: rl.Quantity, UnitPrice: rl.UnitPrice, Amount: rl.Amount,
		})
	}
	now := h.Now().UTC()
	draft := store.EstimateDraft{
		Lines: out, Currency: c.book.Currency, Region: region,
		Subtotal: res.Subtotal, TaxRate: c.taxRate, Tax: res.Tax, Total: res.Total, Monthly: res.Monthly, Yearly: res.Yearly,
		PriceBook:    store.EstimateBook{ID: c.book.ID, Name: c.book.Name, UpdatedAt: c.book.UpdatedAt},
		ContactEmail: email, ClientHash: clientHash(clientIP(r)), Now: now,
	}
	if r.URL.Query().Get("preview") == "1" {
		e := store.Estimate{
			Lines: out, Currency: draft.Currency, Region: region,
			Subtotal: res.Subtotal, TaxRate: c.taxRate, Tax: res.Tax, Total: res.Total, Monthly: res.Monthly, Yearly: res.Yearly,
			PriceBook: draft.PriceBook, ListPrices: true, CreatedAt: now, ValidUntil: now.Add(store.EstimateValidity),
		}
		writeJSON(w, http.StatusOK, h.estimateDoc(e, true))
		return
	}
	e, err := h.Store.CreateEstimate(r.Context(), draft)
	if err != nil {
		publicStoreErr(w, err)
		return
	}
	h.Metrics.Inc("chargeback_public_estimates_total", "Public estimates saved, by lead", map[string]string{"lead": strconv.FormatBool(e.Lead)}, 1)
	writeJSON(w, http.StatusCreated, h.estimateDoc(e, true))
}

// publicGetEstimate — GET /api/v1/public/estimates/{id}: the shareable link.
// The address a prospect left is never in the public document.
func (h *Handler) publicGetEstimate(w http.ResponseWriter, r *http.Request) {
	e, err := h.Store.GetEstimate(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.estimateDoc(e, true))
}

// ---------------------------------------------------------------------------
// operator side
// ---------------------------------------------------------------------------

// setPriceBookPublic — PUT /api/v1/pricebooks/{id}/public {public}. Only a
// cloud book, only one at a time: a second is 409 naming the current one.
func (h *Handler) setPriceBookPublic(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	var in struct {
		Public *bool `json:"public"`
	}
	if err := decode(r, &in); err != nil || in.Public == nil {
		writeErr(w, http.StatusBadRequest, "body must be {\"public\": true|false}")
		return
	}
	id := r.PathValue("id")
	pb, err := h.Store.SetPriceBookPublic(r.Context(), id, *in.Public)
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			writeErr(w, http.StatusBadRequest, invalidMessage(err))
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.public", map[string]any{"id": pb.ID, "name": pb.Name, "public": pb.Public})
	writeJSON(w, http.StatusOK, pb)
}

// listLeads — GET /api/v1/leads[?limit]: the estimates a prospect left an
// address on, newest first, with the shareable link. customers.manage at the
// Sovereign; the proposals module reads the same rows later.
func (h *Handler) listLeads(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CustomersManage); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	leads, err := h.Store.ListLeads(r.Context(), limit)
	if err != nil {
		storeErr(w, err)
		return
	}
	out := make([]estimateDoc, 0, len(leads))
	for _, e := range leads {
		out = append(out, h.estimateDoc(e, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"leads": out})
}
