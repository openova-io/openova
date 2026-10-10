package packages

import "testing"

func TestDRTopology_LevelFeature(t *testing.T) {
	doc, err := Parse([]byte(`{"currency":"OMR","price_book":"OpenOva plans","prices_as_of":"2026-10-10",
	  "packages":[{"sku":"plan.s","name":"S","price_month":"2.490"},{"sku":"plan.xl","name":"XL","price_month":"13.990"}],
	  "features":[{"key":"dr_topology","name":"DR topology","kind":"level","levels":["single region","active-passive"],
	    "cells":{"plan.s":{"state":"included","level":0},"plan.xl":{"state":"included","level":1}}}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if l, ok := doc.DRTopology("plan.xl"); !ok || l != DRActivePassive {
		t.Fatalf("XL: got %q %v", l, ok)
	}
	if l, ok := doc.DRTopology("plan.s"); !ok || l != "single region" {
		t.Fatalf("S: got %q %v", l, ok)
	}
	if _, ok := doc.DRTopology("plan.m"); ok {
		t.Fatal("a package with no cell must report unknown")
	}
}

func TestDRTopology_V1DocumentIsUnknown(t *testing.T) {
	doc, err := Parse([]byte(`{"currency":"OMR","price_book":"b","prices_as_of":"2026-10-10",
	  "packages":[{"sku":"plan.s","name":"S","price_month":"2.490"}],
	  "features":[{"key":"backup","name":"Backup","kind":"boolean","cells":{"plan.s":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500"}}}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := doc.DRTopology("plan.s"); ok {
		t.Fatal("v1 document must report unknown")
	}
}
