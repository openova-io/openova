package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/openova-io/openova/core/services/catalog/store"
)

// memPlanStore is an in-memory planUpserter that behaves like the FerretDB
// store on the one point that matters here: CreatePlan/UpdatePlan settle the
// two price fields (store.Plan.NormalizePrice), exactly as store.go does.
type memPlanStore struct {
	rows    []store.Plan
	creates int
	updates int
}

func (m *memPlanStore) ListPlans(context.Context) ([]store.Plan, error) {
	out := make([]store.Plan, len(m.rows))
	copy(out, m.rows)
	return out, nil
}

func (m *memPlanStore) CreatePlan(_ context.Context, p *store.Plan) error {
	if p.ID == "" {
		p.ID = fmt.Sprintf("id-%s", p.Slug)
	}
	p.NormalizePrice()
	m.rows = append(m.rows, *p)
	m.creates++
	return nil
}

func (m *memPlanStore) UpdatePlan(_ context.Context, id string, p *store.Plan) error {
	for i := range m.rows {
		if m.rows[i].ID == id {
			p.ID = id
			p.NormalizePrice()
			m.rows[i] = *p
			m.updates++
			return nil
		}
	}
	return fmt.Errorf("plan %s not found", id)
}

func (m *memPlanStore) bySlug(slug string) *store.Plan {
	for i := range m.rows {
		if m.rows[i].Slug == slug {
			return &m.rows[i]
		}
	}
	return nil
}

// oldLadderRows is the plan ladder a Sovereign seeded before #6971 carries:
// integer OMR prices (price_omr INT, no price_baisa), the old shapes, the
// invented feature lists, and a Stripe price id an operator wired by hand.
func oldLadderRows() []store.Plan {
	return []store.Plan{
		{ID: "uuid-s", Slug: "s", Name: "S", Description: "For personal projects and small teams", CPU: "2 vCPU", Memory: "4 GB", Storage: "25 GB", PriceOMR: 5, SortOrder: 1,
			Features: []string{"Unlimited apps", "SSO included"}},
		{ID: "uuid-m", Slug: "m", Name: "M", Description: "For growing businesses up to 30 users", CPU: "4 vCPU", Memory: "8 GB", Storage: "50 GB", PriceOMR: 9, Popular: true, SortOrder: 2,
			Features: []string{"Unlimited apps", "Priority support"}, StripePriceID: "price_live_m"},
		{ID: "uuid-l", Slug: "l", Name: "L", Description: "For teams with 30–100 users", CPU: "8 vCPU", Memory: "16 GB", Storage: "100 GB", PriceOMR: 16, SortOrder: 3},
		{ID: "uuid-xl", Slug: "xl", Name: "XL", Description: "For enterprises with 100+ users", CPU: "16 vCPU", Memory: "32 GB", Storage: "200 GB", PriceOMR: 30, SortOrder: 4},
		{ID: "uuid-flexi", Slug: "flexi", Name: "Flexi", Description: "Pay as you go — scale resources on demand", CPU: "On demand", Memory: "On demand", Storage: "On demand", SortOrder: 5},
		// An operator-created plan the seed knows nothing about: must survive untouched.
		{ID: "uuid-custom", Slug: "gov-xxl", Name: "Gov XXL", CPU: "32 vCPU", Memory: "64 GB", Storage: "1 TB", PriceOMR: 99, SortOrder: 9},
	}
}

// TestUpsertSeedPlans_ReseedingOverOldRowsYieldsTheNewPrices is the proof the
// package numbers reach an ALREADY-SEEDED Sovereign: the rows above are what
// hw307 carried; after one pass every S/M/L/XL row holds the National Cloud
// price and shape, under its ORIGINAL id (orders and subscriptions key on the
// plan UUID) and with its Stripe price id intact.
func TestUpsertSeedPlans_ReseedingOverOldRowsYieldsTheNewPrices(t *testing.T) {
	m := &memPlanStore{rows: oldLadderRows()}
	created, updated, err := upsertSeedPlans(context.Background(), m, seedPlanRows())
	if err != nil {
		t.Fatalf("upsertSeedPlans: %v", err)
	}
	if created != 0 {
		t.Errorf("created = %d, want 0 — every seeded slug already had a row", created)
	}
	if updated != 5 {
		t.Errorf("updated = %d, want 5 (s, m, l, xl and flexi's feature list all moved)", updated)
	}

	want := map[string]struct {
		baisa          int
		omr            float64
		cpu, mem, disk string
	}{
		"s":  {2490, 2.49, "1 vCPU", "2 GB", "25 GB"},
		"m":  {4490, 4.49, "2 vCPU", "4 GB", "50 GB"},
		"l":  {7990, 7.99, "4 vCPU", "8 GB", "100 GB"},
		"xl": {13990, 13.99, "8 vCPU", "16 GB", "250 GB"},
	}
	for slug, w := range want {
		got := m.bySlug(slug)
		if got == nil {
			t.Fatalf("plan %q vanished", slug)
		}
		if got.ID != "uuid-"+slug {
			t.Errorf("plan %q id = %q, want %q — the upsert must keep the UUID orders are keyed on", slug, got.ID, "uuid-"+slug)
		}
		if got.PriceBaisa != w.baisa || got.PriceOMR != w.omr {
			t.Errorf("plan %q price = %d baisa / %v OMR, want %d / %v", slug, got.PriceBaisa, got.PriceOMR, w.baisa, w.omr)
		}
		if got.CPU != w.cpu || got.Memory != w.mem || got.Storage != w.disk {
			t.Errorf("plan %q shape = %s / %s / %s, want %s / %s / %s", slug, got.CPU, got.Memory, got.Storage, w.cpu, w.mem, w.disk)
		}
		for _, f := range []string{"Priority support", "Audit logs", "SLA 99.9%", "Dedicated support"} {
			for _, have := range got.Features {
				if have == f {
					t.Errorf("plan %q still lists the invented feature %q", slug, f)
				}
			}
		}
	}
	if got := m.bySlug("m"); got.StripePriceID != "price_live_m" {
		t.Errorf("plan m StripePriceID = %q, want the operator-wired %q kept", got.StripePriceID, "price_live_m")
	}
	if got := m.bySlug("m"); !got.Popular {
		t.Errorf("plan m lost Popular — the plan picker's default selection")
	}
	if got := m.bySlug("xl"); len(got.Features) != len(packageIncludedFeatures)+len(packageXLFeatures) {
		t.Errorf("plan xl features = %v, want the included set plus Backup / AI SEO ready / AI website builder / Domain", got.Features)
	}
	// The operator's own plan is not the seed's to touch.
	if got := m.bySlug("gov-xxl"); got == nil || got.PriceOMR != 99 || got.CPU != "32 vCPU" {
		t.Errorf("operator-created plan gov-xxl was altered: %+v", got)
	}

	// Idempotent: a second pass over converged rows writes nothing.
	m.creates, m.updates = 0, 0
	created, updated, err = upsertSeedPlans(context.Background(), m, seedPlanRows())
	if err != nil {
		t.Fatalf("second upsertSeedPlans: %v", err)
	}
	if created != 0 || updated != 0 || m.creates != 0 || m.updates != 0 {
		t.Errorf("second pass wrote created=%d updated=%d (store saw %d creates, %d updates) — the convergence is not idempotent",
			created, updated, m.creates, m.updates)
	}
}

// TestUpsertSeedPlans_CreatesMissingSlugs: a Sovereign whose ladder lacks a
// seeded slug gets it created with the seeded price — and the rows it did
// have are converged in the same pass.
func TestUpsertSeedPlans_CreatesMissingSlugs(t *testing.T) {
	m := &memPlanStore{rows: oldLadderRows()[:2]} // s and m only
	created, updated, err := upsertSeedPlans(context.Background(), m, seedPlanRows())
	if err != nil {
		t.Fatalf("upsertSeedPlans: %v", err)
	}
	if created != 3 || updated != 2 {
		t.Errorf("created=%d updated=%d, want 3 created (l, xl, flexi) and 2 updated (s, m)", created, updated)
	}
	if got := m.bySlug("xl"); got == nil || got.PriceBaisa != 13990 {
		t.Errorf("xl was not created at 13990 baisa: %+v", got)
	}
}

// TestSeedPlanRows_NationalCloudLadder pins the fresh-seed rows to the
// workbook (NC-OO-Pricing.xlsx, 2026-06-28) by VALUE on every axis the
// storefront renders, so a drift in any one figure is named here.
func TestSeedPlanRows_NationalCloudLadder(t *testing.T) {
	rows := seedPlanRows()
	bySlug := map[string]store.Plan{}
	for _, p := range rows {
		bySlug[p.Slug] = p
	}
	cases := []struct {
		slug           string
		baisa          int
		cpu, mem, disk string
		popular        bool
	}{
		{"s", 2490, "1 vCPU", "2 GB", "25 GB", false},
		{"m", 4490, "2 vCPU", "4 GB", "50 GB", true},
		{"l", 7990, "4 vCPU", "8 GB", "100 GB", false},
		{"xl", 13990, "8 vCPU", "16 GB", "250 GB", false},
		{"flexi", 0, "On demand", "On demand", "On demand", false},
	}
	for _, c := range cases {
		p, ok := bySlug[c.slug]
		if !ok {
			t.Fatalf("plan %q not seeded", c.slug)
		}
		if p.PriceBaisa != c.baisa {
			t.Errorf("plan %q PriceBaisa = %d, want %d", c.slug, p.PriceBaisa, c.baisa)
		}
		if want := store.BaisaToOMR(c.baisa); p.PriceOMR != want {
			t.Errorf("plan %q PriceOMR = %v, want %v (the decimal mirror of %d baisa)", c.slug, p.PriceOMR, want, c.baisa)
		}
		if p.CPU != c.cpu || p.Memory != c.mem || p.Storage != c.disk {
			t.Errorf("plan %q shape = %s / %s / %s, want %s / %s / %s", c.slug, p.CPU, p.Memory, p.Storage, c.cpu, c.mem, c.disk)
		}
		if p.Popular != c.popular {
			t.Errorf("plan %q Popular = %v, want %v", c.slug, p.Popular, c.popular)
		}
		if c.slug != "flexi" {
			for _, f := range packageIncludedFeatures {
				found := false
				for _, have := range p.Features {
					if have == f {
						found = true
					}
				}
				if !found {
					t.Errorf("plan %q lacks the included feature %q", c.slug, f)
				}
			}
		}
	}
	// Only XL carries the four XL extras.
	for _, slug := range []string{"s", "m", "l"} {
		for _, f := range packageXLFeatures {
			for _, have := range bySlug[slug].Features {
				if have == f {
					t.Errorf("plan %q lists %q, which the workbook includes on XL only", slug, f)
				}
			}
		}
	}
}
