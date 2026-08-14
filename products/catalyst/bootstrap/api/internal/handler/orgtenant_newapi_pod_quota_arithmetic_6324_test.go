package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────
// #6324, BSS-door half — THE MODELLED UNIT IS THE POD, NOT ONE CONTAINER.
//
// Lockstep with core/services/provisioning/gitops/
// newapi_pod_quota_arithmetic_6324_test.go, which carries the full derivation.
// Short form: there are TWO producers of a per-Org bp-newapi release — the
// funnel generator (generateNewAPIHR) and this BSS door (orgTenantBPNewAPI) —
// and BOTH sized the release by the one container they pin,
// `newapi.resources.limits.cpu`. A ResourceQuota admits a POD:
//
//	sandbox-bridge     200m   chart default, native sidecar (initContainers +
//	                          restartPolicy: Always, #3374) — counts on k8s 1.29+
//	newapi             500m   pinned by this overlay
//	metering-sidecar   500m   chart default
//	                  ─────
//	POD limits.cpu    1200m
//
// Measured live on hw296 (dep e689e3b34a75fdec), Deployment walkthree/bp-newapi
// ReplicaFailure=True FailedCreate:
//
//	pods "bp-newapi-6867df99bd-..." is forbidden: exceeded quota: plan-quota,
//	requested: limits.cpu=1200m, used: limits.cpu=3300m, limited: limits.cpu=4
//
// WHY THIS IS DUPLICATED RATHER THAN SHARED. The two producers live in two
// separate Go modules (core/services/provisioning and
// products/catalyst/bootstrap/api) with no shared package between them, so the
// arithmetic is written twice. The duplication is made safe by pinning BOTH
// copies to the same derived totals — 1200m for the Pod, 1950m of the 2000m
// smallest-plan cap for the Org bundle — so a change that moves one door's
// number without the other turns exactly one of them red and names it. A fix
// applied to one producer and not the other is the shape that let UAT row 15
// render three of seven Org chips for a month behind a green label-only test.
//
// Every TERM here is read from the file the platform itself reads and a miss is
// FATAL, never zero. The TOTALS are pinned against the cluster's own admission
// arithmetic quoted above — an independent measurement, not a restatement.
//
// This file changes no quota, request or limit value; the plan-capacity
// question belongs to #5393, which is founder-gated.
//
// Refs #6324 #5393 #3988
// ─────────────────────────────────────────────────────────────────────────

// ─── yamlScalar, ported verbatim ─────────────────────────────────────────
//
// This is the SAME indentation-aware lookup the funnel-door guard uses
// (core/services/provisioning/gitops/newapi_hr_installable_5987_test.go),
// copied because the module boundary forbids importing it. It is a verbatim
// port ON PURPOSE: re-hand-rolling the predicate as a regex is how a lookup
// silently crosses out of the block it is scoped to and attributes a
// neighbouring container's number to this term — the same mis-measurement class
// this file exists to remove. TestYAMLScalarBSS_SelfCheck below proves the port
// still refuses to skip a level and still misses what is absent.
func yamlScalarBSS(block, path string) (string, bool) {
	segs := strings.Split(path, ".")
	lines := strings.Split(block, "\n")
	depth, idx := 0, 0
	for si, seg := range segs {
		found := false
		for ; idx < len(lines); idx++ {
			raw := lines[idx]
			trimmed := strings.TrimLeft(raw, " ")
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			ind := len(raw) - len(trimmed)
			if ind < depth {
				return "", false
			}
			if ind > depth {
				continue
			}
			if !strings.HasPrefix(trimmed, seg+":") {
				continue
			}
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, seg+":"))
			if si == len(segs)-1 {
				return rest, true
			}
			idx++
			depth += 2
			found = true
			break
		}
		if !found {
			return "", false
		}
	}
	return "", false
}

// TestYAMLScalarBSS_SelfCheck is the port's vacuity guard: a lookup that
// silently matched nothing would make every assertion below unfalsifiable, and
// one that flattened the tree would find a term under the wrong parent.
func TestYAMLScalarBSS_SelfCheck(t *testing.T) {
	fixture := strings.Join([]string{
		"sovereignFQDN: acme.omani.homes",
		"newapi:",
		"  resources:",
		"    # a comment that must be skipped",
		"    requests:",
		"      cpu: 500m",
		"    limits:",
		"      cpu: 500m",
		"meteringSidecar:",
		"  enabled: true",
		"  resources:",
		"    limits:",
		"      cpu: 500m",
	}, "\n")

	for path, want := range map[string]string{
		"sovereignFQDN":                        "acme.omani.homes",
		"newapi.resources.limits.cpu":          "500m",
		"newapi.resources.requests.cpu":        "500m",
		"meteringSidecar.enabled":              "true",
		"meteringSidecar.resources.limits.cpu": "500m",
	} {
		if got, ok := yamlScalarBSS(fixture, path); !ok || got != want {
			t.Errorf("yamlScalarBSS(%q) = (%q, %v), want (%q, true)", path, got, ok, want)
		}
	}

	// It must MISS what is absent and must NOT skip a level — a flattened
	// lookup would read `limits.cpu` out of whichever block came first and
	// attribute it to every term.
	for _, path := range []string{
		"sandboxBridge.resources.limits.cpu",
		"newapi.limits.cpu",
		"resources.limits.cpu",
		"newapi.resources.limits.memory",
	} {
		if got, ok := yamlScalarBSS(fixture, path); ok {
			t.Errorf("yamlScalarBSS(%q) = (%q, true), want a miss", path, got)
		}
	}
}

// ─── Repo reads ──────────────────────────────────────────────────────────

// repoFileBSS reads a repo-root-relative file. This package lives at
// products/catalyst/bootstrap/api/internal/handler — six levels down. FATAL on
// a miss, because every caller is deriving a term and a term that silently
// resolves to zero is exactly how the Pod total lost 700m.
//
// NOTE: reads like this are INVISIBLE to the `go test` cache key. Mutate the
// chart and a cached `ok` still stands over it. Every proof of this file must
// run under `-count=1`; scripts/check-go-test-count1.sh enforces that on every
// workflow (#6235), and this module's gate already passes it.
func repoFileBSS(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{"..", "..", "..", "..", "..", ".."}, parts...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func newapiChartValuesBSS(t *testing.T) string {
	t.Helper()
	return repoFileBSS(t, "platform", "newapi", "chart", "values.yaml")
}

// orgTenantBPNewAPIValues returns orgTenantBPNewAPI's `spec.values:` mapping
// dedented to column 0, so a path reads like a chart values path.
func orgTenantBPNewAPIValues(t *testing.T) string {
	t.Helper()
	var out []string
	grabbing := false
	for _, l := range strings.Split(orgTenantBPNewAPI, "\n") {
		if l == "  values:" {
			grabbing = true
			continue
		}
		if !grabbing {
			continue
		}
		if strings.TrimSpace(l) == "" {
			out = append(out, "")
			continue
		}
		if !strings.HasPrefix(l, "    ") {
			break
		}
		out = append(out, l[4:])
	}
	if len(out) == 0 {
		t.Fatalf("orgTenantBPNewAPI carries no spec.values block")
	}
	return strings.Join(out, "\n")
}

// ─── The Pod model ───────────────────────────────────────────────────────

// cpuTermBSS is one container that counts toward the bp-newapi Pod's effective
// limits.cpu. Mirrors newapiPodCPUTerms on the funnel door.
type cpuTermBSS struct {
	container string
	block     string
	enabledAt string
	native    bool
}

var newapiPodCPUTermsBSS = []cpuTermBSS{
	{container: "sandbox-bridge", block: "sandboxBridge", enabledAt: "sandboxBridge.enabled", native: true},
	{container: "newapi", block: "newapi"},
	{container: "metering-sidecar", block: "meteringSidecar", enabledAt: "meteringSidecar.enabled"},
}

type podCPUBSS struct {
	total     int
	byTerm    map[string]int
	fromChart map[string]bool
}

func (p podCPUBSS) String() string {
	var b strings.Builder
	b.WriteString("  bp-newapi Pod limits.cpu breakdown:\n")
	for _, term := range newapiPodCPUTermsBSS {
		m, ok := p.byTerm[term.container]
		if !ok {
			fmt.Fprintf(&b, "    %-18s (not rendered — its enabled gate is false)\n", term.container)
			continue
		}
		src := "per-Org overlay"
		if p.fromChart[term.container] {
			src = "chart default"
		}
		kind := "container"
		if term.native {
			kind = "native sidecar"
		}
		fmt.Fprintf(&b, "    %-18s %5dm  (%s, %s)\n", term.container, m, kind, src)
	}
	fmt.Fprintf(&b, "    %-18s %5dm\n", "POD TOTAL", p.total)
	return b.String()
}

// newapiPodCPUMillisBSS models the Pod the way a ResourceQuota admits it: every
// regular container plus every native sidecar that renders, taking this
// overlay's pin where it has one and the chart default otherwise — which is
// what Helm does, and what the single-container model failed to do for the two
// terms this overlay never mentions.
func newapiPodCPUMillisBSS(t *testing.T) podCPUBSS {
	t.Helper()
	overlay := orgTenantBPNewAPIValues(t)
	chart := newapiChartValuesBSS(t)
	out := podCPUBSS{byTerm: map[string]int{}, fromChart: map[string]bool{}}

	for _, term := range newapiPodCPUTermsBSS {
		if term.enabledAt != "" {
			on, ok := yamlScalarBSS(overlay, term.enabledAt)
			if !ok {
				on, ok = yamlScalarBSS(chart, term.enabledAt)
			}
			if !ok {
				t.Fatalf("neither orgTenantBPNewAPI nor platform/newapi/chart/values.yaml resolves "+
					"%q, so this guard cannot tell whether the %q container renders. Re-point the gate.",
					term.enabledAt, term.container)
			}
			if on != "true" {
				continue
			}
		}

		path := term.block + ".resources.limits.cpu"
		v, ok := yamlScalarBSS(overlay, path)
		fromChart := false
		if !ok {
			v, ok = yamlScalarBSS(chart, path)
			fromChart = true
		}
		if !ok {
			t.Fatalf("neither orgTenantBPNewAPI nor platform/newapi/chart/values.yaml resolves %q. "+
				"The chart was restructured and this guard's Pod arithmetic no longer covers the %q "+
				"container. RE-POINT the path — do NOT drop the term; a dropped term is #6324.",
				path, term.container)
		}
		m, parsed := cpuMillisFromYAML(v)
		if !parsed {
			t.Fatalf("%s = %q is not a parseable CPU quantity", path, v)
		}
		out.byTerm[term.container] = m
		out.fromChart[term.container] = fromChart
		out.total += m
	}
	return out
}

// ─── Neighbours in the same Org namespace ────────────────────────────────

func openclawControllerPodCPUMillisBSS(t *testing.T) int {
	t.Helper()
	chart := repoFileBSS(t, "platform", "openclaw", "chart", "values.yaml")
	v, ok := yamlScalarBSS(chart, "controller.resources.limits.cpu")
	if !ok {
		t.Fatalf("platform/openclaw/chart/values.yaml has no controller.resources.limits.cpu — " +
			"re-point this term; the Org bundle arithmetic is incomplete without it")
	}
	m, parsed := cpuMillisFromYAML(v)
	if !parsed {
		t.Fatalf("openclaw controller.resources.limits.cpu = %q is not parseable", v)
	}
	return m
}

// newapiCNPGCPUMillisBSS is the per-Org Postgres cost: per-instance limits.cpu
// times the instance count. The instance count is a term in its own right — a
// move to `instances: 2` would double this silently.
func newapiCNPGCPUMillisBSS(t *testing.T) int {
	t.Helper()
	chart := newapiChartValuesBSS(t)
	cpu, ok := yamlScalarBSS(chart, "cnpg.cluster.resources.limits.cpu")
	if !ok {
		t.Fatalf("platform/newapi/chart/values.yaml has no cnpg.cluster.resources.limits.cpu — re-point this term")
	}
	m, parsed := cpuMillisFromYAML(cpu)
	if !parsed {
		t.Fatalf("cnpg.cluster.resources.limits.cpu = %q is not parseable", cpu)
	}
	inst, ok := yamlScalarBSS(chart, "cnpg.cluster.instances")
	if !ok {
		t.Fatalf("platform/newapi/chart/values.yaml has no cnpg.cluster.instances — re-point this term")
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(inst), "%d", &n); err != nil || n < 1 {
		t.Fatalf("cnpg.cluster.instances = %q is not a positive integer (%v)", inst, err)
	}
	return m * n
}

// smallestPlanCPUMillisFromControllerBSS derives the plan-"s" cap from the ONE
// table the org-controller drives the ResourceQuota off. Restating "2000" as a
// local constant would let a LOWERED cap pass unnoticed.
func smallestPlanCPUMillisFromControllerBSS(t *testing.T) int {
	t.Helper()
	src := repoFileBSS(t, "core", "controllers", "organization", "internal", "gitops", "manifests.go")
	re := regexp.MustCompile(`"s":\s*\{Slug:\s*"s",\s*CPU:\s*"([^"]*)"`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf(`planQuotaTable["s"] not found in ` +
			`core/controllers/organization/internal/gitops/manifests.go — the plan table was ` +
			`restructured and this guard can no longer see the cap it asserts against. Re-point it.`)
	}
	millis, ok := cpuMillisFromYAML(m[1])
	if !ok {
		t.Fatalf(`planQuotaTable["s"].CPU = %q is not a parseable CPU quantity`, m[1])
	}
	return millis
}

// ─── The assertions ──────────────────────────────────────────────────────

// liveAdmissionPodCPUMillisBSS is the quantity the hw296 admission controller
// itself reported (`requested: limits.cpu=1200m`). Pinned rather than derived
// ON PURPOSE: it is the independent observation the derived arithmetic is
// checked against, and it is what keeps the two doors from drifting apart.
const liveAdmissionPodCPUMillisBSS = 1200

func TestOrgTenantBPNewAPI_6324_PodIsTheModelledUnit(t *testing.T) {
	pod := newapiPodCPUMillisBSS(t)

	sum := 0
	for _, m := range pod.byTerm {
		if m <= 0 {
			t.Fatalf("a term resolved to %dm — a zero term means the lookup missed and the Pod "+
				"total silently collapses toward the single-container model:\n%s", m, pod)
		}
		sum += m
	}
	if sum != pod.total {
		t.Fatalf("Pod total %dm != the sum of its terms %dm — the arithmetic lost a term:\n%s",
			pod.total, sum, pod)
	}

	container, ok := pod.byTerm["newapi"]
	if !ok {
		t.Fatalf("the `newapi` container is absent from the Pod model:\n%s", pod)
	}
	if pod.total <= container {
		t.Fatalf("VACUITY: Pod total %dm is not greater than the `newapi` container alone (%dm), "+
			"so this guard would pass on the exact single-container model #6324 replaces:\n%s",
			pod.total, container, pod)
	}

	if pod.total != liveAdmissionPodCPUMillisBSS {
		t.Fatalf("Pod total = %dm, but the live hw296 admission refusal for walkthree/bp-newapi "+
			"read `requested: limits.cpu=%dm`. The BSS door and the cluster now disagree about "+
			"what this Pod costs — reconcile them, and check the funnel door "+
			"(core/services/provisioning/gitops) has moved in lockstep:\n%s",
			pod.total, liveAdmissionPodCPUMillisBSS, pod)
	}
}

func TestOrgTenantBPNewAPI_6324_OrgBundleFitsSmallestPlan(t *testing.T) {
	pod := newapiPodCPUMillisBSS(t)
	openclaw := openclawControllerPodCPUMillisBSS(t)
	cnpg := newapiCNPGCPUMillisBSS(t)
	planCap := smallestPlanCPUMillisFromControllerBSS(t)

	bundle := pod.total + openclaw + cnpg
	if bundle > planCap {
		t.Fatalf("bp-newapi POD %dm + openclaw controller %dm + newapi CNPG %dm = %dm exceeds the "+
			"smallest-plan cap %dm — the Pod is refused at admission, which is the live hw296 "+
			"failure verbatim:\n%s", pod.total, openclaw, cnpg, bundle, planCap, pod)
	}

	// 50m of a 2000m Org cap for everything else the Org will ever install.
	// Pinning it means any term that grows turns this red instead of quietly
	// consuming the last of the margin.
	const wantBundle, wantHeadroom = 1950, 50
	if bundle != wantBundle || planCap-bundle != wantHeadroom {
		t.Fatalf("the Org bundle now totals %dm of the %dm cap (headroom %dm); this guard was "+
			"written against %dm / %dm headroom, measured from the same files. A term moved — "+
			"name it and re-derive:\n  bp-newapi POD  %dm\n  openclaw ctrl  %dm\n  newapi CNPG    %dm\n%s",
			bundle, planCap, planCap-bundle, wantBundle, wantHeadroom, pod.total, openclaw, cnpg, pod)
	}

	// The local mirror used by the pre-#6324 assertions in
	// orgtenant_newapi_plan_quota_row232_test.go must still be the platform's
	// real ceiling.
	if planCap != smallestPlanCPUMillisBSS {
		t.Fatalf("smallestPlanCPUMillisBSS = %dm but planQuotaTable[\"s\"].CPU now grants %dm — "+
			"this package is asserting against a ceiling the platform no longer has",
			smallestPlanCPUMillisBSS, planCap)
	}
}

// TestOrgTenantBPNewAPI_6324_VacuityCheck_TheOldModelWouldHavePassed proves the
// file is load-bearing: it reconstructs the PRE-FIX single-container arithmetic
// from the same derived terms and shows it reported 750m of headroom where the
// truth is 50m — the old guard was green over a Pod the cluster was refusing.
// Collapse the Pod model back to one container and these numbers converge.
func TestOrgTenantBPNewAPI_6324_VacuityCheck_TheOldModelWouldHavePassed(t *testing.T) {
	pod := newapiPodCPUMillisBSS(t)
	openclaw := openclawControllerPodCPUMillisBSS(t)
	cnpg := newapiCNPGCPUMillisBSS(t)
	planCap := smallestPlanCPUMillisFromControllerBSS(t)

	container := pod.byTerm["newapi"]
	oldHeadroom := planCap - (container + openclaw + cnpg)
	newHeadroom := planCap - (pod.total + openclaw + cnpg)

	if oldHeadroom == newHeadroom {
		t.Fatalf("VACUITY: the single-container model and the Pod model report the SAME headroom "+
			"(%dm). Either the sidecar terms have been dropped, or they resolved to zero:\n%s",
			oldHeadroom, pod)
	}
	if oldHeadroom <= newHeadroom {
		t.Fatalf("VACUITY: the single-container model reports LESS headroom (%dm) than the Pod "+
			"model (%dm), which is arithmetically impossible while the sidecars cost anything:\n%s",
			oldHeadroom, newHeadroom, pod)
	}
	const wantOld, wantNew = 750, 50
	if oldHeadroom != wantOld || newHeadroom != wantNew {
		t.Fatalf("the understatement changed: the single-container model now reports %dm of "+
			"headroom and the Pod model %dm (this file was written against %dm vs %dm). Re-derive "+
			"and restate the defect before adjusting these:\n%s",
			oldHeadroom, newHeadroom, wantOld, wantNew, pod)
	}
}

// TestOrgTenantBPNewAPI_6324_DoorsAgreeOnTheOverriddenTerm pins the ONE term
// this door owns against the funnel door's producer, read from source. The two
// overlays are meant to be identical on sizing; a divergence here is the "fix
// landed on one producer" shape, and it is invisible to either door's own
// arithmetic because each is internally consistent.
func TestOrgTenantBPNewAPI_6324_DoorsAgreeOnTheOverriddenTerm(t *testing.T) {
	bss, ok := yamlScalarBSS(orgTenantBPNewAPIValues(t), "newapi.resources.limits.cpu")
	if !ok {
		t.Fatalf("orgTenantBPNewAPI does not pin newapi.resources.limits.cpu")
	}
	bssMillis, parsed := cpuMillisFromYAML(bss)
	if !parsed {
		t.Fatalf("orgTenantBPNewAPI newapi.resources.limits.cpu = %q is not parseable", bss)
	}

	// The funnel producer is a Go source file in another module; read the
	// literal it stamps. A miss is FATAL — "the other door has no override" is
	// never a safe silent default.
	src := repoFileBSS(t, "core", "services", "provisioning", "gitops", "helmrelease_apps.go")
	re := regexp.MustCompile(`(?s)newapi:\s*\n\s*resources:\s*\n.*?limits:\s*\n\s*cpu:\s*(\S+)`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("could not find generateNewAPIHR's newapi.resources.limits.cpu in " +
			"core/services/provisioning/gitops/helmrelease_apps.go — the funnel producer was " +
			"restructured; re-point this cross-door check rather than dropping it")
	}
	funnelMillis, parsed := cpuMillisFromYAML(m[1])
	if !parsed {
		t.Fatalf("funnel newapi.resources.limits.cpu = %q is not parseable", m[1])
	}

	if bssMillis != funnelMillis {
		t.Fatalf("the two per-Org bp-newapi producers now size the `newapi` container "+
			"differently: BSS door %dm vs funnel generator %dm. Both render into the same kind "+
			"of Org ResourceQuota, so one of them is wrong — and neither door's own arithmetic "+
			"can see it, because each is internally consistent (#6324).",
			bssMillis, funnelMillis)
	}
}
