package handler

// Desired-state switchover model (2026-11-07): the operator declares the
// Primary in placement, the continuum-controller converges. These tests pin
// (a) that every read path reports the OBSERVED writer (status.primaryRegion)
// and never the static spec.primaryRegion once an observation exists, (b) the
// desired/converged/in-progress/step/lastSwitchover/convergeReason projection,
// and (c) that the imperative POST .../switchover route is gone.

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// convergenceCR builds a Continuum whose spec home region is `specPrimary`
// and whose status is `status` (merged verbatim). Region strings are opaque
// fixtures — the code under test never interprets them.
func convergenceCR(specPrimary, desired string, status map[string]interface{}) *unstructured.Unstructured {
	u := newContinuumUnstructured("dr-app", "org-ns", "app", specPrimary, []string{"region-b"})
	if desired != "" {
		_ = unstructured.SetNestedField(u.Object, desired, "spec", "desiredPrimaryRegion")
	}
	cur, _, _ := unstructured.NestedMap(u.Object, "status")
	if cur == nil {
		cur = map[string]interface{}{}
	}
	for k, v := range status {
		cur[k] = v
	}
	_ = unstructured.SetNestedMap(u.Object, cur, "status")
	return u
}

func TestConvergence_ObservedPrimaryBeatsSpecAndLease(t *testing.T) {
	cr := convergenceCR("region-a", "region-b", map[string]interface{}{
		"primaryRegion": "region-b",
		"leaseHolder":   "region-a",
		"converged":     true,
	})
	rs := enrichReplicationStatus(cr)
	if rs.CurrentPrimary != "region-b" {
		t.Fatalf("replication-status currentPrimary = %q, want observed region-b (not spec/lease region-a)", rs.CurrentPrimary)
	}
	if rs.DesiredPrimaryRegion != "region-b" || !rs.Converged || rs.SwitchoverInProgress {
		t.Fatalf("convergence = %+v, want desired=region-b converged=true inProgress=false", rs.continuumConvergence)
	}
	en := enrichContinuumResponse(cr)
	if en.CurrentPrimary != "region-b" || en.PrimaryRegion != "region-a" {
		t.Fatalf("enriched currentPrimary=%q primaryRegion=%q, want observed region-b + static home region-a", en.CurrentPrimary, en.PrimaryRegion)
	}
	item := continuumItemFromCR("sov", cr)
	if item.CurrentPrimary != "region-b" || item.DesiredPrimaryRegion != "region-b" || !item.Converged {
		t.Fatalf("fleet item = %+v, want currentPrimary/desired region-b converged", item)
	}
	if got := continuumActingPrimary(cr); got != "region-b" {
		t.Fatalf("continuumActingPrimary = %q, want region-b", got)
	}
}

func TestConvergence_InProgressReportsStepAndIsNotConverged(t *testing.T) {
	cr := convergenceCR("region-a", "region-b", map[string]interface{}{
		"primaryRegion":        "region-a",
		"desiredPrimaryRegion": "region-b",
		"switchoverInProgress": true,
		"phase":                "SwitchingOver",
		"message":              "step 3/7 — fence-old-primary",
		"converged":            false,
		"conditions": []interface{}{
			map[string]interface{}{"type": "Converged", "status": "False", "reason": "Converging", "message": "switching writer region-a → region-b"},
		},
	})
	c := continuumConvergenceFromCR(cr, "region-a")
	if !c.SwitchoverInProgress || c.Converged {
		t.Fatalf("got %+v, want inProgress && !converged", c)
	}
	if c.Step != "step 3/7 — fence-old-primary" {
		t.Fatalf("step = %q", c.Step)
	}
	if c.ConvergeReason != "Converging" || c.ConvergeMessage == "" {
		t.Fatalf("converge reason/message = %q/%q", c.ConvergeReason, c.ConvergeMessage)
	}
	if c.ObservedPrimaryRegion != "region-a" || c.DesiredPrimaryRegion != "region-b" {
		t.Fatalf("observed/desired = %q/%q", c.ObservedPrimaryRegion, c.DesiredPrimaryRegion)
	}
}

func TestConvergence_BlockedSurfacesConditionMessageNoStep(t *testing.T) {
	cr := convergenceCR("region-a", "region-b", map[string]interface{}{
		"primaryRegion": "region-a",
		"phase":         "Degraded",
		"message":       "not a step — must not leak as step",
		"conditions": []interface{}{
			map[string]interface{}{"type": "Converged", "status": "False", "reason": "ConvergeBlocked", "message": "standby lag 40s exceeds RPO; retrying"},
		},
	})
	c := continuumConvergenceFromCR(cr, "region-a")
	if c.Converged || c.SwitchoverInProgress {
		t.Fatalf("got %+v, want blocked (not converged, not in progress)", c)
	}
	if c.Step != "" {
		t.Fatalf("step leaked while not in progress: %q", c.Step)
	}
	if c.ConvergeReason != "ConvergeBlocked" || c.ConvergeMessage != "standby lag 40s exceeds RPO; retrying" {
		t.Fatalf("reason/message = %q/%q", c.ConvergeReason, c.ConvergeMessage)
	}
}

func TestConvergence_DerivedWhenControllerPublishedNothing(t *testing.T) {
	// No status.converged and no Converged condition: derive observed==desired.
	agree := convergenceCR("region-a", "region-a", map[string]interface{}{"primaryRegion": "region-a"})
	if c := continuumConvergenceFromCR(agree, ""); !c.Converged {
		t.Fatalf("observed==desired must derive converged, got %+v", c)
	}
	differ := convergenceCR("region-a", "region-b", map[string]interface{}{"primaryRegion": "region-a"})
	if c := continuumConvergenceFromCR(differ, ""); c.Converged {
		t.Fatalf("observed!=desired must derive NOT converged, got %+v", c)
	}
	// Desired declared but nothing observed: absence of a reading is not agreement.
	unobserved := convergenceCR("region-a", "region-b", nil)
	if c := continuumConvergenceFromCR(unobserved, ""); c.Converged {
		t.Fatalf("declared-but-unobserved must not read converged, got %+v", c)
	}
	// Nothing declared: nothing to converge to.
	none := convergenceCR("region-a", "", map[string]interface{}{"primaryRegion": "region-a"})
	if c := continuumConvergenceFromCR(none, ""); !c.Converged || c.DesiredPrimaryRegion != "" {
		t.Fatalf("nothing declared must read converged with empty desired, got %+v", c)
	}
}

func TestConvergence_LastSwitchoverAndWireKeys(t *testing.T) {
	cr := convergenceCR("region-a", "region-b", map[string]interface{}{
		"primaryRegion": "region-b",
		"converged":     true,
		"lastSwitchover": map[string]interface{}{
			"at": "2026-11-07T12:00:00Z", "from": "region-a", "to": "region-b", "result": "Success",
		},
	})
	rs := enrichReplicationStatus(cr)
	if rs.LastSwitchoverRecord == nil || rs.LastSwitchoverRecord.Result != "Success" || rs.LastSwitchoverRecord.From != "region-a" || rs.LastSwitchoverRecord.To != "region-b" {
		t.Fatalf("lastSwitchover = %+v", rs.LastSwitchoverRecord)
	}
	raw, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"currentPrimary", "desiredPrimaryRegion", "converged", "switchoverInProgress", "lastSwitchover"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("replication-status JSON missing %q: %s", k, raw)
		}
	}
	if m["currentPrimary"] != "region-b" {
		t.Fatalf("wire currentPrimary = %v", m["currentPrimary"])
	}
	ls, _ := m["lastSwitchover"].(map[string]interface{})
	if ls["at"] != "2026-11-07T12:00:00Z" {
		t.Fatalf("wire lastSwitchover = %v", m["lastSwitchover"])
	}
}

func TestContinuumForApplication_ReadsDrAppInAppNamespace(t *testing.T) {
	cr := convergenceCR("region-a", "region-b", map[string]interface{}{"primaryRegion": "region-b", "converged": true})
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, continuumListKinds(), cr)
	app := &unstructured.Unstructured{}
	app.SetName("app")
	app.SetNamespace("org-ns")
	h := &Handler{}
	got := h.continuumForApplication(context.Background(), client, app)
	if got == nil || got.GetName() != "dr-app" {
		t.Fatalf("continuumForApplication = %v, want dr-app", got)
	}
	dr := applicationDRConvergenceFromCR(got)
	if dr.CurrentPrimary != "region-b" || dr.DesiredPrimaryRegion != "region-b" || !dr.Converged {
		t.Fatalf("app dr block = %+v", dr)
	}
	other := &unstructured.Unstructured{}
	other.SetName("no-dr")
	other.SetNamespace("org-ns")
	if h.continuumForApplication(context.Background(), client, other) != nil {
		t.Fatal("an app with no Continuum must yield nil")
	}
}

// The imperative switchover is gone: no handler method, no route. Only the
// read-only preview + history survive.
func TestImperativeSwitchoverRouteRemoved(t *testing.T) {
	if _, ok := reflect.TypeOf(&Handler{}).MethodByName("HandleContinuumSwitchoverRequest"); ok {
		t.Fatal("HandleContinuumSwitchoverRequest still exists — the desired-state model forbids an imperative switchover")
	}
	src, err := os.ReadFile("../../cmd/api/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	bad := regexp.MustCompile(`rg\.(Post|Put|Patch)\("/api/v1/sovereigns/\{id\}/continuums?/\{name\}/switchover"`)
	if loc := bad.FindIndex(src); loc != nil {
		t.Fatalf("imperative switchover route still registered: %s", src[loc[0]:loc[1]])
	}
	if !regexp.MustCompile(`/continuum/\{name\}/switchover/preview", h\.HandleContinuumSwitchoverPreview`).Match(src) {
		t.Fatal("read-only switchover/preview route must stay registered")
	}
}
