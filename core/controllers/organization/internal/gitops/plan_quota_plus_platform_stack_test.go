// plan_quota_plus_platform_stack_test.go — the host-namespace ResourceQuota is
// the purchased plan PLUS the vCluster control plane PLUS the per-Organization
// platform stack (#6902 follow-up, Refs #6867).
//
// Four properties, each with its own vacuity guard:
//
//  1. INSTALL LIST — platformStack covers exactly the HelmReleases the BSS door
//     renders for every Organization (organization_gitops.go orgTenantTemplates)
//     minus the customer's purchase (bp-wordpress-tenant, bp-stalwart-tenant).
//     The list is read from the source, not restated.
//  2. PIN — every figure in platformStack equals what its Source sets today:
//     the BSS door's HelmRelease values (parsed out of organization_gitops.go),
//     the funnel's newapi values (parsed out of helmrelease_apps.go, lockstep
//     with the BSS door) and the chart defaults (values.yaml of bp-newapi,
//     bp-openclaw and bp-agenity, plus the inline init container in bp-newapi's
//     deployment.yaml). Each pod's container inventory is checked against its
//     template, so a new sidecar cannot land in the quota unaccounted, and the
//     one container modelled as unsized is proven unsized.
//  3. FIGURES — the derived per-plan overhead, pinned by value; the figures are
//     restated in docs/SYSTEM-DESIGN.md and products/chargeback/DESIGN.md.
//  4. RULE — platformStackOverheadOf on a synthetic stack: an unsized container
//     takes the LimitRange defaults, an init container dominates when larger,
//     pods sum.
//
// The reads below leave this module (charts and the two other Go modules), so
// `go test` cannot key its cache on them; run under -count=1 —
// scripts/check-go-test-count1.sh enforces that on every workflow (#6235).
package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// wantPlatformStackOverhead is the derived figure per plan, pinned. S/M/L share
// one figure; XL differs because the 2-CPU / 4Gi LimitRange default sizes
// bp-agenity's unsized init container above its app containers (see the
// platformStack comment in manifests.go). If any source moves, property 2
// fails first and names it; this fails second and names the doc lines that
// must move with it.
var wantPlatformStackOverhead = map[string]map[string]string{
	"s":  {"requests.cpu": "3840m", "requests.memory": "6064Mi", "limits.cpu": "4550m", "limits.memory": "7168Mi"},
	"m":  {"requests.cpu": "3840m", "requests.memory": "6064Mi", "limits.cpu": "4550m", "limits.memory": "7168Mi"},
	"l":  {"requests.cpu": "3840m", "requests.memory": "6064Mi", "limits.cpu": "4550m", "limits.memory": "7168Mi"},
	"xl": {"requests.cpu": "4835m", "requests.memory": "8096Mi", "limits.cpu": "5500m", "limits.memory": "9152Mi"},
}

// The pod names platformStack uses, so the tests below address rows by name
// rather than by position.
const (
	wlKeycloak   = "bp-keycloak-0"
	wlKeycloakPG = "bp-keycloak-postgresql-0"
	wlNewAPI     = "bp-newapi-<hash>"
	wlNewAPIPG   = "bp-newapi-newapi-pg-1"
	wlOpenClaw   = "bp-openclaw-<hash>"
	wlAgenity    = "bp-agenity-0"
	wlOIDCGate   = "oidc-gate-agenity-<slug>"
)

// ─── repo access ─────────────────────────────────────────────────────────────

// repoRoot is five levels above this package
// (core/controllers/organization/internal/gitops). It is verified, not assumed:
// a wrong root would make every read below fail loudly rather than pass on a
// missing file.
func repoRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..", "..")
	if _, err := os.Stat(filepath.Join(root, "platform", "newapi", "chart", "Chart.yaml")); err != nil {
		t.Fatalf("repo root not found at %s (platform/newapi/chart/Chart.yaml): %v — the package moved; re-point repoRoot", root, err)
	}
	return root
}

// readRepo reads a repo-root-relative file. FATAL on a miss: every read here is
// a term of the quota arithmetic, and a term that silently reads as empty is
// how an overhead loses a workload.
func readRepo(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{repoRoot(t)}, parts...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	if len(b) == 0 {
		t.Fatalf("vacuity: %s is empty", p)
	}
	return string(b)
}

// goRawString returns the Go string constant that starts at the first
// backtick after marker in src, following the `raw` + "interpreted" + `raw`
// concatenation idiom the BSS door uses to splice backticks into a raw string
// (organization_gitops.go orgTenantBPKeycloak does this in its comment lines).
// A raw literal cannot contain a backtick, so each piece is scanned exactly;
// interpreted pieces go through strconv.Unquote. The scan stops at the first
// token after a literal that is not `+` — for a fmt.Sprintf format string that
// is the `,` before its arguments.
func goRawString(t *testing.T, src, marker string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("marker %q not found — the source was restructured; re-point this pin", marker)
	}
	rest := src[i+len(marker):]
	open := strings.Index(rest, "`")
	if open < 0 {
		t.Fatalf("no raw string after %q", marker)
	}
	rest = rest[open:]
	var b strings.Builder
	for {
		switch {
		case strings.HasPrefix(rest, "`"):
			end := strings.Index(rest[1:], "`")
			if end < 0 {
				t.Fatalf("unterminated raw string after %q", marker)
			}
			b.WriteString(rest[1 : 1+end])
			rest = rest[2+end:]
		case strings.HasPrefix(rest, `"`):
			j := 1
			for j < len(rest) && rest[j] != '"' {
				if rest[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(rest) {
				t.Fatalf("unterminated interpreted string after %q", marker)
			}
			s, err := strconv.Unquote(rest[:j+1])
			if err != nil {
				t.Fatalf("bad interpreted string %s after %q: %v", rest[:j+1], marker, err)
			}
			b.WriteString(s)
			rest = rest[j+1:]
		default:
			t.Fatalf("expected a string literal after %q, found %q", marker, rest[:min(20, len(rest))])
		}
		rest = strings.TrimLeft(rest, " \t\r\n")
		if !strings.HasPrefix(rest, "+") {
			break
		}
		rest = strings.TrimLeft(rest[1:], " \t\r\n")
	}
	if b.Len() == 0 {
		t.Fatalf("vacuity: empty string constant after %q", marker)
	}
	return b.String()
}

var (
	tmplMarkerRe = regexp.MustCompile(`\{\{-?[^}]*-?\}\}`)
	fmtVerbRe    = regexp.MustCompile(`%[sdvq]`)
)

// helmReleaseValues parses the HelmRelease document out of a rendered (or
// still-templated) multi-document YAML string and returns spec.values. Template
// markers and fmt verbs are replaced by a scalar so the YAML parses; they never
// occur inside the resources blocks this file reads.
func helmReleaseValues(t *testing.T, doc string) map[string]any {
	t.Helper()
	neutral := fmtVerbRe.ReplaceAllString(tmplMarkerRe.ReplaceAllString(doc, "x"), "x")
	var hr string
	for _, d := range strings.Split(neutral, "\n---\n") {
		if strings.Contains(d, "kind: HelmRelease") {
			hr = d
			break
		}
	}
	if hr == "" {
		t.Fatalf("no HelmRelease document in:\n%s", doc)
	}
	var obj struct {
		Spec struct {
			Values map[string]any `json:"values"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(hr), &obj); err != nil {
		t.Fatalf("HelmRelease did not parse: %v\n%s", err, hr)
	}
	if len(obj.Spec.Values) == 0 {
		t.Fatal("vacuity: HelmRelease has no spec.values")
	}
	return obj.Spec.Values
}

// yamlFile parses a repo-root-relative YAML file into a map.
func yamlFile(t *testing.T, parts ...string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(readRepo(t, parts...)), &m); err != nil {
		t.Fatalf("%s did not parse: %v", filepath.Join(parts...), err)
	}
	if len(m) == 0 {
		t.Fatalf("vacuity: %s parsed to an empty map", filepath.Join(parts...))
	}
	return m
}

// dig walks nested maps by key; ok is false at the first missing step.
func dig(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// scalarAt returns the scalar at path as a string. FATAL when absent — a
// missing gate or figure must never read as "false" or zero.
func scalarAt(t *testing.T, m map[string]any, path ...string) string {
	t.Helper()
	v, ok := dig(m, path...)
	if !ok {
		t.Fatalf("%s is not set — the source was restructured; re-point this pin, do not drop the term", strings.Join(path, "."))
	}
	if _, isMap := v.(map[string]any); isMap {
		t.Fatalf("%s is a map, not a scalar", strings.Join(path, "."))
	}
	return fmt.Sprint(v)
}

// shapeAt reads a full requests/limits block at path. All four fields are
// required: a resources block missing a side is a quota term this file cannot
// derive, so it is a failure, not a zero.
func shapeAt(t *testing.T, m map[string]any, path ...string) containerShape {
	t.Helper()
	return containerShape{
		RequestsCPU:    scalarAt(t, m, append(path, "requests", "cpu")...),
		RequestsMemory: scalarAt(t, m, append(path, "requests", "memory")...),
		LimitsCPU:      scalarAt(t, m, append(path, "limits", "cpu")...),
		LimitsMemory:   scalarAt(t, m, append(path, "limits", "memory")...),
	}
}

// sameShape compares two shapes as quantities, so "1" and "1000m" agree and
// "2Gi" and "2048Mi" agree, and names the field that moved.
func sameShape(t *testing.T, what string, got, want containerShape) {
	t.Helper()
	for _, f := range []struct{ name, g, w string }{
		{"requests.cpu", got.RequestsCPU, want.RequestsCPU},
		{"requests.memory", got.RequestsMemory, want.RequestsMemory},
		{"limits.cpu", got.LimitsCPU, want.LimitsCPU},
		{"limits.memory", got.LimitsMemory, want.LimitsMemory},
	} {
		g := mustQ(t, what+" platformStack "+f.name, f.g)
		w := mustQ(t, what+" source "+f.name, f.w)
		if g.IsZero() || w.IsZero() {
			t.Errorf("vacuity: %s %s is zero (platformStack %q, source %q)", what, f.name, f.g, f.w)
		}
		if g.Cmp(w) != 0 {
			t.Errorf("%s %s: platformStack carries %s but its source sets %s — the table and the source drifted apart; move them together (and re-pin wantPlatformStackOverhead + the docs)", what, f.name, g.String(), w.String())
		}
	}
}

// podSpecContainerNames returns every container/initContainer name declared in
// a Helm pod template. It reads the TEMPLATE (names are literal there; a render
// would need Helm), bounded to the pod spec — from `initContainers:` or
// `containers:` at the pod-spec indent to the `volumes:` block, the next YAML
// document, or the end — so volume/env entries that share the `- name:` shape
// at other depths are never mistaken for containers. Same discipline as the
// #6324 guard in core/services/provisioning/gitops.
var containerEntryRe = regexp.MustCompile(`(?m)^        - name: ([a-z0-9][a-z0-9-]*)\s*$`)

func podSpecContainerNames(t *testing.T, tmpl, what string) []string {
	t.Helper()
	start := strings.Index(tmpl, "\n      initContainers:")
	if start < 0 {
		start = strings.Index(tmpl, "\n      containers:")
	}
	if start < 0 {
		t.Fatalf("%s: no initContainers:/containers: block at the pod-spec indent — the template was restructured; re-point this scanner", what)
	}
	spec := tmpl[start:]
	end := len(spec)
	for _, stop := range []string{"\n      volumes:", "\n---"} {
		if i := strings.Index(spec, stop); i >= 0 && i < end {
			end = i
		}
	}
	spec = spec[:end]
	var out []string
	seen := map[string]bool{}
	for _, m := range containerEntryRe.FindAllStringSubmatch(spec, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatalf("vacuity: %s: the scanner found no containers — it would pass any inventory", what)
	}
	sort.Strings(out)
	return out
}

// containerBlock returns the template text of one named container: from its
// `- name:` entry to the next sibling entry at the same indent or the end of
// the container list.
func containerBlock(t *testing.T, tmpl, name string) string {
	t.Helper()
	marker := "        - name: " + name + "\n"
	i := strings.Index(tmpl, marker)
	if i < 0 {
		t.Fatalf("container %q not declared — re-point this pin; do not drop the term", name)
	}
	block := tmpl[i+len(marker):]
	end := len(block)
	for _, stop := range []string{"\n        - name: ", "\n      containers:", "\n      volumes:"} {
		if j := strings.Index(block, stop); j >= 0 && j < end {
			end = j
		}
	}
	return block[:end]
}

var inlineResourcesRe = regexp.MustCompile(`(?s)\bresources:\s*\n\s*requests:\s*\n\s*cpu:\s*(\S+)\s*\n\s*memory:\s*(\S+)\s*\n\s*limits:\s*\n\s*cpu:\s*(\S+)\s*\n\s*memory:\s*(\S+)`)

// inlineShape reads a resources block written literally inside one container
// of a template (the only place bp-newapi's wait-for-sql-dsn figures exist).
func inlineShape(t *testing.T, tmpl, name string) containerShape {
	t.Helper()
	m := inlineResourcesRe.FindStringSubmatch(containerBlock(t, tmpl, name))
	if m == nil {
		t.Fatalf("container %q has no literal requests/limits block — if it moved to values.yaml, re-point this lookup; a missing term must never read as zero", name)
	}
	return containerShape{RequestsCPU: m[1], RequestsMemory: m[2], LimitsCPU: m[3], LimitsMemory: m[4]}
}

func namesEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	g, w := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, ",") != strings.Join(w, ",") {
		t.Errorf("%s: template declares containers %v, platformStack models %v — every regular container and native sidecar counts toward the pod the quota charges; reconcile the two", what, g, w)
	}
}

func stackByName(t *testing.T) map[string]platformStackWorkload {
	t.Helper()
	if len(platformStack) == 0 {
		t.Fatal("vacuity: platformStack is empty")
	}
	out := map[string]platformStackWorkload{}
	for _, w := range platformStack {
		if _, dup := out[w.Name]; dup {
			t.Fatalf("platformStack names %q twice", w.Name)
		}
		out[w.Name] = w
	}
	return out
}

func workload(t *testing.T, byName map[string]platformStackWorkload, name string) platformStackWorkload {
	t.Helper()
	w, ok := byName[name]
	if !ok {
		t.Fatalf("platformStack has no workload %q — the pod list changed; update the pins with it", name)
	}
	return w
}

// ─── 1. INSTALL LIST ────────────────────────────────────────────────────────

var orgTenantTemplateEntryRe = regexp.MustCompile(`(?m)^\s*"(bp-[a-z0-9-]+)\.yaml":\s*orgTenantBP\w+,`)

// TestPlatformStack_CoversTheBSSDoorInstallList reads the release list the BSS
// door renders for every Organization and checks platformStack against it in
// both directions, with the customer's purchase subtracted explicitly.
func TestPlatformStack_CoversTheBSSDoorInstallList(t *testing.T) {
	t.Parallel()
	src := readRepo(t, "products", "catalyst", "bootstrap", "api", "internal", "handler", "organization_gitops.go")
	rendered := map[string]bool{}
	for _, m := range orgTenantTemplateEntryRe.FindAllStringSubmatch(src, -1) {
		rendered[m[1]] = true
	}
	if len(rendered) < 4 {
		t.Fatalf("vacuity: found only %d bp-*.yaml entries in orgTenantTemplates (%v) — the scanner missed; re-point it", len(rendered), rendered)
	}
	// The customer's purchase, subtracted by name. Each must actually be in
	// the rendered list, or the subtraction is a no-op that hides a rename.
	purchase := []string{"bp-wordpress-tenant", "bp-stalwart-tenant"}
	for _, p := range purchase {
		if !rendered[p] {
			t.Fatalf("orgTenantTemplates no longer renders %s — the purchase list this test subtracts is stale", p)
		}
		delete(rendered, p)
	}

	modelled := map[string]bool{}
	for _, w := range platformStack {
		modelled[w.Release] = true
	}
	for r := range rendered {
		if !modelled[r] {
			t.Errorf("the BSS door installs %s for every Organization but platformStack has no workload from it — its pods are charged to the quota unaccounted", r)
		}
	}
	for r := range modelled {
		if !rendered[r] {
			t.Errorf("platformStack carries release %s, which orgTenantTemplates does not render — the overhead is overstated", r)
		}
	}
	if len(modelled) != 4 {
		t.Errorf("platformStack spans %d releases %v, want the four platform HelmReleases", len(modelled), modelled)
	}
}

// ─── 2. PIN ─────────────────────────────────────────────────────────────────

// TestPlatformStack_PinnedToSources compares every figure in platformStack with
// the file that sets it, and every pod's container count with its template.
func TestPlatformStack_PinnedToSources(t *testing.T) {
	t.Parallel()
	byName := stackByName(t)

	// bp-keycloak: the BSS door pins keycloak and its postgresql.
	bss := readRepo(t, "products", "catalyst", "bootstrap", "api", "internal", "handler", "organization_gitops.go")
	kc := helmReleaseValues(t, goRawString(t, bss, "const orgTenantBPKeycloak = "))
	kcPod := workload(t, byName, wlKeycloak)
	if len(kcPod.Containers) != 1 || len(kcPod.Inits) != 0 {
		t.Errorf("%s: modelled as %d containers / %d inits, want one sized app container (the bitnami init containers are preset-sized below it)", wlKeycloak, len(kcPod.Containers), len(kcPod.Inits))
	}
	sameShape(t, wlKeycloak+" keycloak", kcPod.Containers[0], shapeAt(t, kc, "keycloak", "resources"))
	pgPod := workload(t, byName, wlKeycloakPG)
	if len(pgPod.Containers) != 1 || len(pgPod.Inits) != 0 {
		t.Errorf("%s: modelled as %d containers / %d inits, want one", wlKeycloakPG, len(pgPod.Containers), len(pgPod.Inits))
	}
	sameShape(t, wlKeycloakPG+" postgresql primary", pgPod.Containers[0], shapeAt(t, kc, "keycloak", "postgresql", "primary", "resources"))

	// bp-newapi: the newapi container is pinned by BOTH doors (lockstep); the
	// sidecars and the init are chart defaults / inline.
	na := helmReleaseValues(t, goRawString(t, bss, "const orgTenantBPNewAPI = "))
	naPod := workload(t, byName, wlNewAPI)
	if len(naPod.Containers) != 3 || len(naPod.Inits) != 1 {
		t.Fatalf("%s: modelled as %d containers / %d inits, want newapi + sandbox-bridge + metering-sidecar and the wait-for-sql-dsn init", wlNewAPI, len(naPod.Containers), len(naPod.Inits))
	}
	sameShape(t, wlNewAPI+" newapi (BSS door)", naPod.Containers[0], shapeAt(t, na, "newapi", "resources"))
	if v := scalarAt(t, na, "valkey", "enabled"); v != "false" {
		t.Errorf("BSS door bp-newapi valkey.enabled = %q — a valkey pod would be a platform workload platformStack does not carry", v)
	}
	funnel := readRepo(t, "core", "services", "provisioning", "gitops", "helmrelease_apps.go")
	fn := helmReleaseValues(t, goRawString(t, funnel, "func generateNewAPIHR("))
	sameShape(t, wlNewAPI+" newapi (funnel door)", naPod.Containers[0], shapeAt(t, fn, "newapi", "resources"))
	if v := scalarAt(t, fn, "valkey", "enabled"); v != "false" {
		t.Errorf("funnel bp-newapi valkey.enabled = %q — a valkey pod would be a platform workload platformStack does not carry", v)
	}
	nv := yamlFile(t, "platform", "newapi", "chart", "values.yaml")
	if v := scalarAt(t, nv, "sandboxBridge", "enabled"); v != "true" {
		t.Errorf("sandboxBridge.enabled = %q — platformStack models the sandbox-bridge sidecar as rendered", v)
	}
	sameShape(t, wlNewAPI+" sandbox-bridge", naPod.Containers[1], shapeAt(t, nv, "sandboxBridge", "resources"))
	if v := scalarAt(t, nv, "meteringSidecar", "enabled"); v != "true" {
		t.Errorf("meteringSidecar.enabled = %q — platformStack models the metering-sidecar as rendered", v)
	}
	sameShape(t, wlNewAPI+" metering-sidecar", naPod.Containers[2], shapeAt(t, nv, "meteringSidecar", "resources"))
	dep := readRepo(t, "platform", "newapi", "chart", "templates", "deployment.yaml")
	sameShape(t, wlNewAPI+" wait-for-sql-dsn", naPod.Inits[0], inlineShape(t, dep, "wait-for-sql-dsn"))
	namesEqual(t, wlNewAPI, podSpecContainerNames(t, dep, "bp-newapi deployment.yaml"),
		[]string{"newapi", "sandbox-bridge", "metering-sidecar", "wait-for-sql-dsn"})

	// bp-newapi's CNPG database: chart default, one instance.
	cnpg := workload(t, byName, wlNewAPIPG)
	if v := scalarAt(t, nv, "cnpg", "enabled"); v != "true" {
		t.Errorf("cnpg.enabled = %q — platformStack models the newapi database pod as rendered", v)
	}
	if v := scalarAt(t, nv, "cnpg", "cluster", "instances"); v != "1" {
		t.Errorf("cnpg.cluster.instances = %q — platformStack models one database pod; a second instance is a second pod", v)
	}
	if len(cnpg.Containers) != 1 || len(cnpg.Inits) != 1 {
		t.Fatalf("%s: modelled as %d containers / %d inits, want the instance container and the operator's bootstrap init", wlNewAPIPG, len(cnpg.Containers), len(cnpg.Inits))
	}
	sameShape(t, wlNewAPIPG+" instance", cnpg.Containers[0], shapeAt(t, nv, "cnpg", "cluster", "resources"))
	sameShape(t, wlNewAPIPG+" bootstrap-controller init", cnpg.Inits[0], shapeAt(t, nv, "cnpg", "cluster", "resources"))

	// bp-openclaw: chart default, one container.
	ov := yamlFile(t, "platform", "openclaw", "chart", "values.yaml")
	oc := workload(t, byName, wlOpenClaw)
	if len(oc.Containers) != 1 || len(oc.Inits) != 0 {
		t.Fatalf("%s: modelled as %d containers / %d inits, want one", wlOpenClaw, len(oc.Containers), len(oc.Inits))
	}
	sameShape(t, wlOpenClaw+" controller", oc.Containers[0], shapeAt(t, ov, "controller", "resources"))
	namesEqual(t, wlOpenClaw, podSpecContainerNames(t, readRepo(t, "platform", "openclaw", "chart", "templates", "controller-deployment.yaml"), "bp-openclaw controller-deployment.yaml"),
		[]string{"controller"})

	// bp-agenity: chart defaults; the init container is unsized by the chart.
	av := yamlFile(t, "products", "agenity", "chart", "values.yaml")
	ag := workload(t, byName, wlAgenity)
	if len(ag.Containers) != 2 || len(ag.Inits) != 1 {
		t.Fatalf("%s: modelled as %d containers / %d inits, want chepherd + creds-resync and the seed-claude-creds init", wlAgenity, len(ag.Containers), len(ag.Inits))
	}
	sameShape(t, wlAgenity+" chepherd", ag.Containers[0], shapeAt(t, av, "resources"))
	if v := scalarAt(t, av, "anthropic", "credentialResync", "enabled"); v != "true" {
		t.Errorf("anthropic.credentialResync.enabled = %q — platformStack models the creds-resync sidecar as rendered", v)
	}
	if v := scalarAt(t, av, "anthropic", "credentialsKey"); v == "" {
		t.Errorf("anthropic.credentialsKey is empty — the creds-resync sidecar renders only under it, so platformStack would overstate the pod")
	}
	sameShape(t, wlAgenity+" creds-resync", ag.Containers[1], shapeAt(t, av, "anthropic", "credentialResync", "resources"))
	sts := readRepo(t, "products", "agenity", "chart", "templates", "statefulset.yaml")
	namesEqual(t, wlAgenity, podSpecContainerNames(t, sts, "bp-agenity statefulset.yaml"),
		[]string{"seed-claude-creds", "chepherd", "creds-resync"})
	if ag.Inits[0] != (containerShape{}) {
		t.Errorf("%s: seed-claude-creds is modelled as sized %+v, but the chart leaves it unsized — the LimitRange default is what admission charges", wlAgenity, ag.Inits[0])
	}
	if strings.Contains(containerBlock(t, sts, "seed-claude-creds"), "resources:") {
		t.Errorf("%s: the chart now sizes seed-claude-creds — replace the zero-value shape in platformStack with its figures (the LimitRange default no longer applies)", wlAgenity)
	}

	// The oidc-gate the bp-agenity chart renders in front of agenity.
	gate := workload(t, byName, wlOIDCGate)
	if len(gate.Containers) != 1 || len(gate.Inits) != 0 {
		t.Fatalf("%s: modelled as %d containers / %d inits, want one", wlOIDCGate, len(gate.Containers), len(gate.Inits))
	}
	sameShape(t, wlOIDCGate+" oauth2-proxy", gate.Containers[0], shapeAt(t, av, "oidcGate", "resources"))
	namesEqual(t, wlOIDCGate, podSpecContainerNames(t, readRepo(t, "products", "agenity", "chart", "templates", "oidc-gate.yaml"), "bp-agenity oidc-gate.yaml"),
		[]string{"oauth2-proxy"})

	// Every workload was addressed above; a new row would otherwise be a
	// figure nothing pins.
	if len(platformStack) != 7 {
		t.Errorf("platformStack has %d workloads, this test pins 7 — add the new workload's source pin here", len(platformStack))
	}
}

// ─── 3. FIGURES ─────────────────────────────────────────────────────────────

// TestPlatformStackOverhead_PinnedFigures pins the derived per-plan overhead
// by value and checks the rendered annotation carries the same figures.
func TestPlatformStackOverhead_PinnedFigures(t *testing.T) {
	t.Parallel()
	hardCapped := 0
	for slug, q := range planQuotaTable {
		if q.Burstable {
			if _, pinned := wantPlatformStackOverhead[slug]; pinned {
				t.Errorf("plan %q is Burstable and renders no quota; it must not carry a pinned stack figure", slug)
			}
			continue
		}
		hardCapped++
		want, ok := wantPlatformStackOverhead[slug]
		if !ok {
			t.Errorf("plan %q has no pinned platform-stack figure — derive it and add it to wantPlatformStackOverhead", slug)
			continue
		}
		ps := platformStackOverheadFor(q)
		got := map[string]resource.Quantity{
			"requests.cpu": ps.RequestsCPU, "requests.memory": ps.RequestsMemory,
			"limits.cpu": ps.LimitsCPU, "limits.memory": ps.LimitsMemory,
		}
		for res, w := range want {
			g := got[res]
			if g.IsZero() {
				t.Errorf("plan %q: platform-stack overhead %s is zero — the stack would eat the plan again", slug, res)
			}
			if g.Cmp(mustQ(t, slug+" "+res, w)) != 0 {
				t.Errorf("plan %q: derived platform-stack overhead %s = %s, pinned figure is %s — a source moved; re-pin here AND in docs/SYSTEM-DESIGN.md and products/chargeback/DESIGN.md", slug, res, g.String(), w)
			}
		}
		rq := string(renderPlan(t, slug)["vcluster/resourcequota.yaml"])
		if !strings.Contains(rq, "openova.io/platform-stack-overhead: "+fmt.Sprintf("%q", ps.String())) {
			t.Errorf("plan %q: resourcequota.yaml annotation openova.io/platform-stack-overhead does not equal %q:\n%s", slug, ps.String(), rq)
		}
	}
	if hardCapped != len(wantPlatformStackOverhead) {
		t.Fatalf("vacuity: %d hard-capped plans, %d pinned figures", hardCapped, len(wantPlatformStackOverhead))
	}
	// XL is the plan where the unsized init container dominates; the figure
	// must differ from S or the LimitRange-default rule is not being applied.
	if s, xl := platformStackOverheadFor(planQuota("s")), platformStackOverheadFor(planQuota("xl")); s.RequestsCPU.Cmp(xl.RequestsCPU) >= 0 {
		t.Errorf("plan xl stack overhead requests.cpu %s is not above plan s %s — the 2-CPU LimitRange default should size agenity's unsized init container above its app containers on xl", xl.RequestsCPU.String(), s.RequestsCPU.String())
	}
}

// ─── 4. RULE ────────────────────────────────────────────────────────────────

// TestPlatformStackOverheadOf_RuleAndDefaults proves the arithmetic on a
// synthetic stack where each branch is load-bearing: an unsized init takes the
// LimitRange defaults and dominates only when those are large enough; a sized
// init dominates on the resource where it is larger; pods sum.
func TestPlatformStackOverheadOf_RuleAndDefaults(t *testing.T) {
	t.Parallel()
	stack := []platformStackWorkload{
		{Name: "a", Containers: []containerShape{
			{RequestsCPU: "100m", RequestsMemory: "100Mi", LimitsCPU: "200m", LimitsMemory: "200Mi"},
			{RequestsCPU: "50m", RequestsMemory: "50Mi", LimitsCPU: "50m", LimitsMemory: "50Mi"},
		}, Inits: []containerShape{{}}},
		{Name: "b", Containers: []containerShape{{RequestsCPU: "100m", RequestsMemory: "1Gi", LimitsCPU: "100m", LimitsMemory: "1Gi"}},
			Inits: []containerShape{{RequestsCPU: "300m", RequestsMemory: "256Mi", LimitsCPU: "300m", LimitsMemory: "256Mi"}}},
	}
	check := func(defCPU, defMem string, want map[string]string) {
		t.Helper()
		o := platformStackOverheadOf(stack, defCPU, defMem)
		got := map[string]resource.Quantity{
			"requests.cpu": o.RequestsCPU, "requests.memory": o.RequestsMemory,
			"limits.cpu": o.LimitsCPU, "limits.memory": o.LimitsMemory,
		}
		for res, w := range want {
			if g := got[res]; g.Cmp(mustQ(t, res, w)) != 0 {
				t.Errorf("defaults %s/%s: %s = %s, want %s", defCPU, defMem, res, g.String(), w)
			}
		}
	}
	// Large defaults: a's unsized init (250m/512Mi) beats its app sum
	// (150m/150Mi requests, 250m/250Mi limits) → a = 250m/512Mi both sides.
	// b: init wins CPU (300m > 100m), app wins memory (1Gi > 256Mi).
	check("250m", "512Mi", map[string]string{
		"requests.cpu": "550m", "requests.memory": "1536Mi", "limits.cpu": "550m", "limits.memory": "1536Mi",
	})
	// Small defaults: a's init (10m/10Mi) loses → a = app sum 150m/150Mi,
	// 250m/250Mi. b unchanged. The two results differ, so the default
	// resolution is load-bearing rather than decorative.
	check("10m", "10Mi", map[string]string{
		"requests.cpu": "450m", "requests.memory": "1174Mi", "limits.cpu": "550m", "limits.memory": "1274Mi",
	})
	if o := platformStackOverheadOf(nil, "250m", "512Mi"); !o.RequestsCPU.IsZero() || !o.LimitsMemory.IsZero() {
		t.Errorf("empty stack must sum to zero, got %s", o.String())
	}
}

// TestPlatformStack_NoZeroTerm guards the table's shape: every release is one
// of the four, every sized container carries all four figures, and every
// hard-capped plan's overhead is non-zero on every resource.
func TestPlatformStack_NoZeroTerm(t *testing.T) {
	t.Parallel()
	releases := map[string]bool{"bp-keycloak": true, "bp-newapi": true, "bp-openclaw": true, "bp-agenity": true}
	for _, w := range platformStack {
		if !releases[w.Release] {
			t.Errorf("%s: release %q is not one of the four platform HelmReleases", w.Name, w.Release)
		}
		if len(w.Containers) == 0 {
			t.Errorf("%s: no app container — a pod without one cannot exist", w.Name)
		}
		if w.Source == "" {
			t.Errorf("%s: no Source — every figure must name where it is pinned", w.Name)
		}
		for i, c := range w.Containers {
			if c == (containerShape{}) {
				t.Errorf("%s: app container %d is unsized — only an init container may be modelled as taking the LimitRange default", w.Name, i)
				continue
			}
			for _, v := range []string{c.RequestsCPU, c.RequestsMemory, c.LimitsCPU, c.LimitsMemory} {
				if q := mustQ(t, w.Name, v); q.IsZero() {
					t.Errorf("%s: container %d carries a zero figure %+v", w.Name, i, c)
				}
			}
		}
	}
	for slug, q := range planQuotaTable {
		if q.Burstable {
			continue
		}
		o := platformStackOverheadFor(q)
		for res, v := range map[string]resource.Quantity{
			"requests.cpu": o.RequestsCPU, "requests.memory": o.RequestsMemory,
			"limits.cpu": o.LimitsCPU, "limits.memory": o.LimitsMemory,
		} {
			if v.IsZero() {
				t.Errorf("plan %q: platform-stack overhead %s is zero", slug, res)
			}
		}
	}
}
