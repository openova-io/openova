package gitops

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// cpuToMillis parses the two Kubernetes CPU spellings that matter here — the
// milli form ("500m") and the bare-core form ("2", "0.5") — into millicores, so
// an assertion cannot be fooled by a value that is numerically right and
// textually different.
func cpuToMillis(v string) (int, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return 0, fmt.Errorf("empty CPU quantity")
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "m"))
		if err != nil {
			return 0, fmt.Errorf("parse %q: %w", v, err)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", v, err)
	}
	return int(f * 1000), nil
}

// UAT row 232 — a fresh funnel Org could not schedule openclaw, because the
// bp-newapi HelmRelease rendered beside it claims the ENTIRE smallest-plan CPU
// quota on its own.
//
// THE ARITHMETIC, and it comes down to one line.
//
//	platform/newapi/chart/values.yaml:153   newapi.resources.limits.cpu: 2
//	                              :150      newapi.resources.requests.cpu: 100m
//	core/controllers/.../gitops/manifests.go:121  planQuotaTable["s"].CPU = "2"
//	                                        :499  rendered as hard["limits.cpu"]
//
// A ResourceQuota counts LIMITS, not requests. So one bp-newapi app container
// reserves 2000m of a 2000m cap before openclaw's own controller (250m,
// platform/openclaw/chart/values.yaml:79-85) or bp-newapi's own CNPG (500m,
// platform/newapi/chart/values.yaml:340-346) ask for anything. Every later pod
// is refused at admission, and a User sees only an opaque Helm
// `context deadline exceeded`.
//
// It became a fresh-Org regression in two merged steps, neither wrong alone:
// 70f6b07aa (#5969) added `"openclaw": {"newapi"}` to impliedHelmReleaseApps
// (helmrelease_apps.go:113), so EVERY openclaw Org renders this HR; then
// 93d824ea4 (#5987) made that HR actually installable. Before the pair, the
// 2000m never landed in an Org namespace at all.
//
// WHY THE FIX IS HERE AND NOT IN THE CHART. The 2-CPU ceiling is a defensible
// default for the SOVEREIGN-level install (bootstrap-kit slot 80), which runs
// in no per-Org ResourceQuota. It is only wrong inside an Org boundary, so the
// per-Org generators override it and the chart default is left alone — no chart
// bump, no five-site lockstep.
//
// NOT ASSERTED HERE, deliberately: `maxLimitRequestRatio`. #4758 REMOVED that
// ratio from the vcluster-Org host-namespace LimitRange
// (core/controllers/organization/internal/gitops/manifests.go:530-543) because
// the vcluster syncer's own pods can never satisfy it. A guard asserting a
// ratio violation would be asserting a rule the platform does not have.

// smallestPlanCPUMillis mirrors planQuotaTable["s"].CPU ("2") from
// core/controllers/organization/internal/gitops/manifests.go:121, which is the
// cap an Org gets when its plan slug is empty or unknown (planQuota(), :132).
const smallestPlanCPUMillis = 2000

// TestNewAPIHR_Row232_FitsSmallestPlanQuota is the RED test: the per-Org
// bp-newapi HelmRelease must pin its own CPU limit, and that limit must leave
// room in the smallest plan for the workloads rendered ALONGSIDE it.
func TestNewAPIHR_Row232_FitsSmallestPlanQuota(t *testing.T) {
	cases := []struct {
		name string
		opt  helmReleaseAppOpts
	}{
		{"host-tier", helmReleaseAppOpts{slug: "acme", parentDomain: "omani.homes"}},
		{"vcluster-tier", helmReleaseAppOpts{slug: "acme", parentDomain: "omani.homes", kubeSecret: "tenant-acme-kubeconfig"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := hrValuesBlock(t, generateNewAPIHR(tc.opt))

			limit, ok := yamlScalar(values, "newapi.resources.limits.cpu")
			if !ok {
				t.Fatalf("newapi.resources.limits.cpu is UNSET, so the chart default "+
					"(limits.cpu: 2 at platform/newapi/chart/values.yaml:153) applies — "+
					"that is %dm, the ENTIRE smallest-plan cap, leaving nothing for the "+
					"openclaw controller this HR is rendered beside.\n\nrendered values:\n%s",
					smallestPlanCPUMillis, values)
			}

			limitMillis, err := cpuToMillis(limit)
			if err != nil {
				t.Fatalf("newapi.resources.limits.cpu = %q is not a parseable CPU quantity: %v", limit, err)
			}

			// The VALUE, not merely the key. A guard that stopped at "the key
			// is set" would pass on `limits.cpu: 2` — the exact defect.
			if limitMillis >= smallestPlanCPUMillis {
				t.Fatalf("newapi.resources.limits.cpu = %q (%dm) consumes the whole "+
					"smallest-plan quota (%dm). Nothing else in the Org can be admitted.",
					limit, limitMillis, smallestPlanCPUMillis)
			}

			// requests == limits: the Org boundary sizes on limits, so a
			// request far below the limit lets a pod be admitted and then
			// starve its neighbours out of the quota.
			req, ok := yamlScalar(values, "newapi.resources.requests.cpu")
			if !ok {
				t.Fatalf("newapi.resources.requests.cpu is UNSET while limits.cpu is pinned")
			}
			reqMillis, err := cpuToMillis(req)
			if err != nil {
				t.Fatalf("newapi.resources.requests.cpu = %q is not parseable: %v", req, err)
			}
			if reqMillis != limitMillis {
				t.Fatalf("newapi.resources: requests.cpu=%q (%dm) != limits.cpu=%q (%dm); "+
					"inside an Org ResourceQuota these must match so the reservation is honest",
					req, reqMillis, limit, limitMillis)
			}

			// The headroom assertion the row actually turns on: openclaw's own
			// controller (250m) plus bp-newapi's CNPG (500m) must still fit.
			const openclawControllerMillis = 250
			const newapiCNPGMillis = 500
			if limitMillis+openclawControllerMillis+newapiCNPGMillis > smallestPlanCPUMillis {
				t.Fatalf("newapi %dm + openclaw controller %dm + newapi CNPG %dm = %dm "+
					"exceeds the smallest-plan cap %dm — row 232's pod is refused at admission",
					limitMillis, openclawControllerMillis, newapiCNPGMillis,
					limitMillis+openclawControllerMillis+newapiCNPGMillis, smallestPlanCPUMillis)
			}
		})
	}
}

// TestNewAPIHR_Row232_VacuityCheck_HelperSeesTheChartDefault proves the guard
// above CAN fail: it feeds the helper the chart's own default value and asserts
// the comparison rejects it. Without this, a `yamlScalar` that silently missed
// the key would make the guard pass on any input at all.
func TestNewAPIHR_Row232_VacuityCheck_HelperSeesTheChartDefault(t *testing.T) {
	// The chart default, verbatim from platform/newapi/chart/values.yaml:153.
	got, err := cpuToMillis("2")
	if err != nil {
		t.Fatalf("cpuToMillis(%q) errored: %v", "2", err)
	}
	if got != 2000 {
		t.Fatalf("cpuToMillis(\"2\") = %dm, want 2000m — the guard's arithmetic is wrong", got)
	}
	if got < smallestPlanCPUMillis {
		t.Fatalf("VACUITY: the chart default %dm must NOT pass the guard's headroom test", got)
	}
	// And the millis spelling must agree with the bare-core spelling, or the
	// guard could pass on "500m" while the CR carried "0.5".
	if m, err := cpuToMillis("500m"); err != nil || m != 500 {
		t.Fatalf("cpuToMillis(\"500m\") = %dm err=%v, want 500m", m, err)
	}
	if m, err := cpuToMillis("0.5"); err != nil || m != 500 {
		t.Fatalf("cpuToMillis(\"0.5\") = %dm err=%v, want 500m", m, err)
	}
}
