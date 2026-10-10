package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// Every vendored showcase icon passes the product's own gate, renders in a
// colour inside an <img> (no currentColor left), and is named in
// LICENSES.md; the manifest maps every feature of the ladder, every group
// and every package, and names no file that is not vendored (DESIGN.md
// §22.10).
func TestShowcaseIconsPassTheValidator(t *testing.T) {
	ids, err := showcaseIconIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 32 {
		t.Fatalf("%d vendored icons, want 32", len(ids))
	}
	licences, err := os.ReadFile("icons/LICENSES.md")
	if err != nil {
		t.Fatal(err)
	}
	for file, id := range ids {
		b, err := showcaseIconFS.ReadFile("icons/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateIcon(store.IconSVG, b); err != nil {
			t.Errorf("%s: the product would refuse it: %v", file, err)
		}
		if store.IconID(b) != id {
			t.Errorf("%s: id %s is not its sha256", file, id)
		}
		if bytes.Contains(b, []byte("currentColor")) {
			t.Errorf("%s: currentColor renders black inside an <img>; write a fixed colour", file)
		}
		name := strings.TrimSuffix(file, ".svg")
		if !bytes.Contains(licences, []byte("`"+name+"`")) && !bytes.Contains(licences, []byte("`"+file+"`")) {
			t.Errorf("%s: not named in icons/LICENSES.md", file)
		}
	}
	m, err := loadIconManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.files() {
		if _, ok := ids[f]; !ok {
			t.Errorf("the manifest names %s, which is not vendored", f)
		}
	}
	if len(m.files()) != len(ids) {
		t.Errorf("the manifest uses %d of the %d vendored icons; vendor only what is used", len(m.files()), len(ids))
	}
	for _, f := range synth.Features {
		fi, ok := m.Features[f.Key]
		if !ok || fi.Icon == "" {
			t.Errorf("feature %s has no icon in the manifest", f.Key)
			continue
		}
		if _, err := store.NormalizeColour("bg", fi.BG); err != nil || fi.BG == "" {
			t.Errorf("feature %s: tile colour %q", f.Key, fi.BG)
		}
	}
	if len(m.Features) != len(synth.Features) {
		t.Errorf("the manifest maps %d features, the ladder has %d", len(m.Features), len(synth.Features))
	}
	for _, g := range store.FeatureGroups {
		if m.Groups[g.Key] == "" {
			t.Errorf("group %s has no icon in the manifest", g.Key)
		}
	}
	recommended := 0
	for _, p := range synth.Packages {
		b, ok := m.Packages["plan."+p.Slug]
		if !ok {
			t.Errorf("package %s has no branding in the manifest", p.Slug)
			continue
		}
		if c, err := store.NormalizeColour("accent", b.Accent); err != nil || c == "" {
			t.Errorf("package %s: accent %q", p.Slug, b.Accent)
		}
		if (b.Badge != "") != p.Recommended {
			t.Errorf("package %s: badge %q, recommended %v — the badge is the recommended package's", p.Slug, b.Badge, p.Recommended)
		}
		if p.Recommended {
			recommended++
		}
	}
	if recommended != 1 {
		t.Errorf("%d recommended packages, want one", recommended)
	}
}
