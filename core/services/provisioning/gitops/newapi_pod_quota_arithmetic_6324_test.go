package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────
// #6324 — THE MODELLED UNIT IS THE POD, NOT ONE CONTAINER.
//
// The per-Org bp-newapi sizing override (#6114, UAT row 232) pins exactly one
// term, `newapi.resources.limits.cpu`, and both of its guards asserted on that
// one term. A ResourceQuota does not admit a container; it admits a POD. The
// bp-newapi chart puts two more quota-counted containers in that same Pod and
// the per-Org overlay overrides neither:
//
//	sandbox-bridge     200m   platform/newapi/chart/values.yaml sandboxBridge
//	newapi             500m   pinned by the per-Org overlay
//	metering-sidecar   500m   platform/newapi/chart/values.yaml meteringSidecar
//	                  ─────
//	POD limits.cpu    1200m
//
// sandbox-bridge is the term that is easy to miss by reading, and the reason is
// structural rather than careless: templates/deployment.yaml renders it as a
// NATIVE SIDECAR — an `initContainers` entry carrying `restartPolicy: Always`
// (#3374) — so a reader scanning the `containers:` list sees only newapi and
// metering-sidecar. Since Kubernetes 1.29 a native sidecar's limits count
// toward the Pod's effective total exactly like a regular container's, and
// Sovereigns run k3s v1.31.4. Note the term does NOT depend on which list it
// lands in: with `sandboxBridge.nativeSidecar: false` the chart renders the
// same image as a plain container instead, and it still counts. The term is
// gated on `sandboxBridge.enabled` alone.
//
// The plain `wait-for-sql-dsn` initContainer is correctly EXCLUDED, and
// TestNewAPIPodCPU_6324_PlainInitDoesNotDominate proves the exclusion instead
// of assuming it: a Pod's effective limit is
//
//	max( sum(containers) + sum(native sidecars),
//	     max over plain init i of ( init_i + sum(sidecars declared before i) ) )
//
// so the DSN gate only matters if its own branch overtakes the container sum.
// Today it does not (100m + the 200m bridge declared before it = 300m against
// 1200m), and a chart change that inverted that would go red here.
//
// MEASURED LIVE, hw296 dep e689e3b34a75fdec, Deployment walkthree/bp-newapi
// ReplicaFailure=True FailedCreate:
//
//	pods "bp-newapi-6867df99bd-..." is forbidden: exceeded quota: plan-quota,
//	requested: limits.cpu=1200m, used: limits.cpu=3300m, limited: limits.cpu=4
//
// `requested: limits.cpu=1200m` is the admission controller's OWN arithmetic
// over the same Pod, and it matches the sum above term for term. That is what
// identifies 500m as the WRONG QUANTITY rather than merely a conservative one.
//
// WHAT IS DERIVED AND WHAT IS PINNED, deliberately split:
//
//   - Every TERM (200m / 500m / 500m, the openclaw controller's 250m, the CNPG
//     500m, the plan-"s" 2000m cap) is read from the file the platform itself
//     reads, and a lookup that misses is FATAL, never zero. A guard that
//     restated these numbers as literals would drift the moment a chart moved
//     and would go on reporting a total that no longer exists — which is the
//     defect being closed, one level up.
//   - The TOTALS (1200m, and the 1950m-of-2000m bundle) are pinned against the
//     cluster's own admission arithmetic quoted above. That is an INDEPENDENT
//     measurement, not a restatement of the source, so guard-vs-cluster
//     disagreement surfaces instead of being defined away.
//
// This file changes no quota, request or limit value. It only counts them
// correctly. Whether 1200m is the right size for an Org boundary is a
// plan-capacity decision and belongs to #5393, which is founder-gated.
//
// Refs #6324 #5393 #3988
// ─────────────────────────────────────────────────────────────────────────

// repoPath joins a path relative to the repository root. This package lives at
// core/services/provisioning/gitops, so the root is four levels up.
func repoPath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "..", ".."}, parts...)...)
}

// readRepoFile reads a repo-root-relative file. FATAL on a miss: every caller
// below is deriving a term of an arithmetic, and a term that silently resolves
// to zero is precisely how the Pod total lost 700m.
//
// NOTE FOR ANY FUTURE READER: reads like this one are INVISIBLE to the `go
// test` cache key (the key covers this module's own files, not
// platform/newapi/chart/**). Mutate the chart and a cached `ok` still stands.
// Every proof of this file must run under `-count=1`;
// scripts/check-go-test-count1.sh enforces that on every workflow (#6235).
func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	p := repoPath(parts...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func newapiChartValues(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "platform", "newapi", "chart", "values.yaml")
}

// cpuTerm is one container that counts toward the bp-newapi Pod's effective
// limits.cpu.
type cpuTerm struct {
	container string // the container name as it appears in the rendered Pod
	block     string // the top-level values key that sizes it
	enabledAt string // the values path that gates whether it renders ("" = always)
	native    bool   // true = rendered as a native sidecar (initContainers + restartPolicy: Always)
}

// newapiPodCPUTerms is the Pod's container inventory, mirroring
// platform/newapi/chart/templates/deployment.yaml. Adding a container to that
// template without adding it here leaves the same 700m hole #6324 closed, so
// TestNewAPIPodCPU_6324_TermsCoverEveryChartContainer cross-checks the list
// against the template rather than trusting it.
var newapiPodCPUTerms = []cpuTerm{
	{container: "sandbox-bridge", block: "sandboxBridge", enabledAt: "sandboxBridge.enabled", native: true},
	{container: "newapi", block: "newapi", enabledAt: ""},
	{container: "metering-sidecar", block: "meteringSidecar", enabledAt: "meteringSidecar.enabled"},
}

// podCPU is a resolved Pod-level CPU model: the per-term breakdown plus the
// total, so a failure message can name the term that moved.
type podCPU struct {
	total     int
	byTerm    map[string]int
	fromChart map[string]bool // true = the term took the chart default (overlay did not pin it)
}

// resolveTermCPU returns one term's limits.cpu in millicores, taking the
// overlay's pin where it has one and the chart default otherwise — which is
// exactly what Helm does, and exactly what the single-container model failed to
// do for the two terms the overlay never mentions.
func resolveTermCPU(t *testing.T, overlay, chart string, term cpuTerm) (millis int, fromChart bool) {
	t.Helper()
	path := term.block + ".resources.limits.cpu"
	if v, ok := yamlScalar(overlay, path); ok {
		m, err := cpuToMillis(v)
		if err != nil {
			t.Fatalf("overlay %s = %q is not a parseable CPU quantity: %v", path, v, err)
		}
		return m, false
	}
	v, ok := yamlScalar(chart, path)
	if !ok {
		t.Fatalf("neither the per-Org overlay nor platform/newapi/chart/values.yaml resolves %q. "+
			"The chart was restructured and this guard's Pod arithmetic no longer covers the %q "+
			"container. RE-POINT the path — do NOT drop the term; a dropped term is #6324.",
			path, term.container)
	}
	m, err := cpuToMillis(v)
	if err != nil {
		t.Fatalf("chart %s = %q is not a parseable CPU quantity: %v", path, v, err)
	}
	return m, true
}

// newapiPodCPUMillis models the bp-newapi Pod the way a ResourceQuota admits
// it: every regular container plus every native sidecar that renders.
func newapiPodCPUMillis(t *testing.T, overlay string) podCPU {
	t.Helper()
	chart := newapiChartValues(t)
	out := podCPU{byTerm: map[string]int{}, fromChart: map[string]bool{}}
	for _, term := range newapiPodCPUTerms {
		if term.enabledAt != "" && !termEnabled(t, overlay, chart, term) {
			continue
		}
		m, fromChart := resolveTermCPU(t, overlay, chart, term)
		out.byTerm[term.container] = m
		out.fromChart[term.container] = fromChart
		out.total += m
	}
	return out
}

// termEnabled reads the term's `enabled` gate — overlay first, chart second. A
// gate that resolves to NEITHER is fatal rather than defaulted, because
// guessing "probably on" is how an arithmetic acquires a term it cannot see.
func termEnabled(t *testing.T, overlay, chart string, term cpuTerm) bool {
	t.Helper()
	if v, ok := yamlScalar(overlay, term.enabledAt); ok {
		return v == "true"
	}
	v, ok := yamlScalar(chart, term.enabledAt)
	if !ok {
		t.Fatalf("neither the per-Org overlay nor platform/newapi/chart/values.yaml resolves %q, "+
			"so this guard cannot tell whether the %q container renders. Re-point the gate.",
			term.enabledAt, term.container)
	}
	return v == "true"
}

// ─── Neighbours in the same Org namespace ────────────────────────────────

// openclawControllerPodCPUMillis derives the openclaw controller Pod's
// limits.cpu from platform/openclaw/chart/values.yaml. Neither per-Org producer
// overrides `controller.resources`, so the chart default is what lands in the
// Org namespace. Its Deployment renders ONE container
// (platform/openclaw/chart/templates/controller-deployment.yaml), so the Pod
// total equals that container.
func openclawControllerPodCPUMillis(t *testing.T) int {
	t.Helper()
	chart := readRepoFile(t, "platform", "openclaw", "chart", "values.yaml")
	v, ok := yamlScalar(chart, "controller.resources.limits.cpu")
	if !ok {
		t.Fatalf("platform/openclaw/chart/values.yaml has no controller.resources.limits.cpu — " +
			"re-point this term; the Org bundle arithmetic is incomplete without it")
	}
	m, err := cpuToMillis(v)
	if err != nil {
		t.Fatalf("openclaw controller.resources.limits.cpu = %q is not parseable: %v", v, err)
	}
	return m
}

// newapiCNPGCPUMillis derives the per-Org CNPG Postgres cost: the per-instance
// limits.cpu times the instance count. The instance count is a term in its own
// right — a chart that moved to `instances: 2` would double this silently, and
// the operator-injected bootstrap-controller init container inherits the same
// resources block, so a single instance's Pod total equals one instance's
// limit.
func newapiCNPGCPUMillis(t *testing.T) int {
	t.Helper()
	chart := newapiChartValues(t)
	cpu, ok := yamlScalar(chart, "cnpg.cluster.resources.limits.cpu")
	if !ok {
		t.Fatalf("platform/newapi/chart/values.yaml has no cnpg.cluster.resources.limits.cpu — re-point this term")
	}
	m, err := cpuToMillis(cpu)
	if err != nil {
		t.Fatalf("cnpg.cluster.resources.limits.cpu = %q is not parseable: %v", cpu, err)
	}
	inst, ok := yamlScalar(chart, "cnpg.cluster.instances")
	if !ok {
		t.Fatalf("platform/newapi/chart/values.yaml has no cnpg.cluster.instances — re-point this term")
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(inst), "%d", &n); err != nil || n < 1 {
		t.Fatalf("cnpg.cluster.instances = %q is not a positive integer (%v)", inst, err)
	}
	return m * n
}

// smallestPlanCPUFromController derives the plan-"s" figures from the ONE table
// the org-controller drives the ResourceQuota off — planQuotaTable in
// core/controllers/organization/internal/gitops/manifests.go. planQuota()
// resolves an empty or unknown plan slug to "s", so this is what a fresh funnel
// Org gets. Since #6971 the row carries two CPU figures: CPULimit, the package
// HEADLINE (the ResourceQuota's limits.cpu plan term — what the customer may
// burst to and pays for; S is 1 vCPU, NC-OO-Pricing.xlsx 2026-06-28), and
// CPURequest, the GUARANTEED share the Sovereign provisions for it (the
// requests.cpu plan term, headline ÷ 6). Restating either as a local constant
// would let a LOWERED cap pass this guard unnoticed.
func smallestPlanCPUFromController(t *testing.T) (limitMillis, requestMillis int) {
	t.Helper()
	src := readRepoFile(t, "core", "controllers", "organization", "internal", "gitops", "manifests.go")
	re := regexp.MustCompile(`"s":\s*\{Slug:\s*"s",\s*CPULimit:\s*"([^"]*)",\s*MemLimit:\s*"[^"]*",\s*CPURequest:\s*"([^"]*)"`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf(`planQuotaTable["s"] not found in ` +
			`core/controllers/organization/internal/gitops/manifests.go — the plan table was ` +
			`restructured and this guard can no longer see the cap it asserts against. Re-point it.`)
	}
	limitMillis, err := cpuToMillis(m[1])
	if err != nil {
		t.Fatalf(`planQuotaTable["s"].CPULimit = %q is not a parseable CPU quantity: %v`, m[1], err)
	}
	requestMillis, err = cpuToMillis(m[2])
	if err != nil {
		t.Fatalf(`planQuotaTable["s"].CPURequest = %q is not a parseable CPU quantity: %v`, m[2], err)
	}
	if requestMillis <= 0 || requestMillis >= limitMillis {
		t.Fatalf(`planQuotaTable["s"]: CPURequest %dm is not strictly between 0 and CPULimit %dm — `+
			`the #6971 overcommit model has the guaranteed share under the headline`, requestMillis, limitMillis)
	}
	return limitMillis, requestMillis
}

// smallestPlanCPUMillisFromController is the plan-"s" HEADLINE — the limits.cpu
// plan term of the ResourceQuota, the ceiling the row-232 assertions compare
// the overlay's own limit against.
func smallestPlanCPUMillisFromController(t *testing.T) int {
	t.Helper()
	limit, _ := smallestPlanCPUFromController(t)
	return limit
}

// platformStackLimitsCPUFromController reads, out of the org-controller's
// platformStack table (manifests.go), the limits.cpu the ResourceQuota adds ON
// TOP of the plan for the named pod: the sum of its app containers' LimitsCPU
// — the pod-usage rule, where the plain init containers are the smaller branch
// for every pod modelled here, exactly as newapiPodCPUMillis leaves
// wait-for-sql-dsn out. Since the #6902 follow-ups THIS term, not the plan, is
// what admits the per-Organization platform pods; the controller's own pin test
// (plan_quota_plus_platform_stack_test.go) re-reads the chart and door sources
// it is derived from, and this reader lets each door check it is one of them.
func platformStackLimitsCPUFromController(t *testing.T, podName string) (millis, containers int) {
	t.Helper()
	src := readRepoFile(t, "core", "controllers", "organization", "internal", "gitops", "manifests.go")
	const open = "var platformStack = []platformStackWorkload{"
	start := strings.Index(src, open)
	if start < 0 {
		t.Fatalf("platformStack table not found in manifests.go — the per-Organization platform-stack " +
			"overhead was restructured and this guard can no longer see the term that admits the pod. Re-point it.")
	}
	block := src[start+len(open):]
	if end := strings.Index(block, "\n}\n"); end > 0 {
		block = block[:end]
	}
	tag := `Name: "` + podName + `"`
	i := strings.Index(block, tag)
	if i < 0 {
		t.Fatalf("platformStack has no entry %s — the pod this door renders is not carried as quota "+
			"overhead, so it is charged to the customer's plan: the #6324 refusal one layer up", tag)
	}
	entry := block[i+len(tag):]
	if j := strings.Index(entry, `Name: "`); j >= 0 {
		entry = entry[:j]
	}
	if j := strings.Index(entry, "Inits:"); j >= 0 {
		entry = entry[:j]
	}
	re := regexp.MustCompile(`LimitsCPU:\s*"([^"]*)"`)
	for _, m := range re.FindAllStringSubmatch(entry, -1) {
		n, err := cpuToMillis(m[1])
		if err != nil {
			t.Fatalf("platformStack %s: LimitsCPU %q is not a parseable CPU quantity: %v", tag, m[1], err)
		}
		millis += n
		containers++
	}
	if containers == 0 || millis <= 0 {
		t.Fatalf("platformStack %s: no app-container LimitsCPU parsed — the reader missed and the "+
			"lockstep below would compare against zero", tag)
	}
	return millis, containers
}

// assertOrgBundleCarriedAboveThePlan is the headroom statement row 232 / #6324
// turn on, re-made for the #6971 package numbers, and shared by the row-232 and
// 6324 guards so the two cannot drift apart.
//
// THE FINDING. The per-Organization bundle rendered beside the customer's
// first app — the bp-newapi Pod (1200m), the openclaw controller (250m) and
// newapi's CNPG (500m) — is 1950m of limits. The smallest package is 1 vCPU
// (planQuotaTable["s"].CPULimit; NC-OO-Pricing.xlsx 2026-06-28). The bundle
// therefore does NOT fit the plan the customer bought — nor did it leave more
// than 50m of the old 2 vCPU row. It is admitted ONLY because the
// org-controller sizes the ResourceQuota as plan + vCluster control plane +
// per-Organization platform stack (#6902 follow-ups), and the stack term is
// these very pods, carried as the Sovereign's overhead on top of the plan: the
// S quota renders limits.cpu 7050m = 1000m plan + 1500m control plane + 4550m
// stack, and the customer's catalog applications have the 1000m. Under the
// plan alone the Pod is refused at admission — the hw296 failure verbatim.
//
// Both halves are asserted. (1) The finding is pinned as stated, so a plan
// that grows past the bundle, or a bundle that shrinks under the plan, forces
// it to be re-made rather than silently inverting. (2) The invariant that
// makes the quota admit the bundle: the controller's platformStack carries each
// pod at EXACTLY the limits this door renders. A door that moves a limit
// without the table moving puts the difference back inside the customer's plan
// — #6324 again, one layer up — and the controller's own pin test fails from
// the other side.
func assertOrgBundleCarriedAboveThePlan(t *testing.T, pod podCPU, openclaw, cnpg int) {
	t.Helper()
	headline, guaranteed := smallestPlanCPUFromController(t)
	bundle := pod.total + openclaw + cnpg

	if bundle <= headline {
		t.Fatalf("the Org bundle (%dm) now FITS the smallest plan headline (%dm). Since #6971 this "+
			"guard states the opposite — the bundle rides on the quota's platform-stack term, not the "+
			"plan. Re-make the statement (and the pins below) before changing it:\n%s", bundle, headline, pod)
	}
	const wantBundle, wantHeadline, wantGuaranteed = 1950, 1000, 167
	if bundle != wantBundle || headline != wantHeadline || guaranteed != wantGuaranteed {
		t.Fatalf("the Org bundle totals %dm against a %dm headline / %dm guaranteed plan; this guard was "+
			"written against %dm / %dm / %dm, measured from the same files. A term moved — name it and "+
			"re-derive:\n  bp-newapi POD  %dm\n  openclaw ctrl  %dm\n  newapi CNPG    %dm\n%s",
			bundle, headline, guaranteed, wantBundle, wantHeadline, wantGuaranteed, pod.total, openclaw, cnpg, pod)
	}

	for _, tc := range []struct {
		name string
		door int
	}{
		{"bp-newapi-<hash>", pod.total},
		{"bp-newapi-newapi-pg-1", cnpg},
		{"bp-openclaw-<hash>", openclaw},
	} {
		carried, containers := platformStackLimitsCPUFromController(t, tc.name)
		if carried != tc.door {
			t.Fatalf("%s: this door renders %dm of limits.cpu but the org-controller's platformStack "+
				"carries %dm as quota overhead — the %dm difference is charged to the customer's %dm "+
				"plan instead (the #6324 refusal, one layer up). Move the two in lockstep.\n%s",
				tc.name, tc.door, carried, tc.door-carried, headline, pod)
		}
		if tc.name == "bp-newapi-<hash>" && containers < 2 {
			t.Fatalf("VACUITY: platformStack models bp-newapi-<hash> as %d container(s) — the Pod has "+
				"sidecars, and a one-container table would be the exact model #6324 replaced", containers)
		}
	}
}

// ─── The assertions ──────────────────────────────────────────────────────

// funnelOverlay renders the funnel producer's values block, which is the thing
// under test on this door.
func funnelOverlay(t *testing.T) string {
	t.Helper()
	return hrValuesBlock(t, generateNewAPIHR(helmReleaseAppOpts{slug: "acme", parentDomain: "omani.homes"}))
}

// liveAdmissionPodCPUMillis is the quantity the hw296 admission controller
// itself reported for this Pod (`requested: limits.cpu=1200m`). It is pinned
// rather than derived ON PURPOSE: it is the independent observation this
// guard's derived arithmetic is checked against.
const liveAdmissionPodCPUMillis = 1200

// TestNewAPIPodCPU_6324_PodIsTheModelledUnit is the RED test for #6324: the
// per-Org bp-newapi Pod costs the sum of its quota-counted containers, not the
// one container the overlay pins.
func TestNewAPIPodCPU_6324_PodIsTheModelledUnit(t *testing.T) {
	pod := newapiPodCPUMillis(t, funnelOverlay(t))

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

	// The whole point: the Pod is strictly more than the container the overlay
	// pins. A regression to the single-container model fails HERE.
	container, ok := pod.byTerm["newapi"]
	if !ok {
		t.Fatalf("the `newapi` container is absent from the Pod model:\n%s", pod)
	}
	if pod.total <= container {
		t.Fatalf("VACUITY: Pod total %dm is not greater than the `newapi` container alone (%dm), "+
			"so this guard would pass on the exact single-container model #6324 replaces:\n%s",
			pod.total, container, pod)
	}

	// Guard-vs-cluster. The hw296 refusal read `requested: limits.cpu=1200m`.
	if pod.total != liveAdmissionPodCPUMillis {
		t.Fatalf("Pod total = %dm, but the live hw296 admission refusal for walkthree/bp-newapi "+
			"read `requested: limits.cpu=%dm`. The guard and the cluster now disagree about what "+
			"this Pod costs — reconcile them (and re-check #5393's plan sizing) before changing "+
			"this number:\n%s", pod.total, liveAdmissionPodCPUMillis, pod)
	}
}

// TestNewAPIPodCPU_6324_TermsCoverEveryChartContainer cross-checks the term
// list against templates/deployment.yaml. A container added to the template and
// not to newapiPodCPUTerms would re-open #6324 exactly as sandbox-bridge did,
// and no assertion above could see it — the sum would simply be smaller.
func TestNewAPIPodCPU_6324_TermsCoverEveryChartContainer(t *testing.T) {
	tmpl := readRepoFile(t, "platform", "newapi", "chart", "templates", "deployment.yaml")
	rendered := deploymentContainerNames(t, tmpl)
	if len(rendered) == 0 {
		t.Fatalf("VACUITY: parsed ZERO container names out of templates/deployment.yaml — the " +
			"scanner missed, so this cross-check would pass on any term list at all")
	}

	// wait-for-sql-dsn is a PLAIN initContainer and is excluded by design; see
	// TestNewAPIPodCPU_6324_PlainInitDoesNotDominate for the proof that the
	// exclusion is safe rather than assumed.
	const plainInit = "wait-for-sql-dsn"

	modelled := map[string]bool{}
	for _, term := range newapiPodCPUTerms {
		modelled[term.container] = true
	}

	// BOTH DIRECTIONS. The check below (rendered ⊆ modelled ∪ {plainInit}) is
	// satisfied vacuously by a scanner that found only ONE container, so assert
	// the other inclusion first: every term this file models must actually
	// appear in the template. That is what makes the len()>0 check above more
	// than a formality.
	renderedSet := map[string]bool{}
	for _, name := range rendered {
		renderedSet[name] = true
	}
	for _, term := range newapiPodCPUTerms {
		if !renderedSet[term.container] {
			t.Fatalf("newapiPodCPUTerms models a container %q that the scanner did not find in "+
				"templates/deployment.yaml. Either the chart dropped it — in which case the Pod "+
				"total is now overstated — or this scanner stopped matching, in which case the "+
				"coverage check below is vacuous.\nrendered: %v", term.container, rendered)
		}
	}
	if !renderedSet[plainInit] {
		t.Fatalf("the scanner did not find the %q plain initContainer, which "+
			"TestNewAPIPodCPU_6324_PlainInitDoesNotDominate depends on being present.\nrendered: %v",
			plainInit, rendered)
	}

	for _, name := range rendered {
		if name == plainInit || modelled[name] {
			continue
		}
		t.Fatalf("templates/deployment.yaml renders a container %q that newapiPodCPUTerms does "+
			"not model. Every regular container and every native sidecar counts toward the Pod's "+
			"limits.cpu, so an unmodelled one understates the quota cost — that is #6324. Add the "+
			"term (or, if it is a PLAIN initContainer, extend the exclusion WITH a proof that it "+
			"cannot dominate).\nrendered: %v", name, rendered)
	}
}

// TestNewAPIPodCPU_6324_PlainInitDoesNotDominate proves the one term this
// arithmetic deliberately leaves OUT is safe to leave out. A Pod's effective
// limit is the max of the container branch and the plain-init branch, and the
// DSN gate only stops being free if its branch overtakes the container sum.
func TestNewAPIPodCPU_6324_PlainInitDoesNotDominate(t *testing.T) {
	tmpl := readRepoFile(t, "platform", "newapi", "chart", "templates", "deployment.yaml")
	gate := initContainerCPULimit(t, tmpl, "wait-for-sql-dsn")
	if gate <= 0 {
		t.Fatalf("VACUITY: wait-for-sql-dsn limits.cpu resolved to %dm — the scanner missed, and a "+
			"zero here would make the comparison below unfalsifiable", gate)
	}

	pod := newapiPodCPUMillis(t, funnelOverlay(t))
	// Native sidecars declared BEFORE a plain init are held for its whole run,
	// so they join its branch. sandbox-bridge is declared first (#3374).
	bridge := pod.byTerm["sandbox-bridge"]
	initBranch := gate + bridge
	if initBranch >= pod.total {
		t.Fatalf("the plain-init branch (wait-for-sql-dsn %dm + sandbox-bridge %dm held across it "+
			"= %dm) now MEETS OR EXCEEDS the container branch (%dm), so the Pod's effective "+
			"limits.cpu is no longer the container sum and this guard's arithmetic is "+
			"incomplete:\n%s", gate, bridge, initBranch, pod.total, pod)
	}
}

// TestNewAPIPodCPU_6324_OrgBundleIsCarriedAboveThePlanByTheQuota is the
// headroom statement the row actually turns on, at Pod granularity, with every
// term derived from the file the platform reads — re-made for the #6971
// package numbers: see assertOrgBundleCarriedAboveThePlan.
func TestNewAPIPodCPU_6324_OrgBundleIsCarriedAboveThePlanByTheQuota(t *testing.T) {
	pod := newapiPodCPUMillis(t, funnelOverlay(t))
	openclaw := openclawControllerPodCPUMillis(t)
	cnpg := newapiCNPGCPUMillis(t)
	assertOrgBundleCarriedAboveThePlan(t, pod, openclaw, cnpg)
}

// TestNewAPIPodCPU_6324_VacuityCheck_TheOldModelWouldHavePassed is the proof
// that this whole file is load-bearing. It reconstructs the PRE-FIX
// single-container arithmetic from the same derived terms and shows it
// understates the bundle by the sidecar cost (700m): against the old 2 vCPU
// row it reported 750m of headroom where the truth was 50m — the old guard was
// green over a Pod the cluster was refusing. Against the #6971 1 vCPU headline
// both models are short (−250m and −950m), and the 700m gap between them is
// the same sidecar cost. If a future edit collapses the Pod model back to one
// container, the two numbers converge and this test fails.
func TestNewAPIPodCPU_6324_VacuityCheck_TheOldModelWouldHavePassed(t *testing.T) {
	pod := newapiPodCPUMillis(t, funnelOverlay(t))
	openclaw := openclawControllerPodCPUMillis(t)
	cnpg := newapiCNPGCPUMillis(t)
	headline := smallestPlanCPUMillisFromController(t)

	container := pod.byTerm["newapi"]
	oldHeadroom := headline - (container + openclaw + cnpg)
	newHeadroom := headline - (pod.total + openclaw + cnpg)

	if oldHeadroom == newHeadroom {
		t.Fatalf("VACUITY: the single-container model and the Pod model report the SAME headroom "+
			"(%dm). Either the sidecar terms have been dropped, or they resolved to zero — in "+
			"both cases this file no longer models what #6324 is about:\n%s", oldHeadroom, pod)
	}
	if oldHeadroom <= newHeadroom {
		t.Fatalf("VACUITY: the single-container model reports LESS headroom (%dm) than the Pod "+
			"model (%dm), which is arithmetically impossible while the sidecars cost anything:\n%s",
			oldHeadroom, newHeadroom, pod)
	}
	if gap := oldHeadroom - newHeadroom; gap != pod.total-container {
		t.Fatalf("the understatement (%dm) is not the sidecar cost (%dm) — the arithmetic lost a term:\n%s",
			gap, pod.total-container, pod)
	}
	const wantOld, wantNew = -250, -950
	if oldHeadroom != wantOld || newHeadroom != wantNew {
		t.Fatalf("the understatement changed: against the smallest plan headline the single-container "+
			"model now reports %dm of headroom and the Pod model %dm (this file was written against "+
			"%dm vs %dm). Re-derive and restate the defect before adjusting these:\n%s",
			oldHeadroom, newHeadroom, wantOld, wantNew, pod)
	}
}

// ─── Template scanners ───────────────────────────────────────────────────

// containerNameRe matches a `- name: <x>` list entry at the indentation
// templates/deployment.yaml uses for containers and initContainers entries
// (8 spaces), which is deeper than the `- name:` entries used for volumes and
// env vars only in that those sit at other depths — hence the anchored indent.
var containerNameRe = regexp.MustCompile(`(?m)^        - name: ([a-z0-9][a-z0-9-]*)\s*$`)

// deploymentContainerNames returns every container/initContainer name declared
// in templates/deployment.yaml. It reads the TEMPLATE rather than a render
// because the render needs Helm; the names are literal in the template, and
// TestNewAPIPodCPU_6324_TermsCoverEveryChartContainer refuses a zero-length
// result so a scanner that stopped matching cannot report a pass.
func deploymentContainerNames(t *testing.T, tmpl string) []string {
	t.Helper()
	// Restrict to the Pod spec: everything from `initContainers:` (or
	// `containers:`) up to the `volumes:` block, so volume entries — which share
	// the `- name:` shape at other depths — cannot be mistaken for containers.
	start := strings.Index(tmpl, "\n      initContainers:")
	if start < 0 {
		start = strings.Index(tmpl, "\n      containers:")
	}
	if start < 0 {
		t.Fatalf("templates/deployment.yaml has neither an initContainers: nor a containers: block " +
			"at the expected depth — the template was restructured; re-point this scanner")
	}
	end := strings.Index(tmpl[start:], "\n      volumes:")
	if end < 0 {
		t.Fatalf("templates/deployment.yaml has no volumes: block after the container lists — the " +
			"scanner cannot bound the Pod spec; re-point it")
	}
	spec := tmpl[start : start+end]

	var out []string
	seen := map[string]bool{}
	for _, m := range containerNameRe.FindAllStringSubmatch(spec, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// initContainerCPULimit pulls a literal `resources.limits.cpu` out of one named
// container block in templates/deployment.yaml. The DSN gate's resources are
// written inline in the template rather than in values.yaml, so this is the
// only place that number exists.
func initContainerCPULimit(t *testing.T, tmpl, name string) int {
	t.Helper()
	marker := "- name: " + name + "\n"
	i := strings.Index(tmpl, marker)
	if i < 0 {
		t.Fatalf("templates/deployment.yaml declares no container %q — re-point this scanner; do "+
			"NOT drop the check", name)
	}
	block := tmpl[i+len(marker):]
	// Stop at the next sibling list entry so a later container's numbers cannot
	// be attributed to this one — the same mis-attribution class #6324 is about.
	if j := strings.Index(block, "\n        - name: "); j >= 0 {
		block = block[:j]
	}
	re := regexp.MustCompile(`(?s)limits:\s*\n\s*cpu:\s*(\S+)`)
	m := re.FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("container %q in templates/deployment.yaml has no literal resources.limits.cpu — "+
			"if it moved to values.yaml, re-point this lookup; a missing term must never read as "+
			"zero", name)
	}
	v, err := cpuToMillis(m[1])
	if err != nil {
		t.Fatalf("container %q limits.cpu = %q is not parseable: %v", name, m[1], err)
	}
	return v
}

// String renders the per-term breakdown for failure messages, so a red test
// names the term that moved instead of only the total.
func (p podCPU) String() string {
	var b strings.Builder
	b.WriteString("  bp-newapi Pod limits.cpu breakdown:\n")
	for _, term := range newapiPodCPUTerms {
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
