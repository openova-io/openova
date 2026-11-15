package api

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/offer"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// TestIntegrationDefaultOfferIsThePublishedDocument: on a fresh database the
// public packages document answers 404; after the engine's start-up step it
// answers 200 with EXACTLY the offer the engine ships (default-offer.json's
// document: every package, price, cell, hint, step-up rule, grow block and
// icon), apart from the date the book was last changed. So the storefront of
// a fresh Sovereign shows the offer published on 20 October 2026, and a file
// that the engine would publish differently fails here.
func TestIntegrationDefaultOfferIsThePublishedDocument(t *testing.T) {
	h, st, _, _, _ := setupAPI(t)
	anon := anonClient(t, h)
	if rec, out := anon.do("GET", "/api/v1/public/packages", "", nil); rec.Code != 404 {
		t.Fatalf("fresh database = %d %v, want 404 before the start-up step", rec.Code, out)
	}
	res, err := st.EnsureDefaultOffer(context.Background(), offer.MustDefault(), "test")
	if err != nil || !res.Wrote {
		t.Fatalf("EnsureDefaultOffer = %+v, %v", res, err)
	}
	_, got := getExact(t, anon, "/api/v1/public/packages", 200)
	if _, ok := got["prices_as_of"].(string); !ok {
		t.Fatalf("the document carries no prices_as_of: %v", got["prices_as_of"])
	}
	delete(got, "prices_as_of")

	raw, err := offer.Files.ReadFile("default-offer.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Document map[string]any `json:"document"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&file); err != nil {
		t.Fatal(err)
	}
	want := publishedIcons(t, file.Document).(map[string]any)

	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", "  ")
		w, _ := json.MarshalIndent(want, "", "  ")
		gl, wl := strings.Split(string(g), "\n"), strings.Split(string(w), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var a, b string
			if i < len(gl) {
				a = gl[i]
			}
			if i < len(wl) {
				b = wl[i]
			}
			if a != b {
				t.Fatalf("the published document differs from default-offer.json at line %d:\n  published: %s\n  the file:  %s", i+1, a, b)
			}
		}
		t.Fatal("the published document differs from default-offer.json")
	}
}

// publishedIcons rewrites every icon of the file's document the way the
// engine publishes it: {"file": "cpu.svg"} becomes the content address of
// that file, {"src": "/api/v1/public/icons/<sha256>"}.
func publishedIcons(t *testing.T, v any) any {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k == "icon" {
				ic := e.(map[string]any)
				b, err := offer.IconBytes(ic["file"].(string))
				if err != nil {
					t.Fatal(err)
				}
				pub := map[string]any{"src": store.IconSrc(store.IconID(b)), "alt": ic["alt"]}
				if bg, ok := ic["bg"]; ok {
					pub["bg"] = bg
				}
				out[k] = pub
				continue
			}
			out[k] = publishedIcons(t, e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = publishedIcons(t, e)
		}
		return out
	default:
		return v
	}
}
