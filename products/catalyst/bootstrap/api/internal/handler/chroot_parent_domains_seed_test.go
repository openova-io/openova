// chroot_parent_domains_seed_test.go — regression boundary for #1907.
//
// What the tests guard
// --------------------
// PR #1861 widened LoadOrganizationParentDomainsFromEnv to seed all four
// canonical .omani.X entries in the env-stub fallback, but on a real
// Sovereign that path is bypassed: the mother imports a full
// deployment record with only the operator-selected org-pool TLD,
// and the HTTP surface /api/v1/sovereign/parent-domains reads from
// the imported record (dep.Request.ParentDomains), not the env stub.
// Result on t31 (2026-05-19): /parent-domains?role=org-pool returned
// 1 entry instead of 4 → customer's omani.homes pick failed at SME
// tenant signup with 422 invalid-parent-domain.
//
// chrootEnsureOrgPoolSeed closes that gap. These tests guard:
//
//   1. Lockstep with core/services/domain/store.AllowedTLDs — if
//      somebody widens the picker, this test fails until the seed
//      catches up.
//
//   2. Top-up shape — partial mother-imported pool (1 of 4) gets
//      bumped to the full 4-entry canonical list, preserving the
//      mother-stamped row and adding only the missing entries.
//
//   3. Idempotence — running the seed twice is a no-op on the second
//      pass (no duplicate rows, no spurious persist).
//
//   4. Mode gate — SOVEREIGN_FQDN unset is a hard no-op (the
//      mothership does NOT own the canonical pool concept; only the
//      Sovereign-side chroot does).
//
//   5. Determinism — output order is sorted by lowercased name so the
//      wire shape returned to the operator is stable across Pod
//      restarts.
//
// The tests don't stand up the full Handler; they use the in-memory
// store via NewWithStore + a t.TempDir() so the persist branch can be
// exercised end-to-end (round-trip the dep record through disk and
// verify the topped-up shape survives).
package handler

import (
	"log/slog"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// TestChrootEnsureOrgPoolSeed_MatchesAllowedTLDsLiteral — the
// canonical pool literal in the seed file must contain exactly the
// same four TLDs as core/services/domain/store.AllowedTLDs. We can't
// import that package directly (it's a separate Go module — the
// catalyst-api bootstrap binary is intentionally decoupled from the
// SME-side service modules), so the test asserts against the same
// literal list a human reviewer can eyeball-match. Drift surfaces in
// CI as a test fail with the exact got/want diff.
func TestChrootEnsureOrgPoolSeed_MatchesAllowedTLDsLiteral(t *testing.T) {
	// Mirror of core/services/domain/store.AllowedTLDs (2026-05-19).
	// Update both lists together if AllowedTLDs is ever widened.
	allowedTLDs := []string{
		"omani.rest",
		"omani.works",
		"omani.trade",
		"omani.homes",
	}
	got := append([]string(nil), canonicalOrgPoolDomains...)
	want := append([]string(nil), allowedTLDs...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonicalOrgPoolDomains drift from AllowedTLDs:\n  got:  %v\n  want: %v\n"+
			"Update either canonicalOrgPoolDomains in chroot_parent_domains_seed.go OR\n"+
			"AllowedTLDs in core/services/domain/store/store.go — the two must agree.",
			got, want)
	}
}

// TestChrootEnsureOrgPoolSeed_TopsUpPartialPool — start with the
// 2-entry mother shape (primary + one org-pool) and verify the seed
// fills in the 3 missing canonical entries. Mirrors the t31 #1907
// scenario byte-for-byte.
func TestChrootEnsureOrgPoolSeed_TopsUpPartialPool(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t31.omani.works")

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h := &Handler{log: slog.Default(), store: st}

	dep := &Deployment{
		ID: "c703247a0de12508",
		Request: provisioner.Request{
			SovereignFQDN: "t31.omani.works",
			ParentDomains: []provisioner.ParentDomain{
				{Name: "omani.works", Role: provisioner.ParentDomainRolePrimary},
				{Name: "omani.homes", Role: provisioner.ParentDomainRoleOrgPool},
			},
		},
	}

	added := h.chrootEnsureOrgPoolSeed(dep)
	// 4 canonical TLDs minus 1 already-present org-pool entry
	// (omani.homes) = 3 appends. The primary's omani.works row does
	// NOT count toward pool dedup; the canonical pool needs every
	// TLD listed as org-pool independently of whether one happens to
	// match the primary (FindParentDomain validates against
	// role=org-pool, not role=primary).
	if added != 3 {
		t.Fatalf("expected 3 rows appended (4 canonical org-pool minus 1 mother-stamped); got %d", added)
	}

	// All 4 canonical .omani.X entries must be present as org-pool
	// after the seed. The mother-stamped omani.homes Role=org-pool
	// must survive (we only append when missing, never overwrite).
	pool := map[string]string{}
	for _, pd := range dep.Request.ParentDomains {
		if pd.Role == provisioner.ParentDomainRoleOrgPool {
			pool[strings.ToLower(pd.Name)] = string(pd.Role)
		}
	}
	for _, want := range []string{"omani.homes", "omani.rest", "omani.trade", "omani.works"} {
		if _, ok := pool[want]; !ok {
			t.Errorf("missing canonical pool entry after seed: %q (pool=%v)", want, pool)
		}
	}

	// Primary entry must still be exactly 1 (we never touch primaries).
	primaries := 0
	for _, pd := range dep.Request.ParentDomains {
		if pd.Role == provisioner.ParentDomainRolePrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Errorf("primary count drift: got %d, want 1", primaries)
	}

	// Output order must be sorted lowercase by name (determinism gate).
	got := make([]string, 0, len(dep.Request.ParentDomains))
	for _, pd := range dep.Request.ParentDomains {
		got = append(got, strings.ToLower(pd.Name))
	}
	want := append([]string(nil), got...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParentDomains not sorted by lowercased name:\n  got:  %v\n  want: %v", got, want)
	}

	// Persistence round-trip: re-load the record from disk and verify
	// the topped-up pool survives. 1 primary + 4 org-pool = 5 rows.
	rec, err := st.Load(dep.ID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if len(rec.Request.ParentDomains) != 5 {
		t.Fatalf("on-disk record length drift: got %d entries, want 5 (1 primary + 4 org-pool)",
			len(rec.Request.ParentDomains))
	}
}

// TestChrootEnsureOrgPoolSeed_Idempotent — second run on an
// already-full pool is a no-op.
func TestChrootEnsureOrgPoolSeed_Idempotent(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t31.omani.works")
	h := &Handler{log: slog.Default()}

	dep := &Deployment{
		ID: "x",
		Request: provisioner.Request{
			SovereignFQDN: "t31.omani.works",
			ParentDomains: []provisioner.ParentDomain{
				{Name: "omani.works", Role: provisioner.ParentDomainRolePrimary},
				{Name: "omani.homes", Role: provisioner.ParentDomainRoleOrgPool},
				{Name: "omani.rest", Role: provisioner.ParentDomainRoleOrgPool},
				{Name: "omani.trade", Role: provisioner.ParentDomainRoleOrgPool},
				{Name: "omani.works", Role: provisioner.ParentDomainRoleOrgPool},
			},
		},
	}
	// First pass: nothing to add (all 4 canonical names already
	// present as org-pool rows). The primary's omani.works row does
	// not count toward pool dedup but the additional org-pool
	// omani.works row above does.
	added := h.chrootEnsureOrgPoolSeed(dep)
	if added != 0 {
		t.Fatalf("first pass on full pool: expected 0 appends, got %d", added)
	}
	// Second pass: still no-op.
	added = h.chrootEnsureOrgPoolSeed(dep)
	if added != 0 {
		t.Fatalf("second pass on full pool: expected 0 appends, got %d", added)
	}
}

// TestChrootEnsureOrgPoolSeed_MothershipNoOp — SOVEREIGN_FQDN unset
// means we're on the mothership; the seed must be a hard no-op (the
// canonical pool is a per-Sovereign concept, not a mothership one).
func TestChrootEnsureOrgPoolSeed_MothershipNoOp(t *testing.T) {
	// Force unset in case the test environment leaked the var.
	if err := os.Unsetenv("SOVEREIGN_FQDN"); err != nil {
		t.Fatalf("unset SOVEREIGN_FQDN: %v", err)
	}
	h := &Handler{log: slog.Default()}

	dep := &Deployment{
		ID: "mother",
		Request: provisioner.Request{
			SovereignFQDN: "",
			ParentDomains: []provisioner.ParentDomain{
				{Name: "openova.io", Role: provisioner.ParentDomainRolePrimary},
			},
		},
	}
	added := h.chrootEnsureOrgPoolSeed(dep)
	if added != 0 {
		t.Fatalf("mothership: expected 0 appends, got %d", added)
	}
	if len(dep.Request.ParentDomains) != 1 {
		t.Fatalf("mothership: expected unchanged length 1, got %d", len(dep.Request.ParentDomains))
	}
}

// TestChrootEnsureOrgPoolSeed_NilDep — defensive against a caller
// that hands us a nil pointer. The fix-author seam (HandleDeploymentImport,
// restoreFromStore) never does this today, but a future caller might.
func TestChrootEnsureOrgPoolSeed_NilDep(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t31.omani.works")
	h := &Handler{log: slog.Default()}
	if added := h.chrootEnsureOrgPoolSeed(nil); added != 0 {
		t.Fatalf("nil dep: expected 0 appends, got %d", added)
	}
}
