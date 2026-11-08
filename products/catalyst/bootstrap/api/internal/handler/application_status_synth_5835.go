package handler

import "context"

// statusFromSynthesised projects the CR-less answer that
// GET /applications/{name} already produces onto the /status wire shape
// (#5835, UAT rows 67 + 69).
//
// WHY IT DELEGATES. synthesiseAppFromHelmRelease (bootstrap-kit HRs) is the
// path that already knows how to answer for a component with no Application
// CR (the Pod-readiness runtime synth, #5827, was deleted by ADR-0015 D7:
// Pod readiness is not Application status). Re-deriving a phase from the HelmRelease
// here would be a THIRD derivation of the same fact, and two was already enough
// for the detail path and the status path to disagree in front of an operator.
//
// It reports only what the synthesised answer carries — name, namespace,
// conditions and the statusOf ObjectStatus (namespace-exact).
//
// `Conditions` is normalised from nil to an empty slice so this path never
// emits JSON `null`. Note that the field carries `omitempty`, so an empty slice
// is OMITTED rather than rendered as `[]` — i.e. the key is already sometimes
// absent on the CR-backed path too, and callers must tolerate that. The
// normalisation is about not emitting `null` where a list is expected, not
// about guaranteeing the key is present; a first draft of this comment claimed
// the latter and the struct tag contradicts it.
//
// NO SPEC. A synthesised answer has no Application CR, so there is no
// spec.placement to project and `Spec` stays nil. The Topology tab derives real
// placement from live Pods via .../placement anyway (#3982), which is the
// authoritative source for bootstrap components; inventing a spec here would
// hand it a second, weaker answer to disagree with.
func (h *Handler) statusFromSynthesised(ctx context.Context, depID, name, ns string) (applicationStatusResponse, bool) {
	var out applicationStatusResponse

	detail, ok := h.synthesiseAppFromHelmRelease(ctx, depID, name, ns)
	if !ok {
		return out, false
	}

	out.Name = detail.Name
	out.Namespace = detail.Namespace
	out.Phase = detail.Phase
	out.ObjectStatus = detail.ObjectStatus
	out.Conditions = detail.Conditions
	if out.Conditions == nil {
		out.Conditions = []map[string]interface{}{}
	}
	out.LastReconciled = detail.LastReconciled
	return out, true
}
