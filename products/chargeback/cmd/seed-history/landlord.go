package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The landlord backfill (founder direction 2026-09-10). internal/synth/
// landlord.go explains the defect and the shape; this file is the part that
// touches the database.
//
// Three rules govern it, and every one of them is a rule about NOT writing:
//
//   - The landlord customer is REAL. It is found by slug and read; it is
//     never created, patched, suspended, audited or backdated. If it is not
//     there the backfill is skipped, not forced.
//   - Its statements are the operator's business. The backfill writes usage
//     and inventory only — it never runs or issues a statement, because
//     issuing one for a real customer would put a bill in front of somebody
//     over data this tool invented.
//   - Its rows live on their OWN source, demo-<slug>-history, so --purge
//     removes exactly them and never reaches the real ledger. The source
//     carries the same price book as the customer's real cloud source, so
//     both halves of the series are rated identically and the seam does not
//     jump.
//
// One deliberate exception writes to the real ledger, and it is a delete:
// --neutralise-reservations (neutralise.go) removes the landlord's real
// eip.bandwidth_mbps rows on addresses the cloud bills by traffic — rows the
// old collector recorded before it could read the charge mode, describing a
// charge the cloud never made. It runs first in the landlord step, so the
// real present the backfill converges on is the one without the fictional
// reservation line.

// resolveLandlordUntil answers where the backfill must stop: the hour
// boundary before the customer's earliest REAL usage record, so the last
// synthetic hour is the one immediately before the real collection begins and
// the daily series has no empty bucket between them.
//
// A row is real when it carries neither the synthetic label nor a synthetic
// source name — both, so a re-run after a partial write still finds the true
// start instead of its own backfill.
func resolveLandlordUntil(ctx context.Context, db *sql.DB, customerID string) (time.Time, bool, error) {
	var first sql.NullTime
	err := db.QueryRowContext(ctx, `SELECT min(u.window_start)
		FROM usage_records u JOIN cost_sources s ON s.id = u.source_id
		WHERE u.customer_id = $1
		  AND coalesce(u.labels->>'`+synth.LabelKey+`', '') <> '`+synth.LabelValue+`'
		  AND (s.`+synth.SQLSourcePredicate+`) IS NOT TRUE`, customerID).Scan(&first)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("find the first real usage record: %w", err)
	}
	if !first.Valid {
		return time.Time{}, false, nil
	}
	return first.Time.UTC().Truncate(time.Hour), true, nil
}

// landlordBook is the price book of the customer's real cloud source, and the
// error naming what was found when there is not exactly one.
func landlordBook(sources []apiSource, slug string) (string, error) {
	var real []apiSource
	for _, s := range sources {
		if s.Internal || s.Layer != synth.LayerCloud {
			continue
		}
		if synth.IsSyntheticSourceName(s.ProjectID) {
			continue
		}
		real = append(real, s)
	}
	switch len(real) {
	case 1:
		if real[0].PriceBookID == nil || *real[0].PriceBookID == "" {
			return "", fmt.Errorf("landlord %q: its cloud source %s (%s) has no price book, so the backfill has no rates to converge on; assign one and re-run",
				slug, real[0].ProjectID, real[0].ID)
		}
		return *real[0].PriceBookID, nil
	case 0:
		return "", fmt.Errorf("landlord %q: no real cloud source to copy a price book from (found %s)", slug, describeSources(sources))
	default:
		return "", fmt.Errorf("landlord %q: %d real cloud sources, so which book the backfill should use is ambiguous (found %s)",
			slug, len(real), describeSources(sources))
	}
}

// shortID is the first 8 characters of an id, for a log line.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func describeSources(sources []apiSource) string {
	if len(sources) == 0 {
		return "no sources at all"
	}
	parts := make([]string, 0, len(sources))
	for _, s := range sources {
		book := "no book"
		if s.PriceBookName != "" {
			book = s.PriceBookName
		} else if s.PriceBookID != nil && *s.PriceBookID != "" {
			book = *s.PriceBookID
		}
		parts = append(parts, fmt.Sprintf("%s kind=%s layer=%s book=%s", s.ProjectID, s.Kind, s.Layer, book))
	}
	return strings.Join(parts, "; ")
}

// landlordCloudBookID is the rate card the landlord's REAL cloud source is
// billed on, or "" when there is no landlord, no such source, or no book on
// it. It is the principled default for the showcase too (book.go rule (b)):
// pricing the showcase off the landlord's own card is what makes the two
// halves of the console comparable.
//
// It never fails the run: every reason it can come back empty is a reason to
// fall through to the next resolution rule, and applyLandlord reports the same
// conditions properly when it is the backfill itself that needs the book.
func (s *seeder) landlordCloudBookID(slug string) string {
	if slug == "" {
		return ""
	}
	customers, err := s.api.listCustomers()
	if err != nil {
		return ""
	}
	for _, c := range customers {
		if c.Slug != slug {
			continue
		}
		sources, err := s.api.listSources(c.ID)
		if err != nil {
			return ""
		}
		book, err := landlordBook(sources, slug)
		if err != nil {
			return ""
		}
		return book
	}
	return ""
}

// applyLandlord writes the backfill for one existing customer. found is false
// when there is no such customer, which is not an error: a database that is
// not this Sovereign's simply has no landlord to backfill.
//
// untilOverride is --landlord-until; zero means discover the cut from the
// ledger.
func (s *seeder) applyLandlord(slug string, untilOverride time.Time) (r result, found bool, err error) {
	customers, err := s.api.listCustomers()
	if err != nil {
		return r, false, fmt.Errorf("list customers: %w", err)
	}
	var landlord *apiCustomer
	for i := range customers {
		if customers[i].Slug == slug {
			landlord = &customers[i]
			break
		}
	}
	if landlord == nil {
		return r, false, nil
	}

	// The real ledger first: reservations the cloud never billed come off
	// whether or not the backfill below has anything to write.
	note := ""
	if s.neutralise {
		n, nerr := neutraliseReservations(s.ctx, s.db, landlord.ID)
		if nerr != nil {
			return r, true, fmt.Errorf("neutralise reservations of %s: %w", slug, nerr)
		}
		s.infof("%s (%s) — reservations the cloud never billed: %s", landlord.Name, slug, n)
		note = "; " + n.String()
	}

	until, discovered := untilOverride.UTC(), false
	if untilOverride.IsZero() {
		t, ok, uerr := resolveLandlordUntil(s.ctx, s.db, landlord.ID)
		if uerr != nil {
			return r, true, uerr
		}
		if !ok {
			return r, true, fmt.Errorf("landlord %q has no real usage record to converge on, so there is no cut to discover; pass --landlord-until <RFC3339> to set one, or --landlord '' to skip the backfill", slug)
		}
		until, discovered = t, true
	}

	sc, err := synth.LandlordScenario(slug, s.sc.Window.From, until, s.sc.Seed)
	if err != nil {
		return r, true, err
	}
	c := sc.Landlord()
	c.Name = landlord.Name
	out := sc.Generate(c)
	r = result{
		Customer: landlord.Name, Slug: slug, Source: c.Source.Name,
		Resources: len(out.Resources), Months: map[string]string{},
	}
	if len(out.Records) == 0 {
		r.Note = "backfill window is empty" + note
		return r, true, nil
	}

	sources, err := s.api.listSources(landlord.ID)
	if err != nil {
		return r, true, fmt.Errorf("list sources of %s: %w", slug, err)
	}
	bookID, err := landlordBook(sources, slug)
	if err != nil {
		return r, true, err
	}

	s.infof("%s (%s) — landlord backfill %s .. %s (exclusive), price book %s",
		landlord.Name, slug,
		sc.Window.From.Format(time.RFC3339), sc.Window.To.Format(time.RFC3339), shortID(bookID))
	if discovered {
		s.infof("  the cut is the hour before this customer's first real usage record")
	} else {
		s.infof("  the cut came from --landlord-until, not from the ledger")
	}

	src, err := s.api.upsertSource(landlord.ID, map[string]any{
		"kind":          c.Source.Kind,
		"region":        c.Source.Region,
		"project_id":    c.Source.Name,
		"price_book_id": bookID,
	})
	if err != nil {
		return r, true, fmt.Errorf("backfill source for %s: %w", slug, err)
	}
	// The book may have been assigned at creation above; on a re-run the
	// source already exists and POST leaves it alone, so set it explicitly.
	if err := s.api.patchSourcePriceBook(landlord.ID, src.ID, bookID); err != nil && !isStatus(err, 404) && !isStatus(err, 405) {
		return r, true, fmt.Errorf("assign price book to the backfill source: %w", err)
	}

	// The write is an upsert per (source, resource, sku, hour), so a row of a
	// SKU this model no longer emits would survive a re-seed (stale.go): clear
	// those from the backfill source, inside its window, before writing.
	if err := s.clearStaleSKUs(slug, src.ID, sc.Window, out.Records); err != nil {
		return r, true, fmt.Errorf("backfill source of %s: %w", slug, err)
	}

	// One seeder over the landlord's own window, sharing this one's
	// connections: writeUsage reads the scenario's months.
	ls := &seeder{sc: sc, api: s.api, st: s.st, db: s.db, ctx: s.ctx, books: s.books, csvImport: s.csvImport}
	rows, err := ls.writeUsage(landlord.ID, src.ID, out)
	if err != nil {
		return r, true, err
	}
	r.Rows = rows
	if err := ls.writeInventory(src.ID, out); err != nil {
		return r, true, err
	}

	// Only the SOURCE is backdated — never the customer, which is real. The
	// columns are set to exactly what a successful collection over that
	// window would have left behind.
	if _, err := s.db.ExecContext(s.ctx,
		`UPDATE cost_sources SET created_at = $2, status = 'verified', verified_at = $2, last_collected_at = $3 WHERE id = $1`,
		src.ID, sc.Window.From, sc.Window.To); err != nil {
		return r, true, fmt.Errorf("backdate the backfill source: %w", err)
	}

	// The figures are read back from the product, priced by the card the
	// source actually carries — never this command's own arithmetic, which
	// would report the fallback rates in internal/synth whatever the operator
	// is really charged. The explorer is scoped to the backfill's own source
	// so the real collection's rows are not folded in.
	rated := "the product's own rates for this source"
	if months, cerr := s.api.monthlyCostOfSource(landlord.ID, src.ID, sc.Window.From, sc.Window.To); cerr == nil {
		r.Months = months
	} else {
		for period, total := range synth.MonthlyCost(out.Records, synth.Prices(synth.NationalCloudRates)) {
			r.Months[period] = strconv.FormatFloat(total, 'f', 3, 64)
		}
		rated = "list price at the National Cloud rates in internal/synth — the explorer could not be read: " + cerr.Error()
	}
	r.Note = fmt.Sprintf("landlord backfill to %s — usage and inventory only, no statements issued (its statements are the operator's); OMR is %s; %s is unpriced until the operator enters a traffic rate%s",
		sc.Window.To.Format(time.RFC3339), rated, synth.EIPTrafficSKU, note)
	return r, true, nil
}
