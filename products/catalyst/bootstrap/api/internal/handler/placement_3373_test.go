// Tests for #3373 — the dual-form spec.placement stamp on the two
// Application-CR write paths (install handler + create-instance seed)
// and the placement read-back helpers.
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

func TestNewApplicationUnstructured_StringForm_StoresCanonical(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
		Name:            "site",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		// Legacy editor dialect on the wire …
		Placement: applicationPlacement{Mode: "single-region", Regions: []string{"fsn"}},
	}
	obj := newApplicationUnstructured(req)
	pl, ok, err := unstructured.NestedString(obj.Object, "spec", "placement")
	// One vocabulary (#3375 DoD-1): the CR STORES the canonical token —
	// the legacy "single-region" is folded to "singleton" so every CR
	// carries one vocabulary the resolver/render path can rely on. The
	// string form is preserved (still a string, not the object form).
	if err != nil || !ok || pl != "singleton" {
		t.Fatalf("legacy single-region must store the canonical singleton string, got (%q, %v, %v)", pl, ok, err)
	}
}

func TestNewApplicationUnstructured_ObjectFormWhenVClusterSet(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
		Name:            "site",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement: applicationPlacement{
			Mode:     "single-region",
			Regions:  []string{"fsn"},
			VCluster: "rtz",
			Clusters: []string{"mgmt-A"},
		},
	}
	obj := newApplicationUnstructured(req)
	vc, _, _ := unstructured.NestedString(obj.Object, "spec", "placement", "vcluster")
	if vc != "rtz" {
		t.Fatalf("spec.placement.vcluster = %q, want rtz", vc)
	}
	mode, _, _ := unstructured.NestedString(obj.Object, "spec", "placement", "mode")
	// One vocabulary (#3375 DoD-1): the object form's mode also stores
	// the canonical token (legacy single-region folded to singleton).
	if mode != "singleton" {
		t.Fatalf("spec.placement.mode = %q, want canonical singleton", mode)
	}
	clusters, _, _ := unstructured.NestedSlice(obj.Object, "spec", "placement", "clusters")
	if len(clusters) != 1 || clusters[0] != "mgmt-A" {
		t.Fatalf("spec.placement.clusters = %v, want [mgmt-A]", clusters)
	}
	// legacy top-level regions[] stays stamped (CRD requires it).
	regions, _, _ := unstructured.NestedSlice(obj.Object, "spec", "regions")
	if len(regions) != 1 {
		t.Fatalf("spec.regions = %v, want 1 entry", regions)
	}
}

func TestValidateApplicationInstall_RejectsUnknownVCluster(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
		Name:            "site",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement: applicationPlacement{
			Mode:     "single-region",
			Regions:  []string{"fsn"},
			VCluster: "warp-zone",
		},
	}
	if msg, ok := validateApplicationInstallRequest(req); ok {
		t.Fatalf("unknown vcluster must be rejected, got ok (msg=%q)", msg)
	}
}

func TestNewApplicationCRFromSeed_PlacementObjectStamped(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:      "obs",
		Namespace: "acme",
		Blueprint: "bp-grafana",
		Topology:  "singleton",
		Placement: &instances.InstancePlacementRequest{
			VCluster: "rtz",
			Regions:  []string{"hetzner-fsn-rtz-prod"},
		},
	}
	obj := newApplicationCRFromSeed(seed)
	vc, _, _ := unstructured.NestedString(obj.Object, "spec", "placement", "vcluster")
	if vc != "rtz" {
		t.Fatalf("spec.placement.vcluster = %q, want rtz", vc)
	}
	mode, _, _ := unstructured.NestedString(obj.Object, "spec", "placement", "mode")
	if mode != "singleton" {
		t.Fatalf("spec.placement.mode = %q, want singleton (the chosen topology)", mode)
	}
	// placement.regions overrides the "primary" sentinel.
	regions, _, _ := unstructured.NestedSlice(obj.Object, "spec", "regions")
	if len(regions) != 1 || regions[0] != "hetzner-fsn-rtz-prod" {
		t.Fatalf("spec.regions = %v, want the placement regions", regions)
	}
}

func TestNewApplicationCRFromSeed_NoPlacement_StringForm(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:      "obs",
		Namespace: "acme",
		Blueprint: "bp-grafana",
		Topology:  "singleton",
	}
	obj := newApplicationCRFromSeed(seed)
	pl, ok, err := unstructured.NestedString(obj.Object, "spec", "placement")
	// One vocabulary (#3375 DoD-1): the silent-accept string form stamps
	// the CANONICAL token via placementForTopology. A "singleton" topology
	// maps to the canonical "singleton" placement (no longer rewritten to
	// the legacy "single-region"); the catalog placementSchema, the
	// editors, and the resolver all speak this single set.
	if err != nil || !ok || pl != "singleton" {
		t.Fatalf("silent-accept flow must stamp the canonical placement token, got (%q, %v, %v)", pl, ok, err)
	}
}

// A legacy / hyphen-variant topology choice on the seed still folds to
// the canonical placement token on the string form.
func TestNewApplicationCRFromSeed_LegacyTopologyFoldsToCanonical(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:      "obs",
		Namespace: "acme",
		Blueprint: "bp-grafana",
		Topology:  "active-hotstandby", // legacy spelling
	}
	obj := newApplicationCRFromSeed(seed)
	pl, _, _ := unstructured.NestedString(obj.Object, "spec", "placement")
	if pl != "active-hot-standby" {
		t.Fatalf("legacy active-hotstandby topology must stamp canonical active-hot-standby, got %q", pl)
	}
}

func TestPlacementInfoFromCR_StatusWinsOverSpec(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"placement": map[string]interface{}{"vcluster": "mgmt"},
		},
		"status": map[string]interface{}{
			"placement": map[string]interface{}{
				"vcluster": "rtz", "source": "instance",
				"regions": []interface{}{"hetzner-fsn-rtz-prod"},
			},
		},
	}}
	pi := placementInfoFromCR(u)
	if pi == nil || pi.VCluster != "rtz" || pi.Source != "instance" {
		t.Fatalf("status.placement (the reconciled effective truth) must win, got %+v", pi)
	}
}

func TestPlacementInfoFromCR_LegacyStringYieldsNil(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"placement": "single-region"},
	}}
	if pi := placementInfoFromCR(u); pi != nil {
		t.Fatalf("legacy string-form spec without status must yield nil, got %+v", pi)
	}
}

func TestReadTopology_ObjectFormMode(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"placement": map[string]interface{}{"vcluster": "mgmt", "mode": "active-hotstandby"},
		},
	}}
	if got := readTopology(u); got != "active-hotstandby" {
		t.Fatalf("readTopology object-form = %q, want active-hotstandby", got)
	}
}

// ── #3830 — dotted Org FQDN → slugged metadata.namespace on both CR
// write paths. The CR must NEVER carry a dotted metadata.namespace (the
// apiserver rejects it); the verbatim Org identity stays on the
// organization label + environmentRef.

func TestNewApplicationUnstructured_DottedOrgSlugsNamespace(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
		Name:            "site",
		OrganizationRef: "hw165.omani.works",
		EnvironmentRef:  "hw165.omani.works-prod",
		Placement:       applicationPlacement{Mode: "singleton", Regions: []string{"fsn"}},
	}
	obj := newApplicationUnstructured(req)
	if ns := obj.GetNamespace(); ns != "hw165-omani-works" {
		t.Fatalf("metadata.namespace = %q, want slugged hw165-omani-works", ns)
	}
	// The verbatim FQDN identity is preserved on the org label …
	if org := obj.GetLabels()["catalyst.openova.io/organization"]; org != "hw165.omani.works" {
		t.Fatalf("organization label = %q, want verbatim FQDN hw165.omani.works", org)
	}
	// … and on environmentRef (a Catalyst Environment ref, not a namespace).
	if env, _, _ := unstructured.NestedString(obj.Object, "spec", "environmentRef"); env != "hw165.omani.works-prod" {
		t.Fatalf("spec.environmentRef = %q, want verbatim hw165.omani.works-prod", env)
	}
}

func TestNewApplicationCRFromSeed_DottedOrgSlugsNamespace(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:      "obs",
		Namespace: "hw165.omani.works", // instances.Build sets Namespace = Org (the FQDN)
		Blueprint: "bp-grafana",
		Topology:  "singleton",
		Labels:    map[string]string{"catalyst.openova.io/organization": "hw165.omani.works"},
	}
	obj := newApplicationCRFromSeed(seed)
	if ns := obj.GetNamespace(); ns != "hw165-omani-works" {
		t.Fatalf("metadata.namespace = %q, want slugged hw165-omani-works", ns)
	}
	// environmentRef keeps the FQDN form (seed.Namespace + "-prod").
	if env, _, _ := unstructured.NestedString(obj.Object, "spec", "environmentRef"); env != "hw165.omani.works-prod" {
		t.Fatalf("spec.environmentRef = %q, want hw165.omani.works-prod", env)
	}
}

// #3830 — the install validator must ACCEPT a dotted Org FQDN (the Org
// identity is a DNS subdomain; the namespace is derived from it). Before
// the fix this 400'd with "must be a valid K8s name".
func TestValidateApplicationInstall_AcceptsDottedOrgFQDN(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
		Name:            "site",
		OrganizationRef: "hw165.omani.works",
		EnvironmentRef:  "hw165.omani.works-prod",
		Placement:       applicationPlacement{Mode: "singleton", Regions: []string{"fsn"}},
	}
	if msg, ok := validateApplicationInstallRequest(req); !ok {
		t.Fatalf("dotted Org FQDN must be accepted, got rejection: %q", msg)
	}
}
