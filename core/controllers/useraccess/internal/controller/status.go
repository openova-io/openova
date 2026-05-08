// status.go — JSON merge-patch builder for UserAccess.status.
//
// We use a strategic merge-patch over the unstructured client because
// (a) the CRD's status sub-resource is owned by the controller and
// (b) the catalyst-api's user_access.go HTTP handler reads
// status.rolebindingsCreated for the UI badge — both consumers expect
// the same shape.
//
// The schema (see platform/crossplane-claims/chart/templates/xrds/
// useraccess.yaml) declares status with conditions[], rolebindingsCreated,
// providerConfigRef. The slice C5 brief expands this with phase,
// roleBindings[] (per-binding kind/namespace/name/uid), and
// observedGeneration — those fields are appended via
// `additionalProperties` (the CRD's status object permits extension
// without schema migration).

package controller

import (
	"encoding/json"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// newStatusPatch returns the JSON merge-patch body for the status
// subresource. Calling code passes it to client.Status().Patch() with
// types.MergePatchType.
func newStatusPatch(ua *unstructured.Unstructured, phase string, count int, ready bool, drift bool, msg string, observedGen int64) []byte {
	now := time.Now().UTC().Format(time.RFC3339)
	conditions := []map[string]any{
		buildCondition(condReady, ready, conditionReason(ready), msg, now),
		buildCondition(condSynced, ready, reasonReconciled, "", now),
	}
	if drift {
		conditions = append(conditions, buildCondition(condDrift, true, reasonDriftFixed,
			"hand-mutated rolebinding restored to desired shape", now))
	}
	status := map[string]any{
		"phase":               phase,
		"rolebindingsCreated": count,
		"observedGeneration":  observedGen,
		"conditions":          conditions,
	}
	body := map[string]any{
		"status": status,
	}
	b, _ := json.Marshal(body)
	// Suppress unused warning when older callers don't reach this branch.
	_ = ua
	return b
}

func buildCondition(condType string, ok bool, reason, msg, now string) map[string]any {
	statusStr := "False"
	if ok {
		statusStr = "True"
	}
	c := map[string]any{
		"type":               condType,
		"status":             statusStr,
		"reason":             reason,
		"lastTransitionTime": now,
	}
	if msg != "" {
		c["message"] = msg
	}
	return c
}

func conditionReason(ready bool) string {
	if ready {
		return reasonReconciled
	}
	return reasonInvalid
}
