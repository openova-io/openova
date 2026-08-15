// applications_placement_identity_6344.go — how the placement projection
// decides that a live Pod belongs to the Application it was asked about.
//
// THE DEFECT (#6344, measured on hw298 dep 2540d866403f1f7c, owner session,
// one binary, one pass). Four Applications, all `phase: Ready`, all asked the
// same question on the same endpoint:
//
//	shared-pg      ns shared-data  active-hot-standby → 2 targets, Primary + Standby, both with a cluster
//	spine-gitea    ns catalyst     active-hot-standby → 1 target,  Standby, cluster ""
//	spine-keycloak ns catalyst     active-hot-standby → 1 target,  Standby, cluster ""
//	spine-openbao  ns catalyst     active-passive     → 1 target,  Standby, cluster ""
//
// `shared-pg` is the CONTROL and it proves the projection itself works: #6268 /
// #6291 / #6301 are present and functioning. The three that collapse differ
// from it in exactly ONE property, and it is not a property of the app.
//
// `derivePlacementTargets` keeps a Pod when `podBelongsToComponent` says so,
// and that predicate compares the ROUTE ID the console asked with — through
// `componentNameCandidates`, i.e. `{name, TrimPrefix(name, "bp-")}` — against
// labels the CHART happens to stamp: `app.kubernetes.io/instance`,
// `app.kubernetes.io/name`, the loft display name. A Pod is therefore joined to
// its Application by STRING COINCIDENCE.
//
//   - `shared-pg` matches because the CNPG Cluster it owns is itself named
//     `shared-pg`, so the pods' identity happens to resolve back to the app's
//     own name.
//   - `spine-gitea` is an Application CR (post_handover_spine_apps.go) named
//     `spine-gitea` in ns `catalyst` that ADOPTS HelmRelease
//     `flux-system/bp-gitea`. That HR declares `releaseName: gitea` and
//     `targetNamespace: gitea`; its Pods are labelled
//     `app.kubernetes.io/instance=gitea` and live in ns `gitea`. Neither the
//     name nor the namespace coincides, occupancy matches nothing, NO Primary
//     is produced, and control falls through to `augmentWithContinuumStandby` —
//     the only term in the whole path that can emit `cluster: ""` — leaving the
//     lone Standby and `unresolvedPrimary: true` the walk recorded.
//
// So the blast radius is not "Catalog-provisioned apps" (the hw296 reading) and
// not "spine apps" (the hw298 reading). It is every Application whose name does
// not coincide with a label on its own Pods.
//
// THE FIX. Stop asking the route id what the workload is called and ask the
// authoritative link instead: Application CR → adopted HelmRelease →
// `spec.releaseName` + `spec.targetNamespace`. That is not a new mechanism —
// it is the SAME chain the rest of this package already trusts:
// `installLabelSelectorForHR` (#1928) derives the Resources/Logs tab selector
// as `app.kubernetes.io/instance=<releaseName>` off exactly these fields, and
// `hrLookupName` (#4889) already resolves `spine-gitea → flux-system/bp-gitea`
// through `spec.helmRelease.name` for the HR-Ready overlay. The placement path
// was the one consumer still deriving identity a second way, which is why it is
// the one that disagreed.
//
// STRICTLY WIDENING, NEVER NARROWING. Resolution can only ADD identity strings
// and namespaces that a live authoritative object NAMES. It never removes a
// route candidate (so every app that resolves today keeps resolving), and it
// never narrows the namespace set: when the caller passed no `?namespace=`, the
// set stays empty — i.e. every namespace — because turning "anywhere" into "the
// declared targetNamespace" would be a new way to MISS pods, which is the
// defect, not the fix.
//
// AND IT CANNOT FABRICATE A PRIMARY. Everything here decides which Pods are
// LOOKED AT. An app with no Pods in a region still yields no occupancy there,
// still yields no Primary, and the #6268 half-pair refusal still fires — the
// declaration is never allowed to stand in for an observation. Refs #6344,
// #3375 (UAT row 60).
package handler

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// spineAdoptsHelmReleaseLabel names the EXISTING bootstrap HelmRelease a spine
// Application CR enrolls rather than re-renders (Invariant #3, #4416). It is
// the back-pointer of last resort here: a CR that carries the label but not
// `spec.helmRelease` still resolves to its workload.
const spineAdoptsHelmReleaseLabel = "catalyst.openova.io/adopts-helmrelease"

// componentWorkloadIdentity is the set of identities a Pod may carry to count
// as part of one Application, plus the namespaces it may occupy.
//
// The two name sets are deliberately NOT merged, because they are matched
// differently and merging them would import a heuristic into a place that has a
// contract:
//
//   - `route` holds what the CALLER said (`componentNameCandidates`). Nothing
//     guarantees a chart stamped it anywhere, so it keeps the historical
//     three-way match — applicationKey, app.kubernetes.io/name, and the loft
//     display-name PREFIX.
//   - `release` holds Helm release names read off a live Application CR /
//     HelmRelease. Here the Helm chart-helpers contract (#1928) DOES guarantee
//     `app.kubernetes.io/instance=<releaseName>` on every rendered object, so
//     the label match is exact and sufficient. Extending the display-name
//     prefix to these would absorb a SIBLING release whose name merely starts
//     the same way — `gitea-pg`, rendered BY bp-gitea but a different workload
//     with its own CNPG role labels, would be counted as gitea itself and its
//     role labels would then drive the collapse in derivePlacementTargets.
type componentWorkloadIdentity struct {
	route      []string
	release    []string
	namespaces []string // empty = every namespace (the bootstrap-safe default)
}

// routeWorkloadIdentity is the identity the caller stated and nothing more —
// byte-for-byte the pre-#6344 matching input. Used directly by callers that
// have no authoritative link to resolve against.
func routeWorkloadIdentity(name, ns string) componentWorkloadIdentity {
	id := componentWorkloadIdentity{route: componentNameCandidates(name)}
	if ns = strings.TrimSpace(ns); ns != "" {
		id.namespaces = []string{ns}
	}
	return id
}

// addRelease records a resolved Helm release name. A release that merely
// repeats a route candidate adds nothing and is dropped, so the route set stays
// the only place a given string is matched loosely.
func (id *componentWorkloadIdentity) addRelease(rel string) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return
	}
	for _, r := range id.route {
		if r == rel {
			return
		}
	}
	for _, r := range id.release {
		if r == rel {
			return
		}
	}
	id.release = append(id.release, rel)
}

// addNamespace widens the namespace set. Only ever called when the set is
// ALREADY restrictive (the caller passed `?namespace=`); widening an empty set
// would narrow "every namespace" down to one, which is the miss this file
// exists to remove.
func (id *componentWorkloadIdentity) addNamespace(ns string) {
	ns = strings.TrimSpace(ns)
	if ns == "" || len(id.namespaces) == 0 {
		return
	}
	for _, n := range id.namespaces {
		if n == ns {
			return
		}
	}
	id.namespaces = append(id.namespaces, ns)
}

// matchesNamespace reports whether the Pod sits in one of the identity's
// namespaces, reusing objectInAppNamespace so the loft-synced case (host ns
// `mgmt`, in-vCluster ns `gitea`) resolves exactly as it does for the
// Resources tab. An empty set matches every namespace.
func (id componentWorkloadIdentity) matchesNamespace(p *unstructured.Unstructured) bool {
	if len(id.namespaces) == 0 {
		return true
	}
	for _, ns := range id.namespaces {
		if objectInAppNamespace(p, ns) {
			return true
		}
	}
	return false
}

// podCarriesReleaseIdentity is the exact-label half of the join: the #1928
// contract that every Helm-rendered object of release R carries
// `app.kubernetes.io/instance=R` (the loft syncer preserves both labels
// verbatim, so a host-synced Pod is covered without touching its mangled name).
func podCarriesReleaseIdentity(p *unstructured.Unstructured, release string) bool {
	if p == nil || release == "" {
		return false
	}
	lbls := p.GetLabels()
	return lbls["app.kubernetes.io/instance"] == release ||
		lbls["app.kubernetes.io/name"] == release
}

// podBelongsToIdentity is THE identity predicate — one function, so the
// placement endpoint, the #5827 runtime-synth existence check, and anything
// added later cannot answer "is this Pod part of that app" two different ways.
func podBelongsToIdentity(p *unstructured.Unstructured, id componentWorkloadIdentity) bool {
	if p == nil {
		return false
	}
	if !id.matchesNamespace(p) {
		return false
	}

	// Route candidates — the historical match, unchanged.
	//
	// nil ReplicaSet index: this join has no cache handle, so a Pod owned by a
	// ReplicaSet keeps the ReplicaSet name (pre-#5485 behavior). The
	// instance/name labels below carry the match in the cases this is called for.
	appKey := applicationKey(p, nil)
	chartName := p.GetLabels()["app.kubernetes.io/name"]
	// The de-mangled in-vCluster object name (loft annotation), e.g. a host Pod
	// `grafana-…-x-grafana-x-mgmt-vcluster` carries object-name `grafana-…`.
	displayName := vClusterSyncedDisplayName(p)
	for _, cand := range id.route {
		if cand == "" {
			continue
		}
		if appKey == cand || chartName == cand {
			return true
		}
		if displayName != "" && strings.HasPrefix(displayName, cand) {
			return true
		}
	}

	// Resolved release names — exact label identity only (see the struct doc).
	for _, rel := range id.release {
		if podCarriesReleaseIdentity(p, rel) {
			return true
		}
	}
	return false
}

// resolveComponentWorkloadIdentity walks the authoritative link from the route
// id to the workload it actually installs.
//
//	Application CR ──spec.helmRelease{name,namespace}──▶ HelmRelease
//	      │                (or the adopts-helmrelease label)     │
//	      └─ spec.releaseName / spec.targetNamespace             └─ spec.releaseName / spec.targetNamespace
//
// Both hops are best-effort and independently useful: a wizard-installed app
// resolves off its own CR fields, a bare bootstrap-kit HelmRelease resolves off
// the HR alone (the #3370 shape, where the console addresses the RELEASE
// `shared-pg` while the HR is named `bp-postgres-shared`), and a spine CR
// resolves through the adoption pointer. Every miss leaves the identity exactly
// as the caller stated it, so a cluster with no CRDs, an unregistered informer,
// or a component that is neither CR- nor HR-backed behaves as it did before.
func (h *Handler) resolveComponentWorkloadIdentity(primaryID, name, ns string) componentWorkloadIdentity {
	id := routeWorkloadIdentity(name, ns)
	if h.k8sCache == nil || strings.TrimSpace(primaryID) == "" || len(id.route) == 0 {
		return id
	}

	route := make(map[string]struct{}, len(id.route))
	for _, c := range id.route {
		route[c] = struct{}{}
	}

	// (1) The Application CR, when the route id names one.
	var hrName, hrNS string
	if a := h.applicationCRForComponent(primaryID, id.route, ns); a != nil {
		if rn, _, _ := unstructured.NestedString(a.Object, "spec", "releaseName"); rn != "" {
			id.addRelease(rn)
		}
		if tn, _, _ := unstructured.NestedString(a.Object, "spec", "targetNamespace"); tn != "" {
			id.addNamespace(tn)
		}
		if n, _, _ := unstructured.NestedString(a.Object, "spec", "helmRelease", "name"); n != "" {
			hrName = n
			hrNS, _, _ = unstructured.NestedString(a.Object, "spec", "helmRelease", "namespace")
		} else if adopted := strings.TrimSpace(a.GetLabels()[spineAdoptsHelmReleaseLabel]); adopted != "" {
			hrName = adopted
		}
	}

	// (2) The HelmRelease — the one the CR adopts, or one the route id names
	// directly (by object name or by spec.releaseName, the #3370 pair).
	if hrs, _, err := h.k8sCache.List(primaryID, "helmrelease", labels.Everything()); err == nil {
		for _, hr := range hrs {
			if hr == nil {
				continue
			}
			releaseName, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName")
			if !helmReleaseNamesComponent(hr, releaseName, route, hrName, hrNS) {
				continue
			}
			if releaseName == "" {
				// Flux v2 default: the release takes the HR's own name.
				releaseName = hr.GetName()
			}
			id.addRelease(releaseName)
			if tn, _, _ := unstructured.NestedString(hr.Object, "spec", "targetNamespace"); tn != "" {
				id.addNamespace(tn)
			}
		}
	}

	return id
}

// applicationCRForComponent is THE selector for "which Application CR does this
// route id name" — one function, so every consumer of that CR (the workload
// identity above, the declared placement in #6347, anything added later) reads
// the SAME object under the SAME isolation rule. Re-hand-rolling this loop is
// how two surfaces start answering "which app is this" differently, which is
// the #5827 / #6344 shape all over again.
//
// 🔒 Organization isolation: the CR is selected the way getApplicationCR already
// selects it — an EXACT (name, namespace) match when the caller scoped the
// request (the console passes the CR's own namespace), and a cluster-wide name
// match only when it did not. Accepting any namespace on a scoped request would
// let one Organization's `wordpress` answer for another's.
//
// Returns nil on every miss: no cache, no primary cluster, no candidates, an
// unlistable Application kind, or simply no CR by that name.
func (h *Handler) applicationCRForComponent(primaryID string, routeCandidates []string, ns string) *unstructured.Unstructured {
	if h.k8sCache == nil || strings.TrimSpace(primaryID) == "" || len(routeCandidates) == 0 {
		return nil
	}
	route := make(map[string]struct{}, len(routeCandidates))
	for _, c := range routeCandidates {
		if c = strings.TrimSpace(c); c != "" {
			route[c] = struct{}{}
		}
	}
	apps, _, err := h.k8sCache.List(primaryID, "application", labels.Everything())
	if err != nil {
		return nil
	}
	crNS := strings.TrimSpace(ns)
	for _, a := range apps {
		if a == nil {
			continue
		}
		if _, ok := route[a.GetName()]; !ok {
			continue
		}
		if crNS != "" && a.GetNamespace() != crNS {
			continue
		}
		return a
	}
	return nil
}

// helmReleaseNamesComponent reports whether this HelmRelease is the one backing
// the component — either because the Application CR pointed at it
// (`spec.helmRelease`, matched on namespace too when the CR stated one), or
// because the route id is the HR's own name or its release name.
func helmReleaseNamesComponent(hr *unstructured.Unstructured, releaseName string, route map[string]struct{}, hrName, hrNS string) bool {
	if hrName != "" && hr.GetName() == hrName && (hrNS == "" || hr.GetNamespace() == hrNS) {
		return true
	}
	if _, ok := route[hr.GetName()]; ok {
		return true
	}
	if releaseName != "" {
		if _, ok := route[releaseName]; ok {
			return true
		}
	}
	return false
}
