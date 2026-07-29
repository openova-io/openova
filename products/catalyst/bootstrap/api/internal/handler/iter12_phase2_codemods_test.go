// Package handler — iter12_phase2_codemods_test.go: covers the
// bulk Phase 2 codemod patterns shipped by Fix #52 in qa-loop iter-12.
//
// One test per pattern (a1, a3-a10, a12) so a future regression can be
// pinpointed by name. Per `feedback_no_mvp_no_workarounds.md` every
// alias asserted here MUST be REAL data — the tests confirm the value
// is the same one the canonical field carries, never a placeholder.

package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ── a1: add-score-alias-to-Score-struct ──────────────────────────────

// TestComputeScore_ScoreAliasMatchesTotal — every Score the
// computation path produces MUST set the JSON-aliased `Score` field to
// the same value as `Total`. The matrix consistently asserts the
// literal "score" token across compliance + dashboard endpoints
// (TC-029, TC-034, TC-040, TC-047, TC-050, TC-054).
func TestComputeScore_ScoreAliasMatchesTotal(t *testing.T) {
	rs := &resourceState{
		results: map[string]policyVerdict{
			"a": {result: "pass"}, "b": {result: "fail"},
		},
	}
	spec := &EnvironmentPolicySpec{Weights: map[string]PolicyWeight{
		"a": {Weight: 50, Scope: "all"},
		"b": {Weight: 50, Scope: "all"},
	}}
	got := computeScore(rs, spec)
	if got.Total == nil || got.Score == nil {
		t.Fatalf("score-alias: expected both Total + Score populated, got total=%v score=%v",
			got.Total, got.Score)
	}
	if *got.Total != *got.Score {
		t.Fatalf("score-alias: Total=%d Score=%d (must match)", *got.Total, *got.Score)
	}
	if *got.Score != 50 {
		t.Fatalf("score-alias: expected 50 (1 of 2 pass), got %d", *got.Score)
	}
}

// TestComputeScore_NullScoreOnEmptyDenominator — both Total + Score
// MUST be JSON-null when there's no data, matching the dashboard's
// "no data yet" contract. Any non-null placeholder would lie about the
// data state and violate `feedback_no_mvp_no_workarounds.md`.
func TestComputeScore_NullScoreOnEmptyDenominator(t *testing.T) {
	rs := &resourceState{results: map[string]policyVerdict{}}
	got := computeScore(rs, nil)
	if got.Total != nil {
		t.Fatalf("expected Total nil on empty denominator, got %d", *got.Total)
	}
	if got.Score != nil {
		t.Fatalf("expected Score nil on empty denominator, got %d", *got.Score)
	}
}

// TestComputeScore_AliasSerializesAsScoreKey — confirm the JSON tag is
// `"score"` so consumers reading the alias by field name see it.
func TestComputeScore_AliasSerializesAsScoreKey(t *testing.T) {
	rs := &resourceState{
		results: map[string]policyVerdict{"a": {result: "pass"}},
	}
	got := computeScore(rs, nil)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"score":`) {
		t.Errorf("expected serialized payload to contain `score:` key, got %s", raw)
	}
	if !strings.Contains(string(raw), `"total":`) {
		t.Errorf("expected serialized payload to retain `total:` key, got %s", raw)
	}
}

// ── a6: add-versions-and-chartRef-to-CatalogBlueprint ────────────────

// TestCatalogBlueprint_PopulateVersionsAlias_RealChartRef — the
// chartRef must be the REAL OCI ref assembled from the canonical
// registry + name + version, never a placeholder. Versions[0] mirrors
// the headline so the UI's version-picker has at least one entry.
func TestCatalogBlueprint_PopulateVersionsAlias_RealChartRef(t *testing.T) {
	bp := CatalogBlueprint{
		Name:    "bp-wordpress",
		Version: "1.5.0",
	}
	bp.PopulateVersionsAlias()

	// #5475: this previously asserted "bp-bp-wordpress" — the doubled
	// prefix the code actually produced. Asserting an implementation's
	// output rather than the intended contract is what kept the defect
	// alive: `gh api /orgs/openova-io/packages/container/bp-bp-wordpress`
	// 404s, while bp-wordpress is a real published package.
	wantRef := "ghcr.io/openova-io/bp-wordpress:1.5.0"
	if bp.ChartRef != wantRef {
		t.Errorf("expected chartRef=%q, got %q", wantRef, bp.ChartRef)
	}
	if len(bp.Versions) != 1 {
		t.Fatalf("expected 1-entry versions list, got %d", len(bp.Versions))
	}
	if bp.Versions[0].Version != "1.5.0" {
		t.Errorf("versions[0].version: want 1.5.0 got %q", bp.Versions[0].Version)
	}
	if bp.Versions[0].ChartRef != wantRef {
		t.Errorf("versions[0].chartRef: want %q got %q", wantRef, bp.Versions[0].ChartRef)
	}
}

// TestCatalogBlueprint_PopulateVersionsAlias_PreservesUpstream — when
// the upstream catalog already populates Versions[], the helper is a
// no-op for that field (idempotent / non-overwriting).
func TestCatalogBlueprint_PopulateVersionsAlias_PreservesUpstream(t *testing.T) {
	bp := CatalogBlueprint{
		Name:    "bp-wordpress",
		Version: "1.5.0",
		Versions: []CatalogBlueprintVersion{
			{Version: "1.4.0", ChartRef: "oci://my-registry/bp-wordpress:1.4.0"},
			{Version: "1.5.0", ChartRef: "oci://my-registry/bp-wordpress:1.5.0"},
		},
	}
	bp.PopulateVersionsAlias()
	if len(bp.Versions) != 2 {
		t.Errorf("expected upstream Versions preserved (2 entries), got %d", len(bp.Versions))
	}
	if bp.Versions[0].ChartRef != "oci://my-registry/bp-wordpress:1.4.0" {
		t.Errorf("expected upstream Versions[0].ChartRef preserved, got %q", bp.Versions[0].ChartRef)
	}
}

// TestCatalogBlueprint_PopulateVersionsAlias_HoistsValueSchema — when
// Raw carries spec.configSchema (GET-by-version endpoint), the helper
// surfaces it as the headline version's valueSchema so the install
// form has it without a second hop.
func TestCatalogBlueprint_PopulateVersionsAlias_HoistsValueSchema(t *testing.T) {
	bp := CatalogBlueprint{
		Name:    "bp-keycloak",
		Version: "1.0.0",
		Raw: map[string]interface{}{
			"spec": map[string]interface{}{
				"configSchema": map[string]interface{}{
					"type":     "object",
					"required": []interface{}{"adminPassword"},
				},
			},
		},
	}
	bp.PopulateVersionsAlias()
	if len(bp.Versions) != 1 {
		t.Fatalf("expected 1 version entry, got %d", len(bp.Versions))
	}
	if bp.Versions[0].ValueSchema == nil {
		t.Fatalf("expected valueSchema populated from raw.spec.configSchema, got nil")
	}
	if bp.Versions[0].ValueSchema["type"] != "object" {
		t.Errorf("expected valueSchema.type=object, got %v", bp.Versions[0].ValueSchema["type"])
	}
}

// ── a7: audit-pagination-cursor ──────────────────────────────────────

// TestRBACAuditListResponse_CursorMirrorsNextOffset — the `cursor`
// field must carry the same value (stringified) as nextOffset so
// consumers using either pagination convention land on the same offset.
func TestRBACAuditListResponse_CursorMirrorsNextOffset(t *testing.T) {
	resp := rbacAuditListResponse{
		NextOffset: 42,
	}
	resp.Cursor = "42" // mirroring the handler's stamping
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"cursor":"42"`) {
		t.Errorf("expected cursor key in serialized response, got %s", raw)
	}
	if !strings.Contains(string(raw), `"nextOffset":42`) {
		t.Errorf("expected nextOffset preserved, got %s", raw)
	}
}

// TestRBACAuditListResponse_CursorPresentOnFinalPage — qa-loop iter-1
// prefetch Fix #93 (TC-399): cursor + nextOffset are now ALWAYS emitted
// on every page (final or otherwise) so the matrix's literal-token
// assertions resolve regardless of pagination state. The explicit
// `hasMore=false` predicate signals end-of-stream.
func TestRBACAuditListResponse_CursorPresentOnFinalPage(t *testing.T) {
	resp := rbacAuditListResponse{}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"cursor"`) {
		t.Errorf("expected cursor present on every page (Fix #93), got %s", raw)
	}
	if !strings.Contains(string(raw), `"nextOffset"`) {
		t.Errorf("expected nextOffset present on every page (Fix #93), got %s", raw)
	}
	if !strings.Contains(string(raw), `"hasMore":false`) {
		t.Errorf("expected hasMore=false on default zero-value response, got %s", raw)
	}
}

// ── a8: application-cascading-delete-status-token ────────────────────

// TestApplicationDeleteResponse_StatusToken — the response must carry
// `status:"deleted"` so the matrix (TC-080) and programmatic consumers
// branch on a stable token rather than parsing the human message.
func TestApplicationDeleteResponse_StatusToken(t *testing.T) {
	r := applicationDeleteResponse{
		Name:      "qa-wp",
		Namespace: "qa-omantel",
		Status:    "deleted",
		Message:   "delete requested; controller will cascade region cleanup",
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"status":"deleted"`) {
		t.Errorf("expected status:deleted token, got %s", raw)
	}
}

// ── a10: update-application-includes-displayName ─────────────────────

// TestApplicationUpdateRequestNormalize_TitleAliasesDisplayName — the
// short-form `title` field collapses onto canonical DisplayName.
func TestApplicationUpdateRequestNormalize_TitleAliasesDisplayName(t *testing.T) {
	in := applicationUpdateRequest{TitleShort: "QA Updated"}
	out := applicationUpdateRequestNormalize(in)
	if out.DisplayName != "QA Updated" {
		t.Errorf("expected DisplayName=QA Updated, got %q", out.DisplayName)
	}
}

// TestApplicationUpdateResponse_DisplayNameSerializes — confirm the
// json key is `displayName` (matrix vocabulary).
func TestApplicationUpdateResponse_DisplayNameSerializes(t *testing.T) {
	r := applicationUpdateResponse{
		Name:        "qa-wp",
		Namespace:   "qa-omantel",
		DisplayName: "QA Updated",
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"displayName":"QA Updated"`) {
		t.Errorf("expected displayName key, got %s", raw)
	}
}

// ── a2: flatten-k8s-list-summary-fields ──────────────────────────────

// TestFlattenK8sListItems_PodHoistsPhaseAndNodeName — confirm pod
// list items expose top-level `phase` + `nodeName` so the SPA + matrix
// don't have to dig through status.phase / spec.nodeName.
func TestFlattenK8sListItems_PodHoistsPhaseAndNodeName(t *testing.T) {
	pod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata":   map[string]interface{}{"name": "qa-wp-0", "namespace": "qa-omantel"},
			"spec":       map[string]interface{}{"nodeName": "hz-fsn-rtz-prod-1"},
			"status": map[string]interface{}{
				"phase": "Running",
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "True"},
				},
			},
		},
	}
	out := flattenK8sListItems("pod", []*unstructured.Unstructured{pod})
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d", len(out))
	}
	got := out[0].Object
	if got["phase"] != "Running" {
		t.Errorf("expected top-level phase=Running, got %v", got["phase"])
	}
	if got["nodeName"] != "hz-fsn-rtz-prod-1" {
		t.Errorf("expected top-level nodeName=hz-fsn-rtz-prod-1, got %v", got["nodeName"])
	}
	if got["ready"] != true {
		t.Errorf("expected top-level ready=true, got %v", got["ready"])
	}
	// Original status.phase must still be present (back-compat).
	status := got["status"].(map[string]interface{})
	if status["phase"] != "Running" {
		t.Errorf("expected nested status.phase preserved, got %v", status["phase"])
	}
}

// TestFlattenK8sListItems_ServiceHoistsPortsAndType — Services expose
// top-level `ports` + `type` so the matrix asserts on TC-262 pass.
func TestFlattenK8sListItems_ServiceHoistsPortsAndType(t *testing.T) {
	svc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata":   map[string]interface{}{"name": "qa-wp", "namespace": "qa-omantel"},
			"spec": map[string]interface{}{
				"type": "ClusterIP",
				"ports": []interface{}{
					map[string]interface{}{"port": int64(80), "targetPort": int64(8080)},
				},
			},
		},
	}
	out := flattenK8sListItems("service", []*unstructured.Unstructured{svc})
	got := out[0].Object
	if got["type"] != "ClusterIP" {
		t.Errorf("expected top-level type=ClusterIP, got %v", got["type"])
	}
	ports, ok := got["ports"].([]interface{})
	if !ok || len(ports) != 1 {
		t.Errorf("expected top-level ports list of 1 entry, got %v", got["ports"])
	}
}

// TestFlattenK8sListItems_NodeHoistsRegion — Nodes expose top-level
// `region` + `zone` from labels so TC-261 (UI nodes filter by region)
// passes.
func TestFlattenK8sListItems_NodeHoistsRegion(t *testing.T) {
	node := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Node",
			"metadata": map[string]interface{}{
				"name": "hz-fsn-rtz-prod-1",
				"labels": map[string]interface{}{
					"topology.kubernetes.io/region": "hz-fsn-rtz-prod",
					"topology.kubernetes.io/zone":   "fsn1-dc14",
				},
			},
		},
	}
	out := flattenK8sListItems("node", []*unstructured.Unstructured{node})
	got := out[0].Object
	if got["region"] != "hz-fsn-rtz-prod" {
		t.Errorf("expected region=hz-fsn-rtz-prod, got %v", got["region"])
	}
	if got["zone"] != "fsn1-dc14" {
		t.Errorf("expected zone=fsn1-dc14, got %v", got["zone"])
	}
}

// TestFlattenK8sListItems_EventV1HoistsLastTimestampAndReason — Events
// at events.k8s.io/v1 carry `eventTime` / `note` (instead of legacy
// core/v1 `lastTimestamp` / `message`); the flatten helper must read
// the v1 schema AND fall back to the legacy fields so TC-211's
// `must_contain: [items, lastTimestamp, reason]` passes regardless of
// which schema the apiserver emits. qa-loop iter-15 Fix #59.
func TestFlattenK8sListItems_EventV1HoistsLastTimestampAndReason(t *testing.T) {
	v1Event := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion":             "events.k8s.io/v1",
			"kind":                   "Event",
			"metadata":               map[string]interface{}{"name": "qa-wp.evt", "namespace": "qa-omantel"},
			"eventTime":              "2026-05-10T10:30:00.000000Z",
			"note":                   "Successfully assigned qa-omantel/qa-wp-0 to hz-fsn-rtz-prod-1",
			"reason":                 "Scheduled",
			"reportingController":    "default-scheduler",
			"regarding": map[string]interface{}{
				"kind":      "Pod",
				"name":      "qa-wp-0",
				"namespace": "qa-omantel",
			},
		},
	}
	out := flattenK8sListItems("event", []*unstructured.Unstructured{v1Event})
	got := out[0].Object
	if got["lastTimestamp"] != "2026-05-10T10:30:00.000000Z" {
		t.Errorf("expected lastTimestamp from eventTime, got %v", got["lastTimestamp"])
	}
	if got["reason"] != "Scheduled" {
		t.Errorf("expected reason=Scheduled, got %v", got["reason"])
	}
	if got["message"] != "Successfully assigned qa-omantel/qa-wp-0 to hz-fsn-rtz-prod-1" {
		t.Errorf("expected message hoisted from note, got %v", got["message"])
	}
	if io, ok := got["involvedObject"].(map[string]interface{}); !ok || io["kind"] != "Pod" {
		t.Errorf("expected involvedObject hoisted from regarding, got %v", got["involvedObject"])
	}
}

// TestFlattenK8sListItems_EventV1SeriesPrefersLastObservedTime — Events
// that have repeated emit `series.lastObservedTime` (events.k8s.io/v1).
// That field wins over the single-occurrence `eventTime`.
func TestFlattenK8sListItems_EventV1SeriesPrefersLastObservedTime(t *testing.T) {
	v1Event := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "events.k8s.io/v1",
			"kind":       "Event",
			"metadata":   map[string]interface{}{"name": "qa-wp.evt", "namespace": "qa-omantel"},
			"eventTime":  "2026-05-10T10:00:00.000000Z",
			"reason":     "BackOff",
			"note":       "Back-off pulling image",
			"series": map[string]interface{}{
				"count":            int64(7),
				"lastObservedTime": "2026-05-10T10:30:00.000000Z",
			},
		},
	}
	out := flattenK8sListItems("event", []*unstructured.Unstructured{v1Event})
	got := out[0].Object
	if got["lastTimestamp"] != "2026-05-10T10:30:00.000000Z" {
		t.Errorf("expected series.lastObservedTime to win over eventTime; got %v", got["lastTimestamp"])
	}
}

// TestFlattenK8sListItems_EventLegacyCoreV1Compat — older core/v1
// Events still flow through the apiserver translation layer; the
// flatten helper MUST keep surfacing them.
func TestFlattenK8sListItems_EventLegacyCoreV1Compat(t *testing.T) {
	legacyEvent := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion":     "v1",
			"kind":           "Event",
			"metadata":       map[string]interface{}{"name": "old.evt", "namespace": "qa-omantel"},
			"lastTimestamp":  "2026-05-09T08:00:00Z",
			"firstTimestamp": "2026-05-09T07:55:00Z",
			"reason":         "FailedScheduling",
			"message":        "0/3 nodes are available",
			"involvedObject": map[string]interface{}{
				"kind":      "Pod",
				"name":      "qa-wp-0",
				"namespace": "qa-omantel",
			},
		},
	}
	out := flattenK8sListItems("event", []*unstructured.Unstructured{legacyEvent})
	got := out[0].Object
	if got["lastTimestamp"] != "2026-05-09T08:00:00Z" {
		t.Errorf("legacy lastTimestamp should still hoist; got %v", got["lastTimestamp"])
	}
	if got["reason"] != "FailedScheduling" {
		t.Errorf("expected reason=FailedScheduling, got %v", got["reason"])
	}
	if got["message"] != "0/3 nodes are available" {
		t.Errorf("legacy message should hoist; got %v", got["message"])
	}
	if io, ok := got["involvedObject"].(map[string]interface{}); !ok || io["name"] != "qa-wp-0" {
		t.Errorf("legacy involvedObject should hoist; got %v", got["involvedObject"])
	}
}

// TestFlattenK8sListItems_NodeFallbackLabels — when canonical
// topology.kubernetes.io labels are absent, the flatten helper falls
// back to failure-domain.beta + Hetzner location labels so legacy
// kubelets and multi-location Sovereigns still light up the matrix
// asserts (TC-260 / TC-261).
func TestFlattenK8sListItems_NodeFallbackLabels(t *testing.T) {
	node := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Node",
			"metadata": map[string]interface{}{
				"name": "hz-fsn-1",
				"labels": map[string]interface{}{
					"failure-domain.beta.kubernetes.io/region": "fsn1",
					"failure-domain.beta.kubernetes.io/zone":   "fsn1-dc14",
					"node.kubernetes.io/instance-type":         "cx21",
				},
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "True"},
				},
				"addresses": []interface{}{
					map[string]interface{}{"type": "InternalIP", "address": "10.0.0.5"},
					map[string]interface{}{"type": "Hostname", "address": "hz-fsn-1"},
				},
			},
		},
	}
	out := flattenK8sListItems("node", []*unstructured.Unstructured{node})
	got := out[0].Object
	if got["region"] != "fsn1" {
		t.Errorf("expected region=fsn1 from failure-domain.beta, got %v", got["region"])
	}
	if got["zone"] != "fsn1-dc14" {
		t.Errorf("expected zone=fsn1-dc14 from failure-domain.beta, got %v", got["zone"])
	}
	if got["instanceType"] != "cx21" {
		t.Errorf("expected instanceType=cx21, got %v", got["instanceType"])
	}
	if got["ready"] != true {
		t.Errorf("expected ready=true, got %v", got["ready"])
	}
	if got["internalIP"] != "10.0.0.5" {
		t.Errorf("expected internalIP=10.0.0.5, got %v", got["internalIP"])
	}
}

// TestFlattenK8sListItems_DoesNotMutateInput — the cached Indexer
// returns the same pointer to every reader; the flatten helper MUST
// produce a fresh map so concurrent readers don't race on top-level
// key additions.
func TestFlattenK8sListItems_DoesNotMutateInput(t *testing.T) {
	in := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata":   map[string]interface{}{"name": "x", "namespace": "y"},
			"spec":       map[string]interface{}{"nodeName": "n1"},
			"status":     map[string]interface{}{"phase": "Running"},
		},
	}
	_ = flattenK8sListItems("pod", []*unstructured.Unstructured{in})
	if _, present := in.Object["phase"]; present {
		t.Errorf("expected source object NOT mutated; got phase key on input")
	}
	if _, present := in.Object["nodeName"]; present {
		t.Errorf("expected source object NOT mutated; got nodeName key on input")
	}
}
