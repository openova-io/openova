package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Icons and branding, end to end (DESIGN.md §22.10): an icon uploaded once
// (and idempotently again), refused when it is too large, of the wrong type,
// an SVG that could run or reach out, or a PNG that is not one; served
// publicly with no session, immutable and sandboxed; named by a feature, a
// floor item, a group and a package through the existing writes; published
// in the packages document — the console and the public document byte for
// byte the same — and not deletable while anything shows it.

const testSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="#0F172A" stroke-width="2"><path d="M4 4h16v16H4z"/></svg>`

func TestIntegrationIconsUploadServeReferenceAndPublish(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	anon := anonClient(t, h)

	upload := func(c *client, ct, body string) (int, map[string]any, string) {
		t.Helper()
		rec, out := c.do("POST", "/api/v1/icons", ct, strings.NewReader(body))
		return rec.Code, out, rec.Body.String()
	}

	// ── upload: content-addressed and idempotent ───────────────────────
	code, first, raw := upload(op, "image/svg+xml", testSVG)
	if code != 201 {
		t.Fatalf("first upload = %d %s", code, raw)
	}
	id := first["id"].(string)
	if id != store.IconID([]byte(testSVG)) || first["src"] != "/api/v1/public/icons/"+id {
		t.Fatalf("upload = %v, want the sha256 id and its public path", first)
	}
	code, again, raw := upload(op, "image/svg+xml; charset=utf-8", testSVG)
	if code != 200 || again["id"] != id {
		t.Fatalf("second upload of the same bytes = %d %s, want 200 and the same id", code, raw)
	}
	if n := cellCount(t, st, `SELECT count(*) FROM icons`); n != 1 {
		t.Fatalf("%d icons stored after two uploads of the same bytes", n)
	}
	if n := cellCount(t, st, `SELECT count(*) FROM audit_log WHERE action = 'icon.upload'`); n != 1 {
		t.Fatalf("%d upload audit entries, want one (the second upload wrote nothing)", n)
	}

	// ── refusals ───────────────────────────────────────────────────────
	big := `<svg xmlns="http://www.w3.org/2000/svg"><!--` + strings.Repeat("x", store.IconMaxBytes) + `--></svg>`
	if code, _, raw := upload(op, "image/svg+xml", big); code != 413 || !strings.Contains(raw, "64 KiB") {
		t.Fatalf("oversized = %d %s", code, raw)
	}
	if code, _, raw := upload(op, "image/gif", "GIF89a"); code != 415 {
		t.Fatalf("gif = %d %s", code, raw)
	}
	if code, _, raw := upload(op, "", testSVG); code != 415 {
		t.Fatalf("no content type = %d %s", code, raw)
	}
	for name, c := range map[string]struct{ body, want string }{
		"script":           {`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, "<script>"},
		"onload":           {`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><path d="M0 0"/></svg>`, "event handler"},
		"onclick on child": {`<svg xmlns="http://www.w3.org/2000/svg"><rect onClick="x()"/></svg>`, "event handler"},
		"foreignObject":    {`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div xmlns="http://www.w3.org/1999/xhtml">x</div></foreignObject></svg>`, "foreignObject"},
		"external href":    {`<svg xmlns="http://www.w3.org/2000/svg"><image href="https://example.org/x.png"/></svg>`, "links out"},
		"external xlink":   {`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="other.svg#a"/></svg>`, "links out"},
		"javascript href":  {`<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><path d="M0 0"/></a></svg>`, "links out"},
		"entity":           {`<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg">&x;</svg>`, "ENTITY"},
		"doctype":          {`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd"><svg xmlns="http://www.w3.org/2000/svg"/>`, "DOCTYPE"},
		"not svg root":     {`<html><body/></html>`, "<svg> root"},
		"not xml":          {`<svg><path></svg>`, "well-formed"},
		"empty":            {``, "empty"},
	} {
		code, out, raw := upload(op, "image/svg+xml", c.body)
		if code != 400 || !strings.Contains(out["error"].(string), c.want) {
			t.Errorf("%s: = %d %s, want 400 naming %q", name, code, raw, c.want)
		}
	}
	// An in-document reference is fine.
	local := `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><defs><path id="p" d="M0 0h4v4z"/></defs><use xlink:href="#p"/><use href="#p"/></svg>`
	if code, _, raw := upload(op, "image/svg+xml", local); code != 201 {
		t.Fatalf("an SVG referencing its own #id = %d %s", code, raw)
	}
	// PNG / WebP: the magic must match the declared type.
	if code, out, raw := upload(op, "image/png", testSVG); code != 400 || !strings.Contains(out["error"].(string), "not a PNG") {
		t.Fatalf("svg declared png = %d %s", code, raw)
	}
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	if code, _, raw := upload(op, "image/webp", png); code != 400 || !strings.Contains(raw, "WebP") {
		t.Fatalf("png declared webp = %d %s", code, raw)
	}
	code, pngUp, raw := upload(op, "image/png", png)
	if code != 201 {
		t.Fatalf("a png = %d %s", code, raw)
	}
	webp := "RIFF\x24\x00\x00\x00WEBPVP8 "
	if code, _, raw := upload(op, "image/webp", webp); code != 201 {
		t.Fatalf("a webp = %d %s", code, raw)
	}

	// ── who may ────────────────────────────────────────────────────────
	if code, _, _ := upload(anon, "image/svg+xml", testSVG); code != 401 {
		t.Fatalf("anonymous upload = %d", code)
	}
	if rec, _ := anon.do("GET", "/api/v1/icons", "", nil); rec.Code != 401 {
		t.Fatalf("anonymous list = %d", rec.Code)
	}
	if rec, _ := anon.do("DELETE", "/api/v1/icons/"+id, "", nil); rec.Code != 401 {
		t.Fatalf("anonymous delete = %d", rec.Code)
	}
	if rec, _ := anon.json("PUT", "/api/v1/feature-groups/ops", map[string]any{"icon_id": id}); rec.Code != 401 {
		t.Fatalf("anonymous group icon = %d", rec.Code)
	}

	// ── the public GET: no session, the stored type, immutable, sandboxed ──
	rec, _ := anon.do("GET", "/api/v1/public/icons/"+id, "", nil)
	if rec.Code != 200 || rec.Body.String() != testSVG {
		t.Fatalf("public icon = %d %q", rec.Code, rec.Body.String())
	}
	for k, want := range map[string]string{
		"Content-Type":                 "image/svg+xml",
		"Cache-Control":                "public, max-age=31536000, immutable",
		"X-Content-Type-Options":       "nosniff",
		"Content-Security-Policy":      "default-src 'none'; style-src 'unsafe-inline'; sandbox",
		"Cross-Origin-Resource-Policy": "cross-origin",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("public icon %s = %q, want %q", k, got, want)
		}
	}
	rec, _ = anon.do("GET", "/api/v1/public/icons/"+pngUp["id"].(string), "", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Body.String() != png {
		t.Fatalf("public png = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec, _ := anon.do("GET", "/api/v1/public/icons/"+strings.Repeat("0", 64), "", nil); rec.Code != 404 {
		t.Fatalf("unknown icon = %d", rec.Code)
	}
	if rec, _ := anon.do("GET", "/api/v1/public/icons/not-an-id", "", nil); rec.Code != 404 {
		t.Fatalf("malformed id = %d", rec.Code)
	}

	// ── the console's list ─────────────────────────────────────────────
	list := op.must("GET", "/api/v1/icons", 200)["icons"].([]any)
	if len(list) != 4 {
		t.Fatalf("icons = %v", list)
	}
	if ic := list[0].(map[string]any); ic["id"] != id || ic["content_type"] != "image/svg+xml" || ic["size"] != float64(len(testSVG)) || ic["references"] != float64(0) || ic["src"] != "/api/v1/public/icons/"+id {
		t.Fatalf("listed icon = %v", ic)
	}

	// ── referencing: a feature, a floor item, a group, a package ───────
	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bookPath := "/api/v1/pricebooks/" + plans.ID
	unknown := strings.Repeat("f", 64)
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "backup", "name": "Backup", "group": "resilience", "icon_id": unknown}); rec.Code != 400 || !strings.Contains(out["error"].(string), "no icon") {
		t.Fatalf("feature with an unknown icon = %d %v", rec.Code, out)
	}
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "backup", "name": "Backup", "group": "resilience", "icon_bg": "red"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "#RRGGBB") {
		t.Fatalf("feature with a bad colour = %d %v", rec.Code, out)
	}
	backup := op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "backup", "name": "Backup", "group": "resilience", "icon_id": id, "icon_bg": "#ffe4e6"}, 201)
	if backup["icon_id"] != id || backup["icon_bg"] != "#FFE4E6" {
		t.Fatalf("feature = %v, want the icon and the colour upper-cased", backup)
	}
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "ssl", "name": "Unlimited free SSL", "group": "floor", "icon_id": id}, 201)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "domain", "name": "Domain", "group": "scope"}, 201)
	if rec, out := op.json("PATCH", "/api/v1/features/domain", map[string]any{"icon_id": "nope"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "64-character") {
		t.Fatalf("patch a malformed icon id = %d %v", rec.Code, out)
	}
	op.mustJSON("PUT", bookPath+"/packages/plan.s/features/backup", map[string]any{"state": "included"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.s/features/domain", map[string]any{"state": "included"}, 200)
	// A group's icon; unknown group 404, unknown icon 400.
	g := op.mustJSON("PUT", "/api/v1/feature-groups/resilience", map[string]any{"icon_id": id}, 200)
	if g["key"] != "resilience" || g["name"] != "Resilience" || g["icon_id"] != id {
		t.Fatalf("group = %v", g)
	}
	if rec, _ := op.json("PUT", "/api/v1/feature-groups/misc", map[string]any{"icon_id": id}); rec.Code != 404 {
		t.Fatalf("unknown group = %d", rec.Code)
	}
	if rec, _ := op.json("PUT", "/api/v1/feature-groups/ops", map[string]any{"icon_id": unknown}); rec.Code != 400 {
		t.Fatalf("group with an unknown icon = %d", rec.Code)
	}
	if groups := op.must("GET", "/api/v1/features", 200)["groups"].([]any); groups[5].(map[string]any)["icon_id"] != id || groups[0].(map[string]any)["icon_id"] != nil {
		t.Fatalf("features list groups = %v", groups)
	}
	// A package: icon, accent, badge — validated; whole like the rest.
	settings := map[string]any{"recommended": true, "icon_id": id, "accent": "#3b82f6", "badge": "Most popular"}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/settings", map[string]any{"accent": "#3b82f"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "#RRGGBB") {
		t.Fatalf("bad accent = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/settings", map[string]any{"badge": strings.Repeat("b", 25)}); rec.Code != 400 || !strings.Contains(out["error"].(string), "24 characters") {
		t.Fatalf("long badge = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/settings", map[string]any{"icon_id": unknown}); rec.Code != 400 || !strings.Contains(out["error"].(string), "no icon") {
		t.Fatalf("package with an unknown icon = %d %v", rec.Code, out)
	}
	ps := op.mustJSON("PUT", bookPath+"/packages/plan.m/settings", settings, 200)
	if ps["icon_id"] != id || ps["accent"] != "#3B82F6" || ps["badge"] != "Most popular" {
		t.Fatalf("settings = %v", ps)
	}

	// ── the document: present where set, absent where not, identical bytes ──
	pub, _ := anon.do("GET", "/api/v1/public/packages", "", nil)
	con, _ := op.do("GET", bookPath+"/packages", "", nil)
	if pub.Code != 200 || con.Code != 200 || !bytes.Equal(pub.Body.Bytes(), con.Body.Bytes()) {
		t.Fatalf("public %d and console %d documents differ:\n%s\n%s", pub.Code, con.Code, pub.Body.String(), con.Body.String())
	}
	var doc struct {
		Groups   []map[string]any `json:"groups"`
		Floor    []map[string]any `json:"floor"`
		Packages []map[string]any `json:"packages"`
		Features []map[string]any `json:"features"`
	}
	if err := json.Unmarshal(pub.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	src := "/api/v1/public/icons/" + id
	if ic := doc.Groups[5]["icon"].(map[string]any); doc.Groups[5]["key"] != "resilience" || ic["src"] != src || ic["alt"] != "Resilience" || len(ic) != 2 {
		t.Fatalf("group in the document = %v", doc.Groups[5])
	}
	if _, has := doc.Groups[0]["icon"]; has {
		t.Fatalf("a group with no icon = %v", doc.Groups[0])
	}
	if ic := doc.Floor[0]["icon"].(map[string]any); ic["src"] != src || ic["alt"] != "Unlimited free SSL" {
		t.Fatalf("floor in the document = %v", doc.Floor[0])
	}
	for _, f := range doc.Features {
		switch f["key"] {
		case "backup":
			if ic := f["icon"].(map[string]any); ic["src"] != src || ic["alt"] != "Backup" || ic["bg"] != "#FFE4E6" {
				t.Fatalf("backup in the document = %v", f)
			}
		case "domain":
			if _, has := f["icon"]; has {
				t.Fatalf("domain has no icon = %v", f)
			}
		}
	}
	for _, p := range doc.Packages {
		switch p["sku"] {
		case "plan.m":
			if ic := p["icon"].(map[string]any); ic["src"] != src || ic["alt"] != "M" || p["accent"] != "#3B82F6" || p["badge"] != "Most popular" {
				t.Fatalf("M in the document = %v", p)
			}
		default:
			for _, k := range []string{"icon", "accent", "badge"} {
				if _, has := p[k]; has {
					t.Fatalf("%v carries %s though nothing is set", p["sku"], k)
				}
			}
		}
	}

	// ── delete: refused while shown, then allowed ──────────────────────
	rec, out := op.do("DELETE", "/api/v1/icons/"+id, "", nil)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "still shown") {
		t.Fatalf("delete in use = %d %v", rec.Code, out)
	}
	if det := out["details"].(map[string]any); det["features"] != float64(2) || det["groups"] != float64(1) || det["packages"] != float64(1) {
		t.Fatalf("delete in use details = %v", out)
	}
	if ic := op.must("GET", "/api/v1/icons", 200)["icons"].([]any)[0].(map[string]any); ic["references"] != float64(4) {
		t.Fatalf("listed references = %v", ic)
	}
	// Clear every reference: "" clears on the feature and the group; the
	// settings are whole, so leaving icon_id out clears it.
	op.mustJSON("PATCH", "/api/v1/features/backup", map[string]any{"icon_id": "", "icon_bg": ""}, 200)
	op.mustJSON("PATCH", "/api/v1/features/ssl", map[string]any{"icon_id": ""}, 200)
	op.mustJSON("PUT", "/api/v1/feature-groups/resilience", map[string]any{"icon_id": ""}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.m/settings", map[string]any{"recommended": true}, 200)
	pub, _ = anon.do("GET", "/api/v1/public/packages", "", nil)
	if strings.Contains(pub.Body.String(), `"icon"`) || strings.Contains(pub.Body.String(), `"accent"`) || strings.Contains(pub.Body.String(), `"badge"`) {
		t.Fatalf("cleared branding still published: %s", pub.Body.String())
	}
	op.must("DELETE", "/api/v1/icons/"+id, 200)
	if rec, _ := anon.do("GET", "/api/v1/public/icons/"+id, "", nil); rec.Code != 404 {
		t.Fatalf("deleted icon = %d", rec.Code)
	}
	if rec, _ := op.do("DELETE", "/api/v1/icons/"+id, "", nil); rec.Code != 404 {
		t.Fatalf("delete twice = %d", rec.Code)
	}
}

// The public icon route answers the storefront's origin like the document,
// ignores a cookie, and charges its OWN budget: a page loading every icon of
// the package table never starves the document of the calculator's budget.
func TestIntegrationPublicIconCORSAndBudget(t *testing.T) {
	_, st, _, _, _ := setupAPI(t)
	ic, created, err := st.PutIcon(context.Background(), store.IconSVG, []byte(testSVG))
	if err != nil || !created {
		t.Fatalf("put = %v %v", created, err)
	}
	if _, created, err := st.PutIcon(context.Background(), store.IconSVG, []byte(testSVG)); err != nil || created {
		t.Fatalf("second put = created %v, %v; want the same row", created, err)
	}
	h := publicHandler(t, st, []string{"https://marketplace.t99.omani.works"}, 2, nil)
	call := func(path, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "not-a-session"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	rec := call("/api/v1/public/icons/"+ic.ID, "https://marketplace.t99.omani.works")
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://marketplace.t99.omani.works" {
		t.Fatalf("storefront origin = %d %v", rec.Code, rec.Header())
	}
	if rec := call("/api/v1/public/icons/"+ic.ID, "https://evil.example"); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("an unlisted origin must not be allowed: %v", rec.Header())
	}
	// Two a minute for the calculator routes means twenty for the icons:
	// eighteen more icon loads pass, and the document still has its two.
	for i := 0; i < 18; i++ {
		if rec := call("/api/v1/public/icons/"+ic.ID, ""); rec.Code != 200 {
			t.Fatalf("icon load %d = %d", i+3, rec.Code)
		}
	}
	if rec := call("/api/v1/public/icons/"+ic.ID, ""); rec.Code != 429 {
		t.Fatalf("icon load 21 = %d, want the icon budget spent", rec.Code)
	}
	if rec := call("/api/v1/public/packages", ""); rec.Code == 429 {
		t.Fatalf("the document shares no budget with the icons: %d", rec.Code)
	}
}
