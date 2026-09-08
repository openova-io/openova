package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// usageChunk is how many hourly rows go into one upsert transaction.
const usageChunk = 2000

// seeder applies a scenario. Anything the API can express goes through the
// API (so validation, auditing and the product's own upsert rules apply);
// the usage ledger, the inventory and the backdating of created/issued
// timestamps go through the store, because no endpoint writes them — the
// collectors do.
type seeder struct {
	sc    *synth.Scenario
	api   *client
	st    *store.Store
	db    *sql.DB
	ctx   context.Context
	books map[string]string // price book name -> id

	// csvImport memoizes whether the per-source CSV import endpoint of the
	// target model exists on this build, so a missing endpoint is probed once
	// rather than once per month.
	csvImport *bool
}

// result is one customer's outcome, for the summary table.
type result struct {
	Customer   string
	Slug       string
	Source     string
	Rows       int
	Resources  int
	Months     map[string]string // YYYY-MM -> statement total (OMR)
	Statements int
	Note       string
}

func (s *seeder) infof(format string, a ...any) { log.Printf(format, a...) }

// ensureBooks makes the two rate cards available and records their ids.
//
// Neither book is ever re-priced: on a Sovereign both may already hold the
// operator's own numbers ("National Cloud list 2026" priced the real August
// statement on hw307), and a showcase must not move a real rate.
func (s *seeder) ensureBooks(needCloud, needPlan bool) error {
	s.books = map[string]string{}
	existing, err := s.api.listPriceBooks()
	if err != nil {
		return fmt.Errorf("list price books: %w", err)
	}
	byName := map[string]apiPriceBook{}
	for _, b := range existing {
		byName[strings.ToLower(b.Name)] = b
	}
	if needCloud {
		name := s.sc.CloudBookName
		if b, ok := byName[strings.ToLower(name)]; ok {
			s.books[name] = b.ID
			s.infof("price book %q already exists (%s) — left untouched", name, b.ID[:8])
		} else {
			b, err := s.api.createPriceBook(name, "OMR", synth.AnnualDivisor)
			if err != nil {
				return fmt.Errorf("create price book %q: %w", name, err)
			}
			items := make([]apiPriceItem, 0, len(synth.NationalCloudRates))
			for _, r := range synth.NationalCloudRates {
				items = append(items, apiPriceItem{
					SKU: r.SKU, Unit: r.Unit,
					AnnualPrice: strconv.FormatFloat(r.Annual, 'f', -1, 64),
					Description: r.Description,
				})
			}
			if err := s.api.putPriceItems(b.ID, items); err != nil {
				return fmt.Errorf("price %q: %w", name, err)
			}
			s.books[name] = b.ID
			s.infof("price book %q created (%s) with %d National Cloud rates", name, b.ID[:8], len(items))
		}
	}
	if needPlan {
		// EnsurePlanBook is the product's OWN plan-book logic (the one OrgSync
		// runs). Calling it rather than re-creating the book by hand is what
		// guarantees the showcase bills a plan at exactly the platform's rate.
		pb, created, err := s.st.EnsurePlanBook(s.ctx)
		if err != nil {
			return fmt.Errorf("ensure plan book: %w", err)
		}
		s.books[s.sc.PlanBookName] = pb.ID
		if created {
			s.infof("price book %q created (%s) by the product's own EnsurePlanBook", pb.Name, pb.ID[:8])
		} else {
			s.infof("price book %q already exists (%s) — left untouched", pb.Name, pb.ID[:8])
		}
	}
	return nil
}

// ensureCustomer creates or updates the customer and returns its id.
func (s *seeder) ensureCustomer(c *synth.Customer) (string, error) {
	existing, err := s.api.listCustomers()
	if err != nil {
		return "", fmt.Errorf("list customers: %w", err)
	}
	var found *apiCustomer
	for i := range existing {
		if existing[i].Slug == c.Slug {
			found = &existing[i]
			break
		}
	}
	bookID := s.books[c.Source.Book]
	body := map[string]any{
		"slug":         c.Slug,
		"name":         c.Name,
		"admin_email":  c.AdminEmail,
		"billing_mode": c.BillingMode,
		"start_date":   c.Joined.Format("2006-01-02"),
	}
	if bookID != "" {
		body["price_book_id"] = bookID
	}
	if found != nil {
		// The customer is already here: bring it back to the shape the
		// scenario wants (a re-run must update in place, never duplicate).
		patch := map[string]any{
			"name": c.Name, "admin_email": c.AdminEmail,
			"billing_mode": c.BillingMode, "start_date": c.Joined.Format("2006-01-02"),
			"status": "active",
		}
		if bookID != "" {
			patch["price_book_id"] = bookID
		}
		out, err := s.api.patchCustomer(found.ID, patch)
		if err != nil {
			return "", fmt.Errorf("update customer %s: %w", c.Slug, err)
		}
		return out.ID, nil
	}
	// kind and org_slug are only settable at creation; plan_slug of an
	// Organization customer is refused by PATCH (it is read from the
	// Organization CR), so it has to be right the first time.
	body["kind"] = c.Kind
	if c.OrgSlug != "" {
		body["org_slug"] = c.OrgSlug
	}
	if c.PlanSlug != "" {
		body["plan_slug"] = c.PlanSlug
	}
	out, err := s.api.createCustomer(body)
	if err != nil {
		return "", fmt.Errorf("create customer %s: %w", c.Slug, err)
	}
	if _, err := s.api.patchCustomer(out.ID, map[string]any{"status": "active"}); err != nil {
		return "", fmt.Errorf("activate customer %s: %w", c.Slug, err)
	}
	return out.ID, nil
}

// ensureSource upserts the customer's single source and assigns its price
// book. The per-source assignment is the target model; when this build has no
// such endpoint the book stays on the customer, which is where today's rating
// run reads it from.
func (s *seeder) ensureSource(customerID string, c *synth.Customer) (string, error) {
	src, err := s.api.upsertSource(customerID, map[string]any{
		"kind":       c.Source.Kind,
		"region":     c.Source.Region,
		"project_id": c.Source.Name,
	})
	if isStatus(err, 400) && c.Source.Layer == synth.LayerPlatform {
		// A PLATFORM source has no create endpoint on purpose: for a real
		// Organization the adapter makes it from the Organization CR, so the
		// operator API refuses to hand-craft one (DESIGN.md §2). A showcase
		// Organization has no CR to sync, so its source goes through the same
		// store the adapter writes — the same reason the usage ledger below
		// does not go through the API either.
		stored, _, serr := s.st.UpsertSource(s.ctx, customerID, c.Source.Kind, c.Source.Region, c.Source.Name)
		if serr != nil {
			return "", fmt.Errorf("platform source for %s (store): %w", c.Slug, serr)
		}
		if stored.Status != "verified" {
			if serr := s.st.SetSourceVerified(s.ctx, stored.ID, ""); serr != nil {
				return "", fmt.Errorf("verify platform source for %s: %w", c.Slug, serr)
			}
		}
		s.infof("  no create endpoint for a platform source; writing it through the store, as the adapter does")
		src, err = apiSource{ID: stored.ID}, nil
	}
	if err != nil {
		return "", fmt.Errorf("source for %s: %w", c.Slug, err)
	}
	if bookID := s.books[c.Source.Book]; bookID != "" {
		err := s.api.patchSourcePriceBook(customerID, src.ID, bookID)
		switch {
		case err == nil:
			s.infof("  source %s: price book assigned per source", src.ID[:8])
		case isStatus(err, 404), isStatus(err, 405):
			// This build assigns the book on the customer; already done in
			// ensureCustomer. Nothing is missing, the model is just older.
		case isStatus(err, 400) && c.Source.Layer == synth.LayerPlatform:
			// Same reason as the source itself: no endpoint owns a showcase
			// Organization's source, so the book is assigned through the store.
			if serr := s.st.SetSourcePriceBook(s.ctx, src.ID, bookID); serr != nil {
				return "", fmt.Errorf("assign price book to platform source %s: %w", src.ID, serr)
			}
			s.infof("  source %s: price book assigned per source (store)", src.ID[:8])
		default:
			return "", fmt.Errorf("assign price book to source %s: %w", src.ID, err)
		}
	}
	return src.ID, nil
}

// writeUsage writes one customer's ledger, one calendar month at a time.
func (s *seeder) writeUsage(customerID, sourceID string, out synth.Output) (int, error) {
	total := 0
	for _, period := range s.sc.Months(out.Customer) {
		recs := synth.InPeriod(out.Records, period)
		if len(recs) == 0 {
			continue
		}
		n, err := s.writeUsageMonth(customerID, sourceID, period, recs)
		if err != nil {
			return total, err
		}
		total += n
		s.infof("  %s %s: %d hourly records", out.Customer.Slug, period, n)
	}
	return total, nil
}

func (s *seeder) writeUsageMonth(customerID, sourceID, period string, recs []synth.Record) (int, error) {
	// The target model imports a file source's usage as CSV. Probe once.
	if s.csvImport == nil || *s.csvImport {
		var buf bytes.Buffer
		if err := synth.WriteCSV(&buf, recs); err != nil {
			return 0, err
		}
		err := s.api.importUsageCSV(sourceID, buf.Bytes())
		switch {
		case err == nil:
			ok := true
			s.csvImport = &ok
			return len(recs), nil
		case isStatus(err, 404), isStatus(err, 405):
			if s.csvImport == nil {
				no := false
				s.csvImport = &no
				s.infof("  no CSV usage-import endpoint on this build; writing the ledger through the store, as the collectors do")
			}
		default:
			return 0, fmt.Errorf("import usage CSV for %s: %w", period, err)
		}
	}
	written := 0
	batch := make([]store.UsageRecord, 0, usageChunk)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := s.st.UpsertUsage(s.ctx, batch)
		written += n
		batch = batch[:0]
		return err
	}
	for _, r := range recs {
		labels, err := jsonLabels(r.Labels)
		if err != nil {
			return written, err
		}
		batch = append(batch, store.UsageRecord{
			CustomerID:   customerID,
			SourceID:     sourceID,
			ResourceID:   r.ResourceID,
			ResourceKind: r.ResourceKind,
			SKU:          r.SKU,
			Quantity:     store.Decimal(strconv.FormatFloat(r.Quantity, 'f', 6, 64)),
			Unit:         r.Unit,
			WindowStart:  r.Start,
			WindowEnd:    r.End,
			Region:       r.Region,
			Labels:       labels,
			RawRef:       "seed-history",
		})
		if len(batch) >= usageChunk {
			if err := flush(); err != nil {
				return written, err
			}
		}
	}
	return written, flush()
}

// writeInventory records the resources the ledger describes, with the real
// first_seen / last_seen and the decommission date. Backdated first_seen and
// deleted_at have no endpoint, so SetInventoryBounds — the store method the
// collector uses to correct bounds from the audit trail — sets them.
func (s *seeder) writeInventory(sourceID string, out synth.Output) error {
	items := make([]store.InventoryUpsert, 0, len(out.Resources))
	for _, r := range out.Resources {
		attrs := map[string]any{}
		for k, v := range r.Attrs {
			attrs[k] = v
		}
		if r.Region != "" {
			attrs["region"] = r.Region
		}
		items = append(items, store.InventoryUpsert{
			ResourceID: r.ID, Kind: r.Kind, Name: r.Name, Attrs: attrs,
			Created: r.FirstSeen, SeenAt: r.LastSeen,
		})
	}
	if _, err := s.st.UpsertInventory(s.ctx, sourceID, items); err != nil {
		return fmt.Errorf("inventory: %w", err)
	}
	for _, r := range out.Resources {
		first, deleted := r.FirstSeen, r.DeletedAt
		if err := s.st.SetInventoryBounds(s.ctx, sourceID, r.ID, &first, &deleted); err != nil {
			return fmt.Errorf("inventory bounds for %s: %w", r.ID, err)
		}
	}
	return nil
}

// ensureDiscounts creates the customer's discounts and the global campaigns,
// matched by name so a re-run never stacks a second copy on the same bill.
func (s *seeder) ensureDiscounts(customerID string, ds []synth.Discount) error {
	existing, err := s.api.listDiscounts()
	if err != nil {
		return fmt.Errorf("list discounts: %w", err)
	}
	have := map[string]bool{}
	for _, d := range existing {
		have[d.Name] = true
	}
	for _, d := range ds {
		if have[d.Name] {
			continue
		}
		body := map[string]any{
			"name": d.Name, "kind": d.Kind, "value": strconv.FormatFloat(d.Value, 'f', -1, 64),
		}
		if customerID != "" {
			body["customer_id"] = customerID
		}
		if d.SKU != "" {
			body["sku"] = d.SKU
		}
		if !d.StartsAt.IsZero() {
			body["starts_at"] = d.StartsAt.Format(time.RFC3339)
		}
		if !d.EndsAt.IsZero() {
			body["ends_at"] = d.EndsAt.Format(time.RFC3339)
		}
		if _, err := s.api.createDiscount(body); err != nil {
			return fmt.Errorf("create discount %q: %w", d.Name, err)
		}
		s.infof("  discount %q created", d.Name)
	}
	return nil
}

func (s *seeder) ensureBudgets(customerID string, bs []synth.Budget) error {
	existing, err := s.api.listBudgets()
	if err != nil {
		return fmt.Errorf("list budgets: %w", err)
	}
	have := map[string]bool{}
	for _, b := range existing {
		have[b.Name] = true
	}
	for _, b := range bs {
		if have[b.Name] {
			continue
		}
		if _, err := s.api.createBudget(map[string]any{
			"name": b.Name, "customer_id": customerID,
			"amount": strconv.FormatFloat(b.Amount, 'f', -1, 64),
			"period": "monthly", "thresholds": b.Thresholds,
		}); err != nil {
			return fmt.Errorf("create budget %q: %w", b.Name, err)
		}
		s.infof("  budget %q created (%.0f OMR/month)", b.Name, b.Amount)
	}
	return nil
}

// runAndIssueStatements rates each month, issues it without mailing anybody,
// and backdates the issue to the 1st of the following month so the statement
// list reads like a history instead of like a batch run today.
func (s *seeder) runAndIssueStatements(customerID string, c *synth.Customer) (map[string]string, int, error) {
	totals := map[string]string{}
	issued := 0
	for _, period := range s.sc.Months(c) {
		results, err := s.api.runStatements(period, customerID)
		if err != nil {
			return totals, issued, fmt.Errorf("run statements %s: %w", period, err)
		}
		var res *apiRunResult
		for i := range results {
			if results[i].CustomerID == customerID {
				res = &results[i]
			}
		}
		switch {
		case res == nil:
			return totals, issued, fmt.Errorf("run statements %s: no result for %s", period, c.Slug)
		case res.Error != "" && strings.Contains(res.Error, "already issued"):
			// A re-run over an issued period: the bill stands, which is the
			// product protecting a financial record. Read the total back.
			s.infof("  %s %s: already issued; the statement stands", c.Slug, period)
		case res.Error != "":
			return totals, issued, fmt.Errorf("run statements %s for %s: %s", period, c.Slug, res.Error)
		default:
			if len(res.Unpriced) > 0 {
				s.infof("  %s %s: unpriced SKUs %v", c.Slug, period, res.Unpriced)
			}
			if err := s.api.issueStatement(res.StatementID); err != nil {
				return totals, issued, fmt.Errorf("issue statement %s %s: %w", c.Slug, period, err)
			}
		}
	}
	// Read every statement back from the API — the totals reported in the
	// summary are the product's own numbers, never this command's prediction.
	list, err := s.api.listStatements(customerID)
	if err != nil {
		return totals, issued, fmt.Errorf("list statements: %w", err)
	}
	for _, st := range list {
		if len(st.PeriodStart) < 7 {
			continue
		}
		period := st.PeriodStart[:7]
		totals[period] = fmt.Sprint(st.Total)
		if st.Status == "issued" {
			issued++
		}
		at := issueInstant(period)
		if _, err := s.db.ExecContext(s.ctx,
			`UPDATE statements SET issued_at = $2, created_at = $2 WHERE id = $1 AND status = 'issued'`,
			st.ID, at); err != nil {
			return totals, issued, fmt.Errorf("backdate statement %s: %w", st.ID, err)
		}
	}
	return totals, issued, nil
}

// issueInstant is 09:00 UTC on the 1st of the month after the period — when a
// monthly billing run would actually have produced the bill.
func issueInstant(period string) time.Time {
	_, end, err := synth.MonthBounds(period)
	if err != nil {
		return time.Now().UTC()
	}
	return end.Add(9 * time.Hour)
}

// decommission ends the customer's life: suspended, with the reason recorded
// in the product's own per-customer audit trail (GET /customers/{id}/audit),
// which is the only note field the customer has.
//
// The audit log is append-only by design, so writing the note unconditionally
// would stack a second copy on every re-run — measured: two runs produced 12
// decommission entries for 6 customers. This command's own prior note is
// therefore removed first, which keeps exactly one and lets a changed note
// replace the old text. Only rows this command wrote (actor seed-history) for
// THIS customer are touched; the product's own entries are never rewritten.
func (s *seeder) decommission(customerID string, c *synth.Customer) error {
	if _, err := s.api.patchCustomer(customerID, map[string]any{"status": "suspended"}); err != nil {
		return fmt.Errorf("suspend %s: %w", c.Slug, err)
	}
	if _, err := s.db.ExecContext(s.ctx,
		`DELETE FROM audit_log WHERE customer_id = $1 AND actor = 'seed-history' AND action = 'customer.decommission'`,
		customerID); err != nil {
		return fmt.Errorf("clear previous decommission note: %w", err)
	}
	if err := s.st.Audit(s.ctx, &customerID, "seed-history", "customer.decommission", map[string]any{
		"note":      c.DecommissionNote,
		"at":        c.Left.Format(time.RFC3339),
		"synthetic": synth.LabelValue,
	}); err != nil {
		return fmt.Errorf("audit decommission: %w", err)
	}
	return nil
}

// backdate moves the rows' own timestamps into the past. Without it every
// customer, source and audit entry claims to have been created today, and the
// history reads as a batch import rather than as three months of trading.
// Only rows this command owns are touched: the customer is addressed by id,
// and the audit entries by that id and this actor.
func (s *seeder) backdate(customerID, sourceID string, c *synth.Customer) error {
	if _, err := s.db.ExecContext(s.ctx,
		`UPDATE customers SET created_at = $2, updated_at = $3 WHERE id = $1`,
		customerID, c.Joined, c.Left); err != nil {
		return fmt.Errorf("backdate customer: %w", err)
	}
	// A `file` source has no credential to verify, so the status the API can
	// reach it through does not exist; the collector's own columns are set
	// here instead, exactly as a successful collection would have left them.
	if _, err := s.db.ExecContext(s.ctx,
		`UPDATE cost_sources SET created_at = $2, status = 'verified', verified_at = $2, last_collected_at = $3 WHERE id = $1`,
		sourceID, c.Joined, c.Left); err != nil {
		return fmt.Errorf("backdate source: %w", err)
	}
	if _, err := s.db.ExecContext(s.ctx,
		`UPDATE audit_log SET at = $2 WHERE customer_id = $1 AND actor = 'seed-history'`,
		customerID, c.Left); err != nil {
		return fmt.Errorf("backdate audit: %w", err)
	}
	return nil
}

// apply seeds one customer end to end.
func (s *seeder) apply(c *synth.Customer) (result, error) {
	r := result{Customer: c.Name, Slug: c.Slug, Source: c.Source.Name, Months: map[string]string{}}
	out := s.sc.Generate(c)
	r.Resources = len(out.Resources)
	if len(out.Records) == 0 {
		r.Note = "no usage in the window"
		return r, nil
	}
	s.infof("%s (%s, %s layer)", c.Name, c.Slug, c.Source.Layer)
	customerID, err := s.ensureCustomer(c)
	if err != nil {
		return r, err
	}
	sourceID, err := s.ensureSource(customerID, c)
	if err != nil {
		return r, err
	}
	rows, err := s.writeUsage(customerID, sourceID, out)
	if err != nil {
		return r, err
	}
	r.Rows = rows
	if err := s.writeInventory(sourceID, out); err != nil {
		return r, err
	}
	if err := s.ensureDiscounts(customerID, c.Discounts); err != nil {
		return r, err
	}
	if err := s.ensureBudgets(customerID, c.Budgets); err != nil {
		return r, err
	}
	totals, issued, err := s.runAndIssueStatements(customerID, c)
	if err != nil {
		return r, err
	}
	r.Months, r.Statements = totals, issued
	if err := s.decommission(customerID, c); err != nil {
		return r, err
	}
	if err := s.backdate(customerID, sourceID, c); err != nil {
		return r, err
	}
	r.Note = c.DecommissionNote
	return r, nil
}

func jsonLabels(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return jsonMarshal(m)
}
