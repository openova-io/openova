package gitops

import (
	"strings"
	"testing"
)

const renderedAppsIndex5104 = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - networkpolicy.yaml
`

// TestMergeAppsKustomizationIndex_HealsNamespace_5104 locks the #5104
// invariant at the unit level: the controller's merge must union its baseline
// (incl. the #4992 vcluster target-ns namespace.yaml) into a funnel-first
// index WITHOUT pruning the funnel's app entries — and must report
// changed=false (skip the write) once the set is complete.
func TestMergeAppsKustomizationIndex_HealsNamespace_5104(t *testing.T) {
	funnelFirst := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-umami.yaml
  - db-postgres.yaml
  - networkpolicy.yaml
`
	merged, changed := MergeAppsKustomizationIndex(funnelFirst, renderedAppsIndex5104)
	if !changed {
		t.Fatalf("merge must report changed=true when namespace.yaml is missing")
	}
	for _, want := range []string{"app-umami.yaml", "db-postgres.yaml", "namespace.yaml", "networkpolicy.yaml"} {
		if !strings.Contains(merged, "- "+want) {
			t.Errorf("merged index missing %q:\n%s", want, merged)
		}
	}

	// Complete index → changed=false so the caller skips the write (no churn).
	if again, changedAgain := MergeAppsKustomizationIndex(merged, renderedAppsIndex5104); changedAgain {
		t.Errorf("merge on a complete index must report changed=false, got merged:\n%s", again)
	}

	// Order/format-only differences must NOT count as a change: an index with
	// the same set in a different order stays untouched.
	reordered := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - networkpolicy.yaml
  - namespace.yaml
  - db-postgres.yaml
  - app-umami.yaml
`
	if _, changedReorder := MergeAppsKustomizationIndex(reordered, renderedAppsIndex5104); changedReorder {
		t.Errorf("merge must not rewrite an index whose resource SET is already complete (order-only difference)")
	}
}

// TestMergeAppsKustomizationIndex_StripsCNP_4567 — a stale
// ciliumnetworkpolicy.yaml entry (whose file lives only in host-apps/) must be
// stripped, and stripping counts as a change.
func TestMergeAppsKustomizationIndex_StripsCNP_4567(t *testing.T) {
	stale := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-wordpress.yaml
  - ciliumnetworkpolicy.yaml
  - namespace.yaml
  - networkpolicy.yaml
`
	merged, changed := MergeAppsKustomizationIndex(stale, renderedAppsIndex5104)
	if !changed {
		t.Fatalf("stripping a stale CNP entry must report changed=true")
	}
	if strings.Contains(merged, "- ciliumnetworkpolicy.yaml") {
		t.Errorf("stale CNP entry survived the apps-index merge (#4567):\n%s", merged)
	}
	if !strings.Contains(merged, "- app-wordpress.yaml") {
		t.Errorf("funnel entry lost while stripping the CNP:\n%s", merged)
	}
}

// TestMergeHostAppsKustomizationIndex_PreservesRoutes_5104 — the host-apps
// variant heals baseline entries without pruning funnel route entries, and the
// CNP is a LEGITIMATE entry there (never stripped).
func TestMergeHostAppsKustomizationIndex_PreservesRoutes_5104(t *testing.T) {
	rendered := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ciliumnetworkpolicy.yaml
  - provisioning-rbac.yaml
`
	funnelFirst := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-wordpress-hostroute.yaml
  - ciliumnetworkpolicy.yaml
`
	merged, changed := MergeHostAppsKustomizationIndex(funnelFirst, rendered)
	if !changed {
		t.Fatalf("merge must report changed=true when provisioning-rbac.yaml is missing")
	}
	for _, want := range []string{"app-wordpress-hostroute.yaml", "ciliumnetworkpolicy.yaml", "provisioning-rbac.yaml"} {
		if !strings.Contains(merged, "- "+want) {
			t.Errorf("merged host-apps index missing %q:\n%s", want, merged)
		}
	}
	if _, changedAgain := MergeHostAppsKustomizationIndex(merged, rendered); changedAgain {
		t.Errorf("merge on a complete host-apps index must report changed=false")
	}
}
