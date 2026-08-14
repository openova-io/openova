package handler

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// UAT row 232, BSS-door half — see the companion guard at
// core/services/provisioning/gitops/newapi_plan_quota_fit_row232_test.go for
// the full arithmetic.
//
// There are TWO producers of a per-Org bp-newapi release: the funnel generator
// (generateNewAPIHR) and this BSS door (orgTenantBPNewAPI). bp-newapi's chart
// default is `limits.cpu: 2` (platform/newapi/chart/values.yaml:153) while plan
// "s" grants `limits.cpu "2"` for the WHOLE Organization
// (core/controllers/organization/internal/gitops/manifests.go:121). A
// ResourceQuota counts LIMITS, so an un-overridden release reserves the entire
// Org cap and every pod rendered beside it — the openclaw controller included —
// is refused at admission.
//
// This guard exists specifically so the two doors cannot drift apart again. A
// fix landed on one producer and not the other is the shape that let UAT row 15
// render three of seven Org chips for a month while its label-only test stayed
// green.

// cpuMillisFromYAML parses the milli ("500m") and bare-core ("2", "0.5")
// spellings so an assertion cannot be fooled by a numerically-equal value
// written differently.
func cpuMillisFromYAML(v string) (int, bool) {
	s := strings.TrimSpace(v)
	if s == "" {
		return 0, false
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "m"))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int(f * 1000), true
}

// smallestPlanCPUMillisBSS mirrors planQuotaTable["s"].CPU ("2").
const smallestPlanCPUMillisBSS = 2000

func TestOrgTenantBPNewAPI_Row232_PinsCPUInsideThePlanQuota(t *testing.T) {
	tmpl := orgTenantBPNewAPI

	// Find the newapi.resources block and read BOTH cpu values out of it.
	// Assert on the VALUES: a guard that merely checked the substring
	// "resources" appears would pass on the chart default that caused the row.
	reqRe := regexp.MustCompile(`(?s)newapi:\s*\n\s*resources:\s*\n\s*requests:\s*\n\s*cpu:\s*(\S+)`)
	limRe := regexp.MustCompile(`(?s)newapi:\s*\n\s*resources:.*?limits:\s*\n\s*cpu:\s*(\S+)`)

	reqM := reqRe.FindStringSubmatch(tmpl)
	if reqM == nil {
		t.Fatalf("orgTenantBPNewAPI does not pin newapi.resources.requests.cpu, so the " +
			"chart default (limits.cpu: 2 = the whole smallest-plan quota) applies")
	}
	limM := limRe.FindStringSubmatch(tmpl)
	if limM == nil {
		t.Fatalf("orgTenantBPNewAPI does not pin newapi.resources.limits.cpu, so the " +
			"chart default (limits.cpu: 2 = the whole smallest-plan quota) applies")
	}

	reqMillis, ok := cpuMillisFromYAML(reqM[1])
	if !ok {
		t.Fatalf("requests.cpu = %q is not a parseable CPU quantity", reqM[1])
	}
	limMillis, ok := cpuMillisFromYAML(limM[1])
	if !ok {
		t.Fatalf("limits.cpu = %q is not a parseable CPU quantity", limM[1])
	}

	if limMillis >= smallestPlanCPUMillisBSS {
		t.Fatalf("newapi limits.cpu = %q (%dm) consumes the whole smallest-plan cap (%dm)",
			limM[1], limMillis, smallestPlanCPUMillisBSS)
	}
	if reqMillis != limMillis {
		t.Fatalf("requests.cpu %q (%dm) != limits.cpu %q (%dm); inside an Org ResourceQuota "+
			"these must match so the reservation is honest",
			reqM[1], reqMillis, limM[1], limMillis)
	}
	// Headroom for the workloads rendered alongside, at POD granularity
	// (#6324). It sums the whole bp-newapi Pod — the container pinned above
	// PLUS the two chart-default containers this overlay never mentions —
	// because a ResourceQuota admits a Pod, not a container. Summing only
	// `limMillis` here understated the cost by 700m and left this guard green
	// while the live cluster refused the Pod. Terms derived in
	// orgtenant_newapi_pod_quota_arithmetic_6324_test.go.
	pod := newapiPodCPUMillisBSS(t)
	openclaw := openclawControllerPodCPUMillisBSS(t)
	cnpg := newapiCNPGCPUMillisBSS(t)
	if pod.total+openclaw+cnpg > smallestPlanCPUMillisBSS {
		t.Fatalf("bp-newapi POD %dm + openclaw controller %dm + newapi CNPG %dm = %dm exceeds "+
			"the %dm cap.\n(the POD costs %dm; this overlay pins only %dm, on the `newapi` "+
			"container)\n%s",
			pod.total, openclaw, cnpg, pod.total+openclaw+cnpg, smallestPlanCPUMillisBSS,
			pod.total, limMillis, pod)
	}
}

// TestOrgTenantBPNewAPI_Row232_VacuityCheck proves the regexes above CAN fail:
// run them against a values block carrying the CHART DEFAULT and confirm the
// limit is rejected. Without this, a regex that silently never matched would
// make the guard above unfalsifiable.
func TestOrgTenantBPNewAPI_Row232_VacuityCheck(t *testing.T) {
	const chartDefault = "" +
		"    newapi:\n" +
		"      resources:\n" +
		"        requests:\n" +
		"          cpu: 100m\n" +
		"        limits:\n" +
		"          cpu: 2\n"

	limRe := regexp.MustCompile(`(?s)newapi:\s*\n\s*resources:.*?limits:\s*\n\s*cpu:\s*(\S+)`)
	m := limRe.FindStringSubmatch(chartDefault)
	if m == nil {
		t.Fatalf("VACUITY: the limits regex does not match a well-formed block — the " +
			"real guard could be passing because it never matched anything")
	}
	millis, ok := cpuMillisFromYAML(m[1])
	if !ok || millis != 2000 {
		t.Fatalf("VACUITY: chart default parsed as %dm (ok=%v), want 2000m", millis, ok)
	}
	if millis < smallestPlanCPUMillisBSS {
		t.Fatalf("VACUITY: the chart default must NOT satisfy the guard's ceiling")
	}
}
