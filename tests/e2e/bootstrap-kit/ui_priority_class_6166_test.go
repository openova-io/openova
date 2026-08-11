package bootstrapkit

// ui_priority_class_6166_test.go — #6166.
//
// THE DEFECT. `https://<mothership>/sovereign/` served a Traefik 503 "no
// available server" while catalyst-api beside it was 1/1 with /readyz 200. The
// /sovereign Ingress backends onto catalyst-ui, and catalyst-ui was Pending on
// "0/1 nodes are available: 1 Too many pods" — the node sat at its 110/110 Pod
// ceiling.
//
// #6157 had already fixed the ROLLOUT DEADLOCK on the same Deployment: at
// replicas=1 the default strategy rounds maxSurge 25% UP to 1 and
// maxUnavailable 25% DOWN to 0, so the roll needed two Pods alive at once and
// could never start on a full node. That fix is correct and necessary.
//
// It is not sufficient, and the gap is the whole point of this guard: letting
// the old Pod leave FREES a slot, it does not WIN it. The scheduler awards a
// freed slot to the highest-priority pending Pod. catalyst-ui ran at priority
// 0 — no priorityClassName at all — while its catalyst-api sibling had carried
// `catalyst-control-plane` (value 100000000) since #4231. Against four other
// queued Pods the console lost every race and sat Pending for 8 days.
//
// The asymmetry is the bug. Two Deployments in one chart, one control-plane
// surface each, and only one of them was ranked. A live patch scheduled
// catalyst-ui by preemption within seconds; Flux then reverted it, which is
// why the fix has to live in the chart.
//
// WHAT THIS GUARD PINS
//
//  1. catalyst-ui declares a non-empty priorityClassName on BOTH render paths.
//     The Helm path (ui-deployment.yaml) and the raw-Kustomize path
//     (ui-deployment-kustomize.yaml) are rendered by different consumers — the
//     mothership `catalyst-platform` Kustomization raw-kustomize-builds the
//     templates directory — so a fix applied to only one path leaves the other
//     starvable. Kustomize is the path the mothership actually served /sovereign
//     from when this broke.
//
//  2. The class it names is one the chart SHIPS. A priorityClassName pointing
//     at a class that was never rendered is not a high-priority Pod; on a
//     cluster without that object the Pod is REJECTED at admission, which is
//     strictly worse than priority 0. The Helm template deliberately reuses the
//     same expression api-priorityclass.yaml uses to NAME the object, so an
//     operator override moves both together — this asserts that coupling rather
//     than trusting the comment that describes it.
//
//  3. catalyst-api still carries it too. That is the CONTROL, not padding: api
//     has had the class since #4231, so if this file's parser ever breaks, the
//     api arm goes red as well and the failure reads as "the guard is broken",
//     not "the chart is fine". A guard whose only subject is the thing being
//     fixed cannot distinguish a passing chart from a blind parser.
//
// VACUITY. Assertions 1 and 2 fail on the parent of the #6166 fix commit: both
// ui-deployment files carried no priorityClassName key whatsoever, so the
// extractor returns "" and the guard reports MISSING. TestUIPriorityClass_
// DetectorIsNotVacuous proves the same extractor over synthetic input — the
// detector must go red on a pod spec with the key removed, and must not be
// fooled by the string appearing in a comment. Without that arm an extractor
// that grep'd the whole file for the word would pass against a chart that only
// MENTIONS priorityClassName in prose, which is exactly the shape of the
// comment blocks these templates are full of.
//
// NO BINARIES ON PURPOSE. This is a static parse, not a `helm template` render.
// The CI job that runs this package on pull_request (test-bootstrap-kit.yaml →
// manifest-validation) installs Go and nothing else, so a helm- or kustomize-
// based assertion would t.Skip there and gate nothing at review time. A guard
// that skips in the job that is supposed to enforce it is decorative.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// priorityClassRefRe matches a `priorityClassName:` key at pod-spec depth on a
// line of its own, capturing the value. Anchored to line start plus indent so a
// mention inside a `#` comment (these templates carry long comment blocks that
// name the field in prose) can never satisfy it — the comment marker precedes
// the key and breaks the anchor.
var priorityClassRefRe = regexp.MustCompile(`(?m)^[ \t]+priorityClassName:[ \t]*(\S.*?)[ \t]*$`)

// priorityClassObjRe captures the `name:` of a rendered PriorityClass object.
var priorityClassNameKeyRe = regexp.MustCompile(`(?m)^[ \t]+name:[ \t]*(\S.*?)[ \t]*$`)

// helmDefaultRe pulls the literal out of `{{ .Values.x | default "lit" }}`,
// which is how the Helm-rendered path names the class.
var helmDefaultRe = regexp.MustCompile(`\|\s*default\s+"([^"]+)"`)

// extractPriorityClassRef returns the priorityClassName value declared in a
// manifest, or "" when the key is absent. Only uncommented lines are
// considered.
func extractPriorityClassRef(manifest string) string {
	for _, line := range strings.Split(manifest, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if m := priorityClassRefRe.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// resolveClassName reduces a priorityClassName value to the class name it
// resolves to by default. A literal returns itself; a Helm expression returns
// the literal inside its `default` filter. An expression with no default
// returns "" — that is a class name nobody can predict, and for this field an
// unpredictable name is a dangling reference.
func resolveClassName(raw string) string {
	if !strings.Contains(raw, "{{") {
		return strings.Trim(raw, `"'`)
	}
	if m := helmDefaultRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return ""
}

// shippedPriorityClasses collects every PriorityClass name the chart's
// templates declare, resolving Helm expressions to their default the same way
// resolveClassName does for the reference side.
func shippedPriorityClasses(t *testing.T, templatesDir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		t.Fatalf("read templates dir %s: %v", templatesDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(templatesDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(b)
		if !strings.Contains(body, "kind: PriorityClass") {
			continue
		}
		// The metadata.name follows the kind line; take the first
		// uncommented `name:` after it.
		idx := strings.Index(body, "kind: PriorityClass")
		for _, line := range strings.Split(body[idx:], "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if m := priorityClassNameKeyRe.FindStringSubmatch(line); m != nil {
				if name := resolveClassName(strings.TrimSpace(m[1])); name != "" {
					out[name] = e.Name()
				}
				break
			}
		}
	}
	return out
}

// TestCatalystControlPlane_UIAndAPIAreBothRanked is the merge gate for #6166.
func TestCatalystControlPlane_UIAndAPIAreBothRanked(t *testing.T) {
	root := repoRoot(t)
	templatesDir := filepath.Join(root, "products", "catalyst", "chart", "templates")

	shipped := shippedPriorityClasses(t, templatesDir)
	if len(shipped) == 0 {
		t.Fatalf("no PriorityClass object found under %s — either the chart stopped "+
			"shipping one (in which case every reference below dangles) or this "+
			"guard's scanner is broken. Either way it must not pass silently.", templatesDir)
	}

	// catalyst-ui is the subject of #6166. catalyst-api is the CONTROL: it has
	// carried the class since #4231, so it must stay green for a ui failure to
	// mean anything.
	cases := []struct {
		file     string
		workload string
		role     string
	}{
		{"ui-deployment.yaml", "catalyst-ui", "subject (#6166, Helm path)"},
		{"ui-deployment-kustomize.yaml", "catalyst-ui", "subject (#6166, raw-Kustomize path — what the mothership builds)"},
		{"api-deployment.yaml", "catalyst-api", "control (#4231, Helm path)"},
		{"api-deployment-kustomize.yaml", "catalyst-api", "control (#4231, raw-Kustomize path)"},
	}

	for _, tc := range cases {
		path := filepath.Join(templatesDir, tc.file)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: read: %v", tc.file, err)
		}

		raw := extractPriorityClassRef(string(b))
		if raw == "" {
			t.Errorf("%s [%s]: %s declares NO priorityClassName, so it schedules at "+
				"priority 0.\n"+
				"On a node at its max-pods cap a freed slot goes to the highest-priority "+
				"pending Pod, and an unranked single-replica control-plane workload loses "+
				"every race — that is #6166, where /sovereign served a Traefik 503 for 8 "+
				"days while catalyst-api beside it stayed green.\n"+
				"Fix: add `priorityClassName: catalyst-control-plane` to the pod spec in %s.",
				tc.file, tc.role, tc.workload, path)
			continue
		}

		class := resolveClassName(raw)
		if class == "" {
			t.Errorf("%s [%s]: priorityClassName is %q, which names no predictable class.\n"+
				"A templated reference with no `| default \"...\"` renders empty when the "+
				"value is unset — and an empty priorityClassName is priority 0 again, with "+
				"the added cost that nobody reading the template can tell.\n"+
				"Fix: give the expression a default naming a class the chart ships.",
				tc.file, tc.role, raw)
			continue
		}

		if src, ok := shipped[class]; !ok {
			t.Errorf("%s [%s]: references PriorityClass %q, which this chart never renders.\n"+
				"Shipped classes: %v.\n"+
				"A dangling reference is worse than no class at all: the API server REJECTS "+
				"a Pod naming a non-existent PriorityClass, so the workload does not schedule "+
				"at low priority — it does not schedule at all.\n"+
				"Fix: reference a shipped class, or ship the object alongside it.",
				tc.file, tc.role, class, keysOf(shipped))
			_ = src
		}
	}
}

// TestCatalystControlPlane_UIMatchesAPIAcrossRenderPaths pins the symmetry
// itself. #6166 existed because one Deployment in the pair was ranked and the
// other was not; a future edit that drops the class from one render path while
// leaving the other is the same defect returning through the same seam, and the
// per-file assertions above would still pass if BOTH ui files were dropped
// together only if the loop above were removed — this arm makes the pairing
// explicit so a partial edit reads as a lockstep break.
func TestCatalystControlPlane_UIMatchesAPIAcrossRenderPaths(t *testing.T) {
	root := repoRoot(t)
	templatesDir := filepath.Join(root, "products", "catalyst", "chart", "templates")

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(templatesDir, name))
		if err != nil {
			t.Fatalf("%s: read: %v", name, err)
		}
		return resolveClassName(extractPriorityClassRef(string(b)))
	}

	uiHelm := read("ui-deployment.yaml")
	uiKust := read("ui-deployment-kustomize.yaml")
	apiHelm := read("api-deployment.yaml")
	apiKust := read("api-deployment-kustomize.yaml")

	if uiHelm != uiKust {
		t.Errorf("catalyst-ui resolves to %q on the Helm path but %q on the raw-Kustomize path.\n"+
			"These render for different consumers — the mothership catalyst-platform "+
			"Kustomization builds the Kustomize path directly — so a split means one of "+
			"the two deployments of the SAME workload is starvable while the other is not, "+
			"and which one you get depends on how the chart was installed.",
			uiHelm, uiKust)
	}
	if apiHelm != apiKust {
		t.Errorf("catalyst-api resolves to %q on the Helm path but %q on the raw-Kustomize path (control arm).",
			apiHelm, apiKust)
	}
	if uiHelm != apiHelm {
		t.Errorf("catalyst-ui resolves to priority class %q but catalyst-api to %q.\n"+
			"Both are single-replica control-plane surfaces in one chart, and #6166 was "+
			"precisely this asymmetry: the console sat unranked behind co-tenant Pods "+
			"while its own API outranked them. If the tiers are meant to differ now, that "+
			"is a deliberate decision and this guard should be updated to state it.",
			uiHelm, apiHelm)
	}
}

// TestUIPriorityClass_DetectorIsNotVacuous is the vacuity control. A guard that
// passes on the first run tells you nothing until you have watched it fail.
func TestUIPriorityClass_DetectorIsNotVacuous(t *testing.T) {
	const withField = `
spec:
  template:
    spec:
      priorityClassName: catalyst-control-plane
      containers:
        - name: ui
`
	// The pre-#6166 shape: the key is simply absent.
	const withoutField = `
spec:
  template:
    spec:
      containers:
        - name: ui
`
	// The trap: the words appear, but only in prose. An extractor that grep'd
	// the file would call this a pass and ship the outage.
	const mentionedInCommentOnly = `
spec:
  template:
    spec:
      # priorityClassName: catalyst-control-plane  <- describing, not declaring
      containers:
        - name: ui
`
	if got := extractPriorityClassRef(withField); got != "catalyst-control-plane" {
		t.Errorf("detector failed to read a declared priorityClassName: got %q, want %q",
			got, "catalyst-control-plane")
	}
	if got := extractPriorityClassRef(withoutField); got != "" {
		t.Errorf("detector reported %q on a pod spec with NO priorityClassName — it cannot "+
			"go red, so every green above is meaningless", got)
	}
	if got := extractPriorityClassRef(mentionedInCommentOnly); got != "" {
		t.Errorf("detector was fooled by a commented-out priorityClassName (got %q). These "+
			"templates carry long comment blocks naming this exact field, so a "+
			"comment-blind extractor would pass against a chart that only talks about "+
			"the fix.", got)
	}

	// resolveClassName must survive the templated form the Helm path uses, and
	// must refuse an expression with no default rather than inventing one.
	if got := resolveClassName(`{{ .Values.catalystApi.priorityClassName | default "catalyst-control-plane" }}`); got != "catalyst-control-plane" {
		t.Errorf("resolveClassName lost the Helm default: got %q", got)
	}
	if got := resolveClassName(`{{ .Values.catalystApi.priorityClassName }}`); got != "" {
		t.Errorf("resolveClassName invented %q for an expression with no default; an "+
			"unset value renders empty and that must read as a defect", got)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
