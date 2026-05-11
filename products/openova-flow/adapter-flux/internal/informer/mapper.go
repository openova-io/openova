// Package informer maps Flux HelmRelease state changes to FlowMessage
// envelopes the openova-flow-server understands.
//
// This file contains the PURE TRANSFORM — no I/O, no client-go calls.
// It is unit-tested against fixture HelmRelease YAML in test/.
//
// Status mapping per the locked OpenovaFlow contract (Agent #2 brief):
//
//	Ready=True                                  → "succeeded"
//	Ready=False, Reason=Progressing             → "running"
//	Ready=False, Reason=InstallFailed|
//	                    UpgradeFailed|
//	                    RetriesExhausted        → "failed"
//	Ready=Unknown                               → "running"
//	No Ready condition yet                      → "pending"
//
// FlowNode.id = "{regionKey}:{hrName}" — region-aware so multi-region
// renders correctly when the canvas pulls bubbles from N adapter sidecars.
// Separator is ":" (not "/") so node ids do not collide with URL routing
// — founder hit "Not Found" drilling into `contabo/phase-0`.
//
// The adapter emits ONE FlowNode per HR (the real leaf) and the
// finish-to-start edges for every spec.dependsOn entry. The natural
// canvas (FlowCanvasOrganic) does its own bounded force layout — there
// are NO synthetic region / phase parent nodes. That earlier
// scaffolding (Agent #6) was rejected by the founder 2026-05-11; this
// file is the post-revert minimum.
package informer

import (
	"strings"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// LabelFamily — when the operator sets this label on the HR, the
// adapter uses it verbatim as FlowNode.family. Mirrors the
// "label-driven config" rule (#4 never-hardcode).
const LabelFamily = "catalyst.openova.io/family"

// LabelSlot — slot number embedded in the bootstrap-kit HR filename
// (`<NN>-bp-<name>.yaml`). Surfaced as a chart label so the adapter
// can derive metadata without hardcoding HR names. Mirrors the
// catalyst.openova.io/component pattern already used by
// platform/external-secrets-stores/chart/templates/*.yaml and
// platform/openclaw/chart/templates/*.yaml.
const LabelSlot = "catalyst.openova.io/slot"

// LabelComponent — generic component-classifier label already in use
// across platform/* charts. Forwarded to the FlowNode meta for
// downstream consumers; not used here to bucket nodes.
const LabelComponent = "catalyst.openova.io/component"

// DependsOnType — relationship type the adapter emits for every
// HelmRelease.spec.dependsOn entry.
const DependsOnType = "finish-to-start"

// idSep — node-id separator. ":" not "/" so node ids do not collide
// with URL routing. Founder caught "/" colliding mid-prov.
const idSep = ":"

// MapResult — what BuildFromHR returns: the FlowNode for this HR + the
// finish-to-start edges from spec.dependsOn entries.
type MapResult struct {
	Node          types.FlowNode
	Relationships []types.Relationship
}

// BuildFromHR converts one HelmRelease (as unstructured) into a single
// FlowNode + zero-or-more finish-to-start Relationships from
// spec.dependsOn. regionKey is the env-driven cluster id (e.g. "fsn1");
// the FlowNode id becomes "{regionKey}:{hr.metadata.name}".
//
// No synthetic parents (region root / phase columns) — the canvas does
// its own bounded force layout.
//
// Pure: no I/O, no clock, deterministic given the input.
func BuildFromHR(hr *unstructured.Unstructured, regionKey string) (MapResult, bool) {
	if hr == nil {
		return MapResult{}, false
	}
	name := hr.GetName()
	if name == "" {
		return MapResult{}, false
	}
	region := strings.TrimSpace(regionKey)
	if region == "" {
		region = "default"
	}

	node := types.FlowNode{
		ID:     region + idSep + name,
		FlowID: "", // populated by emitter with the deployment id
		Label:  name,
		Status: statusFromConditions(hr),
		Family: ptr(familyFor(hr, name)),
		Region: ptr(region),
	}

	rels := make([]types.Relationship, 0)

	// Relationships from spec.dependsOn — one per entry.
	deps, _, _ := unstructured.NestedSlice(hr.Object, "spec", "dependsOn")
	for _, d := range deps {
		m, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		depName, _ := m["name"].(string)
		if strings.TrimSpace(depName) == "" {
			continue
		}
		rels = append(rels, types.Relationship{
			FromID:    region + idSep + depName,
			ToID:      node.ID,
			Type:      DependsOnType,
			Condition: "on-success",
		})
	}
	return MapResult{Node: node, Relationships: rels}, true
}

// statusFromConditions — maps the HR's Ready condition to the
// FlowNode.status palette. Reads from the standard
// .status.conditions[] shape Flux v2 uses (ObservedGeneration,
// LastTransitionTime, Type, Status, Reason, Message).
func statusFromConditions(hr *unstructured.Unstructured) string {
	conds, found, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
	if !found || len(conds) == 0 {
		return "pending"
	}
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if typ != "Ready" {
			continue
		}
		status, _ := m["status"].(string)
		reason, _ := m["reason"].(string)
		switch status {
		case "True":
			return "succeeded"
		case "False":
			switch reason {
			case "InstallFailed", "UpgradeFailed", "RetriesExhausted":
				return "failed"
			case "Progressing":
				return "running"
			default:
				// Default to "running" — a False Ready that's not in
				// the failed-list typically means in-flight Flux
				// retry, which the operator wants to see as
				// non-terminal.
				return "running"
			}
		case "Unknown":
			return "running"
		}
	}
	return "pending"
}

// familyFor reads the operator-set label first; falls back to the
// heuristic "<name without bp- prefix>" only when the label is absent.
//
// Examples:
//
//	bp-cert-manager  → "cert-manager"  (heuristic: drop "bp-")
//	bp-hcloud-ccm    → "hcloud-ccm"    (heuristic)
//	with label "platform" set → "platform"
func familyFor(hr *unstructured.Unstructured, name string) string {
	labels := hr.GetLabels()
	if v, ok := labels[LabelFamily]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return strings.TrimPrefix(name, "bp-")
}

func ptr[T any](v T) *T { return &v }
