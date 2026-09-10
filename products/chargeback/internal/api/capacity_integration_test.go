package api

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/capacity"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Capacity through the API (DESIGN.md §11): the permission model — a
// finance-viewer reads, a billing-operator and the sovereign-admin write, a
// customer principal sees nothing — the overview's wire shape, and the
// audit trail every write leaves.

// capacityGrowth is rating.RunRate's trend and nothing else: the same
// arithmetic the explorer's forecast reports as run rate + trend.
func TestCapacityGrowthIsTheRunRateTrend(t *testing.T) {
	var days []store.CapacityDayPoint
	var serie []rating.DayCost
	for d := 1; d <= 9; d++ {
		day := time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		days = append(days, store.CapacityDayPoint{Day: day, Consumed: store.Decimal(strconv.Itoa(100+10*d) + ".000000")})
		serie = append(serie, rating.DayCost{Day: day, Cost: float64(100 + 10*d)})
	}
	got, ok := capacityGrowth(days)
	_, want, _ := rating.RunRate(serie)
	if !ok || got != want || got < 9.999999 || got > 10.000001 {
		t.Fatalf("capacityGrowth = %v %v, RunRate trend = %v", got, ok, want)
	}
	if _, ok := capacityGrowth(days[:2]); ok {
		t.Fatal("two days cannot carry a trend")
	}
}

func TestIntegrationCapacityPermissionsAndOverviewShape(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 30, 0, 0, time.UTC)
	h, st, mail := setupAPIAt(t, now)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	// A finance-viewer and a billing-operator by binding; a customer owner
	// through its customer's admin_email.
	op.mustJSON("POST", "/api/v1/access/bindings", map[string]any{"subject_email": "fin@nc.example", "role": store.RoleFinanceViewer}, 201)
	op.mustJSON("POST", "/api/v1/access/bindings", map[string]any{"subject_email": "bo@nc.example", "role": store.RoleBillingOperator}, 201)
	cust := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "acme", "name": "Acme", "admin_email": "owner@acme.example"}, 201)
	custID := cust["id"].(string)
	fin := &client{t: t, h: h}
	fin.signIn("fin@nc.example", mail)
	bo := &client{t: t, h: h}
	bo.signIn("bo@nc.example", mail)
	owner := &client{t: t, h: h}
	owner.signIn("owner@acme.example", mail)

	// Empty state reads for every Sovereign principal; a customer is refused
	// with the permission named, at the Sovereign.
	for _, c := range []*client{op, fin, bo} {
		ov := c.must("GET", "/api/v1/capacity/overview", 200)
		if ov["as_of"] != nil || len(ov["regions"].([]any)) != 0 {
			t.Fatalf("empty overview = %v", ov)
		}
		c.must("GET", "/api/v1/capacity/regions", 200)
		c.must("GET", "/api/v1/capacity/footprints", 200)
		c.must("GET", "/api/v1/capacity/caps", 200)
	}
	for _, path := range []string{"/api/v1/capacity/overview", "/api/v1/capacity/regions", "/api/v1/capacity/footprints", "/api/v1/capacity/caps"} {
		rec, _ := owner.do("GET", path, "", nil)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "metering.read") || !strings.Contains(rec.Body.String(), "Sovereign") {
			t.Fatalf("customer-owner GET %s = %d %s, want 403 naming metering.read at the Sovereign", path, rec.Code, rec.Body.String())
		}
	}

	// Writes: the viewer is refused naming capacity.manage; the operator and
	// the billing-operator both write.
	rec, _ := fin.json("POST", "/api/v1/capacity/regions", map[string]any{"code": "me-east-215"})
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "capacity.manage") {
		t.Fatalf("finance-viewer POST regions = %d %s", rec.Code, rec.Body.String())
	}
	region := op.mustJSON("POST", "/api/v1/capacity/regions", map[string]any{"code": "ME-East-215", "name": "Muscat"}, 201)
	regionID := region["id"].(string)
	if region["code"] != "me-east-215" || region["cloud_source_kind"] != store.SourceKindHuaweiProject {
		t.Fatalf("region = %v", region)
	}
	if rec, _ := op.json("POST", "/api/v1/capacity/regions", map[string]any{"code": "me-east-215"}); rec.Code != 409 {
		t.Fatalf("duplicate region = %d", rec.Code)
	}
	if rec, _ := op.json("POST", "/api/v1/capacity/regions", map[string]any{"name": "no code"}); rec.Code != 400 {
		t.Fatalf("region without code = %d", rec.Code)
	}
	zoneA := bo.mustJSON("POST", "/api/v1/capacity/regions/"+regionID+"/zones", map[string]any{"code": "me-east-215a", "name": "AZ 1"}, 201)
	zoneB := op.mustJSON("POST", "/api/v1/capacity/regions/"+regionID+"/zones", map[string]any{"code": "me-east-215b"}, 201)
	if zoneA["is_default"] != true || zoneB["is_default"] != false || len(zoneA["pools"].([]any)) != len(capacity.Families) {
		t.Fatalf("zones = %v / %v", zoneA, zoneB)
	}
	if rec, _ := fin.json("POST", "/api/v1/capacity/regions/"+regionID+"/zones", map[string]any{"code": "x"}); rec.Code != 403 {
		t.Fatalf("finance-viewer creates a zone = %d", rec.Code)
	}
	zoneAID := zoneA["id"].(string)
	pools := fin.must("GET", "/api/v1/capacity/zones/"+zoneAID+"/pools", 200)
	poolList := pools["pools"].([]any)
	if len(poolList) != len(capacity.Families) || pools["zone"].(map[string]any)["code"] != "me-east-215a" {
		t.Fatalf("pools = %v", pools)
	}
	var vcpuPool string
	for _, p := range poolList {
		if p.(map[string]any)["family"] == capacity.FamilyVCPU {
			vcpuPool = p.(map[string]any)["id"].(string)
		}
	}
	rec, _ = fin.json("PUT", "/api/v1/capacity/pools/"+vcpuPool, map[string]any{"total": 20, "note": "x"})
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "capacity.manage") {
		t.Fatalf("finance-viewer PUT pool = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := owner.json("PUT", "/api/v1/capacity/pools/"+vcpuPool, map[string]any{"total": 20}); rec.Code != 403 {
		t.Fatalf("customer-owner PUT pool = %d", rec.Code)
	}
	set := op.mustJSON("PUT", "/api/v1/capacity/pools/"+vcpuPool, map[string]any{"total": "20", "note": "two hosts of 10 vCPU"}, 200)
	if set["total"] != 20.0 || set["note"] != "two hosts of 10 vCPU" || set["updated_by"] != opEmail || set["source"] != capacity.SourceManual {
		t.Fatalf("set pool = %v", set)
	}
	bo.mustJSON("PUT", "/api/v1/capacity/pools/"+vcpuPool, map[string]any{"total": 24}, 200)
	for _, bad := range []map[string]any{{"note": "no total"}, {"total": "-5"}, {"total": "abc"}} {
		if rec, _ := op.json("PUT", "/api/v1/capacity/pools/"+vcpuPool, bad); rec.Code != 400 {
			t.Fatalf("PUT pool %v = %d, want 400", bad, rec.Code)
		}
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/pools/00000000-0000-0000-0000-000000000000", map[string]any{"total": 1}); rec.Code != 404 {
		t.Fatalf("unknown pool = %d", rec.Code)
	}
	// History on the pools document, newest first.
	pools = op.must("GET", "/api/v1/capacity/zones/"+zoneAID+"/pools", 200)
	hist := pools["history"].(map[string]any)[vcpuPool].([]any)
	if len(hist) != 2 || hist[0].(map[string]any)["total"] != 24.0 || hist[1].(map[string]any)["note"] != "two hosts of 10 vCPU" {
		t.Fatalf("history = %v", hist)
	}

	// Footprints: seeded rows listed; PUT replaces; the viewer is refused.
	fps := fin.must("GET", "/api/v1/capacity/footprints", 200)
	if len(fps["footprints"].([]any)) != 6 || len(fps["families"].([]any)) != 7 {
		t.Fatalf("footprints = %v", fps)
	}
	if un := fps["unseeded_skus"].([]any); len(un) != 3 || un[0] != "elb" {
		t.Fatalf("unseeded = %v", un)
	}
	if rec, _ := fin.json("PUT", "/api/v1/capacity/footprints/nat.1", map[string]any{"families": map[string]any{"eip_addresses": 1}}); rec.Code != 403 {
		t.Fatalf("finance-viewer PUT footprint = %d", rec.Code)
	}
	fp := op.mustJSON("PUT", "/api/v1/capacity/footprints/nat.1", map[string]any{"families": map[string]any{"eip_addresses": "1", "bandwidth_mbps": 0}}, 200)
	if fams := fp["families"].(map[string]any); len(fams) != 1 || fams["eip_addresses"] != 1.0 || fp["source"] != capacity.SourceManual {
		t.Fatalf("put footprint = %v", fp)
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/footprints/nat.1", map[string]any{"families": map[string]any{"gpu": 1}}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "gpu") {
		t.Fatalf("unknown family = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/footprints/nat.1", map[string]any{}); rec.Code != 400 {
		t.Fatalf("missing families = %d", rec.Code)
	}

	// Caps: PUT upserts, null total removes, the viewer is refused.
	zoneBID := zoneB["id"].(string)
	if rec, _ := fin.json("PUT", "/api/v1/capacity/caps", map[string]any{"zone_id": zoneBID, "sku": "evs.ssd.gb", "total": 500}); rec.Code != 403 {
		t.Fatalf("finance-viewer PUT cap = %d", rec.Code)
	}
	capDoc := op.mustJSON("PUT", "/api/v1/capacity/caps", map[string]any{"zone_id": zoneBID, "sku": "evs.ssd.gb", "total": 500}, 200)
	if capDoc["zone_code"] != "me-east-215b" || capDoc["region_code"] != "me-east-215" || capDoc["total"] != 500.0 {
		t.Fatalf("cap = %v", capDoc)
	}
	if caps := op.must("GET", "/api/v1/capacity/caps", 200)["caps"].([]any); len(caps) != 1 {
		t.Fatalf("caps = %v", caps)
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/caps", map[string]any{"zone_id": zoneBID, "sku": "evs.ssd.gb", "total": nil}); rec.Code != 200 {
		t.Fatalf("remove cap = %d %s", rec.Code, rec.Body.String())
	}
	if caps := op.must("GET", "/api/v1/capacity/caps", 200)["caps"].([]any); len(caps) != 0 {
		t.Fatalf("caps after remove = %v", caps)
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/caps", map[string]any{"sku": "x", "total": 1}); rec.Code != 400 {
		t.Fatalf("cap without zone = %d", rec.Code)
	}

	// Some metering, then the overview's shape.
	src, _, err := st.UpsertSource(ctx, custID, store.SourceKindHuaweiProject, "me-east-215", "proj-acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{{ResourceID: "vm-1", Kind: "ecs", Name: "web-1", Attrs: map[string]any{"availability_zone": "me-east-215a"}, SeenAt: now}}); err != nil {
		t.Fatal(err)
	}
	var recs []store.UsageRecord
	for d := 1; d <= 9; d++ {
		for hh := 0; hh < 24 && (d < 9 || hh < 10); hh++ {
			at := time.Date(2026, 9, d, hh, 0, 0, 0, time.UTC)
			recs = append(recs, store.UsageRecord{CustomerID: custID, SourceID: src.ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: "ecs.m7n.2xlarge.8", Quantity: "1", Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215"})
			recs = append(recs, store.UsageRecord{CustomerID: custID, SourceID: src.ID, ResourceID: "nat-1", ResourceKind: "nat", SKU: "nat.2", Quantity: "1", Unit: "hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	ov := fin.must("GET", "/api/v1/capacity/overview", 200)
	for _, k := range []string{"as_of", "sources", "lagging_sources", "thresholds", "families", "regions", "unmapped_skus", "unmapped_regions", "summary"} {
		if _, ok := ov[k]; !ok {
			t.Fatalf("overview lacks %q", k)
		}
	}
	if ov["as_of"] != "2026-09-09T09:00:00Z" || ov["sources"] != 1.0 {
		t.Fatalf("as_of/sources = %v / %v", ov["as_of"], ov["sources"])
	}
	th := ov["thresholds"].(map[string]any)
	if th["warn_pct"] != 70.0 || th["critical_pct"] != 85.0 {
		t.Fatalf("thresholds = %v", th)
	}
	regions := ov["regions"].([]any)
	if len(regions) != 1 {
		t.Fatalf("regions = %v", regions)
	}
	zones := regions[0].(map[string]any)["zones"].([]any)
	if len(zones) != 2 {
		t.Fatalf("zones = %v", zones)
	}
	za := zones[0].(map[string]any)
	if za["code"] != "me-east-215a" || za["is_default"] != true {
		t.Fatalf("zone a = %v", za)
	}
	var vcpu map[string]any
	for _, p := range za["pools"].([]any) {
		if p.(map[string]any)["family"] == capacity.FamilyVCPU {
			vcpu = p.(map[string]any)
		}
	}
	for _, k := range []string{"id", "zone_id", "family", "label", "unit", "total", "reserved", "consumed", "available", "utilisation_pct", "status", "clamped", "overcommit", "zone_unknown", "growth_per_day", "exhaustion_days", "history_days", "series", "source", "note", "updated_by", "updated_at"} {
		if _, ok := vcpu[k]; !ok {
			t.Fatalf("pool lacks %q: %v", k, vcpu)
		}
	}
	// 24 vCPU total, one m7n.2xlarge.8 (8 vCPU) running: 16 available, 33 %.
	if vcpu["total"] != 24.0 || vcpu["consumed"] != 8.0 || vcpu["available"] != 16.0 || vcpu["reserved"] != 0.0 || vcpu["status"] != capacity.StatusOK || vcpu["clamped"] != false {
		t.Fatalf("vcpu pool = %v", vcpu)
	}
	if pct := vcpu["utilisation_pct"].(float64); pct < 33.3 || pct > 33.4 {
		t.Fatalf("utilisation = %v", pct)
	}
	// A flat series: growth 0 and no exhaustion date, on the wire as null.
	if vcpu["exhaustion_days"] != nil || vcpu["growth_per_day"] != 0.0 || vcpu["history_days"] != 8.0 {
		t.Fatalf("vcpu growth = %v / %v / %v", vcpu["growth_per_day"], vcpu["exhaustion_days"], vcpu["history_days"])
	}
	skus := za["skus"].([]any)
	var m7 map[string]any
	for _, s := range skus {
		if s.(map[string]any)["sku"] == "ecs.m7n.2xlarge.8" {
			m7 = s.(map[string]any)
		}
	}
	for _, k := range []string{"sku", "footprint", "footprint_source", "consumed_units", "resources", "headroom_units", "binding_family", "cap"} {
		if _, ok := m7[k]; !ok {
			t.Fatalf("sku lacks %q: %v", k, m7)
		}
	}
	// 16 vCPU left → 2 more; memory has no total, so vCPU binds.
	if m7["headroom_units"] != 2.0 || m7["binding_family"] != capacity.FamilyVCPU || m7["consumed_units"] != 1.0 || m7["cap"] != nil {
		t.Fatalf("m7n.2xlarge.8 = %v", m7)
	}
	// nat.2 has no footprint: listed as unmapped with quantity and region.
	un := ov["unmapped_skus"].([]any)
	if len(un) != 1 || un[0].(map[string]any)["sku"] != "nat.2" || un[0].(map[string]any)["quantity"] != 1.0 {
		t.Fatalf("unmapped = %v", un)
	}
	sum := ov["summary"].(map[string]any)
	for _, k := range []string{"regions", "zones", "pools", "pools_with_total", "pools_warn", "pools_critical", "pools_below_threshold", "skus", "unmapped_skus"} {
		if _, ok := sum[k]; !ok {
			t.Fatalf("summary lacks %q: %v", k, sum)
		}
	}
	if sum["pools"] != 14.0 || sum["pools_with_total"] != 1.0 || sum["unmapped_skus"] != 1.0 {
		t.Fatalf("summary = %v", sum)
	}
	// The region filter.
	if f := op.must("GET", "/api/v1/capacity/overview?region=me-east-215", 200); len(f["regions"].([]any)) != 1 {
		t.Fatalf("filtered = %v", f["regions"])
	}
	if f := op.must("GET", "/api/v1/capacity/overview?region=elsewhere", 200); len(f["regions"].([]any)) != 0 {
		t.Fatalf("unknown filter = %v", f["regions"])
	}

	// Every write left an audit row with its action and detail.
	rows, err := st.DB().QueryContext(ctx, `SELECT action, actor, details::text FROM audit_log WHERE action LIKE 'capacity.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	details := map[string][]string{}
	for rows.Next() {
		var action, actor, det string
		if err := rows.Scan(&action, &actor, &det); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action+"@"+actor)
		details[action] = append(details[action], det)
	}
	want := []string{
		"capacity.region@" + opEmail, "capacity.zone@bo@nc.example", "capacity.zone@" + opEmail,
		"capacity.pool@" + opEmail, "capacity.pool@bo@nc.example",
		"capacity.footprint@" + opEmail,
		"capacity.cap@" + opEmail, "capacity.cap@" + opEmail,
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("audit actions = %v, want %v", actions, want)
	}
	if d := details["capacity.pool"][0]; !strings.Contains(d, `"from": "0.000000"`) && !strings.Contains(d, `"from":"0.000000"`) || !strings.Contains(d, "two hosts of 10 vCPU") {
		t.Fatalf("pool audit detail = %s", d)
	}
	if d := details["capacity.cap"][1]; !strings.Contains(d, `"delete"`) {
		t.Fatalf("cap removal audit detail = %s", d)
	}

	// Zone and region deletion, audited; the viewer is refused.
	if rec, _ := fin.do("DELETE", "/api/v1/capacity/zones/"+zoneBID, "", nil); rec.Code != 403 {
		t.Fatalf("finance-viewer DELETE zone = %d", rec.Code)
	}
	op.must("DELETE", "/api/v1/capacity/zones/"+zoneBID, 200)
	op.must("DELETE", "/api/v1/capacity/regions/"+regionID, 200)
	if rec, _ := op.do("DELETE", "/api/v1/capacity/regions/"+regionID, "", nil); rec.Code != 404 {
		t.Fatalf("second delete = %d", rec.Code)
	}
	if ov := op.must("GET", "/api/v1/capacity/overview", 200); len(ov["regions"].([]any)) != 0 || len(ov["unmapped_regions"].([]any)) != 1 {
		t.Fatalf("after deleting the region its usage is unmapped: %v", ov["unmapped_regions"])
	}
}
