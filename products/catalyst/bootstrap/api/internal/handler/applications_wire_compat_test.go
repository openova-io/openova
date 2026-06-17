// applications_wire_compat_test.go — exercises the dual-shape decoder
// for the /applications endpoints (qa-loop iter-7 Cluster-C Fix #36).
package handler

import (
	"reflect"
	"testing"
)

func TestDecodeApplicationInstallBody_CanonicalShape(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"blueprintRef": {"name":"bp-wordpress","version":"1.2.3"},
		"name":"wp-prod",
		"organizationRef":"acme",
		"environmentRef":"acme-prod",
		"placement":{"mode":"single-region","regions":["fsn1"]},
		"parameters":{"domain":"shop.acme.com"}
	}`)
	got, err := decodeApplicationInstallBody(raw)
	if err != nil {
		t.Fatalf("canonical decode failed: %v", err)
	}
	if got.BlueprintRef.Name != "bp-wordpress" || got.BlueprintRef.Version != "1.2.3" {
		t.Errorf("blueprintRef wrong: %+v", got.BlueprintRef)
	}
	if got.OrganizationRef != "acme" || got.EnvironmentRef != "acme-prod" {
		t.Errorf("org/env wrong: org=%q env=%q", got.OrganizationRef, got.EnvironmentRef)
	}
	if got.Placement.Mode != "single-region" || !reflect.DeepEqual(got.Placement.Regions, []string{"fsn1"}) {
		t.Errorf("placement wrong: %+v", got.Placement)
	}
	if got.Parameters["domain"] != "shop.acme.com" {
		t.Errorf("parameters not promoted: %+v", got.Parameters)
	}
}

func TestDecodeApplicationInstallBody_SimplifiedMatrixShape(t *testing.T) {
	t.Parallel()
	// This is the literal payload TC-065 sends.
	raw := []byte(`{"blueprint":"bp-wordpress","version":"latest","namespace":"qa-omantel","name":"qa-wp","values":{"siteTitle":"QA WP"}}`)
	got, err := decodeApplicationInstallBody(raw)
	if err != nil {
		t.Fatalf("simplified decode failed: %v", err)
	}
	if got.BlueprintRef.Name != "bp-wordpress" {
		t.Errorf("blueprint not promoted: %+v", got.BlueprintRef)
	}
	if got.BlueprintRef.Version != "latest" {
		t.Errorf("version not promoted: %+v", got.BlueprintRef)
	}
	if got.OrganizationRef != "qa-omantel" {
		t.Errorf("namespace not promoted to organizationRef: got %q", got.OrganizationRef)
	}
	// environmentRef defaults to "<organizationRef>-prod" per main's
	// applicationInstallRequestNormalize convention (PR #1227, the
	// canonical short-form alias path).
	if got.EnvironmentRef != "qa-omantel-prod" {
		t.Errorf("environmentRef default failed: got %q", got.EnvironmentRef)
	}
	if got.Name != "qa-wp" {
		t.Errorf("name wrong: %q", got.Name)
	}
	// One vocabulary (#3375 DoD-1): the canonical default is "singleton".
	if got.Placement.Mode != "singleton" {
		t.Errorf("default placement mode wrong: %q, want canonical singleton", got.Placement.Mode)
	}
	// Default regions is `["primary"]` (a sentinel) per
	// applicationInstallRequestNormalize; the caller is expected to
	// override with explicit regions or accept the validator's 400.
	if !reflect.DeepEqual(got.Placement.Regions, []string{"primary"}) {
		t.Errorf("default regions wrong: %+v", got.Placement.Regions)
	}
	if got.Parameters["siteTitle"] != "QA WP" {
		t.Errorf("values not promoted to parameters: %+v", got.Parameters)
	}
}

func TestDecodeApplicationChangePreviewBody_StringPlacement(t *testing.T) {
	t.Parallel()
	// Matrix's TC-070 payload: simplified string-form placement.
	raw := []byte(`{"placement":"active-hotstandby","regions":["fsn1","hz-hel-rtz-prod"]}`)
	got, err := decodeApplicationChangePreviewBody(raw)
	if err != nil {
		t.Fatalf("simplified decode failed: %v", err)
	}
	if got.Placement == nil {
		t.Fatal("placement nil")
	}
	if got.Placement.Mode != "active-hotstandby" {
		t.Errorf("placement.mode wrong: %q", got.Placement.Mode)
	}
	want := []string{"fsn1", "hz-hel-rtz-prod"}
	if !reflect.DeepEqual(got.Placement.Regions, want) {
		t.Errorf("placement.regions wrong: got %+v want %+v", got.Placement.Regions, want)
	}
}

func TestDecodeApplicationChangePreviewBody_ToVersion(t *testing.T) {
	t.Parallel()
	// Matrix's TC-078 payload: simplified upgrade preview.
	raw := []byte(`{"toVersion":"1.5.0"}`)
	got, err := decodeApplicationChangePreviewBody(raw)
	if err != nil {
		t.Fatalf("simplified decode failed: %v", err)
	}
	if got.BlueprintRef == nil {
		t.Fatal("blueprintRef nil; toVersion should populate version")
	}
	if got.BlueprintRef.Version != "1.5.0" {
		t.Errorf("version wrong: %q", got.BlueprintRef.Version)
	}
}

func TestDecodeApplicationUpdateBody_ValuesAlias(t *testing.T) {
	t.Parallel()
	// Matrix's TC-108 payload: simplified update with `values` instead of `parameters`.
	raw := []byte(`{"values":{"siteTitle":"QA Updated"}}`)
	got, err := decodeApplicationUpdateBody(raw)
	if err != nil {
		t.Fatalf("simplified decode failed: %v", err)
	}
	if got.Parameters["siteTitle"] != "QA Updated" {
		t.Errorf("values not promoted to parameters: %+v", got.Parameters)
	}
}

func TestDecodePlacementValue_BothShapes(t *testing.T) {
	t.Parallel()
	// String form
	mode, regions, err := decodePlacementValue([]byte(`"active-active"`))
	if err != nil {
		t.Fatalf("string-form failed: %v", err)
	}
	if mode != "active-active" || len(regions) != 0 {
		t.Errorf("string-form wrong: mode=%q regions=%+v", mode, regions)
	}
	// Object form
	mode, regions, err = decodePlacementValue([]byte(`{"mode":"single-region","regions":["fsn1"]}`))
	if err != nil {
		t.Fatalf("object-form failed: %v", err)
	}
	if mode != "single-region" || !reflect.DeepEqual(regions, []string{"fsn1"}) {
		t.Errorf("object-form wrong: mode=%q regions=%+v", mode, regions)
	}
}

func TestNormalizeKindName_PluralAndShort(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"deployments":  "deployment",
		"deployment":   "deployment",
		"Deployment":   "deployment",
		"deploy":       "deployment",
		"statefulsets": "statefulset",
		"sts":          "statefulset",
		"daemonsets":   "daemonset",
		"ds":           "daemonset",
		"configmaps":   "configmap",
		"cm":           "configmap",
		"unknown":      "unknown",
	}
	for in, want := range cases {
		got := normalizeKindName(in)
		if got != want {
			t.Errorf("normalizeKindName(%q) = %q; want %q", in, got, want)
		}
	}
}
