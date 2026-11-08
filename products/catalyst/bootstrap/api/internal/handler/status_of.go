// status_of.go — ADR-0015 D7: "the object is the status".
//
// ONE function answers "what is the status of this thing": statusOf. It
// reads the object's OWN conditions — the Ready condition the reconciler
// publishes, and for Applications the per-region Serving condition the
// application-controller publishes from its functional probe — and returns
// them VERBATIM: state, reason, message, since.
//
// What it deliberately does NOT do — and what this file replaces:
//
//   - It never promotes a phase because some OTHER object (a same-named
//     HelmRelease found anywhere in the cache) is Ready. The HR-Ready overlay
//     (applications.go, C4-003/#4889) was deleted with this file: it matched
//     HRs by NAME ONLY, so on hw307 two HRs named bp-keycloak (flux-system
//     Ready, acmewalk307 UpgradeFailed) answered for each other.
//   - It never defaults an absent condition to "Pending", "Healthy" or
//     "Ready". Absence of a measurement is Unknown — the third answer the
//     Ready tri-state exists for.
//   - It never reads Pod readiness as Application readiness (the deleted
//     runtime synth, #5827).
//
// Every surface — Application detail, /status, the apps grid, Topology —
// carries the SAME ObjectStatus, so two pages can no longer disagree about
// one object. status_of_guard_test.go fails the build if handler code
// outside this file assigns a Phase/"Ready"/"Healthy" literal.
package handler

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// Object states. Mapped 1:1 from the Ready condition's status:
// True → Ready, False → NotReady, Unknown or absent → Unknown.
const (
	StateReady    = "Ready"
	StateNotReady = "NotReady"
	StateUnknown  = "Unknown"
)

// ServingConditionType is the per-region functional-probe condition the
// application-controller publishes (ADR-0015 D7).
const ServingConditionType = "Serving"

// ObjectStatus is the one status contract every console surface renders.
type ObjectStatus struct {
	State     string         `json:"state"`
	Reason    string         `json:"reason,omitempty"`
	Message   string         `json:"message,omitempty"`
	Since     string         `json:"since,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	Name      string         `json:"name,omitempty"`
	PerRegion []RegionStatus `json:"perRegion,omitempty"`
}

// RegionStatus is one region's verbatim condition.
type RegionStatus struct {
	Region  string `json:"region"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Since   string `json:"since,omitempty"`
}

// stateFromConditionStatus maps a condition status string onto the state
// vocabulary. Anything that is not exactly True/False is Unknown.
func stateFromConditionStatus(s string) string {
	switch s {
	case "True":
		return StateReady
	case "False":
		return StateNotReady
	default:
		return StateUnknown
	}
}

// conditionOf returns the named condition map from a conditions slice, or nil.
func conditionOf(conds []interface{}, condType string) map[string]interface{} {
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == condType {
			return cm
		}
	}
	return nil
}

// statusOf reads obj's Ready condition (and, when present, the per-region
// Serving conditions under status.regions[]) and returns them verbatim.
// A nil object or an object with no Ready condition is Unknown with no
// reason — never a guessed phase.
func statusOf(obj *unstructured.Unstructured) ObjectStatus {
	if obj == nil {
		return ObjectStatus{State: StateUnknown}
	}
	out := ObjectStatus{
		State:     StateUnknown,
		Kind:      obj.GetKind(),
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if cm := conditionOf(conds, "Ready"); cm != nil {
		s, _ := cm["status"].(string)
		out.State = stateFromConditionStatus(s)
		out.Reason, _ = cm["reason"].(string)
		out.Message, _ = cm["message"].(string)
		out.Since, _ = cm["lastTransitionTime"].(string)
	}
	regions, _, _ := unstructured.NestedSlice(obj.Object, "status", "regions")
	for _, r := range regions {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := rm["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		rs := RegionStatus{Region: name, State: StateUnknown}
		rconds, _ := rm["conditions"].([]interface{})
		if cm := conditionOf(rconds, ServingConditionType); cm != nil {
			s, _ := cm["status"].(string)
			rs.State = stateFromConditionStatus(s)
			rs.Reason, _ = cm["reason"].(string)
			rs.Message, _ = cm["message"].(string)
			rs.Since, _ = cm["lastTransitionTime"].(string)
		}
		out.PerRegion = append(out.PerRegion, rs)
	}
	return out
}

// statusOfAcrossRegions is statusOf for an object that exists once PER
// REGION (a HelmRelease installed in each region's cluster). Each region's
// copy is the SAME namespace + name — namespace-exact, never name-only — and
// contributes one PerRegion entry. The headline state is the first region's
// verbatim (regions sorted for determinism) unless another region is
// NotReady, in which case that region's condition is the headline: a broken
// region must never be hidden behind a green one.
func statusOfAcrossRegions(byRegion map[string]*unstructured.Unstructured) ObjectStatus {
	if len(byRegion) == 0 {
		return ObjectStatus{State: StateUnknown}
	}
	regions := make([]string, 0, len(byRegion))
	for r := range byRegion {
		regions = append(regions, r)
	}
	sort.Strings(regions)
	var head ObjectStatus
	headSet := false
	per := make([]RegionStatus, 0, len(regions))
	for _, r := range regions {
		st := statusOf(byRegion[r])
		per = append(per, RegionStatus{Region: r, State: st.State, Reason: st.Reason, Message: st.Message, Since: st.Since})
		if !headSet || (st.State == StateNotReady && head.State != StateNotReady) {
			head = st
			headSet = true
		}
	}
	head.PerRegion = per
	return head
}

// orderHelmReleaseCandidates returns the HRs that answer for `name` (by
// metadata.name or spec.releaseName), namespace-exact when ns is given, in a
// deterministic order: flux-system first, then by namespace, then by name.
func orderHelmReleaseCandidates(hrs []*unstructured.Unstructured, name, ns string) []*unstructured.Unstructured {
	ns = strings.TrimSpace(ns)
	var out []*unstructured.Unstructured
	for _, hr := range hrs {
		if hr == nil {
			continue
		}
		if ns != "" && hr.GetNamespace() != ns {
			continue
		}
		rn, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName")
		if hr.GetName() != name && rn != name {
			continue
		}
		out = append(out, hr)
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := out[i].GetNamespace() == "flux-system", out[j].GetNamespace() == "flux-system"
		if fi != fj {
			return fi
		}
		if out[i].GetNamespace() != out[j].GetNamespace() {
			return out[i].GetNamespace() < out[j].GetNamespace()
		}
		return out[i].GetName() < out[j].GetName()
	})
	return out
}

// helmReleaseStatusAcrossRegions finds the SAME namespace/name HelmRelease in
// every cluster of the deployment (primary + each registered region) and
// returns statusOfAcrossRegions over them. Region keys come from
// clusterRegionMap; a cluster with no declared region is keyed by its id.
func (h *Handler) helmReleaseStatusAcrossRegions(depID, primaryClusterID string, primary *unstructured.Unstructured) ObjectStatus {
	if primary == nil {
		return ObjectStatus{State: StateUnknown}
	}
	regionOf := h.clusterRegionMap(depID)
	label := func(cid string) string {
		if r := regionOf[cid]; r != "" {
			return r
		}
		return cid
	}
	byRegion := map[string]*unstructured.Unstructured{label(primaryClusterID): primary}
	if h.k8sCache != nil {
		for _, cid := range h.k8sCache.Clusters() {
			if cid == primaryClusterID {
				continue
			}
			if _, ok := regionOf[cid]; !ok {
				// Not one of THIS deployment's region clusters.
				continue
			}
			hrs, _, err := h.k8sCache.List(cid, "helmrelease", labels.Everything())
			if err != nil {
				continue
			}
			for _, hr := range hrs {
				if hr.GetNamespace() == primary.GetNamespace() && hr.GetName() == primary.GetName() {
					byRegion[label(cid)] = hr
					break
				}
			}
		}
	}
	st := statusOfAcrossRegions(byRegion)
	if len(st.PerRegion) <= 1 {
		st.PerRegion = nil
	}
	return st
}
