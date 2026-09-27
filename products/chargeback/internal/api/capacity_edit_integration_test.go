package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// PUT /capacity/regions/{id} and PUT /capacity/zones/{id} (#6946): a rename
// persists, the default moves in one write and the only default cannot be
// unset, every refusal is a sentence with no constraint name in it, the
// viewer is refused naming capacity.manage, and every write leaves an audit
// row carrying what changed. Everything is asserted on the HTTP response.
func TestIntegrationCapacityRegionAndZoneEdit(t *testing.T) {
	h, st, mail := setupAPIAt(t, time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC))
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	op.mustJSON("POST", "/api/v1/access/bindings", map[string]any{"subject_email": "fin@nc.example", "role": store.RoleFinanceViewer}, 201)
	fin := &client{t: t, h: h}
	fin.signIn("fin@nc.example", mail)

	region := op.mustJSON("POST", "/api/v1/capacity/regions", map[string]any{"code": "me-east-215", "name": "Muscat"}, 201)
	regionID := region["id"].(string)
	other := op.mustJSON("POST", "/api/v1/capacity/regions", map[string]any{"code": "eu-west-101"}, 201)
	zoneA := op.mustJSON("POST", "/api/v1/capacity/regions/"+regionID+"/zones", map[string]any{"code": "me-east-215a", "name": "AZ 1"}, 201)
	zoneB := op.mustJSON("POST", "/api/v1/capacity/regions/"+regionID+"/zones", map[string]any{"code": "me-east-215b"}, 201)
	zoneAID, zoneBID := zoneA["id"].(string), zoneB["id"].(string)

	// The viewer is refused, naming the permission, before anything is read.
	for _, path := range []string{"/api/v1/capacity/regions/" + regionID, "/api/v1/capacity/zones/" + zoneAID} {
		rec, _ := fin.json("PUT", path, map[string]any{"name": "x"})
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "capacity.manage") {
			t.Fatalf("finance-viewer PUT %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// A region rename and name change persist, the kind stays; the code is
	// normalised as on create.
	edited := op.mustJSON("PUT", "/api/v1/capacity/regions/"+regionID, map[string]any{"code": " ME-East-216 ", "name": "Salalah"}, 200)
	if edited["code"] != "me-east-216" || edited["name"] != "Salalah" || edited["cloud_source_kind"] != store.SourceKindHuaweiProject {
		t.Fatalf("edited region = %v", edited)
	}
	list := op.must("GET", "/api/v1/capacity/regions", 200)
	var found bool
	for _, r := range list["regions"].([]any) {
		if rm := r.(map[string]any); rm["id"] == regionID {
			found = rm["code"] == "me-east-216" && rm["name"] == "Salalah"
		}
	}
	if !found {
		t.Fatalf("the rename is not what GET returns: %v", list["regions"])
	}
	kindOnly := op.mustJSON("PUT", "/api/v1/capacity/regions/"+regionID, map[string]any{"cloud_source_kind": "file"}, 200)
	if kindOnly["cloud_source_kind"] != "file" || kindOnly["code"] != "me-east-216" || kindOnly["name"] != "Salalah" {
		t.Fatalf("a kind-only patch touched other fields: %v", kindOnly)
	}
	// Refusals: an empty patch, a platform kind, an empty code, an unknown
	// id, a code another region holds — each a sentence, none a constraint.
	if rec, _ := op.json("PUT", "/api/v1/capacity/regions/"+regionID, map[string]any{}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "nothing to change") {
		t.Fatalf("empty patch = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/regions/"+regionID, map[string]any{"cloud_source_kind": "openova-org"}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "cloud_source_kind must be one of") {
		t.Fatalf("platform kind = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/regions/"+regionID, map[string]any{"code": "  "}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "code is required") {
		t.Fatalf("empty code = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/regions/nope", map[string]any{"name": "x"}); rec.Code != 404 {
		t.Fatalf("unknown region = %d %s", rec.Code, rec.Body.String())
	}
	rec, _ := op.json("PUT", "/api/v1/capacity/regions/"+other["id"].(string), map[string]any{"code": "ME-EAST-216"})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "a region with that code already exists") {
		t.Fatalf("duplicate region code = %d %s", rec.Code, rec.Body.String())
	}
	for _, leak := range []string{"_key", "_idx", "Key (", "capacity_regions"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("the 409 leaks the schema (%q): %s", leak, rec.Body.String())
		}
	}

	// A zone rename persists; then the default MOVES onto b in one write and
	// a has lost it when read back.
	zb := op.mustJSON("PUT", "/api/v1/capacity/zones/"+zoneBID, map[string]any{"code": "ME-EAST-216B", "name": "AZ 2"}, 200)
	if zb["code"] != "me-east-216b" || zb["name"] != "AZ 2" || zb["is_default"] != false || zb["region_code"] != "me-east-216" {
		t.Fatalf("renamed zone = %v", zb)
	}
	zb = op.mustJSON("PUT", "/api/v1/capacity/zones/"+zoneBID, map[string]any{"default": true}, 200)
	if zb["is_default"] != true {
		t.Fatalf("b after the handoff = %v", zb)
	}
	regions := op.must("GET", "/api/v1/capacity/regions", 200)
	defaults := 0
	for _, r := range regions["regions"].([]any) {
		rm := r.(map[string]any)
		if rm["id"] != regionID {
			continue
		}
		for _, z := range rm["zones"].([]any) {
			zm := z.(map[string]any)
			if zm["is_default"] == true {
				defaults++
				if zm["id"] != zoneBID {
					t.Fatalf("the default is still %v after moving it to b", zm["code"])
				}
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("the region holds %d defaults, want 1", defaults)
	}
	// The only default cannot be unset, and the refusal says why.
	rec, _ = op.json("PUT", "/api/v1/capacity/zones/"+zoneBID, map[string]any{"default": false})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "a region always has a default zone") || !strings.Contains(rec.Body.String(), "me-east-216b") {
		t.Fatalf("unset the only default = %d %s", rec.Code, rec.Body.String())
	}
	if again := op.must("GET", "/api/v1/capacity/zones/"+zoneBID+"/pools", 200); again["zone"].(map[string]any)["is_default"] != true {
		t.Fatalf("the refusal changed the row: %v", again["zone"])
	}
	// A duplicate zone code, an empty patch, an unknown zone.
	rec, _ = op.json("PUT", "/api/v1/capacity/zones/"+zoneAID, map[string]any{"code": "me-east-216b"})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "a zone with that code already exists in this region") || strings.Contains(rec.Body.String(), "_key") || strings.Contains(rec.Body.String(), "capacity_zones") {
		t.Fatalf("duplicate zone code = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/zones/"+zoneAID, map[string]any{}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "nothing to change") {
		t.Fatalf("empty zone patch = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := op.json("PUT", "/api/v1/capacity/zones/nope", map[string]any{"name": "x"}); rec.Code != 404 {
		t.Fatalf("unknown zone = %d %s", rec.Code, rec.Body.String())
	}

	// One audit row per successful write, with what it was and what it became.
	rows, err := st.DB().QueryContext(ctx, `SELECT action, actor, details::text FROM audit_log WHERE details->>'op' = 'set' AND action IN ('capacity.region', 'capacity.zone') ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var action, actor, det string
		if err := rows.Scan(&action, &actor, &det); err != nil {
			t.Fatal(err)
		}
		if actor != opEmail || !strings.Contains(det, `"from"`) || !strings.Contains(det, `"to"`) {
			t.Fatalf("audit %s by %s lacks from/to: %s", action, actor, det)
		}
		got = append(got, action)
	}
	// Two region writes (rename, kind) and two zone writes (rename, default);
	// none of the refused ones left a row.
	if want := "capacity.region,capacity.region,capacity.zone,capacity.zone"; strings.Join(got, ",") != want {
		t.Fatalf("audit rows = %v, want %s", got, want)
	}
}
