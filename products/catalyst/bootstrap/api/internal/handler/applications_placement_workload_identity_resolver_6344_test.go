package handler

// applications_placement_workload_identity_resolver_6344_test.go — the half of
// the #6344 contract the endpoint-level table cannot see: resolution is
// ADDITIVE. It lives in its own file because it names the resolver directly,
// which is API that does not exist on the pre-fix tree the table test is
// watched RED against.

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

// Two ways this fix could regress into a NEW miss rather than removing one —
// both pinned here. (a) A route candidate dropped in favour of the resolved
// release name would break every app that resolves today by coincidence
// (shared-pg, grafana, every bp-* route id). (b) Adopting the HelmRelease's
// targetNamespace when the caller passed NO namespace would turn "match
// anywhere" — the documented bootstrap-safe default the console relies on for
// exactly these components (#4000) — into "match one namespace", which is the
// same defect pointed the other way.
func TestResolveComponentWorkloadIdentity_NeverNarrows_6344(t *testing.T) {
	const (
		depID   = "dep-6344-widen"
		regionA = "me-east-215-a"
		regionB = "me-east-215-b"
	)
	h := newIdentityPlacementHandler(t, depID, regionA, regionB,
		[]runtime.Object{
			spineApplicationFixtureCR("spine-gitea", "bp-gitea", "flux-system"),
			helmReleaseFixture("bp-gitea", "flux-system", "gitea", "gitea"),
		}, nil, nil)

	scoped := h.resolveComponentWorkloadIdentity(depID, "spine-gitea", "catalyst")
	if !containsString(scoped.route, "spine-gitea") {
		t.Fatalf("route candidate dropped: %+v — resolution must ADD, never replace", scoped)
	}
	if !containsString(scoped.release, "gitea") {
		t.Fatalf("release identity not resolved through spec.helmRelease -> HR releaseName: %+v", scoped)
	}
	if !containsString(scoped.namespaces, "catalyst") || !containsString(scoped.namespaces, "gitea") {
		t.Fatalf("namespaces %v want BOTH the requested `catalyst` and the HR targetNamespace `gitea`", scoped.namespaces)
	}

	unscoped := h.resolveComponentWorkloadIdentity(depID, "spine-gitea", "")
	if len(unscoped.namespaces) != 0 {
		t.Fatalf("namespaces %v want EMPTY — a caller that scoped nothing must keep matching every namespace; adopting the targetNamespace here is a new way to MISS pods", unscoped.namespaces)
	}
	if !containsString(unscoped.release, "gitea") {
		t.Fatalf("release identity must still resolve without a namespace scope: %+v", unscoped)
	}

	// 🔒 Organization isolation. The CR is selected exactly as
	// getApplicationCR selects it: a SCOPED request must not pick up a
	// same-named Application from another namespace, because the identity it
	// yields then decides which Pods a different Organization's Topology tab
	// reports on.
	elsewhere := h.resolveComponentWorkloadIdentity(depID, "spine-gitea", "some-other-org")
	if containsString(elsewhere.release, "gitea") {
		t.Fatalf("a scoped request resolved a CR from ANOTHER namespace: %+v", elsewhere)
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
