// sandbox_mapper.go — PURE TRANSFORM for Sandbox CR + sandbox-related
// Pod events. Mirrors mapper.go (HelmRelease side) but for the per-Org
// agent-coding workspace introduced in products/sandbox/.
//
// What lands on the canvas:
//
//   - One FlowNode per `sandbox.openova.io/v1 Sandbox` CR. The bubble
//     shows the owner email (Sandbox.metadata.name) and groups under
//     `family = "sandbox"`. Status maps `.status.phase`:
//
//     Pending      → "pending"
//     Provisioning → "running"
//     Ready        → "succeeded"
//     Failed       → "failed"
//     (missing)    → "pending"
//
//   - One FlowNode per pty-server / openova-sandbox-mcp Pod inside the
//     Sandbox's namespace. Each Pod carries
//     `meta.kind = "SandboxPod"`; its parent (via `contains`) is the
//     owning Sandbox node so the canvas groups them under the Sandbox
//     bubble.
//
// `meta.kind` is the discriminator the UI consults to swap the bubble
// glyph (terminal SVG for Sandbox; small "›_" for SandboxPod). Existing
// HR-derived nodes continue to carry NO `meta.kind` so the canvas
// falls back to the legacy status-glyph rendering — backwards
// compatible.
//
// `meta.parent` carries the tenant-org node id so the canvas can pull
// the Sandbox bubble under the tenant-org cluster (the task brief
// `parent=<tenant-org>`). The canvas treats this advisory; the
// `contains` Relationship is the load-bearing edge.
//
// Pure: no I/O, no clock, deterministic given the input.
package informer

import (
	"fmt"
	"strings"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Sandbox node-id format: "<regionKey>:sandbox:<sandbox-name>" — the
// extra "sandbox:" segment keeps the id from colliding with HR node
// ids (which are "<regionKey>:<hr-name>") even when the operator
// names a HelmRelease the same string as a Sandbox.
const sandboxIDPrefix = "sandbox" + idSep

// Pod node-id format: "<regionKey>:sandbox-pod:<namespace>/<pod-name>".
// Namespace is required for stable identity because two Orgs can run
// pty-server pods with identical generated names.
const sandboxPodIDPrefix = "sandbox-pod" + idSep

// FamilySandbox — fixed family for Sandbox CR + its Pods, so the
// canvas colour-bands every sandbox-y bubble identically. Operators
// can still override via the `catalyst.openova.io/family` label per
// LabelFamily in mapper.go.
const FamilySandbox = "sandbox"

// KindSandbox / KindSandboxPod — `meta.kind` values the canvas reads
// to pick the bubble glyph. Open vocabulary: any future K8s object
// type the adapter supports adds a new constant here.
const (
	KindSandbox    = "Sandbox"
	KindSandboxPod = "SandboxPod"
)

// LabelSandboxOwnerNS — namespace label that marks "this namespace
// belongs to Sandbox <name>". The sandbox-controller stamps this so
// the adapter can group Pods back under the parent Sandbox without
// re-parsing the namespace stem.
const LabelSandboxOwnerNS = "sandbox.openova.io/owner-namespace"

// LabelSandboxName — Pod label set by the sandbox-controller
// referencing the parent Sandbox CR name. When present, the Pod's
// FlowNode emits a `contains` edge to that Sandbox.
const LabelSandboxName = "sandbox.openova.io/name"

// SandboxPodComponentLabel — component-classifier the
// sandbox-controller stamps on its Deployments. The adapter watches
// Pods carrying any of the values below.
const SandboxPodComponentLabel = "app.kubernetes.io/component"

var sandboxPodComponents = map[string]struct{}{
	"pty-server":            {},
	"openova-sandbox-mcp":   {},
	"sandbox-pty":           {}, // historic alias; keep until Wave 2 lock-in
	"sandbox-mcp":           {}, // historic alias; keep until Wave 2 lock-in
}

// BuildFromSandbox converts one Sandbox CR (unstructured) into a
// FlowNode + a contains Relationship pulling the bubble under the
// tenant-org node (when spec.owner.orgRef.slug is set).
//
// Region behaviour mirrors HR mapping — empty regionKey falls back to
// "default" so unit tests stay terse.
func BuildFromSandbox(sb *unstructured.Unstructured, regionKey string) (MapResult, bool) {
	if sb == nil {
		return MapResult{}, false
	}
	name := sb.GetName()
	if name == "" {
		return MapResult{}, false
	}
	region := strings.TrimSpace(regionKey)
	if region == "" {
		region = "default"
	}

	id := sandboxNodeID(region, name)
	status := sandboxStatusFromPhase(sb)

	meta := map[string]interface{}{
		"kind":      KindSandbox,
		"namespace": sb.GetNamespace(),
	}
	if owner, ok := sandboxOwnerEmail(sb); ok {
		meta["ownerEmail"] = owner
	}
	orgSlug, _ := sandboxOrgSlug(sb)
	if orgSlug != "" {
		meta["orgSlug"] = orgSlug
		// parent advisory — canvas may inspect this when no explicit
		// contains edge has been received yet (e.g. tenant-org node
		// emitted by a different adapter).
		meta["parent"] = tenantOrgNodeID(region, orgSlug)
	}
	if sess, found, _ := unstructured.NestedInt64(sb.Object, "status", "sessions"); found {
		meta["sessions"] = sess
	}
	if su, found, _ := unstructured.NestedString(sb.Object, "status", "storageUsed"); found && su != "" {
		meta["storageUsed"] = su
	}

	label := name
	if owner, ok := sandboxOwnerEmail(sb); ok && owner != "" {
		label = owner
	}

	node := types.FlowNode{
		ID:     id,
		FlowID: "", // populated by emitter with the deployment id
		Label:  label,
		Status: status,
		Family: ptr(familyForSandbox(sb)),
		Region: ptr(region),
		Meta:   meta,
	}

	rels := make([]types.Relationship, 0, 1)
	if orgSlug != "" {
		// contains: tenant-org node (parent) contains this Sandbox node
		// (child). Per the locked contract — fromId=child, toId=parent.
		rels = append(rels, types.Relationship{
			FromID:    id,
			ToID:      tenantOrgNodeID(region, orgSlug),
			Type:      "contains",
			Condition: "always",
		})
	}
	return MapResult{Node: node, Relationships: rels}, true
}

// BuildFromSandboxPod converts a sandbox-component Pod into a FlowNode
// + contains edge to its parent Sandbox. Returns ok=false for Pods
// that are not part of a Sandbox (caller should ignore them).
//
// Parent identity is resolved in priority order:
//  1. Pod label `LabelSandboxName` — explicit reference set by the
//     sandbox-controller.
//  2. Namespace stem `sandbox-<sanitized-email>` — falls out of the
//     CRD's namespace-naming convention.
//
// When neither resolves, the Pod is treated as standalone and emits
// no contains edge.
func BuildFromSandboxPod(pod *corev1.Pod, regionKey string) (MapResult, bool) {
	if pod == nil {
		return MapResult{}, false
	}
	if !isSandboxComponentPod(pod) {
		return MapResult{}, false
	}
	region := strings.TrimSpace(regionKey)
	if region == "" {
		region = "default"
	}

	component := pod.Labels[SandboxPodComponentLabel]
	id := sandboxPodNodeID(region, pod.Namespace, pod.Name)
	status := sandboxPodStatus(pod)

	meta := map[string]interface{}{
		"kind":      KindSandboxPod,
		"component": component,
		"namespace": pod.Namespace,
		"podName":   pod.Name,
	}

	parentSandboxName := strings.TrimSpace(pod.Labels[LabelSandboxName])
	if parentSandboxName == "" {
		// Fallback: derive from namespace stem `sandbox-<name>`. The
		// sandbox-controller stamps namespaces in this form per the
		// CRD's namespace-naming convention, so the stem-strip is
		// safe even without the LabelSandboxOwnerNS marker.
		if strings.HasPrefix(pod.Namespace, "sandbox-") {
			parentSandboxName = strings.TrimPrefix(pod.Namespace, "sandbox-")
		}
	}

	rels := make([]types.Relationship, 0, 1)
	if parentSandboxName != "" {
		parentID := sandboxNodeID(region, parentSandboxName)
		meta["parent"] = parentID
		rels = append(rels, types.Relationship{
			FromID:    id,
			ToID:      parentID,
			Type:      "contains",
			Condition: "always",
		})
	}

	node := types.FlowNode{
		ID:     id,
		FlowID: "",
		Label:  podShortLabel(pod, component),
		Status: status,
		Family: ptr(FamilySandbox),
		Region: ptr(region),
		Meta:   meta,
	}
	return MapResult{Node: node, Relationships: rels}, true
}

// isSandboxComponentPod returns true when the Pod's
// `app.kubernetes.io/component` label is one of the known sandbox
// components.
func isSandboxComponentPod(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	c, ok := pod.Labels[SandboxPodComponentLabel]
	if !ok || c == "" {
		return false
	}
	_, isComponent := sandboxPodComponents[c]
	return isComponent
}

// sandboxStatusFromPhase maps the CR's `.status.phase` enum to the
// canvas palette. Mirrors statusFromConditions on the HR side — both
// terminate in the same 4-tone palette.
func sandboxStatusFromPhase(sb *unstructured.Unstructured) string {
	phase, _, _ := unstructured.NestedString(sb.Object, "status", "phase")
	switch phase {
	case "Ready":
		return "succeeded"
	case "Provisioning":
		return "running"
	case "Failed":
		return "failed"
	default:
		return "pending"
	}
}

// sandboxPodStatus collapses the standard Pod lifecycle into the same
// 4-tone palette the canvas uses for every other bubble. Crash-loop
// surfaces as "failed"; container-creating / pulling-image stays
// "running" so an operator can SEE the bubble light up before the
// pod is fully Ready.
func sandboxPodStatus(pod *corev1.Pod) string {
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return "succeeded"
	case corev1.PodFailed:
		return "failed"
	case corev1.PodPending:
		// Distinguish ImagePull/CrashLoop from genuine queue wait.
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				switch cs.State.Waiting.Reason {
				case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull",
					"CreateContainerConfigError", "RunContainerError":
					return "failed"
				}
			}
		}
		return "pending"
	case corev1.PodRunning:
		// All containers Ready → succeeded; otherwise "running" (the
		// pod's containers are warming up — pty-server typically takes
		// 5-10s to bind its listener after the container starts).
		allReady := true
		hasContainers := false
		for _, cs := range pod.Status.ContainerStatuses {
			hasContainers = true
			if !cs.Ready {
				allReady = false
				break
			}
		}
		if hasContainers && allReady {
			return "succeeded"
		}
		return "running"
	default:
		return "pending"
	}
}

// sandboxOwnerEmail reads spec.owner.email defensively (the schema
// requires it, but unstructured paths still need ok-checks for
// hand-constructed test fixtures).
func sandboxOwnerEmail(sb *unstructured.Unstructured) (string, bool) {
	v, found, err := unstructured.NestedString(sb.Object, "spec", "owner", "email")
	if err != nil || !found {
		return "", false
	}
	return v, v != ""
}

// sandboxOrgSlug reads spec.owner.orgRef.slug — the parent
// Organization's slug. Used to wire the contains edge into the
// tenant-org node.
func sandboxOrgSlug(sb *unstructured.Unstructured) (string, bool) {
	v, found, err := unstructured.NestedString(sb.Object, "spec", "owner", "orgRef", "slug")
	if err != nil || !found {
		return "", false
	}
	return v, v != ""
}

// familyForSandbox honours the operator-set LabelFamily override,
// otherwise hardcodes FamilySandbox. Mirrors familyFor on HR side.
func familyForSandbox(sb *unstructured.Unstructured) string {
	labels := sb.GetLabels()
	if v, ok := labels[LabelFamily]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return FamilySandbox
}

// sandboxNodeID — canonical FlowNode.id for a Sandbox CR.
func sandboxNodeID(region, name string) string {
	return region + idSep + sandboxIDPrefix + name
}

// sandboxPodNodeID — canonical FlowNode.id for a sandbox-component Pod.
func sandboxPodNodeID(region, namespace, podName string) string {
	return region + idSep + sandboxPodIDPrefix + namespace + "/" + podName
}

// tenantOrgNodeID — id the canvas uses for the tenant-org bubble.
// Convention: "<region>:org:<slug>" — keeps the namespace
// orthogonal to HR ids ("<region>:bp-*") and Sandbox ids
// ("<region>:sandbox:*"). When the tenant-org node has not yet been
// emitted by another adapter the canvas will still render the
// contains edge to a "ghost" id; the d3-force layout drops dangling
// edges in computeLayout.
func tenantOrgNodeID(region, slug string) string {
	return region + idSep + "org" + idSep + slug
}

// podShortLabel collapses long generated pod names to "<component>
// (<pod suffix>)" — fits the canvas's 18-char label cap.
func podShortLabel(pod *corev1.Pod, component string) string {
	if component == "" {
		return pod.Name
	}
	// Strip the deployment-hash + replica-set-suffix to keep the most
	// human-readable bit: e.g. "pty-server-7d4f8" → "(7d4f8)".
	parts := strings.Split(pod.Name, "-")
	if len(parts) > 2 {
		tail := parts[len(parts)-1]
		return fmt.Sprintf("%s (%s)", component, tail)
	}
	return component
}

