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
// customer principal sees nothing — the overview's wire shape, the basket
// endpoint, and the audit trail every write leaves.

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
	reads := []string{"/api/v1/capacity/overview", "/api/v1/capacity/regions", "/api/v1/capacity/shapes", "/api/v1/capacity/placements", "/api/v1/capacity/resources"}
	for _, c := range []*client{op, fin, bo} {
		ov := c.must("GET", "/api/v1/capacity/overview", 200)
		if ov["as_of"] != nil || len(ov["regions"].([]any)) != 0 {
			t.Fatalf("empty overview = %v", ov)
		}
		for _, path := range reads[1:] {
			c.must("GET", path, 200)
		}
	}
	for _, path := range reads {
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
	// A ZONE IS BORN EMPTY: a pool is machines somebody bought.
	if zoneA["is_default"] != true || zoneB["is_default"] != false || len(zoneA["pools"].([]any)) != 0 {
		t.Fatalf("zones = %v / %v", zoneA, zoneB)
	}
	zoneAID, zoneBID := zoneA["id"].(string), zoneB["id"].(string)

	poolBody := map[string]any{
		"name": "m7n-a", "machines": "10", "lead_time_days": 45, "note": "batch one",
		"resources": []any{
			map[string]any{"resource": "vcpu", "per_machine": "64", "reserve": "64", "overcommit_ratio": "4"},
			map[string]any{"resource": "memory_gib", "per_machine": "512", "reserve": "512", "overcommit_ratio": "1"},
		},
	}
	if rec, _ := fin.json("POST", "/api/v1/capacity/zones/"+zoneAID+"/pools", poolBody); rec.Code != 403 || !strings.Contains(rec.Body.String(), "capacity.manage") {
		t.Fatalf("finance-viewer POST pool = %d %s", rec.Code, rec.Body.String())
	}
	pool := op.mustJSON("POST", "/api/v1/capacity/zones/"+zoneAID+"/pools", poolBody, 201)
	poolID := pool["id"].(string)
	if pool["name"] != "m7n-a" || pool["machines"] != 10.0 || pool["lead_time_days"] != 45.0 || len(pool["resources"].([]any)) != 2 {
		t.Fatalf("pool = %v", pool)
	}
	// A SECOND POOL OF THE SAME RESOURCE KIND in the same zone: the whole
	// reason the family pools had to go.
	second := bo.mustJSON("POST", "/api/v1/capacity/zones/"+zoneAID+"/pools", map[string]any{
		"name": "m7n-b", "machines": "5",
		"resources": []any{map[string]any{"resource": "vcpu", "per_machine": "64", "overcommit_ratio": "4"}},
	}, 201)
	if rec, _ := op.json("POST", "/api/v1/capacity/zones/"+zoneAID+"/pools", poolBody); rec.Code != 409 {
		t.Fatalf("a duplicate pool NAME = %d, want 409", rec.Code)
	}
	for _, bad := range []map[string]any{
		{"machines": "1", "resources": []any{map[string]any{"resource": "vcpu", "per_machine": "1"}}},
		{"name": "x", "machines": "1"},
		{"name": "x", "machines": "-1", "resources": []any{map[string]any{"resource": "vcpu", "per_machine": "1"}}},
	} {
		if rec, _ := op.json("POST", "/api/v1/capacity/zones/"+zoneAID+"/pools", bad); rec.Code != 400 {
			t.Fatalf("POST pool %v = %d, want 400", bad, rec.Code)
		}
	}

	// Resizing: two more servers, and the RAM ratio changes with them.
	if rec, _ := owner.json("PUT", "/api/v1/capacity/pools/"+poolID, poolBody); rec.Code != 403 {
		t.Fatalf("customer-owner PUT pool = %d", rec.Code)
	}
	grown := map[string]any{
		"name": "m7n-a", "machines": "12", "lead_time_days": 45, "note": "two more hosts",
		"resources": []any{
			map[string]any{"resource": "vcpu", "per_machine": "64", "reserve": "64", "overcommit_ratio": "4"},
			map[string]any{"resource": "memory_gib", "per_machine": "512", "reserve": "512", "overcommit_ratio": "1.5"},
		},
	}
	set := bo.mustJSON("PUT", "/api/v1/capacity/pools/"+poolID, grown, 200)
	if set["machines"] != 12.0 || set["note"] != "two more hosts" || set["updated_by"] != "bo@nc.example" || set["source"] != capacity.SourceManual {
		t.Fatalf("resized pool = %v", set)
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/pools/00000000-0000-0000-0000-000000000000", poolBody); rec.Code != 404 {
		t.Fatalf("unknown pool = %d", rec.Code)
	}
	// Put it back at ten servers with RAM at 1:1 — the worked example.
	op.mustJSON("PUT", "/api/v1/capacity/pools/"+poolID, poolBody, 200)

	// The pools document carries the zone, the pools, the history per pool
	// (one row per resource per change) and the vocabulary the editor needs.
	pools := fin.must("GET", "/api/v1/capacity/zones/"+zoneAID+"/pools", 200)
	if len(pools["pools"].([]any)) != 2 || pools["zone"].(map[string]any)["code"] != "me-east-215a" {
		t.Fatalf("pools = %v", pools)
	}
	if len(pools["resource_kinds"].([]any)) < 7 || len(pools["classes"].([]any)) != 3 {
		t.Fatalf("pools vocabulary = %v / %v", pools["resource_kinds"], pools["classes"])
	}
	hist := pools["history"].(map[string]any)[poolID].([]any)
	if len(hist) != 6 { // 2 resources × (create + resize + put back)
		t.Fatalf("history = %d rows: %v", len(hist), hist)
	}
	if h0 := hist[0].(map[string]any); h0["resource"] == nil || h0["total"] == nil || h0["machines"] == nil {
		t.Fatalf("a history row names the resource and its size: %v", h0)
	}

	// Shapes: seeded rows listed, PUT replaces, an operator's own resource
	// kind is accepted because there is no list to be on.
	shapes := fin.must("GET", "/api/v1/capacity/shapes", 200)
	if len(shapes["shapes"].([]any)) != 6 || len(shapes["resource_kinds"].([]any)) < 7 {
		t.Fatalf("shapes = %v", shapes)
	}
	if un := shapes["unseeded_skus"].([]any); len(un) != 3 || un[0] != "elb" {
		t.Fatalf("unseeded = %v", un)
	}
	if rec, _ := fin.json("PUT", "/api/v1/capacity/shapes/nat.1", map[string]any{"resources": map[string]any{"eip_addresses": 1}}); rec.Code != 403 {
		t.Fatalf("finance-viewer PUT shape = %d", rec.Code)
	}
	sh := op.mustJSON("PUT", "/api/v1/capacity/shapes/nat.1", map[string]any{"resources": map[string]any{"eip_addresses": "1", "bandwidth_mbps": 0}}, 200)
	if res := sh["resources"].(map[string]any); len(res) != 1 || res["eip_addresses"] != 1.0 || sh["source"] != capacity.SourceManual {
		t.Fatalf("put shape = %v", sh)
	}
	op.mustJSON("PUT", "/api/v1/capacity/shapes/ecs.gpu.large", map[string]any{"resources": map[string]any{"gpu_cards": "1", "vcpu": "8"}}, 200)
	if rec, _ := op.json("PUT", "/api/v1/capacity/shapes/nat.1", map[string]any{}); rec.Code != 400 {
		t.Fatalf("missing resources = %d", rec.Code)
	}

	// Resource kinds carry words an operator can supply.
	kind := op.mustJSON("PUT", "/api/v1/capacity/resources/gpu_cards", map[string]any{"label": "GPU cards", "unit": "cards"}, 200)
	if kind["resource"] != "gpu_cards" || kind["label"] != "GPU cards" || kind["unit"] != "cards" {
		t.Fatalf("resource kind = %v", kind)
	}
	if rec, _ := fin.json("PUT", "/api/v1/capacity/resources/gpu_cards", map[string]any{"label": "x"}); rec.Code != 403 {
		t.Fatalf("finance-viewer PUT resource kind = %d", rec.Code)
	}

	// Placements: the class lives here.
	if rec, _ := fin.json("PUT", "/api/v1/capacity/placements", map[string]any{"pool_id": poolID, "sku": "ecs.m7n.2xlarge.8", "class": "guaranteed"}); rec.Code != 403 {
		t.Fatalf("finance-viewer PUT placement = %d", rec.Code)
	}
	pl := op.mustJSON("PUT", "/api/v1/capacity/placements", map[string]any{"pool_id": poolID, "sku": "ecs.m7n.2xlarge.8", "class": "guaranteed"}, 200)
	if pl["class"] != capacity.ClassGuaranteed || pl["pool_name"] != "m7n-a" || pl["zone_code"] != "me-east-215a" {
		t.Fatalf("placement = %v", pl)
	}
	// A shape that shares no resource with the pool would consume nothing.
	if rec, _ := op.json("PUT", "/api/v1/capacity/placements", map[string]any{"pool_id": second["id"], "sku": "evs.ssd.gb", "class": "guaranteed"}); rec.Code != 409 && rec.Code != 400 {
		t.Fatalf("placing a block SKU on a vCPU pool = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/placements", map[string]any{"sku": "x", "class": "spot"}); rec.Code != 400 {
		t.Fatalf("placement without a pool = %d", rec.Code)
	}
	if pls := op.must("GET", "/api/v1/capacity/placements", 200); len(pls["placements"].([]any)) != 1 || len(pls["classes"].([]any)) != 3 {
		t.Fatalf("placements = %v", pls)
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
			recs = append(recs, store.UsageRecord{CustomerID: custID, SourceID: src.ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: "ecs.m7n.2xlarge.8", Quantity: "40", Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215"})
			recs = append(recs, store.UsageRecord{CustomerID: custID, SourceID: src.ID, ResourceID: "nat-1", ResourceKind: "nat", SKU: "nat.2", Quantity: "1", Unit: "hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	ov := fin.must("GET", "/api/v1/capacity/overview", 200)
	for _, k := range []string{"as_of", "sources", "lagging_sources", "thresholds", "classes", "resource_kinds", "regions", "unshaped_skus", "unmapped_regions", "summary"} {
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
	zones := ov["regions"].([]any)[0].(map[string]any)["zones"].([]any)
	if len(zones) != 2 {
		t.Fatalf("zones = %v", zones)
	}
	za := zones[0].(map[string]any)
	if za["code"] != "me-east-215a" || za["is_default"] != true {
		t.Fatalf("zone a = %v", za)
	}
	var m7 map[string]any
	for _, p := range za["pools"].([]any) {
		if p.(map[string]any)["name"] == "m7n-a" {
			m7 = p.(map[string]any)
		}
	}
	for _, k := range []string{"id", "zone_id", "name", "machines", "lead_time_days", "resources", "resources_view", "placements", "basket", "status", "binding_resource", "utilisation_pct", "order_by_days", "order_by_date", "order_by_resource", "order_by_wall", "late", "zone_unknown"} {
		if _, ok := m7[k]; !ok {
			t.Fatalf("pool lacks %q: %v", k, m7)
		}
	}
	var cpu map[string]any
	for _, r := range m7["resources_view"].([]any) {
		if r.(map[string]any)["resource"] == "vcpu" {
			cpu = r.(map[string]any)
		}
	}
	for _, k := range []string{"raw", "usable", "sellable", "guaranteed", "burstable", "burstable_physical", "spot", "spot_physical", "sold_nominal",
		"remaining", "guaranteed_ceiling", "physical_used", "physical_free", "stranded", "spot_room", "spot_reclaim", "sized", "utilisation_pct",
		"status", "overcommitted", "over", "series", "history_days", "soft_wall_days", "soft_wall_date", "hard_wall_days", "hard_wall_date",
		"order_by_days", "order_by_date", "order_by_wall", "late", "per_machine", "reserve", "overcommit_ratio", "label", "unit"} {
		if _, ok := cpu[k]; !ok {
			t.Fatalf("resource lacks %q: %v", k, cpu)
		}
	}
	// 40 guaranteed m7n.2xlarge: 320 of 576 usable vCPU, sellable 1,344.
	if cpu["usable"] != 576.0 || cpu["guaranteed"] != 320.0 || cpu["sellable"] != 1344.0 || cpu["burstable"] != 0.0 {
		t.Fatalf("vcpu = %v", cpu)
	}
	// RAM: 2,560 of 4,608 at 1:1 — 55 %, the binding resource.
	if m7["binding_resource"] != "memory_gib" {
		t.Fatalf("binding resource = %v", m7["binding_resource"])
	}
	// A flat series: growth 0 on every class, so no wall and no order-by.
	if cpu["soft_wall_days"] != nil || cpu["order_by_date"] != nil || cpu["history_days"] != 8.0 {
		t.Fatalf("vcpu walls on a flat series = %v / %v / %v", cpu["soft_wall_days"], cpu["order_by_date"], cpu["history_days"])
	}
	series := cpu["series"].([]any)
	if len(series) != 1 || series[0].(map[string]any)["class"] != capacity.ClassGuaranteed {
		t.Fatalf("the series is split by class: %v", series)
	}
	// The basket: the mix selling, and how many more of it fit.
	basket := m7["basket"].(map[string]any)
	for _, k := range []string{"items", "units", "reason", "binding_resource", "resources", "unshaped_skus"} {
		if _, ok := basket[k]; !ok {
			t.Fatalf("basket lacks %q: %v", k, basket)
		}
	}
	// 1,024 nominal vCPU left ÷ (8 × 4) = 32 baskets; 2,048 GiB left ÷ 64 =
	// 32 as well, so the two tie and the first in display order is reported.
	if basket["units"] != 32.0 || basket["binding_resource"] != "vcpu" {
		t.Fatalf("basket = %v", basket)
	}
	// nat.2 has no shape: listed by name, counted against nothing.
	un := ov["unshaped_skus"].([]any)
	if len(un) != 1 || un[0].(map[string]any)["sku"] != "nat.2" || un[0].(map[string]any)["quantity"] != 1.0 {
		t.Fatalf("unshaped = %v", un)
	}
	sum := ov["summary"].(map[string]any)
	for _, k := range []string{"regions", "zones", "pools", "pools_sized", "pools_warn", "pools_critical", "pools_past_threshold", "pools_to_order", "pools_order_late", "placements", "shapes", "unplaced_skus", "unshaped_skus", "spot_to_reclaim"} {
		if _, ok := sum[k]; !ok {
			t.Fatalf("summary lacks %q: %v", k, sum)
		}
	}
	if sum["pools"] != 2.0 || sum["pools_sized"] != 2.0 || sum["unshaped_skus"] != 1.0 || sum["placements"] != 1.0 {
		t.Fatalf("summary = %v", sum)
	}

	// The basket endpoint: a mix the operator names, with the binding
	// resource. Two more 2xlarge per basket → 16 baskets.
	hr := fin.must("GET", "/api/v1/capacity/pools/"+poolID+"/headroom?basket=ecs.m7n.2xlarge.8:2", 200)
	b2 := hr["basket"].(map[string]any)
	if b2["units"] != 16.0 || b2["binding_resource"] != "vcpu" {
		t.Fatalf("named basket = %v", b2)
	}
	if rec, _ := owner.do("GET", "/api/v1/capacity/pools/"+poolID+"/headroom", "", nil); rec.Code != 403 {
		t.Fatalf("customer-owner headroom = %d", rec.Code)
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
		"capacity.pool@" + opEmail, "capacity.pool@bo@nc.example", "capacity.pool@bo@nc.example", "capacity.pool@" + opEmail,
		"capacity.shape@" + opEmail, "capacity.shape@" + opEmail,
		"capacity.resource@" + opEmail,
		"capacity.placement@" + opEmail,
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("audit actions = %v, want %v", actions, want)
	}
	// A pool audit carries what it was and what it became, vector and all.
	if d := details["capacity.pool"][2]; !strings.Contains(d, `"from"`) || !strings.Contains(d, "two more hosts") || !strings.Contains(d, "overcommit_ratio") {
		t.Fatalf("pool audit detail = %s", d)
	}
	if d := details["capacity.placement"][0]; !strings.Contains(d, "guaranteed") {
		t.Fatalf("placement audit detail = %s", d)
	}

	// Deletion, audited; the viewer is refused.
	if rec, _ := fin.do("DELETE", "/api/v1/capacity/pools/"+second["id"].(string), "", nil); rec.Code != 403 {
		t.Fatalf("finance-viewer DELETE pool = %d", rec.Code)
	}
	op.must("DELETE", "/api/v1/capacity/pools/"+second["id"].(string), 200)
	if rec, _ := op.json("PUT", "/api/v1/capacity/placements", map[string]any{"pool_id": poolID, "sku": "ecs.m7n.2xlarge.8", "class": nil}); rec.Code != 200 {
		t.Fatalf("removing a placement = %d %s", rec.Code, rec.Body.String())
	}
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
