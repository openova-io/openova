package handler

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// synthesiseAppFromRuntime — the THIRD and last fallback of
// GET /sovereigns/{id}/applications/{name}, after the Application CR and the
// same-named HelmRelease have both missed (#5827, UAT row 188).
//
// WHY IT EXISTS. `catalyst-api` is a Deployment rendered BY the
// bp-catalyst-platform HelmRelease. It owns no Application CR and no HR of its
// own, so both earlier lookups miss and the endpoint 404s — while the sibling
// endpoint on the same path, .../applications/catalyst-api/placement, answers
// 200 with a correct single Primary target because it derives from live Pods.
// The API knew the component and denied it existed, depending on the suffix,
// and the console rendered "App not found — the component catalyst-api is not
// part of this deployment" for something plainly running in front of the caller.
//
// ONE IDENTITY PREDICATE, NOT TWO. Existence is decided by
// podBelongsToComponent — the exact function the placement path uses. Deriving
// it a second way here is what would let the two endpoints drift back apart,
// and re-answering the same question differently is the defect this closes, not
// an implementation detail of it.
//
// IT FABRICATES NOTHING. No CR means no uid, no blueprint, no version, no
// parameters, no environmentRef — all left empty rather than guessed. The
// response carries only what was observed: the namespace the Pods actually run
// in, and a phase read off their readiness. `RuntimeDerived` marks the whole
// answer as an observation rather than a declaration, so a consumer can tell
// the difference without inferring it from which fields happen to be blank.
//
// The primary cluster only. A component present solely in a secondary region is
// out of scope here — the detail endpoint describes one install, and the
// per-region picture is what the placement endpoint is for. Returning a
// half-answer keyed on whichever cluster happened to list first would be worse
// than the miss.
func (h *Handler) synthesiseAppFromRuntime(depID, name, ns string) (applicationDetailResponse, bool) {
	var out applicationDetailResponse
	name = strings.TrimSpace(name)
	if name == "" || h.k8sCache == nil {
		return out, false
	}
	clusterID := h.resolveChrootClusterID(depID)
	if clusterID == "" {
		return out, false
	}
	pods, _, err := h.k8sCache.List(clusterID, "pod", labels.Everything())
	if err != nil {
		return out, false
	}

	var matched []*unstructured.Unstructured
	for _, p := range pods {
		if podBelongsToComponent(p, name, ns) {
			matched = append(matched, p)
		}
	}
	if len(matched) == 0 {
		return out, false
	}

	// Namespace: taken from the Pods, never from the request. A caller that
	// passed no `?namespace=` gets the truth rather than a default, which is
	// the bug ("?namespace=default → 0 items") that the CR path already had to
	// fix once.
	out.Name = name
	out.Namespace = matched[0].GetNamespace()
	out.TargetNamespace = out.Namespace
	out.ReleaseName = name
	out.RuntimeDerived = true
	out.Conditions = []map[string]interface{}{}

	// Phase from readiness, via the EXISTING podIsReady (dashboard.go) rather
	// than a local copy — same reasoning as the identity predicate above: two
	// readings of "is this Pod healthy" is how surfaces start disagreeing.
	//
	// Deliberately only these three words. "Ready"
	// requires EVERY matched Pod ready — one degraded replica of three is not a
	// ready component, and rounding that up is how a status panel starts lying.
	ready := 0
	for _, p := range matched {
		if podIsReady(p) {
			ready++
		}
	}
	switch {
	case ready == len(matched):
		out.Phase = "Ready"
	case ready > 0:
		out.Phase = "Degraded"
	default:
		out.Phase = "Provisioning"
	}
	out.InstallLabelSelector = installLabelSelectorForHR(name)
	return out, true
}
