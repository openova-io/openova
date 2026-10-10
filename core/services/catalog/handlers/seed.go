package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/openova-io/openova/core/services/catalog/store"
)

// SeedIfEmpty checks whether the apps collection is empty and, if so,
// populates the database with default catalog data.
// It also ensures all expected add-ons exist (upserts missing ones).
func (h *Handler) SeedIfEmpty(ctx context.Context) {
	apps, err := h.Store.ListApps(ctx)
	if err != nil {
		slog.Error("seed: failed to check apps", "error", err)
		return
	}

	if len(apps) > 0 {
		slog.Info("seed: catalog already populated, checking migrations")
		h.migrateAppsTo27(ctx)
		h.dedupBySlug(ctx)
		h.seedMissingAddOns(ctx)
		h.migratePlans(ctx)
		h.seedMissingSandboxPlans(ctx)
		h.seedSystemApps(ctx)
		h.migrateAppDependencies(ctx)
		h.migrateAppDeployable(ctx)
		h.migrateAppPublished(ctx)
		return
	}

	slog.Info("seed: catalog is empty, seeding default data")
	h.seedAllData(ctx)

	// D27 fix (2026-05-17 t138 bug fix): on FRESH seed the rows are inserted
	// with zero-value Bool fields — Published=false, Deployable=false.
	// Marketplace storefront filters with ?published=true so it sees [],
	// and the UI renders a blank app grid. Founder caught on t136:
	// "https://marketplace.t136.../apps/ — list of applications are blank".
	//
	// The migrations that flip these to the expected defaults were ONLY
	// being called on the "already populated" path. Call them after
	// seedAllData so a fresh Sovereign + marketplace.enabled=true renders
	// 27 published+deployable apps out of the box.
	h.seedSystemApps(ctx)
	h.migrateAppDeployable(ctx)
	h.migrateAppPublished(ctx)
}

// seedAllData inserts the complete catalog: apps, industries, plans, addons, bundles.
func (h *Handler) seedAllData(ctx context.Context) {
	now := time.Now().UTC()

	// -----------------------------------------------------------------------
	// Apps — exact 27 from marketplace-apps.ts
	// -----------------------------------------------------------------------
	seedApps := seedAppRows(now)

	for i := range seedApps {
		if err := h.Store.CreateApp(ctx, &seedApps[i]); err != nil {
			slog.Error("seed: failed to create app", "slug", seedApps[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created apps", "count", len(seedApps))

	// -----------------------------------------------------------------------
	// Industries
	// -----------------------------------------------------------------------
	seedIndustries := []store.Industry{
		{Slug: "restaurant", Name: "Restaurant", Emoji: "\U0001F37D", Description: "Restaurants, cafes, and food service businesses", DisplayOrder: 1, SuggestedApps: []string{"wordpress", "cal-com", "chatwoot", "invoiceshelf", "umami"}, BundleID: "starter-pack"},
		{Slug: "retail", Name: "Retail", Emoji: "\U0001F6D2", Description: "Shops, e-commerce, and retail businesses", DisplayOrder: 2, SuggestedApps: []string{"erpnext", "medusa", "chatwoot", "invoiceshelf", "umami"}, BundleID: "business-suite"},
		{Slug: "legal", Name: "Legal", Emoji: "\u2696\uFE0F", Description: "Law firms and legal services", DisplayOrder: 3, SuggestedApps: []string{"nextcloud", "bookstack", "vaultwarden", "cal-com", "stalwart-mail"}, BundleID: "business-suite"},
		{Slug: "healthcare", Name: "Healthcare", Emoji: "\U0001FA7A", Description: "Clinics, hospitals, and healthcare providers", DisplayOrder: 4, SuggestedApps: []string{"nextcloud", "cal-com", "rocket-chat", "jitsi-meet", "vaultwarden"}, BundleID: "comms-hub"},
		{Slug: "education", Name: "Education", Emoji: "\U0001F393", Description: "Schools, universities, and training centers", DisplayOrder: 5, SuggestedApps: []string{"nextcloud", "bookstack", "jitsi-meet", "plane", "rocket-chat"}, BundleID: "comms-hub"},
		{Slug: "finance", Name: "Finance", Emoji: "\U0001F4B0", Description: "Banks, insurance, and financial services", DisplayOrder: 6, SuggestedApps: []string{"erpnext", "vaultwarden", "nocodb", "umami", "stalwart-mail"}, BundleID: "business-suite"},
		{Slug: "real-estate", Name: "Real Estate", Emoji: "\U0001F3E0", Description: "Property management and real estate agencies", DisplayOrder: 7, SuggestedApps: []string{"twenty", "wordpress", "cal-com", "chatwoot", "invoiceshelf"}, BundleID: "starter-pack"},
		{Slug: "technology", Name: "Technology", Emoji: "\U0001F4BB", Description: "Software companies and tech startups", DisplayOrder: 8, SuggestedApps: []string{"gitea", "uptime-kuma", "plane", "librechat", "dify"}, BundleID: "business-suite"},
		{Slug: "manufacturing", Name: "Manufacturing", Emoji: "\U0001F3ED", Description: "Factories, production, and supply chain", DisplayOrder: 9, SuggestedApps: []string{"erpnext", "nocodb", "nextcloud", "uptime-kuma", "bookstack"}, BundleID: "business-suite"},
		{Slug: "creative-agency", Name: "Creative Agency", Emoji: "\U0001F3A8", Description: "Design studios, marketing agencies, and creative firms", DisplayOrder: 10, SuggestedApps: []string{"nextcloud", "rocket-chat", "wordpress", "immich", "cal-com"}, BundleID: "comms-hub"},
	}

	for i := range seedIndustries {
		if err := h.Store.CreateIndustry(ctx, &seedIndustries[i]); err != nil {
			slog.Error("seed: failed to create industry", "slug", seedIndustries[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created industries", "count", len(seedIndustries))

	// -----------------------------------------------------------------------
	// Plans
	// -----------------------------------------------------------------------
	seedPlans := seedPlanRows()

	for i := range seedPlans {
		if err := h.Store.CreatePlan(ctx, &seedPlans[i]); err != nil {
			slog.Error("seed: failed to create plan", "slug", seedPlans[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created plans", "count", len(seedPlans))

	// -----------------------------------------------------------------------
	// Add-Ons
	// -----------------------------------------------------------------------
	seedAddOns := expectedAddOns()

	for i := range seedAddOns {
		if err := h.Store.CreateAddOn(ctx, &seedAddOns[i]); err != nil {
			slog.Error("seed: failed to create addon", "slug", seedAddOns[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created addons", "count", len(seedAddOns))

	// -----------------------------------------------------------------------
	// Bundles
	// -----------------------------------------------------------------------
	seedBundles := []store.Bundle{
		{Slug: "starter-pack", Name: "Starter Pack", Tagline: "Everything you need to get online", Apps: []string{"wordpress", "stalwart-mail", "chatwoot", "cal-com", "umami"}, Discount: 10, RecommendedSize: "s"},
		{Slug: "comms-hub", Name: "Comms Hub", Tagline: "Unified team communication", Apps: []string{"rocket-chat", "jitsi-meet", "stalwart-mail", "cal-com", "nextcloud"}, Discount: 15, RecommendedSize: "m"},
		{Slug: "business-suite", Name: "Business Suite", Tagline: "Complete business operations stack", Apps: []string{"erpnext", "twenty", "nextcloud", "invoiceshelf", "plane", "nocodb", "bookstack"}, Discount: 20, RecommendedSize: "l"},
	}

	for i := range seedBundles {
		if err := h.Store.CreateBundle(ctx, &seedBundles[i]); err != nil {
			slog.Error("seed: failed to create bundle", "slug", seedBundles[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created bundles", "count", len(seedBundles))

	slog.Info("seed: catalog seeding complete")
}

// packageIncludedFeatures is what EVERY package includes, in the order the
// National Cloud workbook lists them (NC-OO-Pricing.xlsx, Packages sheet,
// 2026-06-28). Nothing here is invented: no user counts, no SLA figures, no
// support tiers beyond what the sheet names.
var packageIncludedFeatures = []string{
	"Applications",
	"Databases",
	"Mail server (unlimited accounts)",
	"Unlimited free SSL",
	"SSO",
	"Standard DDoS protection",
	"Malware scanner",
	"Web application firewall",
	"24/7 customer support",
}

// packageXLFeatures is what XL includes on top of packageIncludedFeatures.
var packageXLFeatures = []string{
	"Backup",
	"AI SEO ready",
	"AI website builder",
	"Domain",
}

func withXLFeatures() []string {
	out := make([]string, 0, len(packageIncludedFeatures)+len(packageXLFeatures))
	out = append(out, packageIncludedFeatures...)
	return append(out, packageXLFeatures...)
}

// seedPlanRows returns the plan rows a FRESH Sovereign is seeded with, and the
// rows an EXISTING Sovereign is converged onto on every start (upsertSeedPlans).
//
// The numbers are the founder-approved SME packages from the National Cloud
// workbook (NC-OO-Pricing.xlsx, 2026-06-28) \u2014 the same sheet the
// organization-controller's planQuotaTable (core/controllers/organization/
// internal/gitops/manifests.go) takes its headline shapes from: S 1 vCPU /
// 2 GB / 25 GB at 2.490 OMR, M 2 / 4 / 50 at 4.490, L 4 / 8 / 100 at 7.990,
// XL 8 / 16 / 250 at 13.990. Prices are set in baisa (PriceBaisa); the
// decimal price_omr on the wire is derived by NormalizePrice.
//
// Extracted from seedAllData (#5920) to mirror seedAppRows: a pure function
// with no store, so the retired-product assertions in retired_products_test.go
// can read what a fresh Sovereign would actually be sold.
func seedPlanRows() []store.Plan {
	plans := []store.Plan{
		{Slug: "s", Name: "S", Description: "For a first site or a small team", CPU: "1 vCPU", Memory: "2 GB", Storage: "25 GB", PriceBaisa: 2490, Popular: false, SortOrder: 1,
			Features: append([]string(nil), packageIncludedFeatures...)},
		{Slug: "m", Name: "M", Description: "For a growing business", CPU: "2 vCPU", Memory: "4 GB", Storage: "50 GB", PriceBaisa: 4490, Popular: true, SortOrder: 2,
			Features: append([]string(nil), packageIncludedFeatures...)},
		{Slug: "l", Name: "L", Description: "For a busy business running several applications", CPU: "4 vCPU", Memory: "8 GB", Storage: "100 GB", PriceBaisa: 7990, Popular: false, SortOrder: 3,
			Features: append([]string(nil), packageIncludedFeatures...)},
		{Slug: "xl", Name: "XL", Description: "For the largest workloads, with backup and a domain included", CPU: "8 vCPU", Memory: "16 GB", Storage: "250 GB", PriceBaisa: 13990, Popular: false, SortOrder: 4,
			Features: withXLFeatures()},
		{Slug: "flexi", Name: "Flexi", Description: "Pay as you go \u2014 scale resources on demand", CPU: "On demand", Memory: "On demand", Storage: "On demand", PriceBaisa: 0, Popular: false, SortOrder: 5,
			Features: []string{"Unlimited apps", "SSO included", "API access", "TLS certificates", "Pay per use", "Scale on demand"}},
	}
	for i := range plans {
		plans[i].NormalizePrice()
	}

	// Sandbox product plans \u2014 PR #1633 added the Sandbox app to seedApps but
	// never wired the matching plan rows, so the marketplace checkout hit
	// "plan_id not found" the moment a customer picked Sandbox. Sandbox plans
	// are product-scoped (ProductSlug="sandbox") and carry IncludedQuotas the
	// orchestrator (#1639) reads via the `openova.io/plan-id` annotation.
	//
	// #5920: Sandbox was RETIRED on 2026-06-30. Its App row is already gone
	// from seedAppRows and seedMissingSandboxPlans already refuses to re-create
	// these tiers on an existing Sovereign \u2014 with the note "retiring the product
	// retires its price list with it". This call site was the other half of that
	// same decision and was left unguarded, so every FRESH Sovereign was still
	// seeded with sandbox-free / sandbox-pro (9 OMR, Popular) / sandbox-ent
	// (49 OMR) and served them on the public GET /catalog/plans.
	//
	// One switch (RetiredAppSlugs) now governs both paths, so un-retiring the
	// product would restore both without a second edit.
	if _, retired := RetiredAppSlugs()["sandbox"]; !retired {
		plans = append(plans, expectedSandboxPlans()...)
	}

	return plans
}

// expectedSandboxPlans returns the canonical set of Sandbox product plans.
// Shared by seedAllData (fresh catalog) and seedMissingSandboxPlans
// (existing Sovereigns whose catalog was populated before PR #1633's
// Sandbox seedApps addition). Three tiers, all ProductSlug="sandbox":
//
//   - sandbox-free (0 OMR) — 1 session, 1 agent, 5 GB, BYOS
//   - sandbox-pro  (9 OMR) — 3 sessions, 6 agents, 50 GB, BYOS (Popular)
//   - sandbox-ent (49 OMR) — unlimited sessions, all agents, 500 GB, BYOS
//
// IncludedQuotas is the contract between catalog and the
// sandbox-orchestrator: the orchestrator stamps `openova.io/plan-id` on
// the Sandbox CR and the controller derives quota.{cpu,memory,storage,
// concurrentSessions} from these named knobs. "0" on concurrent_sessions
// signals unlimited (controller: <=0 means unbounded).
func expectedSandboxPlans() []store.Plan {
	return []store.Plan{
		{
			Slug: "sandbox-free", Name: "Sandbox Free",
			Description: "1 session, 1 agent — bring your own LLM key",
			CPU:         "0.5 vCPU", Memory: "1 GB", Storage: "5 GB",
			PriceBaisa:  0,
			SortOrder:   10,
			ProductSlug: "sandbox",
			Features: []string{
				"1 concurrent session",
				"1 coding agent",
				"5 GB persistent storage",
				"BYOS — bring your own LLM key",
				"Browser TUI + mobile card-stream",
			},
			IncludedQuotas: map[string]string{
				"concurrent_sessions": "1",
				"agents":              "1",
				"storage_gb":          "5",
				"byos":                "true",
				"cpu":                 "500m",
				"memory":              "1Gi",
			},
		},
		{
			Slug: "sandbox-pro", Name: "Sandbox Pro",
			Description: "3 sessions, all 6 agents, 50 GB — for working developers",
			CPU:         "2 vCPU", Memory: "4 GB", Storage: "50 GB",
			PriceBaisa:  9000,
			Popular:     true,
			SortOrder:   11,
			ProductSlug: "sandbox",
			Features: []string{
				"3 concurrent sessions",
				"All 6 agents (Claude Code, Cursor, Qwen, Aider, Opencode, Little-Coder)",
				"50 GB persistent storage",
				"BYOS — bring your own LLM key",
				"openova-sandbox-mcp cluster primitives",
				"Preview deploys at <pr>.<app>.<sb-owner>.<sov>",
			},
			IncludedQuotas: map[string]string{
				"concurrent_sessions": "3",
				"agents":              "6",
				"storage_gb":          "50",
				"byos":                "true",
				"cpu":                 "2",
				"memory":              "4Gi",
			},
		},
		{
			Slug: "sandbox-ent", Name: "Sandbox Ent",
			Description: "Unlimited sessions, all agents, 500 GB — for teams",
			CPU:         "8 vCPU", Memory: "16 GB", Storage: "500 GB",
			PriceBaisa:  49000,
			SortOrder:   12,
			ProductSlug: "sandbox",
			Features: []string{
				"Unlimited concurrent sessions",
				"All 6 agents + custom agent slots",
				"500 GB persistent storage",
				"BYOS — bring your own LLM key",
				"Dedicated org-scoped namespace",
				"Audit logs + SSO",
				"Priority support",
			},
			IncludedQuotas: map[string]string{
				"concurrent_sessions": "0",
				"agents":              "6",
				"storage_gb":          "500",
				"byos":                "true",
				"cpu":                 "8",
				"memory":              "16Gi",
			},
		},
	}
}

// expectedAddOns returns the canonical set of catalog add-ons \u2014 the rows a
// fresh Sovereign is seeded with and an existing one is converged onto
// (upsertSeedAddOns).
//
// Every row is FREE (#6971). The prices these rows used to carry (Daily
// Backup 3 OMR, Dedicated IP 5, Log Management 3, \u2026) were invented: the
// National Cloud workbook (NC-OO-Pricing.xlsx, 2026-06-28) prices add-ons per
// PACKAGE, and those prices live in Catalyst BSS and reach the storefront as
// the `addon.*` SKUs of the public packages document \u2014 the catalog's rows
// are twinned to them by slug (core/marketplace/src/lib/packages.ts
// CATALOG_ADDON_TWINS) and hidden when the document is present. A catalog
// add-on therefore never carries a number, and its description names no
// retention, response time or SLA either. "Priority Support \u2014 4h response
// SLA" is gone with its number: the workbook's support line is "24/7 customer
// support", included on every package (packageIncludedFeatures).
//
// App marks the rows that are applications the Sovereign installs (the
// Coraza WAF, CrowdSec, Trivy, Grafana Loki) rather than commercial
// entitlements; the storefront lists those with the applications.
func expectedAddOns() []store.AddOn {
	return []store.AddOn{
		{Slug: "daily-backup", Name: "Daily Backup", Description: "Automated daily backups of your applications and data", Included: false, Category: "reliability"},
		{Slug: "custom-domain", Name: "Custom Domain", Description: "Bring your own domain \u2014 DNS configuration with automatic TLS", Included: true, Category: "networking"},
		{Slug: "api-access", Name: "API Access", Description: "Full REST API for integration, automation, and custom workflows", Included: false, Category: "developer"},
		{Slug: "dedicated-ip", Name: "Dedicated IP", Description: "Dedicated IPv4 address with reverse DNS (PTR) registration", Included: false, Category: "networking"},
		{Slug: "waf", Name: "Web Application Firewall", Description: "Block attacks before they reach your apps \u2014 OWASP Core Rule Set", Included: true, Category: "security", App: true},
		{Slug: "ips", Name: "Intrusion Prevention", Description: "Community-powered threat intelligence \u2014 CrowdSec", Included: true, Category: "security", App: true},
		{Slug: "vuln-scan", Name: "Vulnerability Scanning", Description: "Find vulnerabilities before attackers do \u2014 Trivy", Included: true, Category: "security", App: true},
		{Slug: "log-management", Name: "Log Management", Description: "Search and analyze all your app logs \u2014 Grafana Loki", Included: false, Category: "monitoring", App: true},
	}
}

// migrateAppsTo27 checks if the catalog has the old set of apps (with slugs like
// "stalwart" instead of "stalwart-mail") and replaces them with the correct 27.
func (h *Handler) migrateAppsTo27(ctx context.Context) {
	// Check if we already have the correct slugs.
	app, err := h.Store.GetApp(ctx, "stalwart-mail")
	if err != nil {
		slog.Error("seed: failed to check for stalwart-mail", "error", err)
		return
	}
	if app != nil {
		slog.Info("seed: apps already migrated to v2 slugs")
		return
	}

	slog.Info("seed: migrating apps to v2 (27 apps with correct slugs)")

	// Delete all existing apps.
	existing, err := h.Store.ListApps(ctx)
	if err != nil {
		slog.Error("seed: failed to list apps for migration", "error", err)
		return
	}
	for _, a := range existing {
		if err := h.Store.DeleteApp(ctx, a.ID); err != nil {
			slog.Error("seed: failed to delete old app", "slug", a.Slug, "error", err)
		}
	}

	// Delete all existing industries (they reference old slugs).
	existingInd, err := h.Store.ListIndustries(ctx)
	if err != nil {
		slog.Error("seed: failed to list industries for migration", "error", err)
		return
	}
	for _, ind := range existingInd {
		if err := h.Store.DeleteIndustry(ctx, ind.ID); err != nil {
			slog.Error("seed: failed to delete old industry", "slug", ind.Slug, "error", err)
		}
	}

	// Delete all existing bundles (they reference old slugs).
	existingBun, err := h.Store.ListBundles(ctx)
	if err != nil {
		slog.Error("seed: failed to list bundles for migration", "error", err)
		return
	}
	for _, b := range existingBun {
		if err := h.Store.DeleteBundle(ctx, b.ID); err != nil {
			slog.Error("seed: failed to delete old bundle", "slug", b.Slug, "error", err)
		}
	}

	// Delete all existing plans and addons to avoid duplicates when seedAllData re-creates them.
	existingPlans, _ := h.Store.ListPlans(ctx)
	for _, p := range existingPlans {
		_ = h.Store.DeletePlan(ctx, p.ID)
	}
	existingAddOns, _ := h.Store.ListAddOns(ctx)
	for _, a := range existingAddOns {
		_ = h.Store.DeleteAddOn(ctx, a.ID)
	}

	// Re-seed everything.
	h.seedAllData(ctx)
}

// addonUpserter is the slice of the store upsertSeedAddOns needs, so the
// convergence can be proven against an in-memory store without FerretDB.
type addonUpserter interface {
	ListAddOns(ctx context.Context) ([]store.AddOn, error)
	CreateAddOn(ctx context.Context, a *store.AddOn) error
	UpdateAddOn(ctx context.Context, id string, a *store.AddOn) error
	DeleteAddOn(ctx context.Context, id string) error
}

// seedOwnedAddOnFields is the projection of an add-on row the seed OWNS:
// everything but the row identity. Two rows equal through it need no write.
func seedOwnedAddOnFields(a store.AddOn) store.AddOn {
	a.ID = ""
	a.NormalizePrice()
	return a
}

// upsertSeedAddOns converges the live add-on rows onto `want` BY SLUG: a
// missing slug is created, a row whose seed-owned fields differ (price,
// description, included, category, app) is rewritten under its ID, a row
// that matches is left alone, and a row for a slug the seed no longer
// carries is DELETED — the catalog add-on list is wholly seed-owned (unlike
// the plan ladder, where an operator-created plan is kept). It runs on every
// catalog start (SeedIfEmpty → seedMissingAddOns), so the #6971 change — every
// add-on free, Priority Support withdrawn — reaches an already-seeded
// Sovereign on its next roll. Idempotent on converged rows.
func upsertSeedAddOns(ctx context.Context, s addonUpserter, want []store.AddOn) (created, updated, deleted int, err error) {
	existing, err := s.ListAddOns(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list addons: %w", err)
	}
	bySlug := make(map[string]*store.AddOn, len(existing))
	for i := range existing {
		bySlug[existing[i].Slug] = &existing[i]
	}
	wanted := make(map[string]bool, len(want))
	for i := range want {
		w := want[i]
		w.NormalizePrice()
		wanted[w.Slug] = true
		cur, ok := bySlug[w.Slug]
		if !ok {
			if cerr := s.CreateAddOn(ctx, &w); cerr != nil {
				slog.Error("seed: failed to add missing addon", "slug", w.Slug, "error", cerr)
				continue
			}
			created++
			slog.Info("seed: added missing addon", "slug", w.Slug)
			continue
		}
		if reflect.DeepEqual(seedOwnedAddOnFields(*cur), seedOwnedAddOnFields(w)) {
			continue
		}
		next := w
		next.ID = cur.ID
		if uerr := s.UpdateAddOn(ctx, cur.ID, &next); uerr != nil {
			slog.Error("seed: failed to update addon", "slug", w.Slug, "error", uerr)
			continue
		}
		updated++
		slog.Info("seed: updated addon to the seeded row", "slug", w.Slug,
			"price_baisa", fmt.Sprintf("%d -> %d", cur.PriceBaisa, next.PriceBaisa),
			"included", next.Included, "app", next.App)
	}
	for _, a := range existing {
		if wanted[a.Slug] {
			continue
		}
		if derr := s.DeleteAddOn(ctx, a.ID); derr != nil {
			slog.Error("seed: failed to remove stale addon", "slug", a.Slug, "error", derr)
			continue
		}
		deleted++
		slog.Info("seed: removed stale addon", "slug", a.Slug)
	}
	return created, updated, deleted, nil
}

// seedMissingAddOns converges the add-on rows onto expectedAddOns on every
// start (upsertSeedAddOns).
func (h *Handler) seedMissingAddOns(ctx context.Context) {
	created, updated, deleted, err := upsertSeedAddOns(ctx, h.Store, expectedAddOns())
	if err != nil {
		slog.Error("seed: addon convergence failed", "error", err)
		return
	}
	if created > 0 || updated > 0 || deleted > 0 {
		slog.Info("seed: addon convergence complete", "created", created, "updated", updated, "deleted", deleted)
	}
}

// planUpserter is the slice of the store upsertSeedPlans needs, so the
// convergence can be proven against an in-memory store (seed_plans_upsert_test.go)
// without FerretDB.
type planUpserter interface {
	ListPlans(ctx context.Context) ([]store.Plan, error)
	CreatePlan(ctx context.Context, p *store.Plan) error
	UpdatePlan(ctx context.Context, id string, p *store.Plan) error
}

// seedOwnedPlanFields is the projection of a plan row the seed OWNS: every
// field except the row identity (ID) and the operator-wired Stripe price id,
// which the seed never sets and must never erase. Comparing two rows through
// it decides whether an UpdatePlan is needed.
func seedOwnedPlanFields(p store.Plan) store.Plan {
	p.ID = ""
	p.StripePriceID = ""
	p.NormalizePrice()
	if len(p.Features) == 0 {
		p.Features = nil
	}
	if len(p.IncludedQuotas) == 0 {
		p.IncludedQuotas = nil
	}
	return p
}

// upsertSeedPlans converges the live plan rows onto `want` BY SLUG: a slug
// with no row is created; a row whose seed-owned fields differ (price, shape,
// description, features, popularity, order) is rewritten in place, keeping
// its ID (the plan UUID orders and subscriptions are keyed on) and its
// StripePriceID; a row that already matches is left alone. Rows for slugs the
// seed does not know (operator-created plans) are never touched.
//
// It runs on EVERY catalog start (SeedIfEmpty → migratePlans), so a price or
// shape change in seedPlanRows reaches an already-seeded Sovereign on its next
// roll — this is what carries the National Cloud package numbers (#6971) onto
// Sovereigns seeded with the previous ladder. Idempotent: a second pass over
// converged rows writes nothing (created == updated == 0).
func upsertSeedPlans(ctx context.Context, s planUpserter, want []store.Plan) (created, updated int, err error) {
	existing, err := s.ListPlans(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list plans: %w", err)
	}
	bySlug := make(map[string]*store.Plan, len(existing))
	for i := range existing {
		bySlug[existing[i].Slug] = &existing[i]
	}
	for i := range want {
		w := want[i]
		w.NormalizePrice()
		cur, ok := bySlug[w.Slug]
		if !ok {
			if cerr := s.CreatePlan(ctx, &w); cerr != nil {
				slog.Error("seed: failed to create plan", "slug", w.Slug, "error", cerr)
				continue
			}
			created++
			slog.Info("seed: created plan", "slug", w.Slug, "price_baisa", w.PriceBaisa)
			continue
		}
		if reflect.DeepEqual(seedOwnedPlanFields(*cur), seedOwnedPlanFields(w)) {
			continue
		}
		next := w
		next.ID = cur.ID
		next.StripePriceID = cur.StripePriceID
		if uerr := s.UpdatePlan(ctx, cur.ID, &next); uerr != nil {
			slog.Error("seed: failed to update plan", "slug", w.Slug, "error", uerr)
			continue
		}
		updated++
		slog.Info("seed: updated plan to the seeded row", "slug", w.Slug,
			"price_baisa", fmt.Sprintf("%d -> %d", cur.PriceBaisa, next.PriceBaisa),
			"cpu", fmt.Sprintf("%q -> %q", cur.CPU, next.CPU),
			"memory", fmt.Sprintf("%q -> %q", cur.Memory, next.Memory))
	}
	return created, updated, nil
}

// migratePlans converges an existing Sovereign's plan ladder onto
// seedPlanRows on every start (upsertSeedPlans), after retiring the
// pre-2026 XS tier the ladder no longer carries. It replaced three ad-hoc
// passes (the XS→S/M/L/XL rename, a RAM-ratio fixer and a features
// backfill) that each pinned their own copy of the numbers and could not
// carry a price change: the seed rows are now the single source and every
// seed-owned field converges, not only the ones a past migration remembered.
func (h *Handler) migratePlans(ctx context.Context) {
	existing, err := h.Store.ListPlans(ctx)
	if err != nil {
		slog.Error("seed: failed to list plans for migration", "error", err)
		return
	}
	for _, p := range existing {
		if p.Slug != "xs" {
			continue
		}
		if err := h.Store.DeletePlan(ctx, p.ID); err != nil {
			slog.Error("seed: failed to delete XS plan", "error", err)
		} else {
			slog.Info("seed: deleted XS plan")
		}
	}
	created, updated, err := upsertSeedPlans(ctx, h.Store, seedPlanRows())
	if err != nil {
		slog.Error("seed: plan convergence failed", "error", err)
		return
	}
	if created > 0 || updated > 0 {
		slog.Info("seed: plan convergence complete", "created", created, "updated", updated)
	}
}

// seedMissingSandboxPlans backfills Sandbox product plans on Sovereigns
// whose catalog was already populated when PR #1633 (Sandbox app) and
// this PR (Sandbox plans) landed. Idempotent: each plan slug is GET'd
// first and only inserted when absent. Also patches existing rows whose
// ProductSlug or IncludedQuotas drifted from the canonical set, since
// before this PR the Plan struct didn't even have those fields and any
// hand-inserted sandbox-* row would be missing the quota knobs the
// orchestrator reads.
func (h *Handler) seedMissingSandboxPlans(ctx context.Context) {
	// Sandbox is retired (#5920). This routine ran on EVERY seed pass and
	// re-created any sandbox-* tier that was missing, so taking the product off
	// sale while leaving this active would have quietly restored its priced
	// tiers on the next catalyst-api start. Retiring the product retires its
	// price list with it.
	if _, retired := RetiredAppSlugs()["sandbox"]; retired {
		return
	}
	want := expectedSandboxPlans()
	existing, err := h.Store.ListPlans(ctx)
	if err != nil {
		slog.Error("seed: failed to list plans for sandbox plan migration", "error", err)
		return
	}
	bySlug := make(map[string]*store.Plan, len(existing))
	for i := range existing {
		bySlug[existing[i].Slug] = &existing[i]
	}
	added, patched := 0, 0
	for i := range want {
		cur, ok := bySlug[want[i].Slug]
		if !ok {
			if err := h.Store.CreatePlan(ctx, &want[i]); err != nil {
				slog.Error("seed: failed to create sandbox plan", "slug", want[i].Slug, "error", err)
				continue
			}
			added++
			slog.Info("seed: created missing sandbox plan", "slug", want[i].Slug)
			continue
		}
		// Patch ProductSlug and IncludedQuotas when missing — the
		// catalog ListPlans handler returns them straight into the
		// marketplace plan picker and the sandbox-orchestrator's
		// quota derivation, so absence breaks both paths.
		needPatch := false
		if cur.ProductSlug != "sandbox" {
			cur.ProductSlug = "sandbox"
			needPatch = true
		}
		if len(cur.IncludedQuotas) == 0 {
			cur.IncludedQuotas = want[i].IncludedQuotas
			needPatch = true
		}
		if len(cur.Features) == 0 {
			cur.Features = want[i].Features
			needPatch = true
		}
		if needPatch {
			if err := h.Store.UpdatePlan(ctx, cur.ID, cur); err != nil {
				slog.Error("seed: failed to patch sandbox plan", "slug", cur.Slug, "error", err)
				continue
			}
			patched++
			slog.Info("seed: patched sandbox plan", "slug", cur.Slug)
		}
	}
	if added > 0 || patched > 0 {
		slog.Info("seed: sandbox plan migration complete", "added", added, "patched", patched)
	}
}

// seedSystemApps inserts mysql, postgres, and redis as system catalog apps
// (hidden from the marketplace but selectable as dependencies in the admin UI).
// Idempotent: re-runs every startup and only creates missing entries, and
// promotes any existing entry with the same slug to System=true.
func (h *Handler) seedSystemApps(ctx context.Context) {
	now := time.Now().UTC()
	replicasField := store.ConfigField{Key: "replicas", Label: "Replicas", Type: "int", Default: 1, Min: intPtr(1), Max: intPtr(5), Description: "Number of database instances in the cluster.", Advanced: false}
	diskField := store.ConfigField{Key: "disk_gb", Label: "Storage (GB)", Type: "int", Default: 5, Min: intPtr(1), Max: intPtr(500), Description: "Persistent volume size per replica.", Advanced: false}
	backupField := store.ConfigField{Key: "backups_enabled", Label: "Daily backups", Type: "bool", Default: false, Description: "Enable daily backups to object storage.", Advanced: true}
	// Pillar-3 active-hot-standby (TBD-V17 #2068). When the customer
	// flips active_hot_standby on AND picks distinct primary/replica
	// regions, the provisioning gitops emit the bp-cnpg-pair
	// HelmRelease shape instead of the single-Pod legacy postgres
	// Deployment — primary + replica CNPG Cluster CRs across two
	// regions with synchronous WAL streaming over Cilium ClusterMesh.
	// Default-OFF so every existing customer keeps the historical
	// single-cluster shape with zero regression.
	activeHotStandbyField := store.ConfigField{Key: "active_hot_standby", Label: "Active-hot-standby (multi-region DR)", Type: "bool", Default: false, Description: "Primary + replica Postgres across two regions with synchronous replication. Requires distinct primary_region and replica_region.", Advanced: true}
	primaryRegionField := store.ConfigField{Key: "primary_region", Label: "Primary region", Type: "string", Description: "Region key for the primary Cluster (e.g. hz-fsn-rtz-prod). Required when active_hot_standby=true.", Advanced: true}
	replicaRegionField := store.ConfigField{Key: "replica_region", Label: "Replica region", Type: "string", Description: "Region key for the replica Cluster — MUST differ from primary_region.", Advanced: true}

	systemApps := []store.App{
		{Slug: "mysql", Name: "MySQL", Tagline: "Relational database engine", Description: "Managed MySQL backing store. Provisioned automatically when required by an app dependency.", Category: "database", Icon: "\U0001F5C4", IconBg: "#00758F", System: true, Kind: "service", Shareable: true, Free: true, RamMB: 256, CpuMilli: 200, DiskGB: 5, HelmChart: "mysql", HelmRepo: "https://charts.bitnami.com/bitnami", ConfigSchema: []store.ConfigField{replicasField, diskField, backupField}, CreatedAt: now, UpdatedAt: now},
		{Slug: "postgres", Name: "PostgreSQL", Tagline: "Advanced open-source relational database", Description: "Managed PostgreSQL backing store. Provisioned automatically when required by an app dependency.", Category: "database", Icon: "\U0001F418", IconBg: "#336791", System: true, Kind: "service", Shareable: true, Free: true, RamMB: 256, CpuMilli: 200, DiskGB: 5, HelmChart: "postgresql", HelmRepo: "https://charts.bitnami.com/bitnami", ConfigSchema: []store.ConfigField{replicasField, diskField, backupField, activeHotStandbyField, primaryRegionField, replicaRegionField}, CreatedAt: now, UpdatedAt: now},
		{Slug: "redis", Name: "Redis", Tagline: "In-memory key-value cache", Description: "Managed Redis backing cache. Provisioned automatically when required by an app dependency.", Category: "database", Icon: "R", IconBg: "#DC382D", System: true, Kind: "service", Shareable: true, Free: true, RamMB: 128, CpuMilli: 100, DiskGB: 1, HelmChart: "redis", HelmRepo: "https://charts.bitnami.com/bitnami", ConfigSchema: []store.ConfigField{{Key: "replicas", Label: "Replicas", Type: "int", Default: 1, Min: intPtr(1), Max: intPtr(3), Description: "Number of Redis instances.", Advanced: false}, {Key: "persistence", Label: "Persistence", Type: "bool", Default: true, Description: "Persist data to disk (disable for pure cache).", Advanced: true}}, CreatedAt: now, UpdatedAt: now},
	}

	created, promoted := 0, 0
	for i := range systemApps {
		existing, err := h.Store.GetApp(ctx, systemApps[i].Slug)
		if err != nil {
			slog.Error("seed: failed to check system app", "slug", systemApps[i].Slug, "error", err)
			continue
		}
		if existing == nil {
			if err := h.Store.CreateApp(ctx, &systemApps[i]); err != nil {
				slog.Error("seed: failed to create system app", "slug", systemApps[i].Slug, "error", err)
				continue
			}
			created++
			continue
		}
		changed := false
		if !existing.System {
			existing.System = true
			existing.Category = "database"
			changed = true
		}
		if existing.Kind != "service" {
			existing.Kind = "service"
			changed = true
		}
		if !existing.Shareable {
			existing.Shareable = true
			changed = true
		}
		if len(existing.ConfigSchema) == 0 && len(systemApps[i].ConfigSchema) > 0 {
			existing.ConfigSchema = systemApps[i].ConfigSchema
			changed = true
		}
		if changed {
			if err := h.Store.UpdateApp(ctx, existing.ID, existing); err != nil {
				slog.Error("seed: failed to promote app to system", "slug", existing.Slug, "error", err)
				continue
			}
			promoted++
		}
	}
	if created > 0 {
		slog.Info("seed: created system apps", "count", created)
	}
	if promoted > 0 {
		slog.Info("seed: promoted apps to system", "count", promoted)
	}
}

// migrateAppDependencies fills in the Dependencies field on existing catalog
// apps where we know the required backing service. Idempotent — re-applies on
// every startup and only updates when the current Dependencies differ.
func (h *Handler) migrateAppDependencies(ctx context.Context) {
	knownDeps := map[string][]string{
		"wordpress":    {"mysql"},
		"ghost":        {"mysql"},
		"invoiceshelf": {"mysql"},
		"bookstack":    {"mysql"},
		"umami":        {"postgres"},
		"cal-com":      {"postgres"},
		"nextcloud":    {"postgres"},
		"gitea":        {"postgres"},
		"nocodb":       {"postgres"},
		"listmonk":     {"postgres"},
		"formbricks":   {"postgres"},
		"chatwoot":     {"postgres", "redis"},
	}

	updated := 0
	for slug, deps := range knownDeps {
		app, err := h.Store.GetApp(ctx, slug)
		if err != nil || app == nil {
			continue
		}
		if sameStrings(app.Dependencies, deps) {
			continue
		}
		app.Dependencies = deps
		if err := h.Store.UpdateApp(ctx, app.ID, app); err != nil {
			slog.Error("seed: failed to update app dependencies", "slug", slug, "error", err)
			continue
		}
		updated++
	}
	if updated > 0 {
		slog.Info("seed: app dependencies migration complete", "updated", updated)
	}
}

// migrateAppDeployable marks catalog apps as deployable (real provisioning
// template + verified end-to-end install) or not. Issue #102. Before this
// flag, unknown slugs silently deployed an nginx:1-alpine placeholder that
// the UI reported as "installed OK" — a correctness bug worse than an
// outright failure because it fabricated working installs. InstallApp in the
// tenant service now refuses to queue day-2 work for non-deployable apps.
//
// The list below must agree with KnownApps in
// services/provisioning/gitops/apps.go and with what the dod harness has
// actually driven green. Apps listed in KnownApps but known to crashloop
// (chatwoot → needs Redis, issue #100; listmonk → config.toml bug,
// issue #101; rocket-chat → needs MongoDB backing service) are marked
// NOT deployable until their fixes ship.
//
// Issue #941 (2026-05-05): openclaw + stalwart-mail were missing here even
// though both blueprints (bp-openclaw, bp-stalwart-sovereign) ship with
// visibility=listed in products/catalyst/bootstrap/api/internal/catalog/
// blueprints.json. The result on a fresh Sovereign was the marketplace UI
// drawing a "COMING SOON" overlay on every "AI" + "Communication" card —
// alice signup gates 4 (LLM) and 5 (mail) blocked before alice could click
// Install. Both are Organization-tenant-pipeline-installable per the per-tenant
// overlay templates emitted by org_tenant_gitops.go (bp-openclaw +
// bp-stalwart-tenant HelmRelease emit), so they MUST appear as Available
// to install. The marketplace shows the customer-app slug (`stalwart-mail`,
// matching seedApps line 48) — bp-stalwart-sovereign is the Sovereign-side
// realisation; bp-stalwart-tenant is the per-tenant one driven by the
// Organization-tenant orchestrator.
// DeployableAppSlugs returns the canonical map of catalog app slugs the
// Organization provisioning service / Organization-tenant orchestrator can install end to
// end. Apps NOT in this map are flagged with Deployable=false on the
// catalog rows so the marketplace UI overlays them with "COMING SOON"
// per issue #102. The map is exported as a function so unit tests can
// assert membership without invoking a Mongo store.
//
// Issue #941 (2026-05-05): added `openclaw` + `stalwart-mail` after
// C5-final hit "27 apps COMING SOON" on otech113 — both blueprints
// (bp-openclaw, bp-stalwart-{sovereign,tenant}) ship with visibility=
// listed in the embedded blueprints.json AND have working Organization-tenant
// overlay templates in org_tenant_gitops.go, but the catalog handler
// silently filtered them out because they were missing here.
func DeployableAppSlugs() map[string]bool {
	return map[string]bool{
		"wordpress":    true,
		"ghost":        true,
		"nextcloud":    true,
		"bookstack":    true,
		"uptime-kuma":  true,
		"gitea":        true,
		"vaultwarden":  true,
		"umami":        true,
		"nocodb":       true,
		"cal-com":      true,
		"invoiceshelf": true,
		"formbricks":   true,
		"listmonk":     true, // fixed in #101 — DBEnvStyle:"listmonk" + InitCommand
		// openclaw (#4272) + stalwart-mail (#4307) — NOW DEPLOYABLE. They were
		// flagged non-deployable since #941 because the generic provisioning
		// generator only rendered a single Deployment (Image + Port) and both
		// apps need HelmRelease-shaped overlays (controller + per-user pods +
		// HTTPRoute for openclaw; StatefulSet + IMAP/SMTP/web Services + OIDC
		// setup Job for stalwart-mail). Live failure 2026-05-06 on tenant
		// "test11": `Deployment.apps "openclaw" is invalid: containers[0].image
		// Required value`. The fix (core/services/provisioning/gitops/
		// helmrelease_apps.go) emits the upstream bp-openclaw /
		// bp-stalwart-tenant HelmReleases as HOST files from the generic
		// generator — mirroring the BSS-door orgTenantBPOpenClaw /
		// orgTenantBPStalwart overlays — so a funnel cart Org (#4364, which only
		// dispatches DEPLOYABLE apps) now renders their HelmReleases into the
		// per-Org gitops vcluster/apps tree. Flagging them deployable here is
		// what re-opens the marketplace cards AND admits them through the funnel
		// cart filter.
		"openclaw":      true, // #4272 — bp-openclaw HelmRelease overlay
		"stalwart-mail": true, // #4307 — bp-stalwart-tenant HelmRelease overlay
		// Backing services are always deployable — they come bundled with
		// whichever business app needs them. Marking them true so the
		// catalog UI doesn't draw a 'Coming soon' overlay on them. #112.
		"postgres": true,
		"mysql":    true,
		"redis":    true,
		// `sandbox` was here (#1615 + Wave 4) and is REMOVED — see
		// RetiredAppSlugs. Deployable=true is what stopped the storefront
		// drawing a "Coming soon" overlay on the card; leaving it set kept a
		// retired product fully purchasable. migrateAppDeployable converges
		// existing rows onto this map, so dropping the key actively flips
		// Deployable=false on Sovereigns that already carry the row.
	}
}

// RetiredAppSlugs maps a WITHDRAWN product's catalog slug to why it was
// withdrawn. A slug listed here must not be purchasable on the marketplace
// storefront: it is not seeded into the catalog, it is not deployable, and the
// publish migration actively unpublishes it (#5920).
//
// WHY THIS EXISTS SEPARATELY FROM THE BLUEPRINT'S `visibility`. There are two
// different catalogs, and only one of them is what a paying customer sees:
//
//   - platform/<x>/blueprint.yaml -> the generated catalog
//     (products/catalyst/bootstrap/{api/internal/catalog/blueprints.json,
//     ui/src/shared/constants/catalog.generated.ts}). That is the
//     SOVEREIGN-ADMIN console's catalog. Flipping `visibility: listed` ->
//     `unlisted` there delists the Blueprint from the admin surface.
//
//   - THIS service's App rows -> GET /catalog/apps?published=true, which is
//     what core/marketplace (the customer storefront) renders as the /apps
//     grid with the "Add to stack" button.
//
// Sandbox was retired by founder decision on 2026-06-30 and superseded by
// products/agenity + products/openova-mcp. Delisting its Blueprint does NOT
// remove it from the storefront, because the storefront never reads the
// Blueprint catalog. It was still on sale on hw292 six weeks later: a FREE
// card in the grid, counted in "16 available", with a live detail page at
// /app?slug=sandbox — and a source grep of core/marketplace/src returns only
// two incidental comments, so the sweep that should have caught it came back
// clean. The row is produced HERE.
//
// Retiring rather than deleting the Blueprint remains correct: bootstrap-kit
// slot 19a still auto-installs the sandbox controller and existing Sandbox CRs
// must keep resolving. What ends is the product being offered for sale.
func RetiredAppSlugs() map[string]string {
	return map[string]string{
		"sandbox": "retired 2026-06-30 by founder decision; superseded by " +
			"products/agenity (the per-Org Agenity workspace) and products/openova-mcp",
	}
}

// PublishedForSlug is the Published value the catalog must converge on for a
// slug. Retired products converge on false; everything else keeps the #710
// opt-OUT default of true, so a newly added app is visible without an operator
// having to opt in.
func PublishedForSlug(slug string) bool {
	_, retired := RetiredAppSlugs()[slug]
	return !retired
}

// appPublishAction is migrateAppPublished's per-row decision, split out so it
// can be tested without a Mongo store. Returns the value the row must converge
// on and whether a write is needed.
//
// The pre-#5920 loop was one-way — `if a.Published { continue }` — so it could
// only ever turn an app ON. That is why delisting a retired product could not
// take it off sale on a Sovereign that already had the row: the only path to
// published=false was an operator toggling it by hand, per Sovereign.
func appPublishAction(slug string, current bool) (want bool, change bool) {
	want = PublishedForSlug(slug)
	return want, current != want
}

func (h *Handler) migrateAppDeployable(ctx context.Context) {
	deployable := DeployableAppSlugs()
	apps, err := h.Store.ListApps(ctx)
	if err != nil {
		slog.Error("seed: list apps for deployable migration", "error", err)
		return
	}
	updated := 0
	for i := range apps {
		a := &apps[i]
		want := deployable[a.Slug]
		if a.Deployable == want {
			continue
		}
		a.Deployable = want
		if err := h.Store.UpdateApp(ctx, a.ID, a); err != nil {
			slog.Error("seed: failed to mark app deployable",
				"slug", a.Slug, "deployable", want, "error", err)
			continue
		}
		updated++
	}
	if updated > 0 {
		slog.Info("seed: app deployable flag migration complete", "updated", updated)
	}
}

// migrateAppPublished defaults Published=true on every existing app on
// the day Catalyst 1.3.x ships (#710 wave 2). Operators opt OUT of
// marketplace visibility per app, not IN — this is how a SaaS team
// curates a real storefront and prevents an empty marketplace from
// rendering on the day the flag lands.
//
// System apps stay invisible to marketplace via ListPublishedApps's
// `system: false` predicate regardless of this flag, so flipping
// Published=true on mysql/postgres/redis is harmless. Apps with
// Deployable=false are gated by the storefront filter too.
//
// One-shot: only writes to rows where Published is the zero-value AND
// updated_at has never seen a published-aware write. Idempotent on
// re-run because the second pass sees Published already true and skips.
func (h *Handler) migrateAppPublished(ctx context.Context) {
	apps, err := h.Store.ListApps(ctx)
	if err != nil {
		slog.Error("seed: list apps for published migration", "error", err)
		return
	}
	updated := 0
	for i := range apps {
		a := &apps[i]
		// Converge on the wanted value in BOTH directions. This loop used to
		// only ever flip false->true, which meant a retired product already
		// sitting in an existing Sovereign's catalog could never be taken off
		// sale by a release — dropping it from the seed only helps a Sovereign
		// provisioned after the change (#5920).
		want, change := appPublishAction(a.Slug, a.Published)
		if !change {
			continue
		}
		if err := h.Store.SetAppPublished(ctx, a.Slug, want); err != nil {
			slog.Error("seed: failed to set app published",
				"slug", a.Slug, "published", want, "error", err)
			continue
		}
		updated++
	}
	if updated > 0 {
		slog.Info("seed: app published flag migration complete", "updated", updated)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dedupBySlug removes duplicate plans and addons (keeps the first occurrence of each slug).
func (h *Handler) dedupBySlug(ctx context.Context) {
	// Dedup plans
	plans, _ := h.Store.ListPlans(ctx)
	seenPlan := make(map[string]bool)
	removed := 0
	for _, p := range plans {
		if seenPlan[p.Slug] {
			_ = h.Store.DeletePlan(ctx, p.ID)
			removed++
		} else {
			seenPlan[p.Slug] = true
		}
	}
	if removed > 0 {
		slog.Info("seed: removed duplicate plans", "count", removed)
	}

	// Dedup addons
	addons, _ := h.Store.ListAddOns(ctx)
	seenAddon := make(map[string]bool)
	removed = 0
	for _, a := range addons {
		if seenAddon[a.Slug] {
			_ = h.Store.DeleteAddOn(ctx, a.ID)
			removed++
		} else {
			seenAddon[a.Slug] = true
		}
	}
	if removed > 0 {
		slog.Info("seed: removed duplicate addons", "count", removed)
	}
}

func intPtr(i int) *int { return &i }

// seedAppRows returns the catalog rows a fresh Sovereign is seeded with.
// Extracted from seedAllData so retired_products_test.go can assert the
// retirement invariants against the ACTUAL seeded rows rather than against a
// copy that could drift away from them (#5920).
func seedAppRows(now time.Time) []store.App {
	return []store.App{
		// D31: `ha` + `cnpg-pair` tags surface this app in the marketplace
		// HA filter — bp-wordpress-tenant chart 0.3.0 supports
		// `pg.activeHotStandby.enabled=true` to render a primary+replica
		// CNPG Cluster pair across two regions with WAL streaming over
		// Cilium ClusterMesh (mirrors the bp-cnpg-pair pattern).
		{Slug: "wordpress", Name: "WordPress", Tagline: "The world's most popular content management system", Description: "Build anything from a simple blog to a full e-commerce site with WooCommerce. Thousands of plugins and themes, WYSIWYG editor, and a massive ecosystem.", Category: "cms", Tags: []string{"blog", "ecommerce", "website", "woocommerce", "ha", "cnpg-pair"}, Icon: "W", IconBg: "#21759B", MinimumSize: "s", RecommendedSize: "m", Website: "https://wordpress.org", License: "GPLv2", Featured: true, Popular: true, Free: true, Features: []string{"Full content management with WYSIWYG editor", "Thousands of plugins and themes", "WooCommerce for e-commerce", "Multi-user with role-based access", "REST API for headless usage", "SEO tools and analytics integration"}, RelatedApps: []string{"stalwart-mail", "nextcloud", "umami"}, RamMB: 256, CpuMilli: 250, DiskGB: 5, HelmChart: "wordpress", HelmRepo: "https://charts.bitnami.com/bitnami", CreatedAt: now, UpdatedAt: now},
		{Slug: "ghost", Name: "Ghost", Tagline: "Modern publishing with built-in memberships and newsletters", Description: "Independent technology for modern publishing. Built-in membership and subscription management, native email newsletters, and a clean writing experience.", Category: "cms", Tags: []string{"blog", "publishing", "newsletter", "membership"}, Icon: "G", IconBg: "#15171A", MinimumSize: "xs", RecommendedSize: "s", Website: "https://ghost.org", License: "MIT", Featured: true, Popular: true, Free: true, Features: []string{"Clean, distraction-free writing editor", "Built-in membership and subscription system", "Native email newsletters", "Theme marketplace", "SEO and social sharing built in", "Content API for headless usage"}, RelatedApps: []string{"stalwart-mail", "umami", "listmonk"}, RamMB: 256, CpuMilli: 250, DiskGB: 5, HelmChart: "ghost", HelmRepo: "https://charts.bitnami.com/bitnami", CreatedAt: now, UpdatedAt: now},
		{Slug: "stalwart-mail", Name: "Stalwart Mail", Tagline: "All-in-one mail server with IMAP, JMAP, SMTP, CalDAV, and CardDAV", Description: "Modern, high-performance mail server written in Rust. Supports every protocol you need: IMAP, JMAP, SMTP, CalDAV, CardDAV, and WebDAV. Built-in spam filter and web admin.", Category: "email", Tags: []string{"email", "smtp", "imap", "calendar", "contacts"}, Icon: "\u2709", IconBg: "#4F46E5", MinimumSize: "m", RecommendedSize: "m", Website: "https://stalw.art", License: "AGPL-3.0", Featured: true, Popular: true, Free: true, Features: []string{"All protocols: IMAP, JMAP, SMTP, CalDAV, CardDAV, WebDAV", "Built-in spam filter (sieve scripting)", "Web-based admin panel", "Single Rust binary, low resource usage", "Full-text search", "DKIM, SPF, DMARC support"}, RelatedApps: []string{"wordpress", "rocket-chat", "listmonk"}, RamMB: 400, CpuMilli: 250, DiskGB: 10, HelmChart: "stalwart", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "rocket-chat", Name: "Rocket.Chat", Tagline: "Open-source team chat with omnichannel customer support", Description: "The most feature-rich open-source chat platform. Team messaging, video/audio calls, omnichannel customer support (LiveChat), and a marketplace of integrations.", Category: "communication", Tags: []string{"chat", "messaging", "video", "livechat", "support"}, Icon: "\U0001F680", IconBg: "#F5455C", MinimumSize: "s", RecommendedSize: "m", Website: "https://rocket.chat", License: "MIT", Featured: false, Popular: true, Free: true, Features: []string{"Team channels, DMs, and threads", "Video and audio conferencing", "Omnichannel LiveChat widget for customer support", "Integration marketplace", "Mobile apps for iOS and Android", "End-to-end encryption"}, RelatedApps: []string{"stalwart-mail", "jitsi-meet", "cal-com"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "rocketchat", HelmRepo: "https://rocketchat.github.io/helm-charts", CreatedAt: now, UpdatedAt: now},
		{Slug: "nextcloud", Name: "Nextcloud", Tagline: "Self-hosted file sync, collaboration, and office suite", Description: "A complete productivity platform: file sync, real-time document editing, calendar, contacts, video calls, and hundreds of apps. Replace Google Workspace and Dropbox.", Category: "productivity", Tags: []string{"files", "office", "calendar", "collaboration"}, Icon: "\u2601", IconBg: "#0082C9", MinimumSize: "s", RecommendedSize: "m", Website: "https://nextcloud.com", License: "AGPL-3.0", Featured: true, Popular: true, Free: true, Features: []string{"File sync across all devices", "Collaborative document editing (OnlyOffice/Collabora)", "Calendar and contacts (CalDAV/CardDAV)", "Video calls and chat (Talk)", "Hundreds of apps in the app store", "Desktop and mobile sync clients"}, RelatedApps: []string{"stalwart-mail", "wordpress", "vaultwarden"}, RamMB: 512, CpuMilli: 500, DiskGB: 20, HelmChart: "nextcloud", HelmRepo: "https://nextcloud.github.io/helm/", CreatedAt: now, UpdatedAt: now},
		{Slug: "twenty", Name: "Twenty", Tagline: "Modern open-source CRM built for the way you work", Description: "A beautiful, modern alternative to Salesforce. Custom objects, Kanban views, email integration, and a clean TypeScript/React codebase. Built by the community.", Category: "crm", Tags: []string{"crm", "sales", "contacts", "pipeline"}, Icon: "XX", IconBg: "#141414", MinimumSize: "s", RecommendedSize: "m", Website: "https://twenty.com", License: "AGPL-3.0", Featured: true, Popular: true, Free: true, Features: []string{"Custom objects and fields", "Kanban and table views", "Email integration", "Activity timeline", "API-first architecture", "Import/export from other CRMs"}, RelatedApps: []string{"stalwart-mail", "cal-com", "invoiceshelf"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "twenty", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "umami", Name: "Umami", Tagline: "Privacy-first web analytics without cookies", Description: "A simple, fast, privacy-focused alternative to Google Analytics. No cookies, fully GDPR compliant. Track website visitors, page views, and events.", Category: "analytics", Tags: []string{"analytics", "privacy", "gdpr", "tracking"}, Icon: "U", IconBg: "#000000", MinimumSize: "xs", RecommendedSize: "s", Website: "https://umami.is", License: "MIT", Featured: false, Popular: true, Free: true, Features: []string{"No cookies required, GDPR compliant", "Real-time visitor dashboard", "Custom event tracking", "Multiple website support", "API for data export", "Lightweight tracking script (<1KB)"}, RelatedApps: []string{"wordpress", "ghost", "medusa"}, RamMB: 256, CpuMilli: 250, DiskGB: 5, HelmChart: "umami", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "medusa", Name: "Medusa", Tagline: "Open-source headless commerce platform", Description: "The most flexible open-source e-commerce platform. Headless architecture, multi-region, multi-currency, and a rich plugin ecosystem.", Category: "ecommerce", Tags: []string{"ecommerce", "shop", "payments", "headless"}, Icon: "M", IconBg: "#7C3AED", MinimumSize: "s", RecommendedSize: "m", Website: "https://medusajs.com", License: "MIT", Featured: false, Popular: false, Free: true, Features: []string{"Headless API-first architecture", "Multi-region and multi-currency", "Payment provider plugins (Stripe, PayPal)", "Inventory and order management", "Admin dashboard", "Custom storefront support"}, RelatedApps: []string{"wordpress", "umami", "stalwart-mail"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "medusa", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "plane", Name: "Plane", Tagline: "Open-source project management for modern teams", Description: "A beautiful alternative to Jira, Linear, and Monday. Issues, sprints, cycles, modules, and docs. Modern UI with powerful project tracking.", Category: "project-management", Tags: []string{"project", "issues", "kanban", "sprints", "agile"}, Icon: "P", IconBg: "#3F76FF", MinimumSize: "s", RecommendedSize: "m", Website: "https://plane.so", License: "AGPL-3.0", Featured: true, Popular: true, Free: true, Features: []string{"Issues with multiple views (Board, List, Gantt)", "Sprints and cycles", "Modules for project grouping", "Built-in docs/pages", "GitHub and Slack integration", "Custom workflows and labels"}, RelatedApps: []string{"gitea", "rocket-chat", "cal-com"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "plane", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "erpnext", Name: "ERPNext", Tagline: "Full-featured open-source ERP for any business", Description: "100% open-source enterprise resource planning. Accounting, HR, manufacturing, CRM, inventory, and project management. No per-user licensing fees.", Category: "erp", Tags: []string{"erp", "accounting", "hr", "inventory", "manufacturing"}, Icon: "E", IconBg: "#0089FF", MinimumSize: "m", RecommendedSize: "l", Website: "https://erpnext.com", License: "GPL-3.0", Featured: true, Popular: false, Free: true, Features: []string{"Full double-entry accounting", "HR and payroll management", "Inventory and warehouse management", "Manufacturing and BOM", "CRM and sales pipeline", "Project management and timesheets"}, RelatedApps: []string{"twenty", "invoiceshelf", "nocodb"}, RamMB: 1024, CpuMilli: 500, DiskGB: 10, HelmChart: "erpnext", HelmRepo: "https://helm.erpnext.com", CreatedAt: now, UpdatedAt: now},
		{Slug: "invoiceshelf", Name: "InvoiceShelf", Tagline: "Simple, beautiful invoicing for freelancers and small businesses", Description: "Create professional invoices, track expenses, and manage payments. True open-source invoicing solution with a clean, modern interface.", Category: "invoicing", Tags: []string{"invoicing", "billing", "expenses", "payments"}, Icon: "$", IconBg: "#5851DB", MinimumSize: "xs", RecommendedSize: "s", Website: "https://invoiceshelf.com", License: "AGPL-3.0", Featured: false, Popular: false, Free: true, Features: []string{"Professional invoice generation", "Recurring invoices", "Expense tracking", "Payment tracking", "Tax management", "Multi-currency support"}, RelatedApps: []string{"erpnext", "twenty", "nocodb"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "invoiceshelf", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "listmonk", Name: "Listmonk", Tagline: "High-performance newsletter and mailing list manager", Description: "Self-hosted newsletter and mailing list manager. Single Go binary, modern dashboard, powerful templating. Replace Mailchimp at a fraction of the cost.", Category: "marketing", Tags: []string{"newsletter", "email", "marketing", "mailing-list"}, Icon: "L", IconBg: "#7C3AED", MinimumSize: "xs", RecommendedSize: "s", Website: "https://listmonk.app", License: "AGPL-3.0", Featured: false, Popular: false, Free: true, Features: []string{"High-performance bulk email sending", "Advanced email templating", "Subscriber management and segmentation", "Campaign analytics and tracking", "Media management", "REST API for automation"}, RelatedApps: []string{"stalwart-mail", "wordpress", "ghost"}, RamMB: 256, CpuMilli: 200, DiskGB: 2, HelmChart: "listmonk", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "cal-com", Name: "Cal.com", Tagline: "Scheduling infrastructure for everyone", Description: "The open-source Calendly alternative. Booking pages, round-robin scheduling, team availability, and calendar integrations.", Category: "scheduling", Tags: []string{"scheduling", "booking", "calendar", "appointments"}, Icon: "\U0001F4C5", IconBg: "#292929", MinimumSize: "s", RecommendedSize: "s", Website: "https://cal.com", License: "AGPLv3", Featured: false, Popular: true, Free: true, Features: []string{"Individual and team booking pages", "Round-robin and collective scheduling", "Google Calendar and Outlook integration", "Custom availability rules", "Webhook and Zapier integration", "Embeddable booking widget"}, RelatedApps: []string{"stalwart-mail", "rocket-chat", "jitsi-meet"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "calcom", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "gitea", Name: "Gitea", Tagline: "Lightweight self-hosted Git with CI/CD and packages", Description: "A painless, self-hosted Git service. Code hosting, pull requests, issues, CI/CD (Actions), and package registry. Written in Go, extremely lightweight.", Category: "devtools", Tags: []string{"git", "ci-cd", "code", "devops"}, Icon: "\U0001F375", IconBg: "#609926", MinimumSize: "xs", RecommendedSize: "s", Website: "https://gitea.com", License: "MIT", Featured: false, Popular: false, Free: true, Features: []string{"Git repository hosting", "Pull requests with code review", "Issue tracking", "CI/CD via Gitea Actions (GitHub Actions compatible)", "Package registry (npm, Docker, Maven, etc.)", "Organization and team management"}, RelatedApps: []string{"plane", "rocket-chat", "uptime-kuma"}, RamMB: 256, CpuMilli: 250, DiskGB: 10, HelmChart: "gitea", HelmRepo: "https://dl.gitea.io/charts/", CreatedAt: now, UpdatedAt: now},
		{Slug: "uptime-kuma", Name: "Uptime Kuma", Tagline: "Beautiful self-hosted monitoring with status pages", Description: "A fancy self-hosted monitoring tool. HTTP, TCP, DNS, and ping monitors with 90+ notification integrations and beautiful public status pages.", Category: "monitoring", Tags: []string{"monitoring", "uptime", "status-page", "alerts"}, Icon: "\U0001F4CA", IconBg: "#5CDD8B", MinimumSize: "xs", RecommendedSize: "xs", Website: "https://uptime.kuma.pet", License: "MIT", Featured: false, Popular: true, Free: true, Features: []string{"HTTP, TCP, DNS, ping, and gRPC monitors", "Beautiful public status pages", "90+ notification integrations (Slack, Discord, Telegram)", "Multi-language dashboard", "Certificate expiry monitoring", "Maintenance windows"}, RelatedApps: []string{"gitea", "rocket-chat", "wordpress"}, RamMB: 128, CpuMilli: 100, DiskGB: 1, HelmChart: "uptime-kuma", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "librechat", Name: "LibreChat", Tagline: "Multi-provider AI chat with agents and code interpreter", Description: "Enhanced ChatGPT clone supporting multiple AI providers (OpenAI, Anthropic, Google, local models). Multi-user auth, agents, MCP, code interpreter, and DALL-E.", Category: "ai", Tags: []string{"ai", "chat", "llm", "agents", "mcp"}, Icon: "\U0001F916", IconBg: "#6366F1", MinimumSize: "s", RecommendedSize: "m", Website: "https://librechat.ai", License: "MIT", Featured: true, Popular: true, Free: true, Features: []string{"Multiple AI provider support (OpenAI, Anthropic, Google)", "Multi-user authentication", "AI agents with tool use", "Model Context Protocol (MCP) support", "Code interpreter", "File upload and DALL-E image generation"}, RelatedApps: []string{"dify", "openclaw"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "librechat", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "documenso", Name: "Documenso", Tagline: "Open-source document signing, the DocuSign alternative", Description: "Create, send, and sign documents digitally. Beautiful interface, API-first design, and full audit trail. Replace DocuSign with your own infrastructure.", Category: "documents", Tags: []string{"signing", "documents", "contracts", "legal"}, Icon: "\u270D", IconBg: "#A2E771", MinimumSize: "xs", RecommendedSize: "s", Website: "https://documenso.com", License: "AGPL-3.0", Featured: false, Popular: false, Free: true, Features: []string{"Digital document signing", "Document templates", "Multi-signer workflows", "Complete audit trail", "REST API for integration", "Email notifications"}, RelatedApps: []string{"stalwart-mail", "twenty", "bookstack"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "documenso", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "vaultwarden", Name: "Vaultwarden", Tagline: "Lightweight Bitwarden-compatible password manager", Description: "An unofficial Bitwarden server written in Rust. Full compatibility with all Bitwarden clients (browser, desktop, mobile). Team password sharing, 2FA, and secure notes.", Category: "security", Tags: []string{"passwords", "security", "2fa", "secrets"}, Icon: "\U0001F512", IconBg: "#175DDC", MinimumSize: "xs", RecommendedSize: "xs", Website: "https://github.com/dani-garcia/vaultwarden", License: "AGPL-3.0", Featured: false, Popular: true, Free: true, Features: []string{"Full Bitwarden client compatibility", "Team and organization password sharing", "Two-factor authentication (TOTP, WebAuthn)", "Secure notes and file attachments", "Password generator", "Emergency access"}, RelatedApps: []string{"nextcloud", "stalwart-mail", "rocket-chat"}, RamMB: 64, CpuMilli: 50, DiskGB: 1, HelmChart: "vaultwarden", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "bookstack", Name: "BookStack", Tagline: "Simple, self-hosted wiki with a book/chapter/page structure", Description: "A platform for organizing and storing information. Content is structured as Books, Chapters, and Pages for intuitive navigation. WYSIWYG and Markdown editors.", Category: "knowledge-base", Tags: []string{"wiki", "docs", "knowledge", "documentation"}, Icon: "\U0001F4DA", IconBg: "#0288D1", MinimumSize: "xs", RecommendedSize: "s", Website: "https://bookstackapp.com", License: "MIT", Featured: false, Popular: false, Free: true, Features: []string{"Book/Chapter/Page content structure", "WYSIWYG and Markdown editors", "Full-text search", "Role-based access control", "Diagram drawing (diagrams.net)", "API for content management"}, RelatedApps: []string{"nextcloud", "rocket-chat", "plane"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "bookstack", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "formbricks", Name: "Formbricks", Tagline: "Open-source survey and experience management platform", Description: "The open-source alternative to Qualtrics and Typeform. In-app surveys, website popups, link surveys, and advanced targeting.", Category: "forms", Tags: []string{"forms", "surveys", "feedback", "nps"}, Icon: "\U0001F4DD", IconBg: "#00C4B8", MinimumSize: "xs", RecommendedSize: "s", Website: "https://formbricks.com", License: "AGPL-3.0", Featured: false, Popular: false, Free: true, Features: []string{"In-app surveys and website popups", "Link surveys for external distribution", "Advanced targeting and segmentation", "Pre-built survey templates (NPS, CSAT, CES)", "Response analytics and dashboards", "Webhook and Zapier integration"}, RelatedApps: []string{"wordpress", "umami", "twenty"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "formbricks", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "dify", Name: "Dify", Tagline: "Build AI agents and workflows with a visual editor", Description: "Production-ready LLMOps platform. Visual workflow builder, RAG pipelines, prompt management, multi-model support, and observability.", Category: "ai", Tags: []string{"ai", "agents", "rag", "llm", "workflows"}, Icon: "\U0001F9E0", IconBg: "#1570EF", MinimumSize: "m", RecommendedSize: "l", Website: "https://dify.ai", License: "Custom (Apache-based)", Featured: true, Popular: true, Free: true, Features: []string{"Visual AI workflow builder", "RAG pipeline with document ingestion", "Multi-model support (OpenAI, Anthropic, Ollama)", "Prompt management and versioning", "API publishing for built workflows", "Observability and usage analytics"}, RelatedApps: []string{"librechat", "openclaw"}, RamMB: 1024, CpuMilli: 1000, DiskGB: 10, HelmChart: "dify", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		// bp-agenity — UAT rows 219/G9. The Blueprint exists and is
		// `visibility: listed` in the platform catalog, but the STOREFRONT is a
		// separate hand-seeded list, so a customer could never select it: there
		// was no path, customer-chosen or platform-injected, by which a User
		// obtained the Agenity workspace the row asserts. Sold here so the
		// funnel can offer it; the per-Org emitter that renders it landed in
		// #6353.
		{Slug: "agenity", Name: "Agenity", Tagline: "Your own AI agent workspace, per Organization", Description: "A per-Organization agentic workspace: a solo agent with persistent memory that can read your Organization's state and create Applications for you through the OpenOva MCP tools.", Category: "ai", Tags: []string{"ai", "agent", "workspace", "mcp", "automation"}, Icon: "\U0001F916", IconBg: "#6E56CF", MinimumSize: "s", RecommendedSize: "m", Website: "https://openova.io", License: "Apache-2.0", Featured: true, Popular: false, Free: false, Features: []string{"Solo agent with persistent memory", "Reads your Organization's live state", "Creates Applications via the OpenOva MCP tools", "Per-Organization isolation", "Signed-in through your Organization console"}, RelatedApps: []string{"openclaw", "librechat"}, RamMB: 1024, CpuMilli: 500, DiskGB: 10, HelmChart: "agenity", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "openclaw", Name: "OpenClaw", Tagline: "Personal AI assistant across all your messaging apps", Description: "Connect your AI assistant to WhatsApp, Telegram, Slack, Discord, Teams, Signal, and 20+ channels. 5,400+ skills, persistent memory, and voice support.", Category: "ai", Tags: []string{"ai", "assistant", "chatbot", "whatsapp", "telegram", "slack"}, Icon: "\U0001F980", IconBg: "#FF6B35", MinimumSize: "xs", RecommendedSize: "s", Website: "https://openclaw.ai", License: "MIT", Featured: true, Popular: true, Free: true, Features: []string{"24+ messaging platform integrations", "5,400+ skills in the ClawHub registry", "Persistent memory and knowledge base", "Multi-agent support", "Voice chat on mobile", "Local model support via Ollama"}, RelatedApps: []string{"dify", "librechat"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "openclaw", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "chatwoot", Name: "Chatwoot", Tagline: "Omnichannel customer support platform", Description: "Open-source alternative to Intercom and Zendesk. Live chat widget, email, WhatsApp, Facebook, Telegram, and SMS in one inbox.", Category: "support", Tags: []string{"support", "helpdesk", "livechat", "ticketing"}, Icon: "\U0001F4AC", IconBg: "#1F93FF", MinimumSize: "s", RecommendedSize: "m", Website: "https://chatwoot.com", License: "MIT", Featured: true, Popular: true, Free: true, Features: []string{"Omnichannel inbox (chat, email, WhatsApp, FB, Telegram)", "Embeddable live chat widget", "Shared team inbox with assignments", "Canned responses and automation rules", "Customer satisfaction surveys (CSAT)", "Knowledge base / help center"}, RelatedApps: []string{"stalwart-mail", "rocket-chat", "twenty"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "chatwoot", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "postiz", Name: "Postiz", Tagline: "AI-powered social media scheduler and manager", Description: "Schedule and manage posts across 14+ social media platforms. AI-powered content suggestions, analytics, and team collaboration.", Category: "social-media", Tags: []string{"social", "marketing", "scheduling", "content"}, Icon: "\U0001F4F1", IconBg: "#000000", MinimumSize: "xs", RecommendedSize: "s", Website: "https://postiz.com", License: "AGPLv3", Featured: false, Popular: true, Free: true, Features: []string{"Schedule posts across 14+ platforms", "AI-powered content suggestions", "Visual content calendar", "Team collaboration and approval workflows", "Post performance analytics", "Bulk scheduling and CSV import"}, RelatedApps: []string{"umami", "wordpress", "ghost"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "postiz", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "nocodb", Name: "NocoDB", Tagline: "Open-source Airtable alternative on any database", Description: "Turn any SQL database into a smart spreadsheet. Grid, Kanban, Gallery, and Form views. API generation, automations, and collaboration.", Category: "database", Tags: []string{"database", "spreadsheet", "nocode", "airtable"}, Icon: "\U0001F5C3", IconBg: "#1348FC", MinimumSize: "xs", RecommendedSize: "s", Website: "https://nocodb.com", License: "AGPLv3", Featured: true, Popular: true, Free: true, Features: []string{"Spreadsheet UI on any SQL database", "Grid, Kanban, Gallery, and Form views", "Automatic REST API generation", "Automations and webhooks", "Role-based access control", "Import from Airtable, CSV, Excel"}, RelatedApps: []string{"erpnext", "twenty", "formbricks"}, RamMB: 256, CpuMilli: 250, DiskGB: 5, HelmChart: "nocodb", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "jitsi-meet", Name: "Jitsi Meet", Tagline: "Self-hosted video conferencing, no account required", Description: "Secure, fully-featured video conferencing that runs in your browser. No downloads, no accounts required for participants.", Category: "video-conferencing", Tags: []string{"video", "conferencing", "meetings", "webrtc"}, Icon: "\U0001F4F9", IconBg: "#17A0DB", MinimumSize: "s", RecommendedSize: "m", Website: "https://jitsi.org", License: "Apache-2.0", Featured: false, Popular: true, Free: true, Features: []string{"Browser-based, no download required", "No account needed for participants", "Screen sharing and recording", "Breakout rooms", "End-to-end encryption", "Calendar integration"}, RelatedApps: []string{"rocket-chat", "cal-com", "stalwart-mail"}, RamMB: 1024, CpuMilli: 1000, DiskGB: 5, HelmChart: "jitsi-meet", HelmRepo: "https://jitsi-contrib.github.io/jitsi-helm/", CreatedAt: now, UpdatedAt: now},
		{Slug: "immich", Name: "Immich", Tagline: "Self-hosted Google Photos with ML-powered search", Description: "High-performance photo and video management. Automatic backup from mobile, ML-powered search and face recognition, shared albums, and a beautiful timeline view.", Category: "photo-management", Tags: []string{"photos", "videos", "backup", "gallery", "ml"}, Icon: "\U0001F4F7", IconBg: "#4250AF", MinimumSize: "s", RecommendedSize: "m", Website: "https://immich.app", License: "AGPL-3.0", Featured: false, Popular: true, Free: true, Features: []string{"Automatic backup from iOS and Android", "ML-powered search (CLIP)", "Face recognition and person grouping", "Shared albums and partner sharing", "Timeline and map views", "RAW photo support"}, RelatedApps: []string{"nextcloud", "vaultwarden"}, RamMB: 1024, CpuMilli: 1000, DiskGB: 50, HelmChart: "immich", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
	}
}
