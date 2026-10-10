package handlers

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/openova-io/openova/core/services/catalog/store"
)

// memAddOnStore is an in-memory addonUpserter mirroring the FerretDB store
// on the one point that matters: writes settle the two price fields.
type memAddOnStore struct {
	rows                      []store.AddOn
	creates, updates, deletes int
}

func (m *memAddOnStore) ListAddOns(context.Context) ([]store.AddOn, error) {
	out := make([]store.AddOn, len(m.rows))
	copy(out, m.rows)
	return out, nil
}

func (m *memAddOnStore) CreateAddOn(_ context.Context, a *store.AddOn) error {
	if a.ID == "" {
		a.ID = "id-" + a.Slug
	}
	a.NormalizePrice()
	m.rows = append(m.rows, *a)
	m.creates++
	return nil
}

func (m *memAddOnStore) UpdateAddOn(_ context.Context, id string, a *store.AddOn) error {
	for i := range m.rows {
		if m.rows[i].ID == id {
			a.ID = id
			a.NormalizePrice()
			m.rows[i] = *a
			m.updates++
			return nil
		}
	}
	return fmt.Errorf("addon %s not found", id)
}

func (m *memAddOnStore) DeleteAddOn(_ context.Context, id string) error {
	for i := range m.rows {
		if m.rows[i].ID == id {
			m.rows = append(m.rows[:i], m.rows[i+1:]...)
			m.deletes++
			return nil
		}
	}
	return fmt.Errorf("addon %s not found", id)
}

func (m *memAddOnStore) bySlug(slug string) *store.AddOn {
	for i := range m.rows {
		if m.rows[i].Slug == slug {
			return &m.rows[i]
		}
	}
	return nil
}

// oldAddOnRows is the add-on list a Sovereign seeded before #6971 carries:
// invented integer prices, no price_baisa, no app flag, and Priority Support
// with its invented 4h SLA.
func oldAddOnRows() []store.AddOn {
	return []store.AddOn{
		{ID: "uuid-backup", Slug: "daily-backup", Name: "Daily Backup", Description: "Automated daily backups with 30-day retention", PriceOMR: 3, Category: "reliability"},
		{ID: "uuid-support", Slug: "priority-support", Name: "Priority Support", Description: "Get help fast when it matters — 4h response SLA", PriceOMR: 5, Category: "support"},
		{ID: "uuid-domain", Slug: "custom-domain", Name: "Custom Domain", Description: "Bring your own domain — free DNS configuration with automatic TLS", Included: true, Category: "networking"},
		{ID: "uuid-api", Slug: "api-access", Name: "API Access", Description: "Full REST API for integration, automation, and custom workflows", PriceOMR: 5, Category: "developer"},
		{ID: "uuid-ip", Slug: "dedicated-ip", Name: "Dedicated IP", Description: "Dedicated IPv4 address with reverse DNS (PTR) registration", PriceOMR: 5, Category: "networking"},
		{ID: "uuid-waf", Slug: "waf", Name: "Web Application Firewall", Description: "Block attacks before they reach your apps — OWASP Core Rule Set", Included: true, Category: "security"},
		{ID: "uuid-ips", Slug: "ips", Name: "Intrusion Prevention", Description: "Community-powered threat intelligence — CrowdSec", Included: true, Category: "security"},
		{ID: "uuid-vuln", Slug: "vuln-scan", Name: "Vulnerability Scanning", Description: "Find vulnerabilities before attackers do — Trivy", Included: true, Category: "security"},
		{ID: "uuid-logs", Slug: "log-management", Name: "Log Management", Description: "Search and analyze all your app logs — Grafana Loki", PriceOMR: 3, Category: "monitoring"},
	}
}

// TestExpectedAddOns_AllFreeAndUninvented pins the seed rows: no price, no
// invented retention / response time / SLA in a description, the application
// rows flagged, Priority Support gone.
func TestExpectedAddOns_AllFreeAndUninvented(t *testing.T) {
	rows := expectedAddOns()
	if len(rows) == 0 {
		t.Fatal("expectedAddOns is empty — nothing was checked")
	}
	invented := regexp.MustCompile(`(?i)\d+\s*-?\s*(day|hour|h\b|minute)|\bSLA\b|priority`)
	apps := map[string]bool{"waf": true, "ips": true, "vuln-scan": true, "log-management": true}
	for _, a := range rows {
		if a.PriceBaisa != 0 || a.PriceOMR != 0 {
			t.Errorf("add-on %q is priced (%d baisa / %v OMR) — catalog add-ons are free; the priced add-ons are the BSS package SKUs", a.Slug, a.PriceBaisa, a.PriceOMR)
		}
		if invented.MatchString(a.Description) || invented.MatchString(a.Name) {
			t.Errorf("add-on %q carries an invented figure or promise: %q / %q", a.Slug, a.Name, a.Description)
		}
		if a.App != apps[a.Slug] {
			t.Errorf("add-on %q App = %v, want %v (the application rows are waf, ips, vuln-scan, log-management)", a.Slug, a.App, apps[a.Slug])
		}
		if a.Slug == "priority-support" {
			t.Errorf("priority-support is still seeded — its 4h SLA was invented; the workbook's support line is 24/7 customer support, included on every package")
		}
	}
	// CONTROL: the invented-figure regex must be able to fire.
	if !invented.MatchString("Automated daily backups with 30-day retention") || !invented.MatchString("4h response SLA") {
		t.Fatal("CONTROL FAILED: the invented-figure regex does not match the old descriptions")
	}
}

// TestUpsertSeedAddOns_ReseedingOverOldRowsMakesThemFree is the proof the
// change reaches an already-seeded Sovereign: the old rows become free under
// their original IDs, the application rows gain the flag, and Priority Support
// is removed.
func TestUpsertSeedAddOns_ReseedingOverOldRowsMakesThemFree(t *testing.T) {
	m := &memAddOnStore{rows: oldAddOnRows()}
	created, updated, deleted, err := upsertSeedAddOns(context.Background(), m, expectedAddOns())
	if err != nil {
		t.Fatalf("upsertSeedAddOns: %v", err)
	}
	if created != 0 {
		t.Errorf("created = %d, want 0", created)
	}
	if deleted != 1 || m.bySlug("priority-support") != nil {
		t.Errorf("deleted = %d (priority-support present: %v), want exactly priority-support removed", deleted, m.bySlug("priority-support") != nil)
	}
	if updated != 8 {
		t.Errorf("updated = %d, want 8 (every remaining row changed: price, description, or app flag)", updated)
	}
	for _, slug := range []string{"daily-backup", "custom-domain", "api-access", "dedicated-ip", "waf", "ips", "vuln-scan", "log-management"} {
		got := m.bySlug(slug)
		if got == nil {
			t.Fatalf("add-on %q vanished", slug)
		}
		if got.PriceBaisa != 0 || got.PriceOMR != 0 {
			t.Errorf("add-on %q still priced after reseed: %d baisa / %v OMR", slug, got.PriceBaisa, got.PriceOMR)
		}
		if got.ID[:5] != "uuid-" {
			t.Errorf("add-on %q lost its ID: %q", slug, got.ID)
		}
	}
	for _, slug := range []string{"waf", "ips", "vuln-scan", "log-management"} {
		if !m.bySlug(slug).App {
			t.Errorf("add-on %q is not flagged as an application after reseed", slug)
		}
	}
	if m.bySlug("daily-backup").Description == "Automated daily backups with 30-day retention" {
		t.Errorf("daily-backup still describes the invented 30-day retention")
	}

	// Idempotent: a second pass writes nothing.
	m.creates, m.updates, m.deletes = 0, 0, 0
	created, updated, deleted, err = upsertSeedAddOns(context.Background(), m, expectedAddOns())
	if err != nil {
		t.Fatalf("second upsertSeedAddOns: %v", err)
	}
	if created+updated+deleted != 0 || m.creates+m.updates+m.deletes != 0 {
		t.Errorf("second pass wrote created=%d updated=%d deleted=%d — not idempotent", created, updated, deleted)
	}
}

// TestUpsertSeedAddOns_CreatesMissing: an empty store gets every row.
func TestUpsertSeedAddOns_CreatesMissing(t *testing.T) {
	m := &memAddOnStore{}
	created, updated, deleted, err := upsertSeedAddOns(context.Background(), m, expectedAddOns())
	if err != nil {
		t.Fatalf("upsertSeedAddOns: %v", err)
	}
	if created != len(expectedAddOns()) || updated != 0 || deleted != 0 {
		t.Errorf("created=%d updated=%d deleted=%d, want %d/0/0", created, updated, deleted, len(expectedAddOns()))
	}
}
