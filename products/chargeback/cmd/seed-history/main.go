// seed-history populates a chargeback database with a synthetic three-month
// trading history for six showcase customers — three buying National Cloud
// resources, three Organizations of this Sovereign on catalog plans — all of
// them decommissioned before the real data begins on 2 September 2026 (EPIC
// #6867, founder direction 2026-09-08).
//
// It exists so the console can be shown with a past: cost that moves, worker
// pools that scale, a migration, a promo, a cost anomaly, budgets that cross
// their thresholds, plan upgrades and downgrades, and statements that were
// issued month by month. From 2 September onwards only the real data is
// there, so the showcase customers read as having been moved off, deleted or
// decommissioned.
//
// Everything the API can express goes through the API, as the operator, so
// the product's own validation, auditing and upsert rules apply. The usage
// ledger, the inventory and the backdating of created/issued timestamps have
// no endpoint — the collectors write them — so those go through the same
// store package the collectors use, which is why --dsn is required.
//
// It also backfills the Sovereign's own LANDLORD customer (--landlord, see
// landlord.go): a real customer given a synthetic past that converges on the
// shape its real usage actually has and stops one hour before its first real
// record. Without it the daily chart had an empty bucket on 1 September and
// then jumped from the showcase customers straight to full real usage, which
// is the founder's 2026-09-10 defect: "step 1st is empty and the actual usage
// was already there from the beginning, you failed to show the continuity".
//
// The landlord step also corrects the REAL ledger in one narrow way
// (--neutralise-reservations, on by default; neutralise.go): the
// eip.bandwidth_mbps rows the pre-0.1.26 collector recorded on addresses the
// cloud bills by traffic describe a charge the cloud never made, and they are
// removed and the removal written to the landlord's audit trail. Nothing else
// real is ever written or deleted.
//
// Every row it writes is marked and removable: customers and sources are
// named demo-*, discounts and budgets "demo: *", and every usage record and
// inventory row carries {"synthetic":"true"}. --purge removes exactly those
// rows and nothing else — including the landlord's backfill source, which is
// reached by its own demo- name because its customer is real. The
// neutralisation is NOT reversed by --purge: the rows it removed were never
// billable.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

type options struct {
	baseURL       string
	forwardEmail  string
	authHeader    string
	sessionToken  string
	dsn           string
	from, to      string
	seed          uint64
	dryRun        bool
	doPurge       bool
	only          string
	landlord      string
	landlordUntil string
	cloudBook     string
	neutralise    bool
}

// landlordOnly is what --only takes to seed the landlord backfill alone,
// besides the landlord's own slug.
const landlordOnly = "landlord"

func main() {
	log.SetFlags(0)
	var o options
	flag.StringVar(&o.baseURL, "base-url", envOr("CB_BASE", "http://127.0.0.1:8080"), "base URL of the running chargeback service")
	flag.StringVar(&o.forwardEmail, "forward-auth-email", "", "operator identity to send in the trusted forward-auth header (header name from TRUSTED_FORWARD_AUTH_HEADER)")
	flag.StringVar(&o.authHeader, "forward-auth-header", envOr("TRUSTED_FORWARD_AUTH_HEADER", "X-Forwarded-Email"), "name of the trusted forward-auth header")
	flag.StringVar(&o.sessionToken, "session-cookie", "", "value of an operator cb_session cookie, instead of --forward-auth-email")
	flag.StringVar(&o.dsn, "dsn", envOr("DATABASE_URL", ""), "Postgres DSN, for the usage ledger and backdating (no endpoint writes those)")
	flag.StringVar(&o.from, "from", "", "window start, RFC3339 or YYYY-MM-DD (default 2026-06-01)")
	flag.StringVar(&o.to, "to", "", "window end, EXCLUSIVE, RFC3339 or YYYY-MM-DD (default 2026-09-01)")
	flag.Uint64Var(&o.seed, "seed", synth.DefaultSeed, "random seed; the same seed always produces the same data")
	flag.BoolVar(&o.dryRun, "dry-run", false, "print the plan and the totals, write nothing")
	flag.BoolVar(&o.doPurge, "purge", false, "remove everything seed-history created, and nothing else")
	flag.StringVar(&o.only, "only", "", "seed one customer only (slug, with or without the demo- prefix, or \""+landlordOnly+"\")")
	flag.StringVar(&o.landlord, "landlord", synth.LandlordDefaultSlug, "slug of the EXISTING landlord customer to backfill a converging past for; empty disables the backfill")
	flag.StringVar(&o.landlordUntil, "landlord-until", "", "exclusive end of the landlord backfill, RFC3339 (default: the hour before that customer's first real usage record)")
	flag.StringVar(&o.cloudBook, "cloud-book", "", "name or id of the cloud rate card to price the showcase from (default: the card the landlord's own cloud source is billed on, else the Sovereign's National Cloud card)")
	flag.BoolVar(&o.neutralise, "neutralise-reservations", true, "with the landlord step, remove the landlord's real "+synth.EIPReservationSKU+" rows on addresses whose inventory says "+synth.EIPChargeModeAttr+"="+synth.EIPChargeModeTraffic+" (recorded before the collector could read the charge mode; the cloud never billed them) and record the removal on the landlord's audit trail; addresses on mode "+synth.EIPChargeModeBandwidth+" or with no mode are left alone; not reversed by --purge")
	flag.Parse()

	if err := run(o); err != nil {
		log.Fatalf("seed-history: %v", err)
	}
}

func run(o options) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	window, err := parseWindow(o.from, o.to)
	if err != nil {
		return err
	}
	sc := synth.DefaultScenario(window, o.seed)

	landlordSlug := strings.ToLower(strings.TrimSpace(o.landlord))
	landlordUntil, err := parseLandlordUntil(o.landlordUntil)
	if err != nil {
		return err
	}
	doLandlord := landlordSlug != ""

	customers := sc.Customers
	if o.only != "" {
		only := strings.ToLower(strings.TrimSpace(o.only))
		if doLandlord && (only == landlordOnly || only == landlordSlug) {
			customers = nil
		} else {
			c := sc.Customer(o.only)
			if c == nil {
				return fmt.Errorf("--only %q names no showcase customer; known: %s, %s", o.only, strings.Join(slugs(sc.Customers), ", "), landlordOnly)
			}
			customers, doLandlord = []*synth.Customer{c}, false
		}
	}

	if o.dryRun {
		return dryRun(sc, customers, landlordSlug, landlordUntil, doLandlord, o.neutralise)
	}

	// --purge needs only the database: it is a delete, and the API has no
	// endpoint that could express "everything this tool made".
	if o.dsn == "" {
		return fmt.Errorf("--dsn is required: the usage ledger has no API endpoint (the collectors write it), so it is written through the store")
	}
	dbCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	db, err := store.Open(dbCtx, o.dsn)
	cancel()
	if err != nil {
		return fmt.Errorf("connect to Postgres: %w", err)
	}
	defer db.Close()
	st := store.New(db)

	if o.doPurge {
		counts, err := purge(ctx, db)
		if err != nil {
			return err
		}
		log.Printf("purged: %d usage records, %d inventory rows, %d rated lines, %d statements, %d discounts, %d budgets, %d sources, %d audit entries, %d customers",
			counts.Usage, counts.Inventory, counts.RatedLines, counts.Statements, counts.Discounts, counts.Budgets, counts.Sources, counts.Audit, counts.Customers)
		log.Printf("price books were NOT removed: they are shared with real customers")
		return nil
	}

	if o.baseURL == "" {
		return fmt.Errorf("--base-url is required")
	}
	if o.forwardEmail == "" && o.sessionToken == "" {
		return fmt.Errorf("one of --forward-auth-email or --session-cookie is required to act as the operator")
	}
	api := newClient(o.baseURL, o.authHeader, o.forwardEmail, o.sessionToken)
	role, err := api.whoami()
	if err != nil {
		return fmt.Errorf("authenticate against %s: %w", o.baseURL, err)
	}
	if role != store.RoleOperator {
		return fmt.Errorf("signed in as %q, but seeding needs the operator role", role)
	}
	log.Printf("seeding %s as operator; window %s .. %s (exclusive); seed %d",
		o.baseURL, window.From.Format(time.RFC3339), window.To.Format(time.RFC3339), o.seed)

	s := &seeder{sc: sc, api: api, st: st, db: db, ctx: ctx, neutralise: o.neutralise}
	needCloud, needPlan := false, false
	for _, c := range customers {
		switch c.Source.Layer {
		case synth.LayerCloud:
			needCloud = true
		case synth.LayerPlatform:
			needPlan = true
		}
	}
	// The cloud rate card is RESOLVED, not minted: the showcase must be priced
	// off the same card as the Sovereign's own usage or the two halves of the
	// console are not comparable. The landlord's own card is the default, so
	// it is looked up before the books are settled.
	landlordBookID := ""
	if doLandlord {
		landlordBookID = s.landlordCloudBookID(landlordSlug)
	}
	if err := s.ensureBooks(needCloud, needPlan, o.cloudBook, landlordBookID); err != nil {
		return err
	}
	if landlordBookID != "" && s.cloudBook.ID != "" && s.cloudBook.ID != landlordBookID {
		log.Printf("note: the showcase is priced from %q while the landlord is billed on another card; the two halves of the console will not be comparable", s.cloudBook.Name)
	}

	// Move a database an earlier run left on a duplicate card, and drop the
	// bills it rated there, BEFORE the statements below are run again.
	_, rerated, err := s.repointShowcase(s.cloudBook.ID)
	if err != nil {
		return err
	}

	// The global launch campaign belongs to no customer.
	if err := s.ensureDiscounts("", sc.GlobalDiscounts); err != nil {
		return err
	}

	results := make([]result, 0, len(customers)+1)
	start := time.Now()
	for _, c := range customers {
		r, err := s.apply(c)
		if err != nil {
			return err
		}
		results = append(results, r)
	}

	// The landlord backfill runs last and reaches furthest: it ends one hour
	// before the customer's first real record, not at the showcase window's
	// close, so it is what closes the 1 September hole and joins the six
	// showcase customers to the real collection.
	summaryWindow := sc.Window
	if doLandlord {
		r, found, err := s.applyLandlord(landlordSlug, landlordUntil)
		if err != nil {
			return err
		}
		if !found {
			log.Printf("landlord backfill skipped: no customer with slug %q on this database (pass --landlord <slug>, or --landlord '' to stop looking)", landlordSlug)
		} else {
			results = append(results, r)
			if to := landlordEnd(r); to.After(summaryWindow.To) {
				summaryWindow.To = to
			}
		}
	}
	// Last: the duplicate rate cards an earlier run left behind. By now every
	// row this tool owns has moved onto the resolved card, so anything still
	// pointing at a duplicate belongs to somebody else and stops the delete.
	if err := s.dropDuplicateBooks(); err != nil {
		return err
	}

	log.Printf("done in %s", time.Since(start).Round(time.Second))
	printSummary(results, summaryWindow)
	if rerated {
		log.Printf("the showcase moved onto %q, so the OMR figures above are NOT the ones this database held before: every showcase month was re-rated on the operator's own rates.",
			s.cloudBook.Name)
	}
	return nil
}

// landlordEnd is the last month the backfill wrote into, as an exclusive
// window end, so the summary table grows the column the backfill needs.
func landlordEnd(r result) time.Time {
	var last time.Time
	for period := range r.Months {
		if _, end, err := synth.MonthBounds(period); err == nil && end.After(last) {
			last = end
		}
	}
	return last
}

// parseLandlordUntil reads --landlord-until. Zero means "discover it from the
// customer's own ledger", which is the default and the only value that can be
// trusted to sit exactly one hour before the real data.
func parseLandlordUntil(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--landlord-until: %q is not RFC3339", s)
	}
	t = t.UTC()
	if !t.Equal(t.Truncate(time.Hour)) {
		return time.Time{}, fmt.Errorf("--landlord-until: %s is not a whole hour; the backfill writes hourly rows and must stop on an hour boundary", s)
	}
	return t, nil
}

// dryRun prints what would be written, with the totals computed from the
// product's own rates, and touches nothing.
func dryRun(sc *synth.Scenario, customers []*synth.Customer, landlordSlug string, landlordUntil time.Time, doLandlord, neutralise bool) error {
	prices := synth.Prices(synth.NationalCloudRates, synth.PlanRates)
	log.Printf("DRY RUN — nothing is written")
	log.Printf("window %s .. %s (exclusive); seed %d",
		sc.Window.From.Format(time.RFC3339), sc.Window.To.Format(time.RFC3339), sc.Seed)
	results := make([]result, 0, len(customers)+1)
	for _, c := range customers {
		out := sc.Generate(c)
		r := result{
			Customer: c.Name, Slug: c.Slug, Source: c.Source.Name,
			Rows: len(out.Records), Resources: len(out.Resources),
			Months: map[string]string{}, Note: c.DecommissionNote,
		}
		for period, total := range synth.MonthlyCost(out.Records, prices) {
			r.Months[period] = strconv.FormatFloat(total, 'f', 3, 64)
		}
		r.Statements = len(sc.Months(c))
		results = append(results, r)
	}

	summaryWindow := sc.Window
	if doLandlord {
		until := landlordUntil
		if until.IsZero() {
			// A dry run has no database to ask, so it shows the measured cut
			// a real run would discover on hw307.
			until = synth.LandlordDefaultUntil
			log.Printf("landlord %q: no database to discover the cut from, showing the measured %s",
				landlordSlug, until.Format(time.RFC3339))
		}
		lsc, err := synth.LandlordScenario(landlordSlug, sc.Window.From, until, sc.Seed)
		if err != nil {
			return err
		}
		c := lsc.Landlord()
		out := lsc.Generate(c)
		trafficGB, trafficRows := 0.0, 0
		for _, rec := range out.Records {
			if rec.SKU == synth.EIPTrafficSKU {
				trafficGB += rec.Quantity
				trafficRows++
			}
		}
		r := result{
			Customer: c.Name, Slug: c.Slug, Source: c.Source.Name,
			Rows: len(out.Records), Resources: len(out.Resources), Months: map[string]string{},
			Note: fmt.Sprintf("landlord backfill to %s — usage and inventory only, no statements; %.1f GB of %s over %d address-hours is unpriced until the operator enters a traffic rate",
				until.Format(time.RFC3339), trafficGB, synth.EIPTrafficSKU, trafficRows),
		}
		if neutralise {
			r.Note += fmt.Sprintf("; a real run first removes the landlord's real %s rows on traffic-billed addresses (--neutralise-reservations) — no database here to count them", synth.EIPReservationSKU)
		}
		for period, total := range synth.MonthlyCost(out.Records, synth.Prices(synth.NationalCloudRates)) {
			r.Months[period] = strconv.FormatFloat(total, 'f', 3, 64)
		}
		results = append(results, r)
		if end := landlordEnd(r); end.After(summaryWindow.To) {
			summaryWindow.To = end
		}
	}

	printSummary(results, summaryWindow)
	log.Printf("the OMR figures above are list price at the product's own rates; a real run reports what the statements actually rated")
	log.Printf("a real run does NOT price from those rates: it resolves the Sovereign's own cloud rate card (--cloud-book, else the landlord's card, else the National Cloud card) and only creates %q when there is none to borrow",
		synth.ShowcaseCloudBookName)
	return nil
}

// printSummary renders the per-customer table: rows written, resources, and
// the statement total for each month of the window.
func printSummary(results []result, w synth.Window) {
	months := w.Months()
	head := []string{"customer", "source", "rows", "res", "stmts"}
	head = append(head, months...)
	rows := [][]string{head}
	totalRows, totalRes := 0, 0
	perMonth := map[string]float64{}
	for _, r := range results {
		line := []string{r.Customer, r.Source, fmt.Sprint(r.Rows), fmt.Sprint(r.Resources), fmt.Sprint(r.Statements)}
		for _, m := range months {
			v := r.Months[m]
			if v == "" {
				v = "-"
			} else if f, err := strconv.ParseFloat(v, 64); err == nil {
				perMonth[m] += f
				v = strconv.FormatFloat(f, 'f', 2, 64)
			}
			line = append(line, v)
		}
		rows = append(rows, line)
		totalRows += r.Rows
		totalRes += r.Resources
	}
	tot := []string{"TOTAL", "", fmt.Sprint(totalRows), fmt.Sprint(totalRes), ""}
	for _, m := range months {
		tot = append(tot, strconv.FormatFloat(perMonth[m], 'f', 2, 64))
	}
	rows = append(rows, tot)

	width := make([]int, len(head))
	for _, r := range rows {
		for i, cell := range r {
			if len(cell) > width[i] {
				width[i] = len(cell)
			}
		}
	}
	var sb strings.Builder
	sb.WriteString("\n")
	for ri, r := range rows {
		for i, cell := range r {
			if i > 0 {
				sb.WriteString("  ")
			}
			if i <= 1 {
				sb.WriteString(pad(cell, width[i]))
			} else {
				sb.WriteString(lpad(cell, width[i]))
			}
		}
		sb.WriteString("\n")
		if ri == 0 || ri == len(rows)-2 {
			for i := range r {
				if i > 0 {
					sb.WriteString("  ")
				}
				sb.WriteString(strings.Repeat("-", width[i]))
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nOMR per month = the statement total for that period.\n")
	for _, r := range results {
		if r.Note != "" {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", r.Slug, r.Note))
		}
	}
	fmt.Print(sb.String())
}

func pad(s string, n int) string  { return s + strings.Repeat(" ", n-len(s)) }
func lpad(s string, n int) string { return strings.Repeat(" ", n-len(s)) + s }

// parseWindow reads --from/--to, defaulting to the showcase window. The end
// is exclusive: 2026-09-01 means the last generated hour is 31 August 23:00,
// and the real data from 2 September is never touched.
func parseWindow(from, to string) (synth.Window, error) {
	w := synth.DefaultWindow()
	parse := func(s string) (time.Time, error) {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z", "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), nil
			}
		}
		return time.Time{}, fmt.Errorf("%q is not RFC3339 or YYYY-MM-DD", s)
	}
	if from != "" {
		t, err := parse(from)
		if err != nil {
			return w, fmt.Errorf("--from: %w", err)
		}
		w.From = t
	}
	if to != "" {
		t, err := parse(to)
		if err != nil {
			return w, fmt.Errorf("--to: %w", err)
		}
		w.To = t
	}
	if err := w.Validate(); err != nil {
		return w, err
	}
	return w, nil
}

func slugs(cs []*synth.Customer) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Slug)
	}
	sort.Strings(out)
	return out
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
