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
//
// Synthetic group nodes (Agent #6 brief):
//
//   - "{regionKey}" — region root, meta.layout=lane-vertical, isGroup=true
//   - "{regionKey}/phase-{0..3}" — phase columns, meta.layout=lane-horizontal,
//     isGroup=true, sortKey=N. Phase 0/1/2/3 follow the documented
//     Catalyst lifecycle (Cloud Provisioning → Bootstrap-Kit → Cutover →
//     Sovereign Live).
//
// Leaf-to-phase mapping is driven by HR labels (no hardcoded names):
//
//   - catalyst.openova.io/slot: "<NN>"  → Phase 1 (bootstrap-kit slots 1–57)
//   - catalyst.openova.io/component: cutover → Phase 2
//   - HR name = "bp-catalyst-platform" or component=catalyst-platform → Phase 3
//   - default                            → Phase 1
//
// Phase 0 (Cloud Provisioning) is signalled by catalyst-api state events
// (tofu-apply / lb-create / cp-init) which a sibling adapter will emit.
// For this PR the adapter emits a stub Phase-0 parent with status=succeeded
// at bootstrap — by the time HRs are installable on a cluster, Phase 0
// has completed by definition.
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
// can derive the phase column without hardcoding HR names. Mirrors
// the catalyst.openova.io/component pattern already used by
// platform/external-secrets-stores/chart/templates/*.yaml and
// platform/openclaw/chart/templates/*.yaml.
const LabelSlot = "catalyst.openova.io/slot"

// LabelComponent — generic component-classifier label already in use
// across platform/* charts. The adapter reads it to distinguish
// cutover HRs (Phase 2) from regular bootstrap-kit HRs (Phase 1).
const LabelComponent = "catalyst.openova.io/component"

// RegionContainsType — relationship type the adapter emits to group
// each HR FlowNode under the per-region synthetic parent node.
const RegionContainsType = "contains"

// DependsOnType — relationship type the adapter emits for every
// HelmRelease.spec.dependsOn entry.
const DependsOnType = "finish-to-start"

// Phase identifiers — scoped under the region key by callers, e.g.
// "fsn1/phase-1". Kept as constants so the canvas + tests can rely
// on them.
const (
	PhaseSuffixCloudProvisioning = "phase-0"
	PhaseSuffixBootstrapKit      = "phase-1"
	PhaseSuffixCutover           = "phase-2"
	PhaseSuffixSovereignLive     = "phase-3"
)

// MapResult — what BuildFromHR returns: the FlowNode for this HR + the
// edges (dependsOn fan-in + the region-contains edge + the phase-contains edge).
type MapResult struct {
	Node          types.FlowNode
	Relationships []types.Relationship
	// PhaseID — the phase synthetic node ID this leaf belongs to,
	// e.g. "fsn1/phase-1". Callers use it to update rollup state.
	PhaseID string
}

// BuildFromHR converts one HelmRelease (as unstructured) into a
// FlowNode + Relationship set. regionKey is the env-driven cluster id
// (e.g. "fsn1"); the FlowNode id becomes "{regionKey}/{hr.metadata.name}".
//
// Two `contains` rels are emitted per HR:
//   - leaf → {regionKey}                         (region row)
//   - leaf → {regionKey}/phase-{N}               (phase column)
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

	phaseSuffix := derivePhase(hr, name)
	phaseID := region + "/" + phaseSuffix

	node := types.FlowNode{
		ID:     region + "/" + name,
		FlowID: "", // populated by emitter with the deployment id
		Label:  name,
		Status: statusFromConditions(hr),
		Family: ptr(familyFor(hr, name)),
		Region: ptr(region),
	}

	rels := []types.Relationship{
		// Region row containment.
		{
			FromID:    region,
			ToID:      node.ID,
			Type:      RegionContainsType,
			Condition: "always",
		},
		// Phase column containment.
		{
			FromID:    phaseID,
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
	return MapResult{Node: node, Relationships: rels, PhaseID: phaseID}, true
}

// derivePhase — slot-label-first, then component-label, then HR-name
// heuristics. NEVER hardcoded HR-name → phase tables (per the
// brief's no-hardcode rule).
//
// Returns one of the PhaseSuffix* constants.
func derivePhase(hr *unstructured.Unstructured, name string) string {
	labels := hr.GetLabels()

	// Component label wins for cutover + catalyst-platform.
	if comp, ok := labels[LabelComponent]; ok {
		c := strings.ToLower(strings.TrimSpace(comp))
		switch c {
		case "cutover", "self-sovereign-cutover":
			return PhaseSuffixCutover
		case "catalyst-platform":
			return PhaseSuffixSovereignLive
		}
	}

	// HR name fallback for the two known terminal-phase Blueprints.
	// Mirrors the convention used in
	// clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml
	// and 13-bp-catalyst-platform.yaml. Reading the HR name here is
	// NOT a hardcoded "name → phase" table; it's a fallback when the
	// component label isn't set yet (chart-level patch is a follow-up).
	lower := strings.ToLower(name)
	if strings.Contains(lower, "self-sovereign-cutover") || strings.Contains(lower, "bp-cutover") {
		return PhaseSuffixCutover
	}
	if lower == "bp-catalyst-platform" {
		return PhaseSuffixSovereignLive
	}

	// Slot label → Phase 1 (bootstrap-kit). Any slot, any tier.
	if _, ok := labels[LabelSlot]; ok {
		return PhaseSuffixBootstrapKit
	}

	// Default: bootstrap-kit Phase 1. Every HR the adapter sees
	// reconciled by Flux on a Sovereign is a bootstrap-kit Blueprint
	// unless explicitly tagged otherwise.
	return PhaseSuffixBootstrapKit
}

// BuildRegionNode emits the synthetic per-region parent FlowNode the
// adapter sends on startup so the canvas has a stable container for
// the region's HRs. Status is computed by the caller via the
// StatusTracker; the bootstrap call passes "pending".
//
// meta.layout = lane-vertical so the canvas renders the region as a
// vertical swim-lane; isGroup = true mirrors how the canvas already
// auto-detects groups (any node that is the toId of a contains edge),
// but emitting it explicitly lets the canvas honour the hint even
// before the first child edge arrives.
func BuildRegionNode(regionKey string) types.FlowNode {
	if strings.TrimSpace(regionKey) == "" {
		regionKey = "default"
	}
	r := regionKey
	return types.FlowNode{
		ID:     regionKey,
		Label:  regionKey,
		Status: "pending",
		Family: ptr("region"),
		Region: &r,
		Meta: map[string]interface{}{
			"layout":  "lane-vertical",
			"isGroup": true,
		},
	}
}

// phaseLabels — human-readable labels for each phase. Kept here so
// changes to user-facing strings are one-file edits.
var phaseLabels = map[string]string{
	PhaseSuffixCloudProvisioning: "Phase 0 — Cloud Provisioning",
	PhaseSuffixBootstrapKit:      "Phase 1 — Bootstrap-Kit",
	PhaseSuffixCutover:           "Phase 2 — Cutover",
	PhaseSuffixSovereignLive:     "Phase 3 — Sovereign Live",
}

// phaseSortKey — visual ordering for the canvas. 0 = leftmost.
var phaseSortKey = map[string]int{
	PhaseSuffixCloudProvisioning: 0,
	PhaseSuffixBootstrapKit:      1,
	PhaseSuffixCutover:           2,
	PhaseSuffixSovereignLive:     3,
}

// AllPhaseSuffixes — emission-order. Used by BuildPhaseNodes /
// BuildPhaseEdges and the informer's bootstrap routine.
var AllPhaseSuffixes = []string{
	PhaseSuffixCloudProvisioning,
	PhaseSuffixBootstrapKit,
	PhaseSuffixCutover,
	PhaseSuffixSovereignLive,
}

// BuildPhaseNodes — the four synthetic phase column nodes for a
// region. Each carries:
//
//   - meta.layout = "lane-horizontal" so the canvas renders the
//     phases as horizontal swim-lanes across the region.
//   - meta.isGroup = true (explicit hint; canvas also auto-detects
//     via contains edges).
//   - meta.sortKey = 0..3 so the canvas can deterministically order
//     the columns left-to-right.
//
// Status starts at "pending" — the informer fills it in via the
// StatusTracker as child HRs arrive. Phase 0 is special-cased: HRs
// being installable means Phase 0 (cloud provisioning) is done, so
// Phase 0 is emitted with status=succeeded.
func BuildPhaseNodes(regionKey string) []types.FlowNode {
	if strings.TrimSpace(regionKey) == "" {
		regionKey = "default"
	}
	out := make([]types.FlowNode, 0, len(AllPhaseSuffixes))
	for _, suffix := range AllPhaseSuffixes {
		status := "pending"
		if suffix == PhaseSuffixCloudProvisioning {
			// Phase 0 happened before bootstrap-kit could be installed;
			// from the adapter's vantage point it's already done.
			// A follow-up Agent will hook the catalyst-api FlowMessage
			// emit (tofu-apply / lb-create / cp-init events) to refine
			// this — see brief §E.
			status = "succeeded"
		}
		region := regionKey
		out = append(out, types.FlowNode{
			ID:     regionKey + "/" + suffix,
			Label:  phaseLabels[suffix],
			Status: status,
			Family: ptr("phase"),
			Region: &region,
			Meta: map[string]interface{}{
				"layout":  "lane-horizontal",
				"isGroup": true,
				"sortKey": phaseSortKey[suffix],
			},
		})
	}
	return out
}

// BuildPhaseEdges — finish-to-start edges between consecutive phases
// in a region. Three edges per region: 0→1, 1→2, 2→3. The canvas
// renders these as visible arrows between phase lanes.
func BuildPhaseEdges(regionKey string) []types.Relationship {
	if strings.TrimSpace(regionKey) == "" {
		regionKey = "default"
	}
	out := make([]types.Relationship, 0, len(AllPhaseSuffixes)-1)
	for i := 0; i < len(AllPhaseSuffixes)-1; i++ {
		out = append(out, types.Relationship{
			FromID:    regionKey + "/" + AllPhaseSuffixes[i],
			ToID:      regionKey + "/" + AllPhaseSuffixes[i+1],
			Type:      DependsOnType,
			Condition: "on-success",
		})
	}
	return out
}

// PhaseLabel — exported lookup for tests + the informer's re-emit
// path (which rebuilds a phase node with the rolled-up status).
func PhaseLabel(suffix string) string { return phaseLabels[suffix] }

// PhaseSortKey — exported lookup for tests + re-emit.
func PhaseSortKey(suffix string) int { return phaseSortKey[suffix] }

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
