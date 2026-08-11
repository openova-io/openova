package gitops

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// #6114 — a Secret REFERENCE whose producer was never written.
//
// `openclaw-newapi-controller-token` was consumed in three places (the chart's
// controller Deployment via `llm.apiKey`, and both HelmRelease generators that
// pin that value) while NOTHING in the tree ever created it. It was not a
// producer that got deleted: `git log --all -S"openclaw-newapi-controller-token"
// -- '*.yaml' '*.tpl'` returns ZERO commits, so no manifest ever carried the
// name in a producing position.
//
// The two guards below encode the invariant that was missing, at the two seams
// where it broke:
//
//	A. chart seam  — a helper used as the `name:` of a `secretKeyRef` must also
//	   name a `kind: Secret` the SAME chart renders.
//	B. overlay seam — a generator may only pin a Secret name at a values path
//	   that some chart Secret template actually consumes.
//
// CONTROL (shares the suspect property, must stay green in both guards):
// `bp-openclaw.oidc.clientSecretName` / values path `oidc.clientSecret`. It is
// the SIBLING secret reference — same chart, same container, same
// helper-indirection, same `optional: true` secretKeyRef shape, pinned by the
// same generators. It differs from the phantom in exactly one respect: it has a
// producer (`templates/oidc-client-secret.yaml`). If a change to these guards
// stops distinguishing "has a producer" from "does not", the control goes red.

// openclawTemplatesDir is the bp-openclaw chart's template dir, relative to
// this package (core/services/provisioning/gitops → repo root is four up).
const openclawTemplatesDir = "../../../../platform/openclaw/chart/templates"

// controlSecretHelper / controlSecretPath are the CONTROL — a secret reference
// that shares every structural property with the phantom except the one under
// test (a producer exists).
const (
	controlSecretHelper = "bp-openclaw.oidc.clientSecretName"
	controlSecretPath   = "oidc.clientSecret"
)

var (
	// `secretKeyRef:` followed by `name: {{ include "helper" . }}`.
	reSecretKeyRefHelper = regexp.MustCompile(`secretKeyRef:\s*\n\s*name:\s*\{\{-?\s*include\s+"([^"]+)"`)
	// `metadata:` … `name: {{ include "helper" . }}` (direct form).
	reMetaNameInclude = regexp.MustCompile(`\n\s*name:\s*\{\{-?\s*include\s+"([^"]+)"`)
	// `metadata:` … `name: {{ $var }}` (indirect form — resolved via reAssign).
	reMetaNameVar = regexp.MustCompile(`\n\s*name:\s*\{\{-?\s*(\$[A-Za-z0-9_]+)\s*\}\}`)
	// `$var := include "helper" .`
	reAssign = regexp.MustCompile(`(\$[A-Za-z0-9_]+)\s*:=\s*include\s+"([^"]+)"`)
	// `.Values.a.b.c` inside a helper body.
	reValuesPath = regexp.MustCompile(`\.Values\.([A-Za-z0-9_.]+)`)
	// `{{- define "helper" -}}` … block header.
	reDefine = regexp.MustCompile(`\{\{-?\s*define\s+"([^"]+)"`)
)

// readOpenClawTemplates returns every chart template keyed by base filename.
func readOpenClawTemplates(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(openclawTemplatesDir)
	if err != nil {
		t.Fatalf("read bp-openclaw templates dir %s: %v", openclawTemplatesDir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(openclawTemplatesDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("VACUITY: bp-openclaw chart has no templates — the guard would pass on an empty tree")
	}
	return out
}

// producingHelpers returns the helpers that name a `kind: Secret` the chart
// itself renders. Both the direct (`name: {{ include "h" . }}`) and the
// indirect (`$v := include "h" .` … `name: {{ $v }}`) forms are resolved — the
// indirect form is the one a literal grep for a secret name MISSES, and it is
// the form the CONTROL producer uses.
func producingHelpers(t *testing.T, tmpls map[string]string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for name, body := range tmpls {
		if !strings.Contains(body, "kind: Secret") {
			continue
		}
		vars := map[string]string{}
		for _, m := range reAssign.FindAllStringSubmatch(body, -1) {
			vars[m[1]] = m[2]
		}
		for _, m := range reMetaNameInclude.FindAllStringSubmatch(body, -1) {
			out[m[1]] = true
		}
		for _, m := range reMetaNameVar.FindAllStringSubmatch(body, -1) {
			if h, ok := vars[m[1]]; ok {
				out[h] = true
			} else {
				t.Errorf("%s: Secret metadata.name uses %s but no `%s := include ...` assignment was found — the producer resolver cannot see this template", name, m[1], m[1])
			}
		}
	}
	return out
}

// TestOpenClawChartSecretRefsHaveProducers is guard A — the chart seam.
//
// Every helper the controller Deployment uses as a `secretKeyRef.name` must
// also name a Secret this chart renders. `optional: true` on the ref means a
// missing Secret does NOT fail the Pod — it silently leaves the env unset,
// which is exactly why this went unnoticed: there is no runtime signal at all.
func TestOpenClawChartSecretRefsHaveProducers(t *testing.T) {
	tmpls := readOpenClawTemplates(t)

	dep, ok := tmpls["controller-deployment.yaml"]
	if !ok {
		t.Fatal("VACUITY: controller-deployment.yaml not found — nothing would be checked")
	}

	consumed := map[string]bool{}
	for _, m := range reSecretKeyRefHelper.FindAllStringSubmatch(dep, -1) {
		consumed[m[1]] = true
	}
	produced := producingHelpers(t, tmpls)

	// ── Vacuity: both sets must be non-empty and the CONTROL present in each.
	// Without this a regex that matches nothing makes the guard trivially pass.
	if len(consumed) == 0 {
		t.Fatal("VACUITY: no secretKeyRef helper found in controller-deployment.yaml — the guard cannot fail")
	}
	if len(produced) == 0 {
		t.Fatal("VACUITY: no Secret-producing helper found in the chart — the guard cannot fail")
	}
	if !consumed[controlSecretHelper] {
		t.Fatalf("CONTROL missing from consumed set: %q is not used as a secretKeyRef name; the guard is no longer testing the surface it claims to", controlSecretHelper)
	}
	if !produced[controlSecretHelper] {
		t.Fatalf("CONTROL missing from produced set: %q names no Secret template; the producer resolver is broken, so a negative result is worthless", controlSecretHelper)
	}

	var orphans []string
	for h := range consumed {
		if !produced[h] {
			orphans = append(orphans, h)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("bp-openclaw mounts a Secret no chart template creates (#6114).\n"+
			"  consumed as secretKeyRef.name : %v\n"+
			"  produced by a kind: Secret    : %v\n"+
			"  ORPHANED (no producer)        : %v\n"+
			"Either add the producer or drop the reference. `optional: true` hides this at\n"+
			"runtime — the env is silently unset, so no Pod ever reports the gap.",
			sortedSet(consumed), sortedSet(produced), orphans)
	}
}

// TestOpenClawOverlayPinsOnlyProducedSecretPaths is guard B — the overlay seam.
//
// A generator may pin a Secret NAME only at a values path that some chart
// Secret template consumes. Pinning `llm.apiKey.name` was writing a name for a
// Secret the chart never creates.
func TestOpenClawOverlayPinsOnlyProducedSecretPaths(t *testing.T) {
	tmpls := readOpenClawTemplates(t)
	helpers, ok := tmpls["_helpers.tpl"]
	if !ok {
		t.Fatal("VACUITY: _helpers.tpl not found — values paths cannot be resolved")
	}

	// values paths read by helpers that name a Secret the chart renders.
	producedPaths := map[string]bool{}
	for h := range producingHelpers(t, tmpls) {
		for _, p := range valuesPathsOfHelper(helpers, h) {
			producedPaths[strings.TrimSuffix(p, ".name")] = true
		}
	}

	out := cartOrgFor(t, "acme", "s", []string{"openclaw"})
	overlay, ok := out[testBasePath+"/acme/app-openclaw.yaml"]
	if !ok {
		t.Fatalf("VACUITY: bp-openclaw overlay not rendered — nothing to check (keys: %v)", keys(out))
	}
	pinned := pinnedSecretPaths(overlay)

	// ── Vacuity + CONTROL.
	if len(producedPaths) == 0 {
		t.Fatal("VACUITY: no produced values path resolved — the guard cannot fail")
	}
	if len(pinned) == 0 {
		t.Fatal("VACUITY: overlay pins no name/key secret reference — the guard cannot fail")
	}
	if !producedPaths[controlSecretPath] {
		t.Fatalf("CONTROL %q absent from produced paths %v — the helper→values resolver is broken", controlSecretPath, sortedSet(producedPaths))
	}
	if !pinned[controlSecretPath] {
		t.Fatalf("CONTROL %q is no longer pinned by the overlay — the guard is testing the wrong surface", controlSecretPath)
	}

	var orphans []string
	for p := range pinned {
		if !producedPaths[p] {
			orphans = append(orphans, p)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("the funnel overlay pins a Secret name the bp-openclaw chart never creates (#6114).\n"+
			"  pinned by overlay        : %v\n"+
			"  consumed by a producer   : %v\n"+
			"  ORPHANED (no producer)   : %v\n%s",
			sortedSet(pinned), sortedSet(producedPaths), orphans, overlay)
	}
}

// valuesPathsOfHelper returns the `.Values.x.y` paths read inside one helper.
func valuesPathsOfHelper(helpers, helper string) []string {
	locs := reDefine.FindAllStringSubmatchIndex(helpers, -1)
	for i, loc := range locs {
		if helpers[loc[2]:loc[3]] != helper {
			continue
		}
		end := len(helpers)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		var out []string
		for _, m := range reValuesPath.FindAllStringSubmatch(helpers[loc[0]:end], -1) {
			out = append(out, m[1])
		}
		return out
	}
	return nil
}

// pinnedSecretPaths finds every `name:`/`key:` pair in the overlay's values —
// the shape of a Secret reference — and returns the dotted path of its parent
// key (e.g. `oidc.clientSecret`).
func pinnedSecretPaths(overlay string) map[string]bool {
	out := map[string]bool{}
	lines := strings.Split(overlay, "\n")
	// indent → key, rebuilt as we descend.
	stack := map[int]string{}
	for i, ln := range lines {
		trimmed := strings.TrimLeft(ln, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		indent := len(ln) - len(trimmed)
		key, _, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		stack[indent] = key
		if key != "name" || i+1 >= len(lines) {
			continue
		}
		next := strings.TrimLeft(lines[i+1], " ")
		nextIndent := len(lines[i+1]) - len(next)
		if nextIndent != indent || !strings.HasPrefix(next, "key:") {
			continue
		}
		// Walk enclosing keys, stopping at the `values:` block root.
		var parts []string
		for ind := indent - 2; ind >= 0; ind -= 2 {
			k, ok := stack[ind]
			if !ok {
				continue
			}
			if k == "values" {
				break
			}
			parts = append([]string{k}, parts...)
		}
		if len(parts) > 0 {
			out[strings.Join(parts, ".")] = true
		}
	}
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
