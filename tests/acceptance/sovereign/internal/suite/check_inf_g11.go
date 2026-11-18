package suite

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check INF-G11, Host vulnerability reports: after a scan run every node has a
// vulnerability report whose results name the database version, and the database is
// not older than 7 days.
//
// In each region the check reads, in namespace catalyst-infra, the Machines, the
// MachineRuns and the PolicyReports. It writes nothing. It fails when the region has no
// Machine of class node; when no MachineRun of kind scan of the policy nodes has the
// phase Succeeded (a run whose match Job failed is Failed); when a node has no
// PolicyReport machine-<node>-vulnerability; when a result of such a report has no
// property databaseVersion; or when its result of the rule database-age is missing or
// not pass, which the operations controller sets to warn once the database is older
// than 7 days.
const infG11ID = "INF-G11"

const (
	g11Namespace    = "catalyst-infra"
	g11Policy       = "nodes"
	g11Machines     = "machines.machines.openova.io"
	g11Runs         = "machineruns.machines.openova.io"
	g11Reports      = "policyreports.wgpolicyk8s.io"
	g11RuleAge      = "database-age"
	g11ClassNode    = "node"
	g11RunSucceeded = "Succeeded"
)

type vulnReportsCheck struct{}

func init() { RegisterCheck(vulnReportsCheck{}) }

func (vulnReportsCheck) ID() string { return infG11ID }

func (vulnReportsCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	for i, region := range cc.Regions {
		rec.Step(infG11ID+"."+strconv.Itoa(i+1), "every node has a vulnerability report on a database of at most 7 days in region "+region.ID, func(ev Evidence) error {
			lists := map[string][]kube.Object{}
			for _, resource := range []string{g11Machines, g11Runs, g11Reports} {
				items, err := region.GetList(ctx, resource, g11Namespace)
				if err != nil {
					return err
				}
				lists[resource] = items
			}
			return judgeVulnReports(lists[g11Machines], lists[g11Runs], lists[g11Reports], ev)
		})
	}
	return nil
}

// judgeVulnReports applies the rules of INF-G11 to the objects of one region.
func judgeVulnReports(machines, runs, reports []kube.Object, ev Evidence) error {
	var nodes []string
	for _, m := range machines {
		if m.Str("spec", "class") == g11ClassNode {
			nodes = append(nodes, m.Name())
		}
	}
	sort.Strings(nodes)
	ev["nodes"] = nodes
	if len(nodes) == 0 {
		return fmt.Errorf("no Machine of class node in %s", g11Namespace)
	}
	var newest time.Time
	for _, r := range runs {
		finished, err := time.Parse(time.RFC3339, r.Str("status", "finishedAt"))
		if r.Str("spec", "kind") != "scan" || r.Str("spec", "policyRef", "name") != g11Policy || r.Str("status", "phase") != g11RunSucceeded || err != nil {
			continue
		}
		if finished.After(newest) {
			newest, ev["scan_run"] = finished, r.Name()
		}
	}
	if newest.IsZero() {
		return fmt.Errorf("no MachineRun of kind scan of the policy %s has the phase Succeeded in %s", g11Policy, g11Namespace)
	}
	byName := map[string]kube.Object{}
	for _, r := range reports {
		byName[r.Name()] = r
	}
	versions := map[string]string{}
	ev["database_versions"] = versions
	var problems []string
	for _, node := range nodes {
		name := "machine-" + node + "-vulnerability"
		report, ok := byName[name]
		if !ok {
			problems = append(problems, "node "+node+" has no PolicyReport "+name)
			continue
		}
		age, unnamed := "", 0
		for _, result := range report.Objects("results") {
			version := result.Str("properties", "databaseVersion")
			if version == "" {
				unnamed++
			}
			if result.Str("rule") == g11RuleAge {
				age, versions[node] = result.Str("result"), version
			}
		}
		switch {
		case unnamed > 0:
			problems = append(problems, fmt.Sprintf("%d results of %s have no databaseVersion", unnamed, name))
		case age == "":
			problems = append(problems, name+" has no result of the rule "+g11RuleAge)
		case age != "pass":
			problems = append(problems, fmt.Sprintf("the rule %s of %s is %s: the database %s is older than 7 days", g11RuleAge, name, age, versions[node]))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}
