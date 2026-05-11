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
// FlowNode.id = "{regionKey}/{hrName}" — region-aware so multi-region
// renders correctly when the canvas pulls bubbles from N adapter sidecars.
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

// RegionContainsType — relationship type the adapter emits to group
// each HR FlowNode under the per-region synthetic parent node.
const RegionContainsType = "contains"

// DependsOnType — relationship type the adapter emits for every
// HelmRelease.spec.dependsOn entry.
const DependsOnType = "finish-to-start"

// MapResult — what BuildFromHR returns: the FlowNode for this HR + the
// edges (dependsOn fan-in + the region-contains edge).
type MapResult struct {
	Node          types.FlowNode
	Relationships []types.Relationship
}

// BuildFromHR converts one HelmRelease (as unstructured) into a
// FlowNode + Relationship set. regionKey is the env-driven cluster id
// (e.g. "fsn1"); the FlowNode id becomes "{regionKey}/{hr.metadata.name}".
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
		ID:     region + "/" + name,
		FlowID: "", // populated by emitter with the deployment id
		Label:  name,
		Status: statusFromConditions(hr),
		Family: ptr(familyFor(hr, name)),
		Region: ptr(region),
	}

	// Relationship #1: contains under the synthetic region node.
	rels := []types.Relationship{
		{
			FromID:    region,
			ToID:      node.ID,
			Type:      RegionContainsType,
			Condition: "always",
		},
	}

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
			FromID:    region + "/" + depName,
			ToID:      node.ID,
			Type:      DependsOnType,
			Condition: "on-success",
		})
	}
	return MapResult{Node: node, Relationships: rels}, true
}

// BuildRegionNode emits the synthetic per-region parent FlowNode the
// adapter sends on startup so the canvas has a stable container for
// the region's HRs. Status stays "running" — the region itself has
// no terminal lifecycle.
func BuildRegionNode(regionKey string) types.FlowNode {
	if strings.TrimSpace(regionKey) == "" {
		regionKey = "default"
	}
	r := regionKey
	return types.FlowNode{
		ID:     regionKey,
		Label:  regionKey,
		Status: "running",
		Family: ptr("region"),
		Region: &r,
	}
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
// heuristic "<name without bp- prefix without first word>" only when
// the label is absent.
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
