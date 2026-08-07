package handler

import "testing"

// #5833 (UAT row 25, residual ii) — "/api/v1/organizations returns 3 records
// where only 2 Organization CRs exist."
//
// mergeOrgResponses unions the store-backed rows with the CR-backed rows. It
// keyed on `Subdomain` alone, and that had two holes — both of which produce a
// DUPLICATE rather than a miss, which is why the symptom was an inflated count
// and not a disappearing Org:
//
//  1. The CR side always sets Subdomain to the slug (orgCRToResponse), but the
//     store side sets whatever the provision record carries — and a
//     custom-domain Org (DomainMode != free-subdomain) legitimately has no pool
//     subdomain. One Organization, two different keys, two rows.
//  2. An EMPTY key skipped dedupe ENTIRELY: the old loops appended without
//     registering anything, so a store row with no subdomain and its own CR row
//     both landed.
//
// Row 25's whole assertion is "one consistent model across surfaces". A
// directory that disagrees with `kubectl get organizations` about how many
// Organizations exist fails that before any field is compared.

func orgResp(id, sub string) orgTenantResponse {
	return orgTenantResponse{OrganizationID: id, Subdomain: sub}
}

func slugsOf(rows []orgTenantResponse) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.OrganizationID+"/"+r.Subdomain)
	}
	return out
}

// The exact hw292 shape: two Organizations, one of which the store knows
// without a subdomain. Old key → 3 rows. This is the regression test for the
// reported number.
func TestMergeOrgResponses_CustomDomainOrgDoesNotDoubleCount(t *testing.T) {
	local := []orgTenantResponse{
		orgResp("tnt-parent", "hw292-omani-works"),
		orgResp("tnt-uatco", ""), // custom-domain Org: no pool subdomain
	}
	fromCR := []orgTenantResponse{
		orgResp("tnt-parent", "hw292-omani-works"),
		orgResp("tnt-uatco", "uatco"), // the CR always carries the slug
	}

	got := mergeOrgResponses(local, fromCR)
	if len(got) != 2 {
		t.Fatalf("merge produced %d rows for 2 Organizations: %v\n"+
			"The directory now disagrees with `kubectl get organizations` about how many "+
			"Organizations exist, which fails UAT row 25's 'one consistent model' before any "+
			"field is compared (#5833).", len(got), slugsOf(got))
	}
}

// Local wins on collision — it carries the in-flight provisioning detail (the
// 7-step timeline, last_error, commit_sha) that the CR does not.
func TestMergeOrgResponses_LocalRowWinsOnCollision(t *testing.T) {
	local := []orgTenantResponse{{OrganizationID: "tnt-1", Subdomain: "acme", CompanyName: "from-store"}}
	fromCR := []orgTenantResponse{{OrganizationID: "tnt-1", Subdomain: "acme", CompanyName: "from-cr"}}

	got := mergeOrgResponses(local, fromCR)
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].CompanyName != "from-store" {
		t.Fatalf("CR row displaced the store row (%q) — the in-flight provisioning detail "+
			"the BSS door authored is lost from the directory", got[0].CompanyName)
	}
}

// A CR-only Organization (funnel-created, no store record) must still appear.
// This is the control: a dedupe key that is too aggressive would silently drop
// exactly the rows #4479 added this merge to surface.
func TestMergeOrgResponses_CROnlyOrgStillAppears(t *testing.T) {
	got := mergeOrgResponses(
		[]orgTenantResponse{orgResp("tnt-1", "acme")},
		[]orgTenantResponse{orgResp("tnt-1", "acme"), orgResp("tnt-2", "funnelco")},
	)
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d: %v — a funnel-created Org went missing, which is "+
			"the defect #4479 existed to fix", len(got), slugsOf(got))
	}
}

// Legacy store record predating OrganizationID must still merge on subdomain.
// Without the fallback this fix would trade a duplicate for a duplicate.
func TestMergeOrgResponses_FallsBackToSubdomainWhenIDAbsent(t *testing.T) {
	got := mergeOrgResponses(
		[]orgTenantResponse{orgResp("", "legacy")},
		[]orgTenantResponse{orgResp("", "legacy")},
	)
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d — the subdomain fallback is gone, so a legacy store "+
			"record with no OrganizationID double-counts against its own CR", len(got))
	}
}

// A row identifiable on NEITHER axis is kept, not dropped. Losing a row the
// operator's own store authored would be worse than showing it — but it also
// must not be silently treated as deduped.
func TestMergeOrgResponses_UnidentifiableRowIsKept(t *testing.T) {
	got := mergeOrgResponses([]orgTenantResponse{orgResp("", "")}, nil)
	if len(got) != 1 {
		t.Fatalf("expected the unidentifiable row to survive, got %d rows", len(got))
	}
}

func TestOrgMergeKeys_ReturnsBothAxesNamespaced(t *testing.T) {
	// BOTH axes are returned, not the first non-empty one. That distinction is
	// the correction: my first cut returned id-first, all six tests here passed,
	// and the FULL suite went red on a pre-existing case where a store record
	// and its own CR carry DIFFERENT ids for the same slug — id-first split one
	// Organization into two, mirroring the very defect being fixed.
	got := orgMergeKeys(orgResp("tnt-1", "acme"))
	if len(got) != 2 || got[0] != "id:tnt-1" || got[1] != "sub:acme" {
		t.Fatalf("orgMergeKeys = %v, want both axes [id:tnt-1 sub:acme] — matching on EITHER "+
			"is what makes same-id/different-subdomain AND same-subdomain/different-id both "+
			"resolve to one Organization", got)
	}
	if got := orgMergeKeys(orgResp("", "acme")); len(got) != 1 || got[0] != "sub:acme" {
		t.Fatalf("orgMergeKeys = %v, want only the subdomain axis", got)
	}
	if got := orgMergeKeys(orgResp("", "")); len(got) != 0 {
		t.Fatalf("orgMergeKeys = %v, want none for an unidentifiable row", got)
	}
	// Namespacing: a subdomain equal to some other Org's tenant id must never
	// dedupe two DIFFERENT Organizations into one.
	if orgMergeKeys(orgResp("acme", ""))[0] == orgMergeKeys(orgResp("", "acme"))[0] {
		t.Fatal("id and subdomain axes collide — a subdomain equal to another Org's id " +
			"would dedupe two DIFFERENT Organizations into one")
	}
}

// The pre-existing invariant my first cut broke, pinned here so it cannot be
// broken again by a future key change: the store row and the CR row for the
// SAME slug carry different OrganizationIDs, and they are still one Org.
func TestMergeOrgResponses_SameSlugDifferentIDsIsOneOrg(t *testing.T) {
	got := mergeOrgResponses(
		[]orgTenantResponse{orgResp("uuid-acme", "acme")},
		[]orgTenantResponse{orgResp("cr-acme", "acme")},
	)
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d: %v — keying on the id alone splits one Organization "+
			"in two, which is the same defect #5833 fixes, mirrored", len(got), slugsOf(got))
	}
}
