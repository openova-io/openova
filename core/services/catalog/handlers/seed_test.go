package handlers

import "testing"

// TestExpectedSandboxPlans_Shape locks the Sandbox plan triple shipped
// alongside PR #1633's Sandbox seedApp. Marketplace checkout dies with
// "plan_id not found" the moment any of these slugs drift from
// sandbox-{free,pro,ent}, and the sandbox-orchestrator (#1639) can't
// derive spec.quota when IncludedQuotas lacks concurrent_sessions,
// agents, or storage_gb. Test guards the contract on both ends.
func TestExpectedSandboxPlans_Shape(t *testing.T) {
	plans := expectedSandboxPlans()
	wantSlugs := []string{"sandbox-free", "sandbox-pro", "sandbox-ent"}
	if len(plans) != len(wantSlugs) {
		t.Fatalf("expectedSandboxPlans count = %d, want %d", len(plans), len(wantSlugs))
	}
	bySlug := make(map[string]int, len(plans))
	for i, p := range plans {
		bySlug[p.Slug] = i
	}
	for _, slug := range wantSlugs {
		if _, ok := bySlug[slug]; !ok {
			t.Errorf("expected plan slug %q missing from expectedSandboxPlans", slug)
		}
	}

	// Every Sandbox plan MUST carry ProductSlug="sandbox" so the
	// marketplace plan-picker can filter by app. An empty ProductSlug
	// would surface the row as a generic compute tier next to S/M/L/XL.
	for _, p := range plans {
		if p.ProductSlug != "sandbox" {
			t.Errorf("plan %q ProductSlug = %q, want %q", p.Slug, p.ProductSlug, "sandbox")
		}
	}

	// Every Sandbox plan MUST carry the three quota keys the
	// orchestrator consumes. Missing knobs would silently degrade to
	// orchestrator defaults — paid tiers would inherit free-tier
	// resources, the bug we shipped this PR to fix.
	requiredQuota := []string{"concurrent_sessions", "agents", "storage_gb", "byos"}
	for _, p := range plans {
		if p.IncludedQuotas == nil {
			t.Errorf("plan %q IncludedQuotas is nil", p.Slug)
			continue
		}
		for _, key := range requiredQuota {
			if _, ok := p.IncludedQuotas[key]; !ok {
				t.Errorf("plan %q IncludedQuotas missing %q", p.Slug, key)
			}
		}
		if p.IncludedQuotas["byos"] != "true" {
			t.Errorf("plan %q byos = %q, want %q", p.Slug, p.IncludedQuotas["byos"], "true")
		}
	}

	// Price ladder: Free=0, Pro=9, Ent=49 OMR/mo. Locked in so a
	// future "Sandbox Lite" swap doesn't silently dump the founder's
	// pricing intent.
	priceBySlug := map[string]int{}
	for _, p := range plans {
		priceBySlug[p.Slug] = p.PriceOMR
	}
	wantPrices := map[string]int{"sandbox-free": 0, "sandbox-pro": 9, "sandbox-ent": 49}
	for slug, want := range wantPrices {
		if got := priceBySlug[slug]; got != want {
			t.Errorf("plan %q PriceOMR = %d, want %d", slug, got, want)
		}
	}

	// Quota ladder: Free → 1/1/5, Pro → 3/6/50, Ent → 0(unlimited)/6/500.
	wantQuotas := map[string]map[string]string{
		"sandbox-free": {"concurrent_sessions": "1", "agents": "1", "storage_gb": "5"},
		"sandbox-pro":  {"concurrent_sessions": "3", "agents": "6", "storage_gb": "50"},
		"sandbox-ent":  {"concurrent_sessions": "0", "agents": "6", "storage_gb": "500"},
	}
	for slug, want := range wantQuotas {
		got := plans[bySlug[slug]].IncludedQuotas
		for k, v := range want {
			if got[k] != v {
				t.Errorf("plan %q quota %q = %q, want %q", slug, k, got[k], v)
			}
		}
	}

	// Exactly one plan must carry Popular=true; that's the marketplace
	// plan picker's default selection. Pro is the intended default.
	popular := []string{}
	for _, p := range plans {
		if p.Popular {
			popular = append(popular, p.Slug)
		}
	}
	if len(popular) != 1 || popular[0] != "sandbox-pro" {
		t.Errorf("popular Sandbox plan want [sandbox-pro], got %v", popular)
	}
}

// TestDeployableAppSlugs_OpenclawStalwartDisabled asserts that openclaw and
// stalwart-mail are NOT in the deployable map. They were briefly enabled by
// #941 (catalog flag flipped) but the Organization provisioning generator at
// core/services/provisioning/gitops/apps.go has no AppSpec for either, so
// the rendered Deployment manifests are invalid (missing image + ports).
// Live failure 2026-05-06 on tenant "test11":
//
//	tenant-test11-apps Kustomization rejected:
//	Deployment.apps "openclaw" is invalid: containers[0].image: Required value
//
// Re-enabling these requires per-app HelmRelease overlays beyond the single
// Deployment template the generator currently supports.
func TestDeployableAppSlugs_OpenclawStalwartDisabled(t *testing.T) {
	d := DeployableAppSlugs()
	for _, slug := range []string{"openclaw", "stalwart-mail"} {
		if d[slug] {
			t.Errorf("%q must NOT be deployable until per-app overlay exists in the Organization provisioning generator", slug)
		}
	}
}

// TestDeployableAppSlugs_StableShape locks the map's exported keys so a
// rename (or accidental delete) of a deployable slug fails the test
// instead of silently flipping a marketplace card to COMING SOON.
func TestDeployableAppSlugs_StableShape(t *testing.T) {
	d := DeployableAppSlugs()
	expected := []string{
		"wordpress", "ghost", "nextcloud", "bookstack", "uptime-kuma",
		"gitea", "vaultwarden", "umami", "nocodb", "cal-com",
		"invoiceshelf", "formbricks", "listmonk",
		"postgres", "mysql", "redis",
		// Wave 4 — Sandbox marketplace catalog entry.
		"sandbox",
	}
	if got, want := len(d), len(expected); got != want {
		t.Errorf("deployable map size = %d, want %d", got, want)
	}
	for _, slug := range expected {
		if _, ok := d[slug]; !ok {
			t.Errorf("expected %q in deployable map, missing", slug)
		}
	}
}
