// Package handler — blueprints_wire_compat_test.go: unit tests for
// the simplified-shape decoders introduced by qa-loop iter-15 Fix #58.
//
// Each test exercises a single matrix-shape body (the exact bytes the
// qa-loop test executor sends per the test-matrix-target-state-final
// .json `action` field) and asserts the promoted canonical struct
// matches what the downstream Gitea client expects.

package handler

import (
	"strings"
	"testing"
)

func TestDecodeBlueprintPublishBody_Canonical(t *testing.T) {
	t.Parallel()
	in := []byte(`{"org":"acme","name":"bp-x","version":"1.0.0","blueprintYaml":"apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: bp-x\nspec:\n  version: 1.0.0\n"}`)
	got, err := decodeBlueprintPublishBody(in, "default-org-from-fqdn")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Org != "acme" || got.Name != "bp-x" || got.Version != "1.0.0" {
		t.Fatalf("canonical decode mangled: %+v", got)
	}
	if !strings.Contains(got.BlueprintYAML, "Blueprint") {
		t.Fatalf("yaml lost: %q", got.BlueprintYAML)
	}
}

func TestDecodeBlueprintPublishBody_Simplified_TC081(t *testing.T) {
	t.Parallel()
	// Exact matrix shape per TC-081 action.
	in := []byte(`{"name":"bp-qa-custom","version":"0.1.0","chartTar":"BASE64GOESHERE"}`)
	got, err := decodeBlueprintPublishBody(in, "omantel")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "bp-qa-custom" {
		t.Fatalf("name not promoted: %q", got.Name)
	}
	if got.Version != "0.1.0" {
		t.Fatalf("version not promoted: %q", got.Version)
	}
	if got.ChartTarball != "BASE64GOESHERE" {
		t.Fatalf("chartTar not aliased to chartTarball: %q", got.ChartTarball)
	}
	if got.Org != "omantel" {
		t.Fatalf("org default not applied from defaultOrg: %q", got.Org)
	}
	// Synthesized YAML must satisfy validateBlueprintYAML.
	if msg, ok := validateBlueprintYAML(got); !ok {
		t.Fatalf("synthesized yaml invalid: %s", msg)
	}
}

func TestDecodeBlueprintCurateBody_Simplified_TC083(t *testing.T) {
	t.Parallel()
	in := []byte(`{"name":"bp-qa-custom","newOrigin":"sovereign-curated"}`)
	got, newOrigin, err := decodeBlueprintCurateBody(in, "omantel")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.BlueprintName != "bp-qa-custom" {
		t.Fatalf("blueprintName not promoted: %q", got.BlueprintName)
	}
	if got.SourceOrg != "omantel" {
		t.Fatalf("sourceOrg default: %q", got.SourceOrg)
	}
	if newOrigin != "sovereign-curated" {
		t.Fatalf("newOrigin not extracted: %q", newOrigin)
	}
}

func TestDecodeBlueprintEditPRBody_Simplified_TC085(t *testing.T) {
	t.Parallel()
	in := []byte(`{"name":"bp-qa-custom","diff":"---\napiVersion: v1\n"}`)
	got, err := decodeBlueprintEditPRBody(in, "omantel")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Path != "bp-qa-custom/blueprint.yaml" {
		t.Fatalf("path inference from name failed: %q", got.Path)
	}
	if !strings.Contains(got.Content, "apiVersion") {
		t.Fatalf("diff alias not applied to content: %q", got.Content)
	}
	if got.Org != "omantel" {
		t.Fatalf("org default not applied: %q", got.Org)
	}
}

func TestDecodeBlueprintEditPRBody_Canonical_PreservesEmpty(t *testing.T) {
	t.Parallel()
	// The canonical strict-decode path MUST preserve an explicitly-empty
	// org so the downstream validator returns 400 — matches the pre-Fix
	// #58 contract that TestHandleBlueprintEditPR_BadRequest asserts.
	in := []byte(`{"path":"x","content":"y","title":"z"}`)
	got, err := decodeBlueprintEditPRBody(in, "omantel")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Org != "" {
		t.Fatalf("canonical decode should not default org: %q", got.Org)
	}
}

func TestSynthesizeMinimalBlueprintYAML(t *testing.T) {
	t.Parallel()
	yaml := synthesizeMinimalBlueprintYAML("bp-foo", "1.2.3")
	for _, want := range []string{"apiVersion: catalyst.openova.io/v1", "kind: Blueprint", "name: bp-foo", "version: 1.2.3"} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("synthesized yaml missing %q:\n%s", want, yaml)
		}
	}
	// Must satisfy validateBlueprintYAML when wired into a request.
	req := blueprintPublishRequest{Name: "bp-foo", Version: "1.2.3", BlueprintYAML: yaml}
	if msg, ok := validateBlueprintYAML(req); !ok {
		t.Fatalf("synthesized yaml fails validateBlueprintYAML: %s", msg)
	}
}
