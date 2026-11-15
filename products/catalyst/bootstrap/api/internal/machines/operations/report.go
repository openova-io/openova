package operations

import (
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
)

// PolicyReportKind is the report kind the compliance view reads.
var PolicyReportKind = schema.GroupVersionKind{Group: "wgpolicyk8s.io", Version: "v1alpha2", Kind: "PolicyReport"}

// The fixed parts of a hardening report.
const (
	SourceHardening            = "machine-hardening"
	LabelReportMachine         = "machines.openova.io/machine"
	LabelReport                = "machines.openova.io/report"
	ReportHardening            = "hardening"
	AnnotationResultsTruncated = "machines.openova.io/results-truncated"
	MaxReportResults           = 200
)

// HardeningReportName is the name of the hardening report of a Machine.
func HardeningReportName(machine string) string { return "machine-" + machine + "-hardening" }

// reportResult maps the XCCDF result of a rule to a result of the report and its rank,
// worst first. A rule that was not evaluated is skip, never pass.
func reportResult(xccdf string) (string, int) {
	switch xccdf {
	case "fail":
		return "fail", 0
	case "error", "unknown":
		return "error", 1
	case "pass", "fixed":
		return "pass", 3
	}
	return "skip", 2
}

// HardeningReport builds the hardening report of one Machine from the file
// hardening.results.json of a run: one result per rule, worst first (fail, error, skip,
// pass; then by rule identifier), at most MaxReportResults. A longer list is cut and
// marked with the annotation machines.openova.io/results-truncated; the property
// evidenceKey of every result names the full file. The summary counts every rule.
// Results with no rule give the one result skip with the rule no-profile-for-release.
func HardeningReport(m *v1alpha1.Machine, run string, res runner.HardeningResults, evidenceKey string, at time.Time) *unstructured.Unstructured {
	type row struct {
		rule, result string
		rank         int
	}
	rows := make([]row, 0, len(res.Rules))
	summary := map[string]interface{}{"pass": int64(0), "fail": int64(0), "warn": int64(0), "error": int64(0), "skip": int64(0)}
	for _, r := range res.Rules {
		result, rank := reportResult(r.Result)
		rows = append(rows, row{rule: r.ID, result: result, rank: rank})
		summary[result] = summary[result].(int64) + 1
	}
	policy := res.ProfileID
	if len(rows) == 0 {
		rows, summary["skip"] = []row{{rule: runner.MessageNoProfile, result: "skip"}}, int64(1)
		if policy == "" {
			policy = ReportHardening
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		return rows[i].rule < rows[j].rule
	})
	truncated := len(rows) > MaxReportResults
	if truncated {
		rows = rows[:MaxReportResults]
	}
	results := make([]interface{}, 0, len(rows))
	for _, r := range rows {
		properties := map[string]interface{}{"runName": run, "contentRelease": res.ContentRelease}
		if evidenceKey != "" {
			properties["evidenceKey"] = evidenceKey
		}
		results = append(results, map[string]interface{}{
			"source": SourceHardening, "policy": policy, "rule": r.rule, "result": r.result,
			"timestamp": map[string]interface{}{"seconds": at.Unix(), "nanos": int64(0)}, "properties": properties,
		})
	}
	scope := map[string]interface{}{"apiVersion": v1alpha1.GroupVersion.String(), "kind": v1alpha1.MachineKind.Kind, "namespace": m.Namespace, "name": m.Name, "uid": string(m.UID)}
	u := &unstructured.Unstructured{Object: map[string]interface{}{"scope": scope, "summary": summary, "results": results}}
	u.SetGroupVersionKind(PolicyReportKind)
	u.SetNamespace(m.Namespace)
	u.SetName(HardeningReportName(m.Name))
	u.SetLabels(map[string]string{LabelReportMachine: m.Name, LabelReport: ReportHardening})
	if truncated {
		u.SetAnnotations(map[string]string{AnnotationResultsTruncated: "true"})
	}
	owner := map[string]interface{}{"apiVersion": scope["apiVersion"], "kind": scope["kind"], "name": m.Name, "uid": string(m.UID)}
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{owner}, "metadata", "ownerReferences")
	return u
}
