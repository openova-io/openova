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
// Every row it writes is marked and removable: customers and sources are
// named demo-*, discounts and budgets "demo: *", and every usage record and
// inventory row carries {"synthetic":"true"}. --purge removes exactly those
// rows and nothing else.
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
	baseURL      string
	forwardEmail string
	authHeader   string
	sessionToken string
	dsn          string
	from, to     string
	seed         uint64
	dryRun       bool
	doPurge      bool
	only         string
}

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
	flag.StringVar(&o.only, "only", "", "seed one customer only (slug, with or without the demo- prefix)")
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

	customers := sc.Customers
	if o.only != "" {
		c := sc.Customer(o.only)
		if c == nil {
			return fmt.Errorf("--only %q names no showcase customer; known: %s", o.only, strings.Join(slugs(sc.Customers), ", "))
		}
		customers = []*synth.Customer{c}
	}

	if o.dryRun {
		return dryRun(sc, customers)
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

	s := &seeder{sc: sc, api: api, st: st, db: db, ctx: ctx}
	needCloud, needPlan := false, false
	for _, c := range customers {
		switch c.Source.Layer {
		case synth.LayerCloud:
			needCloud = true
		case synth.LayerPlatform:
			needPlan = true
		}
	}
	if err := s.ensureBooks(needCloud, needPlan); err != nil {
		return err
	}

	// The global launch campaign belongs to no customer.
	if err := s.ensureDiscounts("", sc.GlobalDiscounts); err != nil {
		return err
	}

	results := make([]result, 0, len(customers))
	start := time.Now()
	for _, c := range customers {
		r, err := s.apply(c)
		if err != nil {
			return err
		}
		results = append(results, r)
	}
	log.Printf("done in %s", time.Since(start).Round(time.Second))
	printSummary(results, sc.Window)
	return nil
}

// dryRun prints what would be written, with the totals computed from the
// product's own rates, and touches nothing.
func dryRun(sc *synth.Scenario, customers []*synth.Customer) error {
	prices := synth.Prices(synth.NationalCloudRates, synth.PlanRates)
	log.Printf("DRY RUN — nothing is written")
	log.Printf("window %s .. %s (exclusive); seed %d",
		sc.Window.From.Format(time.RFC3339), sc.Window.To.Format(time.RFC3339), sc.Seed)
	results := make([]result, 0, len(customers))
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
	printSummary(results, sc.Window)
	log.Printf("the OMR figures above are list price at the product's own rates; a real run reports what the statements actually rated")
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
