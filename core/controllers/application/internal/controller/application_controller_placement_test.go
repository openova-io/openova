// Tests for #3373 — instance-level placement — after NET-223 removed the
// tier pivot.
//
// `Application.spec.placement` (object form {vcluster, regions, clusters,
// mode}) still carries the WHERE of an instance, but a placement tier
// selects nothing any more: every release lands in the Application's own
// namespace on the region's host cluster, with no `spec.kubeConfig` pivot.
// A tier an older object still names is ignored and logged once per object.
//
// Matrix:
//
//  1. parseSpec dual-form — legacy string preserved verbatim.
//  2. parseSpec object form — clusters/regions/mode parsed; vcluster is not
//     read, so even an unknown value is no error.
//  3. Instance placement.vcluster=rtz → the HR stays host-side.
//  4. Blueprint spec.defaultPlacement.vcluster=mgmt → host-side.
//  5. A tier outside the Blueprint's allowedPlacements is no refusal.
//  6. A Git edit of spec.placement.vcluster changes nothing; status.placement
//     reports host / host-default.
//  7. The ignored tier is logged once per object, naming the field.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/render"
)

// makeAppWithPlacement builds an Application whose spec.placement is
// the #3373 OBJECT form.
func makeAppWithPlacement(namespace, name, env, bpName, bpVer string, placement map[string]interface{}, regions []string) *unstructured.Unstructured {
	u := makeApp(namespace, name, env, bpName, bpVer, "single-region", regions, nil)
	spec := u.Object["spec"].(map[string]interface{})
	spec["placement"] = placement
	return u
}

// makeBlueprintWithDefaultPlacement builds a Blueprint declaring the
// #3373 class-level suggestion + allow-list.
func makeBlueprintWithDefaultPlacement(name, version string, defaultVCluster string, allowed []string) *unstructured.Unstructured {
	bp := makeBlueprint(name, version, nil, []string{"single-region"})
	spec := bp.Object["spec"].(map[string]interface{})
	if defaultVCluster != "" {
		spec["defaultPlacement"] = map[string]interface{}{
			"vcluster": defaultVCluster,
		}
	}
	if allowed != nil {
		al := make([]interface{}, len(allowed))
		for i, a := range allowed {
			al[i] = a
		}
		spec["allowedPlacements"] = al
	}
	return bp
}

func getCommittedHR(t *testing.T, fg *fakeGitea, org, app, region string) string {
	t.Helper()
	// ADR-0015 D1 — the region's file in the Org repo.
	hrBytes, ok := fg.get(org, "catalyst-tenant", "main", "regions/"+region+"/apps/"+app+".yaml")
	if !ok {
		t.Fatalf("HelmRelease for %s/%s on %s should have been committed to Gitea", org, app, region)
	}
	return string(hrBytes)
}

// assertHostSideHR parses the committed HelmRelease and asserts the host
// placement: metadata.namespace is the Application's namespace, and there is
// no spec.kubeConfig and no vcluster label.
func assertHostSideHR(t *testing.T, fg *fakeGitea, org, app, region string) {
	t.Helper()
	hr, err := render.ParseHelmRelease([]byte(getCommittedHR(t, fg, org, app, region)))
	if err != nil {
		t.Fatalf("parse committed HR: %v", err)
	}
	if ns := hr.GetNamespace(); ns != org {
		t.Errorf("HR.metadata.namespace = %q, want the Application namespace %q", ns, org)
	}
	if kc, found, _ := unstructured.NestedMap(hr.Object, "spec", "kubeConfig"); found {
		t.Errorf("HR must carry no spec.kubeConfig pivot, got %v", kc)
	}
	if vc, ok := hr.GetLabels()["catalyst.openova.io/vcluster"]; ok {
		t.Errorf("HR must carry no vcluster label, got %q", vc)
	}
}

func assertStatusPlacementHost(t *testing.T, r *Reconciler, ns, name string) {
	t.Helper()
	got := readApp(t, r, ns, name)
	vc, _, _ := unstructured.NestedString(got.Object, "status", "placement", "vcluster")
	if vc != "host" {
		t.Errorf("status.placement.vcluster = %q, want host", vc)
	}
	src, _, _ := unstructured.NestedString(got.Object, "status", "placement", "source")
	if src != "host-default" {
		t.Errorf("status.placement.source = %q, want host-default", src)
	}
}

// --- 1+2. parseSpec dual-form ---------------------------------------------

func TestParseSpec_PlacementLegacyString(t *testing.T) {
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	spec, err := parseSpec(app)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.Placement != "single-region" {
		t.Errorf("Placement = %q, want single-region", spec.Placement)
	}
}

func TestParseSpec_PlacementObjectForm(t *testing.T) {
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{
			"vcluster": "rtz",
			"mode":     "single-region",
			"clusters": []interface{}{"mgmt-A"},
			"regions":  []interface{}{"hetzner-nbg-rtz-prod"},
		},
		[]string{"hetzner-fsn-rtz-prod"})
	spec, err := parseSpec(app)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.Placement != "single-region" {
		t.Errorf("mode → Placement = %q, want single-region", spec.Placement)
	}
	if len(spec.PlacementClusters) != 1 || spec.PlacementClusters[0] != "mgmt-A" {
		t.Errorf("PlacementClusters = %v, want [mgmt-A]", spec.PlacementClusters)
	}
	// placement.regions wins over the legacy top-level spec.regions[].
	if len(spec.Regions) != 1 || spec.Regions[0] != "hetzner-nbg-rtz-prod" {
		t.Errorf("Regions = %v, want [hetzner-nbg-rtz-prod] (placement.regions override)", spec.Regions)
	}
}

// TestParseSpec_PlacementUnknownVClusterIgnored — before NET-223 an unknown
// tier made parseSpec fail; vcluster is not read any more.
func TestParseSpec_PlacementUnknownVClusterIgnored(t *testing.T) {
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "warp-zone", "mode": "single-region"},
		[]string{"hetzner-fsn-rtz-prod"})
	spec, err := parseSpec(app)
	if err != nil {
		t.Fatalf("parseSpec must not read spec.placement.vcluster, got error: %v", err)
	}
	if spec.Placement != "single-region" {
		t.Errorf("Placement = %q, want single-region", spec.Placement)
	}
}

// --- 3-5. A tier selects nothing ------------------------------------------

func TestReconcile_TierIgnored_RendersHostSide(t *testing.T) {
	cases := []struct {
		name      string
		bp        *unstructured.Unstructured
		placement map[string]interface{}
	}{
		{"instance vcluster rtz", makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"}),
			map[string]interface{}{"vcluster": "rtz", "mode": "single-region"}},
		{"blueprint defaultPlacement mgmt", makeBlueprintWithDefaultPlacement("bp-wp", "1.0.0", "mgmt", nil),
			nil},
		{"instance dmz outside allowedPlacements [mgmt rtz]", makeBlueprintWithDefaultPlacement("bp-wp", "1.0.0", "mgmt", []string{"mgmt", "rtz"}),
			map[string]interface{}{"vcluster": "dmz", "mode": "single-region"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := makeEnv("acme-prod", "acme", "prod")
			org := makeOrg("acme")
			app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
				[]string{"hetzner-fsn-rtz-prod"}, nil)
			if c.placement != nil {
				app = makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
					c.placement, []string{"hetzner-fsn-rtz-prod"})
			}
			fg := newFakeGitea()
			fg.orgsExist["acme"] = true
			r := newReconciler(t, fg, app, env, org, c.bp)

			reconcileFromCluster(t, r, "acme", "site")

			got := readApp(t, r, "acme", "site")
			if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
				t.Fatalf("a tier must not refuse the Application: phase=%q reason=%q msg=%q", phase, reason, msg)
			}
			assertHostSideHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")
			assertStatusPlacementHost(t, r, "acme", "site")
		})
	}
}

// --- 6. A Git edit of the tier changes nothing ----------------------------

func TestReconcile_GitEditOfTierChangesNothing(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "mgmt", "mode": "single-region"},
		[]string{"hetzner-fsn-rtz-prod"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")
	before := getCommittedHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")
	assertHostSideHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")

	ctx := context.Background()
	latest, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Get(ctx, "site", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if err := unstructured.SetNestedField(latest.Object, "rtz", "spec", "placement", "vcluster"); err != nil {
		t.Fatalf("set placement: %v", err)
	}
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Update(ctx, latest, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update app: %v", err)
	}

	reconcileFromCluster(t, r, "acme", "site")

	if after := getCommittedHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod"); after != before {
		t.Errorf("a tier edit must not change the committed HR;\nbefore:\n%s\nafter:\n%s", before, after)
	}
	assertStatusPlacementHost(t, r, "acme", "site")
}

// --- 7. Logged once per object --------------------------------------------

func TestReconcile_IgnoredTierLoggedOncePerObject(t *testing.T) {
	bp := makeBlueprintWithDefaultPlacement("bp-wp", "1.0.0", "mgmt", []string{"host", "rtz"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "dmz", "mode": "single-region"},
		[]string{"hetzner-fsn-rtz-prod"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)
	var buf bytes.Buffer
	r.Log = slog.New(slog.NewJSONHandler(&buf, nil))

	reconcileFromCluster(t, r, "acme", "site")
	reconcileFromCluster(t, r, "acme", "site")

	got := map[string][][]string{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec struct {
			Msg    string   `json:"msg"`
			Kind   string   `json:"kind"`
			Fields []string `json:"fields"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		if rec.Msg == "placement tier ignored: the release is placed on the region's host cluster" {
			got[rec.Kind] = append(got[rec.Kind], rec.Fields)
		}
	}
	want := map[string][][]string{
		"Application": {{"spec.placement.vcluster=dmz"}},
		"Blueprint":   {{"spec.defaultPlacement.vcluster=mgmt", "spec.allowedPlacements[1]=rtz"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ignored-tier log lines over two reconciles = %v, want %v", got, want)
	}
}

// TestIgnoredTierFields — every tier field is named with its value; "" and
// "host" name no tier.
func TestIgnoredTierFields(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"placement": map[string]interface{}{
				"vcluster": "host",
				"targets": []interface{}{
					map[string]interface{}{"cluster": "rtz-A", "vcluster": "rtz"},
					map[string]interface{}{"cluster": "rtz-B", "vcluster": ""},
				},
			},
			"placementSchema":   map[string]interface{}{"vcluster": "dmz"},
			"allowedPlacements": []interface{}{"host"},
			"topology": map[string]interface{}{
				"perTopology": map[string]interface{}{
					"singleton":          map[string]interface{}{"placement": map[string]interface{}{"tier": ""}},
					"active-hot-standby": map[string]interface{}{"placement": map[string]interface{}{"tier": "mgmt"}},
				},
			},
		},
	}}
	want := []string{
		"spec.placement.targets[0].vcluster=rtz",
		"spec.placementSchema.vcluster=dmz",
		"spec.topology.perTopology.active-hot-standby.placement.tier=mgmt",
	}
	if got := ignoredTierFields(obj); !reflect.DeepEqual(got, want) {
		t.Errorf("ignoredTierFields = %v, want %v", got, want)
	}
	host := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"placement": map[string]interface{}{"vcluster": "host"}},
	}}
	if got := ignoredTierFields(host); len(got) != 0 {
		t.Errorf("ignoredTierFields(host placement) = %v, want none", got)
	}
}
