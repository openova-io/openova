package gitops

import (
	"fmt"
	"sort"
	"strings"
)

// This file is the org-controller half of the #5104 fix.
//
// #4384/#4993 made the controller's `vcluster/apps/kustomization.yaml` +
// `vcluster/host-apps/kustomization.yaml` writes SEED-ONLY so a reconcile
// never clobbered the funnel-merged app entries. That left a structural hole:
// when the funnel's cart-install commits its merged index FIRST (before the
// controller's seed) and that merge does not know a controller baseline doc,
// the controller keeps committing the doc's FILE on every reconcile but never
// gets to index it — the doc is authored yet never applied. On hw255 that
// orphaned the #4992 vcluster target-ns `namespace.yaml` for 2/2 funnel Orgs:
// Flux wedged permanently on `namespaces "<slug>" not found` and the
// purchased app never deployed (#5104 Facet A).
//
// The durable shape is a RECONCILING merge-write: union the controller's
// rendered baseline entries INTO the existing index, never pruning an entry
// the controller does not own (the funnel's app/db/route docs), and skip the
// write entirely when the resource set is already complete (so a steady-state
// reconcile stays write-free and the funnel's bytes are never churned). This
// also self-heals every already-wedged Org on its next reconcile — no
// hand-edit of live per-Org repos needed.

// MergeAppsKustomizationIndex merges the controller's rendered
// `vcluster/apps/kustomization.yaml` baseline into the existing (possibly
// funnel-merged) index. #4567: `ciliumnetworkpolicy.yaml` is force-stripped —
// that file only ever exists in `vcluster/host-apps/`, so a stale apps-index
// entry breaks the whole kustomize build.
func MergeAppsKustomizationIndex(existing, rendered string) (merged string, changed bool) {
	return mergeKustomizationResources(existing, rendered, []string{ciliumNetworkPolicyDoc})
}

// MergeHostAppsKustomizationIndex merges the controller's rendered
// `vcluster/host-apps/kustomization.yaml` baseline into the existing (possibly
// funnel-merged) index.
func MergeHostAppsKustomizationIndex(existing, rendered string) (merged string, changed bool) {
	return mergeKustomizationResources(existing, rendered, nil)
}

// mergeKustomizationResources unions the resource entries of `existing` and
// `rendered` (minus the index itself and the `excluded` docs) into a canonical
// sorted kustomization document. `changed` reports whether the merged resource
// SET differs from the existing one — order/format differences alone do NOT
// count, so callers can leave an already-complete funnel-authored index
// byte-untouched.
func mergeKustomizationResources(existing, rendered string, excluded []string) (string, bool) {
	excl := make(map[string]struct{}, len(excluded))
	for _, e := range excluded {
		excl[e] = struct{}{}
	}

	existingSet := parseKustomizationResources(existing)
	merged := make(map[string]struct{}, len(existingSet))
	for _, set := range []map[string]struct{}{existingSet, parseKustomizationResources(rendered)} {
		for name := range set {
			if name == "kustomization.yaml" {
				continue
			}
			if _, bad := excl[name]; bad {
				continue
			}
			merged[name] = struct{}{}
		}
	}

	changed := len(merged) != len(existingSet)
	if !changed {
		for name := range merged {
			if _, ok := existingSet[name]; !ok {
				changed = true
				break
			}
		}
	}

	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	// Plain lexicographic sort — byte-identical to the funnel's
	// MergePerOrgAppsKustomization / MergePerOrgHostAppsKustomization
	// renderers, so the two writers converge on the same bytes for the same
	// set instead of ping-ponging the file. (Apply ORDER does not matter to
	// Flux — kustomize-controller sorts by kind, Namespaces first.)
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("resources:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  - %s\n", name)
	}
	return b.String(), changed
}

// parseKustomizationResources extracts the `- <entry>` resource names from a
// kustomization document (the same line-shape the funnel's merge parses).
func parseKustomizationResources(doc string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}
