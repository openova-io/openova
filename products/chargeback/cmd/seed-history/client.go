package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client talks to a running chargeback as the operator, through the same
// public API a person uses. Everything the API can do is done here so the
// product's own validation, auditing and upsert semantics apply; only the
// usage ledger (which has no endpoint — the collectors write it) goes to the
// store directly.
type client struct {
	base   string
	header string // TRUSTED_FORWARD_AUTH_HEADER name
	email  string // identity for that header
	cookie string // cb_session value, when signing in with a session instead
	http   *http.Client
}

// errStatus is a non-2xx answer, kept whole so a caller can branch on the code
// (a 404 from an endpoint that does not exist yet is a fallback, not a fault).
type errStatus struct {
	Code   int
	Method string
	Path   string
	Body   string
}

func (e *errStatus) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Code, body)
}

func isStatus(err error, code int) bool {
	var es *errStatus
	if ok := asErrStatus(err, &es); !ok {
		return false
	}
	return es.Code == code
}

func asErrStatus(err error, out **errStatus) bool {
	for err != nil {
		if es, ok := err.(*errStatus); ok {
			*out = es
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func newClient(base, header, email, cookie string) *client {
	return &client{
		base:   strings.TrimRight(base, "/"),
		header: header,
		email:  email,
		cookie: cookie,
		http:   &http.Client{Timeout: 120 * time.Second},
	}
}

// do performs one API call; out may be nil. body nil sends no payload.
func (c *client) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.email != "" && c.header != "" {
		req.Header.Set(c.header, c.email)
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "cb_session", Value: c.cookie})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &errStatus{Code: resp.StatusCode, Method: method, Path: path, Body: string(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// wire shapes (only the fields this command reads or writes)
// ---------------------------------------------------------------------------

type apiCustomer struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	PriceBookID *string `json:"price_book_id,omitempty"`
	PlanSlug    string  `json:"plan_slug"`
}

type apiSource struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Layer     string `json:"layer"`
	Region    string `json:"region"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	Internal  bool   `json:"internal"`
	// PriceBookID is the book that rates this source. The landlord backfill
	// reads it off the customer's REAL cloud source and assigns the same one
	// to its synthetic source, so the two halves of the series are priced by
	// the same rates and the seam does not jump.
	PriceBookID   *string `json:"price_book_id"`
	PriceBookName string  `json:"price_book_name,omitempty"`
}

type apiPriceBook struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Scope string `json:"scope"`
	// Currency and BillStopped are read so a resolved book can be reported
	// with the terms it actually carries, which is how the hw307 duplicate
	// was spotted (`compute` against `none`).
	Currency    string `json:"currency"`
	BillStopped string `json:"bill_stopped"`
}

type apiPriceItem struct {
	SKU         string `json:"sku"`
	Unit        string `json:"unit"`
	AnnualPrice string `json:"annual_price,omitempty"`
	Description string `json:"description,omitempty"`
}

type apiDiscount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiBudget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiStatement struct {
	ID          string `json:"id"`
	CustomerID  string `json:"customer_id"`
	PeriodStart string `json:"period_start"`
	Status      string `json:"status"`
	Total       any    `json:"total"`
}

type apiRunResult struct {
	CustomerID  string   `json:"customer_id"`
	StatementID string   `json:"statement_id"`
	Lines       int      `json:"lines"`
	Total       string   `json:"total"`
	Unpriced    []string `json:"unpriced_skus"`
	Error       string   `json:"error"`
}

// ---------------------------------------------------------------------------
// calls
// ---------------------------------------------------------------------------

func (c *client) whoami() (string, error) {
	var me struct {
		Role  string `json:"role"`
		Email string `json:"email"`
	}
	if err := c.do("GET", "/api/v1/auth/me", nil, &me); err != nil {
		return "", err
	}
	return me.Role, nil
}

func (c *client) listCustomers() ([]apiCustomer, error) {
	var out struct {
		Customers []apiCustomer `json:"customers"`
	}
	err := c.do("GET", "/api/v1/customers", nil, &out)
	return out.Customers, err
}

func (c *client) createCustomer(body map[string]any) (apiCustomer, error) {
	var out apiCustomer
	err := c.do("POST", "/api/v1/customers", body, &out)
	return out, err
}

func (c *client) patchCustomer(id string, body map[string]any) (apiCustomer, error) {
	var out apiCustomer
	err := c.do("PATCH", "/api/v1/customers/"+id, body, &out)
	return out, err
}

// listSources returns one customer's sources with their layer and book.
func (c *client) listSources(customerID string) ([]apiSource, error) {
	var out struct {
		Sources []apiSource `json:"sources"`
	}
	err := c.do("GET", "/api/v1/customers/"+url.PathEscape(customerID)+"/sources", nil, &out)
	return out.Sources, err
}

func (c *client) upsertSource(customerID string, body map[string]any) (apiSource, error) {
	var out apiSource
	err := c.do("POST", "/api/v1/customers/"+customerID+"/sources", body, &out)
	return out, err
}

// patchSourcePriceBook assigns a price book to ONE source — the per-source
// assignment of the ownership rebuild. The endpoint does not exist in every
// build; the caller falls back to the customer-level book when it answers
// 404 or rejects the field.
func (c *client) patchSourcePriceBook(customerID, sourceID, bookID string) error {
	return c.do("PATCH", "/api/v1/customers/"+customerID+"/sources/"+sourceID,
		map[string]any{"price_book_id": bookID}, nil)
}

func (c *client) listPriceBooks() ([]apiPriceBook, error) {
	var out struct {
		PriceBooks []apiPriceBook `json:"pricebooks"`
	}
	err := c.do("GET", "/api/v1/pricebooks", nil, &out)
	return out.PriceBooks, err
}

func (c *client) createPriceBook(name, currency string, divisor int) (apiPriceBook, error) {
	var out apiPriceBook
	err := c.do("POST", "/api/v1/pricebooks", map[string]any{
		"name": name, "currency": currency, "annual_divisor": divisor, "bill_stopped": "compute",
	}, &out)
	return out, err
}

// deletePriceBook removes a book. The product refuses while any source is
// assigned to it (store.DeletePriceBook), which is a guard this command relies
// on rather than reimplements.
func (c *client) deletePriceBook(id string) error {
	return c.do("DELETE", "/api/v1/pricebooks/"+url.PathEscape(id), nil, nil)
}

// putPriceItems merges items into a book (merge=true never removes a rate the
// operator added by hand).
func (c *client) putPriceItems(bookID string, items []apiPriceItem) error {
	return c.do("PUT", "/api/v1/pricebooks/"+bookID+"/items?merge=true",
		map[string]any{"items": items}, nil)
}

func (c *client) listDiscounts() ([]apiDiscount, error) {
	var out struct {
		Discounts []apiDiscount `json:"discounts"`
	}
	err := c.do("GET", "/api/v1/discounts", nil, &out)
	return out.Discounts, err
}

func (c *client) createDiscount(body map[string]any) (apiDiscount, error) {
	var out apiDiscount
	err := c.do("POST", "/api/v1/discounts", body, &out)
	return out, err
}

func (c *client) listBudgets() ([]apiBudget, error) {
	var out struct {
		Budgets []apiBudget `json:"budgets"`
	}
	err := c.do("GET", "/api/v1/budgets", nil, &out)
	return out.Budgets, err
}

func (c *client) createBudget(body map[string]any) (apiBudget, error) {
	var out apiBudget
	err := c.do("POST", "/api/v1/budgets", body, &out)
	return out, err
}

func (c *client) runStatements(period, customerID string) ([]apiRunResult, error) {
	var out struct {
		Results []apiRunResult `json:"results"`
	}
	err := c.do("POST", "/api/v1/statements/run",
		map[string]any{"period": period, "customer_id": customerID}, &out)
	return out.Results, err
}

// issueStatement issues WITHOUT mailing the customer: a showcase must never
// send a bill to anybody.
func (c *client) issueStatement(id string) error {
	return c.do("POST", "/api/v1/statements/"+id+"/issue", map[string]any{"notify": false}, nil)
}

func (c *client) listStatements(customerID string) ([]apiStatement, error) {
	var out struct {
		Statements []apiStatement `json:"statements"`
	}
	err := c.do("GET", "/api/v1/customers/"+url.PathEscape(customerID)+"/statements", nil, &out)
	return out.Statements, err
}

// monthlyCostOfSource is the product's OWN monthly cost for one source over
// [from, to) — rated by whatever card that source carries. The landlord
// backfill issues no statement, so this is the only number about it that is
// the product's rather than this command's arithmetic.
func (c *client) monthlyCostOfSource(customerID, sourceID string, from, to time.Time) (map[string]string, error) {
	q := url.Values{
		"from": {from.UTC().Format("2006-01-02")},
		// The explorer's `to` is exclusive on the day, so the last day of the
		// window has to be asked for explicitly.
		"to":          {to.UTC().AddDate(0, 0, 1).Format("2006-01-02")},
		"granularity": {"month"},
		"group_by":    {"none"},
		"source":      {sourceID},
	}
	var out struct {
		Buckets []string      `json:"buckets"`
		Totals  []json.Number `json:"totals_by_bucket"`
	}
	if err := c.do("GET", "/api/v1/customers/"+url.PathEscape(customerID)+"/cost/explore?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	res := map[string]string{}
	for i, b := range out.Buckets {
		if i < len(out.Totals) {
			res[b] = out.Totals[i].String()
		}
	}
	return res, nil
}

// importUsageCSV offers one month's records to a source's CSV import
// endpoint. The endpoint belongs to the target model (`file` sources import
// their own usage); where it does not exist the caller writes the ledger
// through the store instead, which is what the collectors do.
func (c *client) importUsageCSV(sourceID string, csv []byte) error {
	req, err := http.NewRequest("POST", c.base+"/api/v1/sources/"+sourceID+"/usage/import", bytes.NewReader(csv))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/csv")
	if c.email != "" && c.header != "" {
		req.Header.Set(c.header, c.email)
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "cb_session", Value: c.cookie})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &errStatus{Code: resp.StatusCode, Method: "POST", Path: "/api/v1/sources/" + sourceID + "/usage/import", Body: string(raw)}
	}
	return nil
}
