package openova

import (
	"context"
	"strings"
	"testing"

	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The overage mode hand-over (DESIGN.md §22.11): spec.commerce.overageMode,
// growCeiling and spendLimitMonth are read tolerantly and set on the
// Organization's platform Source through SetSourceOverage, only when they
// differ; a block naming no mode drives nothing; a refusal is a WARN.

func dp(s string) *store.Decimal { v := store.Decimal(s); return &v }

func growRepo(t *testing.T) *fakeRepo {
	repo := commerceRepo(t)
	repo.growPackages = map[string]store.PackageLimits{
		repo.planBook.ID + "|m": {PlanSlug: "m", GrowAllowed: true,
			Headline: store.GrowCeiling{VCPU: dp("2"), MemoryGB: dp("4"), DiskGB: dp("50"), BandwidthMbps: dp("100")},
			Ceiling:  store.GrowCeiling{VCPU: dp("8"), MemoryGB: dp("16"), DiskGB: dp("250"), BandwidthMbps: dp("1000")}},
	}
	return repo
}

func TestReadOrgCommerceOverage(t *testing.T) {
	f, err := readOrg(orgUnstructured("acme", withCommerce("plan.m", nil, map[string]any{
		"overageMode": "Grow", "growCeiling": map[string]any{"vcpu": float64(4), "memoryGB": int64(8), "diskGB": "100", "bandwidthMbps": float64(0)}, "spendLimitMonth": " 25.000 ",
	})))
	if err != nil {
		t.Fatal(err)
	}
	c := f.Commerce
	if c.OverageMode != "grow" || c.GrowCeiling == nil || string(*c.GrowCeiling.VCPU) != "4" || string(*c.GrowCeiling.MemoryGB) != "8" ||
		string(*c.GrowCeiling.DiskGB) != "100" || c.GrowCeiling.BandwidthMbps != nil || c.SpendLimitMonth == nil || string(*c.SpendLimitMonth) != "25.000" {
		t.Fatalf("commerce = %+v ceiling %+v", c, c.GrowCeiling)
	}
	// An unknown mode, a ceiling that is not a map, a limit that is not a
	// string: each reads as absent.
	f, _ = readOrg(orgUnstructured("acme", withCommerce("plan.m", nil, map[string]any{"overageMode": "unlimited", "growCeiling": "8", "spendLimitMonth": 25})))
	if f.Commerce.OverageMode != "" || f.Commerce.GrowCeiling != nil || f.Commerce.SpendLimitMonth != nil {
		t.Fatalf("malformed → %+v", f.Commerce)
	}
}

func TestSyncOrganizationSetsTheOverageModeFromTheOrder(t *testing.T) {
	repo := growRepo(t)
	logs := captureLogs(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	grow := func(extra map[string]any) func(map[string]any) {
		return func(spec map[string]any) {
			spec["planSlug"] = "m"
			withCommerce("plan.m", nil, extra)(spec)
		}
	}
	order := map[string]any{"overageMode": "grow", "growCeiling": map[string]any{"vcpu": float64(4)}, "spendLimitMonth": "25.000"}
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", grow(order))); err != nil {
		t.Fatal(err)
	}
	src := orgSourceOf(t, repo, "acme")
	if src.OverageMode != "grow" || string(*src.GrowCeiling.VCPU) != "4" || string(*src.GrowCeiling.MemoryGB) != "16" || string(*src.SpendLimitMonth) != "25.000" || repo.overageWrites != 1 {
		t.Fatalf("source = %+v ceiling %+v writes %d", src, src.GrowCeiling, repo.overageWrites)
	}
	if !strings.Contains(logs.String(), "set to the Organization's overage mode") {
		t.Fatalf("no Info for the set:\n%s", logs.String())
	}
	// A resync of the same order writes nothing.
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", grow(order))); err != nil || repo.overageWrites != 1 {
		t.Fatalf("resync wrote again: %d err %v", repo.overageWrites, err)
	}
	// A block that names no mode drives nothing: the mode set by hand stays.
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", grow(nil))); err != nil || repo.overageWrites != 1 || orgSourceOf(t, repo, "acme").OverageMode != "grow" {
		t.Fatalf("no mode changed the source: writes %d err %v", repo.overageWrites, err)
	}
	// Back to capped clears the ceiling and the limit.
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", grow(map[string]any{"overageMode": "capped"}))); err != nil {
		t.Fatal(err)
	}
	if src = orgSourceOf(t, repo, "acme"); src.OverageMode != "capped" || src.GrowCeiling != nil || src.SpendLimitMonth != nil || repo.overageWrites != 2 {
		t.Fatalf("after capped = %+v writes %d", src, repo.overageWrites)
	}
}

func TestSyncOrganizationRefusedOverageIsLoggedNotFatal(t *testing.T) {
	repo := growRepo(t)
	logs := captureLogs(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	// A ceiling above the package's: refused by the store's own rule.
	org := orgUnstructured("acme", func(spec map[string]any) {
		spec["planSlug"] = "m"
		withCommerce("plan.m", nil, map[string]any{"overageMode": "grow", "growCeiling": map[string]any{"vcpu": float64(9)}})(spec)
	})
	if err := s.SyncOrganization(context.Background(), org); err != nil {
		t.Fatalf("a refused overage failed the sync: %v", err)
	}
	if src := orgSourceOf(t, repo, "acme"); src.OverageMode == "grow" {
		t.Fatalf("refused write applied: %+v", src)
	}
	if out := logs.String(); !strings.Contains(out, "overage mode not set") || !strings.Contains(out, "above the M package's ceiling 8") {
		t.Fatalf("the WARN must carry the store's sentence:\n%s", out)
	}
}
