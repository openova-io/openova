// org_consumption_billable_set_6114_test.go — UAT row 25's set-agreement
// conjunct: "/organizations shows the Orgs that /apps, the dashboard and
// showback all agree on".
//
// The measurement this locks (hw293, dep a0077ba47e3720e5, 2026-08-11):
//
//	(c) g7doora has NO Organization CR — `kubectl get organizations` listed
//	    hw293vch, hw293walkone, hw293walktwo, uat107org, uat107vc and not
//	    g7doora — yet it drew the LARGEST customer showback slice on the
//	    Sovereign, 4272.25 units. Showback billed an entity the control
//	    plane does not model.
//
//	(b) p474del1 and uat107org appear in the directory and draw no showback
//	    slice at all, while uat107org's twin uat107vc — created and deleted
//	    the same way on the same day, both reading state deleted — does
//	    carry one (525.5 units). Same disposition, different treatment.
//
// Those are the two halves of ONE join defect, and it cuts both ways. The
// forward half billed a slug with no CR because the join trusted the
// `openova.io/organization` namespace label on its own. The inverse half
// dropped an Org that HAS a CR because rows only ever materialised from
// pod rows, so an Organization's presence in the feed tracked whether it
// happened to be running a pod rather than whether it exists. Checking one
// direction is how the other survived, so both are asserted here.
//
// Every assertion below is on a VALUE — the unit count, the joined set,
// the ordering rank — never on key presence. The over-correction guard is
// explicit: TestBillableSet_GenuineOrganizationsAreStillBilledInFull is
// the CONTROL, and it shares the suspect property (its Orgs are resolved
// through the very same label join key that mis-billed g7doora); it must
// stay green and its numbers must not move.
package handler

import (
	"testing"
)

// hw293OrgCRSet is the Organization CR set exactly as hw293 reported it:
// five Organizations, and g7doora is NOT among them.
func hw293OrgCRSet() map[string]struct{} {
	return map[string]struct{}{
		"hw293vch":     {},
		"hw293walkone": {},
		"hw293walktwo": {},
		"uat107org":    {},
		"uat107vc":     {},
	}
}

// billedOrgs returns the slugs the response bills as customer
// Organizations — every row that is not the parent estate and not one of
// the two synthetic rollups. This is the "billable set" row 25 compares
// against the Organization set.
func billedOrgs(resp SovereignConsumptionResponse) map[string]float64 {
	out := map[string]float64{}
	for _, oc := range resp.Orgs {
		if oc.IsParent || oc.IsPlatform || oc.IsUnowned {
			continue
		}
		out[oc.Org] = oc.CostUnits
	}
	return out
}

// TestBillableSet_ANamespaceWithNoOrganizationCRIsNeverBilled is the
// forward half: the g7doora shape. A namespace carrying the join-key label
// whose slug has no Organization CR must not appear as a billable
// Organization — and its consumption must not be dropped either, because
// the units are real cluster resource and dropping them understates the
// estate.
func TestBillableSet_ANamespaceWithNoOrganizationCRIsNeverBilled(t *testing.T) {
	const parent = "hw293-omantel-biz"
	rows := []podRow{
		// g7doora: the namespace kept converging after the Org create
		// failed. Six real Applications, no Organization CR.
		{namespace: "g7doora", application: "bp-keycloak", org: "g7doora", ownerKind: "StatefulSet", cpuReq: 1000, memReq: 2 << 30},
		{namespace: "g7doora", application: "bp-agenity", org: "g7doora", ownerKind: "Deployment", cpuReq: 500, memReq: 1 << 30},
		{namespace: "g7doora", application: "bp-wordpress", org: "g7doora", ownerKind: "Deployment", cpuReq: 250, memReq: 512 << 20},
		// hw293vch: a genuine Organization, CR present.
		{namespace: "hw293vch", application: "bp-newapi", org: "hw293vch", ownerKind: "Deployment", cpuReq: 400, memReq: 1 << 30},
	}

	resp := aggregateConsumption(rows, parent, testInfraSet(), hw293OrgCRSet())

	// 1. g7doora draws NO billable Organization slice.
	billed := billedOrgs(resp)
	if cost, present := billed["g7doora"]; present {
		t.Errorf("g7doora is billed %v units as a customer Organization, but it has no Organization CR "+
			"(the CR set is %v) — this is the hw293 4272.25-unit phantom", cost, sortedSlugs6114(hw293OrgCRSet()))
	}

	// 2. The consumption is NOT dropped: it lands in the unowned rollup
	//    with its exact cost. 1750 millicores + 3.5 GiB =
	//    1750*1.0 + 3.5*4.0 = 1764.
	var unownedRow *orgConsumption
	for i := range resp.Orgs {
		if resp.Orgs[i].IsUnowned {
			unownedRow = &resp.Orgs[i]
		}
	}
	if unownedRow == nil {
		t.Fatalf("no unowned rollup row: g7doora's consumption vanished entirely from the feed, "+
			"which understates the estate by its whole footprint. Orgs=%#v", resp.Orgs)
	}
	if unownedRow.CostUnits != 1764 {
		t.Errorf("unowned CostUnits = %v, want 1764 (1750 milli * 1.0 + 3.5 GiB * 4.0)", unownedRow.CostUnits)
	}
	if unownedRow.CPUMilli != 1750 {
		t.Errorf("unowned CPUMilli = %v, want 1750", unownedRow.CPUMilli)
	}

	// 3. The orphan is NAMED, not merely bucketed. Silence is the failure
	//    mode this row exists to catch; an unlabelled rollup would hide
	//    g7doora just as effectively as billing it did.
	if got, want := resp.UnownedOrgs, []string{"g7doora"}; !equalStringSlices6114(got, want) {
		t.Errorf("UnownedOrgs = %v, want %v", got, want)
	}

	// 4. It is NOT folded into platform overhead — that line asserts the
	//    consumption is the control plane's own, which is a false claim
	//    about a half-torn-down customer namespace.
	for _, oc := range resp.Orgs {
		if !oc.IsPlatform {
			continue
		}
		if oc.CostUnits != 0 {
			t.Errorf("platform overhead absorbed %v units — the orphan must not hide inside it", oc.CostUnits)
		}
	}

	// 5. The estate total still counts every unit: 1764 unowned + 400 milli
	//    + 1 GiB = 404 for hw293vch.
	if resp.TotalCostUnits != 2168 {
		t.Errorf("TotalCostUnits = %v, want 2168 (1764 unowned + 404 hw293vch) — "+
			"the Sovereign-wide total must not shrink when attribution is corrected", resp.TotalCostUnits)
	}
}

// TestBillableSet_AnOrganizationWithACRIsBilledEvenWithNothingRunning is
// the INVERSE half: the uat107org shape. An Organization the CR set holds
// must draw a slice whether or not it currently runs a pod, otherwise its
// presence in showback tracks pod liveness rather than existence — which
// is what made uat107vc (525.5 units) and uat107org (absent) diverge
// despite identical disposition.
func TestBillableSet_AnOrganizationWithACRIsBilledEvenWithNothingRunning(t *testing.T) {
	const parent = "hw293-omantel-biz"
	rows := []podRow{
		// uat107vc still has a terminating pod.
		{namespace: "uat107vc", application: "bp-wordpress", org: "uat107vc", ownerKind: "Deployment", cpuReq: 500, memReq: 64 << 20},
		// uat107org's pods are already gone. It has a CR all the same.
	}

	resp := aggregateConsumption(rows, parent, testInfraSet(), hw293OrgCRSet())
	billed := billedOrgs(resp)

	// Every Organization in the CR set is present in the billable set.
	for slug := range hw293OrgCRSet() {
		if _, present := billed[slug]; !present {
			t.Errorf("Organization %q has a CR but draws no showback slice at all — "+
				"the billable set %v is missing it", slug, sortedSlugs6114(slugSetOf6114(billed)))
		}
	}

	// And the values are right: uat107vc carries its real cost, uat107org
	// renders at exactly zero rather than vanishing.
	if got := billed["uat107vc"]; got != 500.25 {
		t.Errorf("uat107vc CostUnits = %v, want 500.25 (500 milli * 1.0 + 0.0625 GiB * 4.0)", got)
	}
	if got := billed["uat107org"]; got != 0 {
		t.Errorf("uat107org CostUnits = %v, want 0 — an Org with a CR and no workload is a zero slice, not an absence", got)
	}

	// The two surfaces now agree as SETS, which is the row-25 conjunct.
	if len(billed) != len(hw293OrgCRSet()) {
		t.Errorf("billable set has %d Orgs, Organization CR set has %d — the sets must be equal. billed=%v crs=%v",
			len(billed), len(hw293OrgCRSet()), sortedSlugs6114(slugSetOf6114(billed)), sortedSlugs6114(hw293OrgCRSet()))
	}
}

// TestBillableSet_GenuineOrganizationsAreStillBilledInFull is the CONTROL.
//
// It shares the suspect property with the defect: acme and beta are
// attributed through the exact same `openova.io/organization` label join
// key that mis-billed g7doora, and they sit alongside an orphan in the
// same aggregation. The whole risk of this fix is over-correcting into
// "nothing is billed", so the control asserts the full billed values, the
// per-app breakdown and the percent share — not merely that a row exists.
func TestBillableSet_GenuineOrganizationsAreStillBilledInFull(t *testing.T) {
	const parent = "hw293-omantel-biz"
	knownOrgs := map[string]struct{}{"acme": {}, "beta": {}}
	rows := []podRow{
		{namespace: "acme", application: "blog", org: "acme", ownerKind: "StatefulSet", cpuReq: 200, memReq: 256 << 20},
		{namespace: "acme", application: "shop", org: "acme", ownerKind: "Deployment", cpuReq: 600, memReq: 256 << 20},
		{namespace: "beta", application: "api", org: "beta", ownerKind: "Deployment", cpuReq: 300, memReq: 512 << 20},
		// An orphan in the same estate — the control must survive its presence.
		{namespace: "ghost", application: "bp-wordpress", org: "ghost", ownerKind: "Deployment", cpuReq: 100},
	}

	resp := aggregateConsumption(rows, parent, testInfraSet(), knownOrgs)
	billed := billedOrgs(resp)

	// acme: (200+600) milli + 0.5 GiB = 800 + 2 = 802.
	if got := billed["acme"]; got != 802 {
		t.Errorf("acme CostUnits = %v, want 802 — a genuine Organization must still be billed in full", got)
	}
	// beta: 300 milli + 0.5 GiB = 300 + 2 = 302.
	if got := billed["beta"]; got != 302 {
		t.Errorf("beta CostUnits = %v, want 302 — a genuine Organization must still be billed in full", got)
	}
	if len(billed) != 2 {
		t.Errorf("billable set = %v, want exactly {acme, beta}", sortedSlugs6114(slugSetOf6114(billed)))
	}

	// The per-app breakdown inside a genuine Org is untouched: acme keeps
	// both Applications, ordered by descending cost, summing to 100%.
	var acme *orgConsumption
	for i := range resp.Orgs {
		if resp.Orgs[i].Org == "acme" {
			acme = &resp.Orgs[i]
		}
	}
	if acme == nil || len(acme.Apps) != 2 {
		t.Fatalf("acme lost its per-app breakdown: %#v", acme)
	}
	if acme.Apps[0].Application != "shop" || acme.Apps[1].Application != "blog" {
		t.Errorf("acme apps = %q/%q, want shop/blog (descending cost)", acme.Apps[0].Application, acme.Apps[1].Application)
	}
	if sum := acme.Apps[0].Percent + acme.Apps[1].Percent; sum < 99.9 || sum > 100.1 {
		t.Errorf("acme per-app percent sums to %v, want 100", sum)
	}

	// Nothing is silently zeroed estate-wide: 802 + 302 + 100 (ghost) = 1204.
	if resp.TotalCostUnits != 1204 {
		t.Errorf("TotalCostUnits = %v, want 1204 — the fix must not zero the estate", resp.TotalCostUnits)
	}
}

// TestBillableSet_AnUnreadOrganizationSetChangesNothing guards the other
// over-correction. nil means "the Organization CR set could not be read",
// NOT "there are no Organizations". A cold or unsynced informer must never
// relabel a whole estate as unowned and bill nobody: to call a namespace
// an orphan we require positive evidence that the CR set was read.
func TestBillableSet_AnUnreadOrganizationSetChangesNothing(t *testing.T) {
	const parent = "hw293-omantel-biz"
	rows := []podRow{
		{namespace: "acme", application: "blog", org: "acme", ownerKind: "StatefulSet", cpuReq: 200, memReq: 256 << 20},
		{namespace: "g7doora", application: "bp-keycloak", org: "g7doora", ownerKind: "StatefulSet", cpuReq: 1000, memReq: 2 << 30},
	}

	resp := aggregateConsumption(rows, parent, testInfraSet(), nil)
	billed := billedOrgs(resp)

	if got := billed["acme"]; got != 201 {
		t.Errorf("acme CostUnits = %v, want 201 with an unread Organization set", got)
	}
	if got := billed["g7doora"]; got != 1008 {
		t.Errorf("g7doora CostUnits = %v, want 1008 with an unread Organization set — "+
			"an unread set must not be treated as an empty one", got)
	}
	if len(resp.UnownedOrgs) != 0 {
		t.Errorf("UnownedOrgs = %v, want empty when the Organization set was never read", resp.UnownedOrgs)
	}
}

// TestBillableSet_ParentEstateIsOwnedWithoutACR — the parent row is the
// Sovereign itself (§2.2), not a customer Organization, and it is seeded
// unconditionally. It must never be demoted into the unowned rollup just
// because its self-org CR is absent from the set, or the estate row would
// disappear from its own showback page.
func TestBillableSet_ParentEstateIsOwnedWithoutACR(t *testing.T) {
	const parent = "hw293-omantel-biz"
	rows := []podRow{
		{namespace: "spine", application: "catalyst-ui", org: parent, ownerKind: "Deployment", cpuReq: 100},
	}

	// A CR set that does NOT contain the parent slug.
	resp := aggregateConsumption(rows, parent, testInfraSet(), map[string]struct{}{"acme": {}})

	if len(resp.Orgs) == 0 || !resp.Orgs[0].IsParent {
		t.Fatalf("parent estate row must be present and first, got %#v", resp.Orgs)
	}
	if got := resp.Orgs[0].CostUnits; got != 100 {
		t.Errorf("parent CostUnits = %v, want 100 — the estate's own consumption must stay on the parent row", got)
	}
	if len(resp.UnownedOrgs) != 0 {
		t.Errorf("UnownedOrgs = %v, want empty — the parent estate is not an orphan", resp.UnownedOrgs)
	}
}

// TestBillableSet_UnownedRollupOrdersBeforePlatformOverhead — the console
// renders the trailing synthetic lines in a fixed order. Asserted because
// the pre-#6114 pairwise-boolean sort mis-orders as soon as a third
// synthetic bucket exists: comparing an unowned row against a platform row
// consults the wrong flag.
func TestBillableSet_UnownedRollupOrdersBeforePlatformOverhead(t *testing.T) {
	const parent = "hw293-omantel-biz"
	rows := []podRow{
		{namespace: "acme", application: "blog", org: "acme", ownerKind: "Deployment", cpuReq: 100},
		{namespace: "ghost", application: "bp-wordpress", org: "ghost", ownerKind: "Deployment", cpuReq: 50},
		{namespace: "kube-system", application: "cilium", ownerKind: "DaemonSet", cpuReq: 25},
	}

	resp := aggregateConsumption(rows, parent, testInfraSet(), map[string]struct{}{"acme": {}})

	got := make([]string, 0, len(resp.Orgs))
	for _, oc := range resp.Orgs {
		got = append(got, oc.Org)
	}
	want := []string{parent, "acme", unownedOrg, platformOrg}
	if !equalStringSlices6114(got, want) {
		t.Errorf("row order = %v, want %v", got, want)
	}
}

// ── vacuity check ────────────────────────────────────────────────────
//
// A guard that cannot fail is decorative. This proves the new assertions
// DO fail on the pre-fix behaviour by reconstructing it exactly: the old
// orgForRow returned any non-empty label value verbatim, and the old
// aggregateConsumption seeded only the parent. Feeding the hw293 rows
// through that reconstruction must reproduce BOTH halves of the measured
// defect — g7doora billed, uat107org absent — which is precisely what the
// two tests above reject. If this test ever goes green with the assertions
// inverted, the new tests are asserting on nothing.
func TestBillableSet_VacuityCheck_PreFixBehaviourFailsBothAssertions(t *testing.T) {
	const parent = "hw293-omantel-biz"
	rows := []podRow{
		{namespace: "g7doora", application: "bp-keycloak", org: "g7doora", ownerKind: "StatefulSet", cpuReq: 1000, memReq: 2 << 30},
		{namespace: "uat107vc", application: "bp-wordpress", org: "uat107vc", ownerKind: "Deployment", cpuReq: 500, memReq: 64 << 20},
	}

	// The pre-#6114 resolver, verbatim: label wins, no CR cross-check.
	preFixOrgForRow := func(row podRow, infra map[string]struct{}) string {
		if _, isInfra := infra[row.namespace]; isInfra {
			return platformOrg
		}
		if row.ownerKind == "Job" {
			return platformOrg
		}
		if row.org != "" {
			return row.org
		}
		return platformOrg
	}
	// The pre-#6114 aggregation's org set: parent + whatever the rows
	// produced. No CR seeding.
	preFixBilled := map[string]struct{}{}
	for _, row := range rows {
		if org := preFixOrgForRow(row, testInfraSet()); org != platformOrg && org != parent {
			preFixBilled[org] = struct{}{}
		}
	}

	// Forward half: the old join DID bill g7doora. The new assertion
	// "g7doora is absent from the billable set" therefore fails here.
	if _, billed := preFixBilled["g7doora"]; !billed {
		t.Fatalf("vacuity check is broken: the reconstructed pre-fix join did not bill g7doora, "+
			"so TestBillableSet_ANamespaceWithNoOrganizationCRIsNeverBilled would have passed before the fix. got=%v",
			sortedSlugs6114(preFixBilled))
	}

	// Inverse half: the old aggregation produced NO row for uat107org,
	// which has a CR but no pod. The new assertion "every CR draws a
	// slice" therefore fails here too.
	if _, billed := preFixBilled["uat107org"]; billed {
		t.Fatalf("vacuity check is broken: the reconstructed pre-fix aggregation produced a uat107org row " +
			"without a pod, so the inverse assertion would have passed before the fix")
	}

	// And the same rows through the SHIPPED aggregation must invert both.
	resp := aggregateConsumption(rows, parent, testInfraSet(), hw293OrgCRSet())
	billed := billedOrgs(resp)
	if _, present := billed["g7doora"]; present {
		t.Errorf("shipped aggregation still bills g7doora — the fix is inert")
	}
	if _, present := billed["uat107org"]; !present {
		t.Errorf("shipped aggregation still drops uat107org — the inverse half is inert")
	}
}

// ── tiny test helpers ────────────────────────────────────────────────

func equalStringSlices6114(a, b []string) bool {
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

func slugSetOf6114(m map[string]float64) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func sortedSlugs6114(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
