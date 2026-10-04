package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/api"
	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The showcase contracts, end to end against a real API on a real schema
// (DESIGN.md §7, §15): the seeder creates three agreements with exactly the
// scenario's lines, a second run writes nothing, the statement run rates Gulf
// Retail's committed SKU at the committed rate for the committed head and at
// list above it, and the purge removes the three with the rest.

const seedOperator = "ops@nc.example"

type quietMail struct{}

func (quietMail) Send(context.Context, string, string, string) error { return nil }

type acceptAll struct{}

func (acceptAll) VerifyProject(context.Context, string, string, string, string) error { return nil }

// setupSeeder serves the product's API over HTTP with the trusted forward-auth
// header on — the way the seeder signs in on a Sovereign behind the OIDC gate
// — and returns a seeder whose client speaks to it as the operator.
func setupSeeder(t *testing.T, w synth.Window) (*seeder, *sql.DB) {
	t.Helper()
	st, db := openDB(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{3}, 32))
	var logbuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := api.New(api.Deps{
		Store: st, Keys: keys, Mail: quietMail{}, Verifier: acceptAll{},
		Config: config.Config{
			PublicURL: "https://billing.t99.omani.works", Profile: "operator-central",
			OperatorEmails: []string{seedOperator}, TrustedForwardAuthHeader: "X-Forwarded-Email",
		},
		Metrics: metrics.New(), Version: "test",
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s := &seeder{
		sc:  synth.DefaultScenario(w, synth.DefaultSeed),
		api: newClient(srv.URL, "X-Forwarded-Email", seedOperator, ""),
		st:  st, db: db, ctx: context.Background(),
	}
	if role, err := s.api.whoami(); err != nil || role != store.RoleOperator {
		t.Fatalf("the seeder is not the operator: role=%q err=%v", role, err)
	}
	return s, db
}

// onboard runs the seeder's own steps up to and including the agreement for
// one showcase customer and returns its ids.
func onboard(t *testing.T, s *seeder, c *synth.Customer) (customerID, sourceID string) {
	t.Helper()
	customerID, err := s.ensureCustomer(c)
	if err != nil {
		t.Fatalf("customer %s: %v", c.Slug, err)
	}
	if sourceID, err = s.ensureSource(customerID, c); err != nil {
		t.Fatalf("source %s: %v", c.Slug, err)
	}
	if err := s.ensureContracts(customerID, c); err != nil {
		t.Fatalf("contracts %s: %v", c.Slug, err)
	}
	return customerID, sourceID
}

type contractRow struct {
	id, startsOn, endsOn, status, po, notes, currency string
	term, notice                                      int
	autoRenew                                         bool
	minimum                                           sql.NullString
	signed                                            sql.NullTime
}

func readContract(t *testing.T, db *sql.DB, name string) contractRow {
	t.Helper()
	var r contractRow
	if err := db.QueryRow(`SELECT id, to_char(starts_on,'YYYY-MM-DD'), to_char(ends_on,'YYYY-MM-DD'), status, po_reference, notes, currency, term_months, renewal_notice_days, auto_renew, minimum_commitment::text, signed_at
		FROM contracts WHERE name = $1`, name).Scan(&r.id, &r.startsOn, &r.endsOn, &r.status, &r.po, &r.notes, &r.currency, &r.term, &r.notice, &r.autoRenew, &r.minimum, &r.signed); err != nil {
		t.Fatalf("contract %q: %v", name, err)
	}
	return r
}

type lineRow struct {
	kind, sku, unit, qty string
	price, pct, amount   sql.NullString
	rollover             bool
}

func readLines(t *testing.T, db *sql.DB, contractID string) map[string]lineRow {
	t.Helper()
	rows, err := db.Query(`SELECT kind, sku, unit, quantity::text, committed_price::text, discount_pct::text, amount::text, rollover FROM contract_items WHERE contract_id = $1`, contractID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]lineRow{}
	for rows.Next() {
		var l lineRow
		if err := rows.Scan(&l.kind, &l.sku, &l.unit, &l.qty, &l.price, &l.pct, &l.amount, &l.rollover); err != nil {
			t.Fatal(err)
		}
		out[l.kind+"|"+l.sku] = l
	}
	return out
}

func f(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("%q is not a number", s)
	}
	return v
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestShowcaseContractsAreSeededRatedAndPurged(t *testing.T) {
	// The seeder runs over the showcase window, as a real run does; the usage
	// written below is August alone — the showcase month whose compute
	// exceeds the committed head (6,734 instance-hours against 5,952).
	s, db := setupSeeder(t, synth.DefaultWindow())
	if err := s.ensureBooks(true, true, "", ""); err != nil {
		t.Fatalf("books: %v", err)
	}
	gulf, dhofar, nizwa := s.sc.Customer("gulf-retail"), s.sc.Customer("dhofar-logistics"), s.sc.Customer("nizwa-fintech")
	gulfID, gulfSrc := onboard(t, s, gulf)
	onboard(t, s, dhofar)
	onboard(t, s, nizwa)

	// ── three contracts, exactly the scenario's terms and lines ────────
	if n := scalar[int](t, db, `SELECT count(*) FROM contracts`); n != 3 {
		t.Fatalf("%d contracts after the run, want 3", n)
	}
	g := readContract(t, db, "Gulf Retail Group 2026 agreement")
	if g.startsOn != "2026-01-01" || g.endsOn != "2026-12-31" || g.term != 12 || !g.autoRenew || g.notice != 60 || g.status != "active" ||
		g.po != "PO-GRG-2026-011" || g.currency != "OMR" || !g.minimum.Valid || !near(f(t, g.minimum.String), 1500) || !g.signed.Valid || !g.signed.Time.Equal(gulf.Joined) {
		t.Fatalf("Gulf Retail agreement = %+v", g)
	}
	if g.notes != "Annual framework agreement. Eight memory-optimised servers committed for the year; 1 TB of SSD and a 10 Mbps pipe included; 1,500 OMR monthly floor." {
		t.Fatalf("Gulf Retail notes = %q", g.notes)
	}
	gl := readLines(t, db, g.id)
	if len(gl) != 3 {
		t.Fatalf("Gulf Retail lines = %v, want 3", gl)
	}
	if l := gl["commitment|ecs.m7n.xlarge.8"]; l.unit != "instance-hour" || !near(f(t, l.qty), 5952) || !l.pct.Valid || !near(f(t, l.pct.String), 30) || l.price.Valid || l.rollover {
		t.Fatalf("Gulf Retail commitment = %+v, want 5952 instance-hour at 30 %% off", l)
	}
	if l := gl["allowance|evs.ssd.gb"]; l.unit != "gb-hour" || !near(f(t, l.qty), 744000) || l.rollover {
		t.Fatalf("Gulf Retail SSD allowance = %+v, want 744000 gb-hour, no rollover", l)
	}
	if l := gl["allowance|eip.bandwidth_mbps"]; l.unit != "mbps-hour" || !near(f(t, l.qty), 7440) || l.rollover {
		t.Fatalf("Gulf Retail bandwidth allowance = %+v, want 7440 mbps-hour, no rollover", l)
	}

	d := readContract(t, db, "Dhofar Logistics 2026 agreement")
	if d.startsOn != "2026-01-01" || d.endsOn != "2026-12-31" || d.term != 12 || d.autoRenew || d.notice != 90 || d.status != "active" ||
		d.po != "PO-DHL-7731" || !d.minimum.Valid || !near(f(t, d.minimum.String), 1000) {
		t.Fatalf("Dhofar agreement = %+v", d)
	}
	dl := readLines(t, db, d.id)
	if len(dl) != 2 {
		t.Fatalf("Dhofar lines = %v, want 2", dl)
	}
	if l := dl["commitment|evs.ssd.gb"]; l.unit != "gb-hour" || !near(f(t, l.qty), 2976000) || !l.price.Valid || !near(f(t, l.price.String), 0.00018) || l.pct.Valid {
		t.Fatalf("Dhofar SSD commitment = %+v, want 2976000 gb-hour at 0.00018", l)
	}
	if l := dl["allowance|eip.bandwidth_mbps"]; l.unit != "mbps-hour" || !near(f(t, l.qty), 2976) || !l.rollover {
		t.Fatalf("Dhofar bandwidth allowance = %+v, want 2976 mbps-hour with rollover", l)
	}

	n := readContract(t, db, "Nizwa Fintech 2026 platform agreement")
	if n.startsOn != "2026-01-01" || n.endsOn != "2026-12-31" || n.term != 12 || !n.autoRenew || n.notice != 30 || n.status != "active" ||
		n.po != "PO-NZF-2026-03" || n.minimum.Valid {
		t.Fatalf("Nizwa agreement = %+v (the floor is the spend line's, the header carries none)", n)
	}
	nl := readLines(t, db, n.id)
	if len(nl) != 1 {
		t.Fatalf("Nizwa lines = %v, want the one spend commitment", nl)
	}
	if l := nl["spend|"]; !l.amount.Valid || !near(f(t, l.amount.String), 20) || !l.pct.Valid || !near(f(t, l.pct.String), 15) || l.sku != "" || l.price.Valid {
		t.Fatalf("Nizwa spend commitment = %+v, want 20 a month at 15 %% off", l)
	}

	// ── a second run adds nothing and writes nothing ───────────────────
	audits := scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE action LIKE 'contract.%'`)
	updated := scalar[time.Time](t, db, `SELECT max(updated_at) FROM contracts`)
	for _, c := range []*synth.Customer{gulf, dhofar, nizwa} {
		onboard(t, s, c)
	}
	if got := scalar[int](t, db, `SELECT count(*) FROM contracts`); got != 3 {
		t.Fatalf("%d contracts after a second run, want still 3", got)
	}
	if got := scalar[int](t, db, `SELECT count(*) FROM contract_items`); got != 6 {
		t.Fatalf("%d lines after a second run, want still 6", got)
	}
	if got := scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE action LIKE 'contract.%'`); got != audits {
		t.Fatalf("a second run wrote %d contract audit entries; identical terms must write nothing", got-audits)
	}
	if got := scalar[time.Time](t, db, `SELECT max(updated_at) FROM contracts`); !got.Equal(updated) {
		t.Fatal("a second run touched a contract that was already as the scenario has it")
	}
	// A contract an operator changed is brought BACK — that is the
	// re-run's job — and that is a write.
	mustExec(t, db, `UPDATE contracts SET renewal_notice_days = 7 WHERE id = $1`, g.id)
	mustExec(t, db, `DELETE FROM contract_items WHERE contract_id = $1 AND kind = 'allowance'`, g.id)
	onboard(t, s, gulf)
	if r := readContract(t, db, "Gulf Retail Group 2026 agreement"); r.notice != 60 {
		t.Fatalf("notice after the repair = %d, want 60", r.notice)
	}
	if got := len(readLines(t, db, g.id)); got != 3 {
		t.Fatalf("%d lines after the repair, want 3", got)
	}

	// ── the statement run rates under the agreement ───────────────────
	aug := synth.DefaultScenario(synth.Window{From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, synth.DefaultSeed)
	out := aug.Generate(aug.Customer("gulf-retail"))
	if _, err := s.writeUsage(gulfID, gulfSrc, out); err != nil {
		t.Fatalf("usage: %v", err)
	}
	var run struct {
		Results []struct {
			apiRunResult
			AppliedTerms []struct {
				SKU            string      `json:"sku"`
				Quantity       json.Number `json:"quantity"`
				Committed      json.Number `json:"committed"`
				CommittedPrice json.Number `json:"committed_price"`
				Excess         json.Number `json:"excess"`
				Amount         json.Number `json:"amount"`
				Allowance      json.Number `json:"allowance"`
			} `json:"applied_terms"`
			TrueUp     string `json:"true_up"`
			ContractID string `json:"contract_id"`
		} `json:"results"`
	}
	if err := s.api.do("POST", "/api/v1/statements/run", map[string]any{"period": "2026-08", "customer_id": gulfID}, &run); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(run.Results) != 1 || run.Results[0].Error != "" {
		t.Fatalf("run results = %+v", run.Results)
	}
	res := run.Results[0]
	if res.ContractID != g.id {
		t.Fatalf("the period was rated under contract %q, want the Gulf Retail agreement %s", res.ContractID, g.id)
	}
	list := f(t, scalar[string](t, db, `SELECT unit_price::text FROM price_items WHERE sku = 'ecs.m7n.xlarge.8' AND price_book_id = $1`, s.cloudBook.ID))
	var sawCompute, sawSSD bool
	for _, br := range res.AppliedTerms {
		switch br.SKU {
		case "ecs.m7n.xlarge.8":
			sawCompute = true
			qty, committed, price, excess, amount := f(t, br.Quantity.String()), f(t, br.Committed.String()), f(t, br.CommittedPrice.String()), f(t, br.Excess.String()), f(t, br.Amount.String())
			if qty <= 5952 {
				t.Fatalf("August compute is %.0f instance-hours; the test needs a month above the 5,952 committed head", qty)
			}
			if !near(committed, 5952) || !near(excess, qty-5952) {
				t.Fatalf("committed head %.0f and excess %.0f of %.0f, want 5952 and the rest", committed, excess, qty)
			}
			if !near(price, math.Round(list*0.7*1e8)/1e8) {
				t.Fatalf("committed price %.8f, want 30 %% off the list price %.8f", price, list)
			}
			// The committed head at the committed rate, the excess at list.
			if want := 5952*price + excess*list; math.Abs(amount-want) > 0.01 {
				t.Fatalf("compute rated %.6f, want %.6f = 5952 × %.8f + %.0f × %.8f", amount, want, price, excess, list)
			}
		case "evs.ssd.gb":
			sawSSD = true
			if !near(f(t, br.Allowance.String()), 744000) {
				t.Fatalf("SSD allowance applied = %s, want 744000", br.Allowance)
			}
		}
	}
	if !sawCompute || !sawSSD {
		t.Fatalf("applied terms %+v name neither the compute commitment nor the SSD allowance", res.AppliedTerms)
	}
	// August is ~1,746 at list; the committed head, the SSD allowance and the
	// bandwidth allowance take it under the 1,500 floor, so the month carries
	// a true-up and the subtotal is the floor exactly.
	if res.TrueUp == "" {
		t.Fatal("August under the agreement raised no true-up; the floor is 1,500 and the shaped month is below it")
	}
	if sub := f(t, scalar[string](t, db, `SELECT subtotal::text FROM statements WHERE id = $1`, res.StatementID)); !near(sub, 1500) {
		t.Fatalf("August subtotal %.6f, want the 1,500 floor exactly (true-up %s)", sub, res.TrueUp)
	}

	// ── the purge removes the agreements with the rest ─────────────────
	counts, err := purge(context.Background(), db)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if counts.Contracts != 3 || counts.Customers != 3 {
		t.Fatalf("purge removed %d contracts and %d customers, want 3 and 3", counts.Contracts, counts.Customers)
	}
	if got := scalar[int](t, db, `SELECT count(*) FROM contracts`) + scalar[int](t, db, `SELECT count(*) FROM contract_items`); got != 0 {
		t.Fatalf("%d contract rows survived the purge", got)
	}
}

// Term covers the showcase window: the agreement's own anchor when the window
// sits inside it, the window's first month when the window starts earlier,
// and whole further terms when the window runs past the end.
func TestContractTermCoversTheWindow(t *testing.T) {
	ct := synth.Contract{StartsOn: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), TermMonths: 12}
	starts, months := ct.Term(synth.DefaultWindow())
	if starts.Format("2006-01-02") != "2026-01-01" || months != 12 {
		t.Fatalf("default window: %s for %d months, want 2026-01-01 for 12", starts.Format("2006-01-02"), months)
	}
	starts, months = ct.Term(synth.Window{From: time.Date(2025, 11, 15, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)})
	if starts.Format("2006-01-02") != "2025-11-01" || months != 12 {
		t.Fatalf("earlier window: %s for %d months, want 2025-11-01 for 12", starts.Format("2006-01-02"), months)
	}
	starts, months = ct.Term(synth.Window{From: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)})
	if starts.Format("2006-01-02") != "2026-01-01" || months != 12 {
		t.Fatalf("window ending on the term's last day: %s for %d, want 2026-01-01 for 12", starts.Format("2006-01-02"), months)
	}
	starts, months = ct.Term(synth.Window{From: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC)})
	if starts.Format("2006-01-02") != "2026-01-01" || months != 24 {
		t.Fatalf("window one hour past the term: %s for %d, want 2026-01-01 for 24", starts.Format("2006-01-02"), months)
	}
	if _, err := store.AddTerm(starts.Format("2006-01-02"), months); err != nil {
		t.Fatal(err)
	}
}
