package store

import "testing"

// Without a database: the code rule, and the migration locator.

func TestValidCostCentreCode(t *testing.T) {
	for _, code := range []string{"ENG", "cc-1001", "a", "R.and.D", "eng/platform", "ops@hq", "CC_1", "a1:b2"} {
		if !ValidCostCentreCode(code) {
			t.Errorf("%q should be a valid code", code)
		}
	}
	for _, code := range []string{"", " ENG", "ENG ", "has space", "-leading", ".leading", CostCentreUnassigned, "a#b", "a,b", string(make([]byte, 65))} {
		if ValidCostCentreCode(code) {
			t.Errorf("%q should NOT be a valid code", code)
		}
	}
	// The unassigned bucket is outside the rule BY CONSTRUCTION, which is
	// what stops a real cost centre ever colliding with it.
	if ValidCostCentreCode(CostCentreUnassigned) {
		t.Fatal("a customer could claim the unassigned bucket's own name as a code")
	}
}

// The migration is located by CONTENT, like every other one. If the constant
// is edited without the locator following it, the locator silently falls back
// to len(migrations) and every test that stands a database at a version reads
// the wrong number — so the locator must resolve to a real entry, and that
// entry must be the one it names.
func TestMigrationCostCentresIsLocatedByContent(t *testing.T) {
	if MigrationCostCentres < 1 || MigrationCostCentres > len(migrations) {
		t.Fatalf("MigrationCostCentres = %d, migrations = %d", MigrationCostCentres, len(migrations))
	}
	if migrations[MigrationCostCentres-1] != costCentreMigrationSQL {
		t.Fatalf("MigrationCostCentres points at a different migration")
	}
	// Migrations are POSITIONAL: this one was appended at the end, and an
	// entry inserted above a recorded version is silently skipped. A later
	// migration may legitimately follow it, but nothing may take its place.
	if MigrationCostCentres != len(migrations) {
		t.Logf("a migration has been appended after the cost-centre one (position %d of %d) — that is fine, as long as it was APPENDED", MigrationCostCentres, len(migrations))
	}
}
