package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// The conformance report of a run in self (runner.go, executeSelf): one
// ClusterPolicyReport, a kind that the compliance pipeline already watches,
// with one result per selected check, written with `kubectl apply`.

// ReportAPIVersion is the API group and version of the report. The package
// that moves the compliance pipeline to another group of policy reports
// changes this one constant.
const ReportAPIVersion = "wgpolicyk8s.io/v1alpha2"

// ReportSource is the source of every result of the report, and the value of
// its label app.kubernetes.io/managed-by.
const ReportSource = "catalyst-conformance"

// reportRunIDLabel is the label of the report that holds the run identifier.
const reportRunIDLabel = "catalyst.openova.io/run-id"

// suiteImageDigest is the form of --suite-image-digest.
var suiteImageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// releaseWaitInterval is how often a run in self reads the HelmReleases while
// it waits for them.
var releaseWaitInterval = 15 * time.Second

// selfProducer is the Job that ACCEPT_JOB_NAMESPACE, ACCEPT_JOB_NAME and
// ACCEPT_JOB_UID name (the downward API of its pod), or manual when one is empty.
func selfProducer(getenv func(string) string) Producer {
	job := ProducerJob{Namespace: getenv("ACCEPT_JOB_NAMESPACE"), Name: getenv("ACCEPT_JOB_NAME"), UID: getenv("ACCEPT_JOB_UID")}
	if job.Namespace == "" || job.Name == "" || job.UID == "" {
		return Producer{Kind: "manual"}
	}
	return Producer{Kind: "job", Job: &job}
}

// exactChartVersion is a chart version that names one release: major, minor
// and patch, with an optional pre-release and build. A range such as `*`,
// which the HelmRelease API writes when the spec names no version (a chart
// from a GitRepository), `>=1.0.0` or `1.x` names no single version.
var exactChartVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// releasesBehind compares the deployed chart version of each HelmRelease with
// the version its spec names; a suspended one, or one whose spec names no
// single version (none, or a range), is not compared. It returns the number
// compared and, sorted, those behind.
func releasesBehind(releases []kube.Object) (int, []string) {
	compared, behind := 0, []string{}
	for _, hr := range releases {
		want := hr.Str("spec", "chart", "spec", "version")
		if suspend, _ := hr.Get("spec", "suspend").(bool); suspend || !exactChartVersion.MatchString(want) {
			continue
		}
		compared++
		if got := deployedChartVersion(hr); got != want {
			behind = append(behind, hr.Namespace()+"/"+hr.Name()+" runs "+nonEmpty(got, "no deployed version")+" and its spec names "+want)
		}
	}
	sort.Strings(behind)
	return compared, behind
}

// waitForReleases is step D0.12 of a run in self: it reads the HelmReleases
// until none is behind, for at most timeout (0: once). It fails when the time
// runs out, when none is found or when they cannot be read.
func (r *runner) waitForReleases(ctx context.Context, region kube.Region, timeout time.Duration) {
	r.step("D0", "D0.12", "every HelmRelease runs the chart version its spec names", func(ev Evidence) error {
		start := time.Now()
		releases, compared, behind := 0, 0, []string{}
		_, err := poll(ctx, timeout, releaseWaitInterval, func() (bool, string, error) {
			list, err := region.GetList(ctx, hrResource, "")
			if err != nil {
				return false, "", err
			}
			releases = len(list)
			compared, behind = releasesBehind(list)
			if releases == 0 {
				return false, "no HelmRelease was found", nil
			}
			if len(behind) > 0 {
				return false, fmt.Sprintf("%d of %d HelmReleases do not run their version: %s", len(behind), compared, strings.Join(behind, "; ")), nil
			}
			return true, "", nil
		})
		ev["releases"], ev["compared"], ev["behind"] = releases, compared, behind
		ev["waited_seconds"] = int(time.Since(start).Seconds())
		return err
	})
}

// BuildReport is the report of a finished run in self, one result per check:
// pass, fail (a required check failed), warn (an expected-fail check failed)
// or skip, with the severity high for a required check and low otherwise.
func BuildReport(name string, run *Run) map[string]any {
	labels := map[string]any{"app.kubernetes.io/managed-by": ReportSource, reportRunIDLabel: run.RunID}
	summary := map[string]any{"pass": 0, "fail": 0, "warn": 0, "error": 0, "skip": 0}
	results := []any{}
	for _, c := range run.Checks {
		result, severity, message := "pass", "low", "every step passed"
		if c.StateAtRun == StateRequired {
			severity = "high"
		}
		switch c.Status {
		case Fail:
			result, message = "warn", trunc(c.Reason, 1000)
			if c.StateAtRun == StateRequired {
				result = "fail"
			}
		case Skip:
			result, message = "skip", trunc(c.Reason, 1000)
		}
		summary[result] = summary[result].(int) + 1
		results = append(results, map[string]any{
			"source": ReportSource, "policy": c.ID, "rule": c.ID, "result": result, "severity": severity,
			"message": message, "timestamp": map[string]any{"seconds": run.FinishedAt.Unix(), "nanos": 0},
			"properties": map[string]any{"state": c.StateAtRun},
		})
	}
	return map[string]any{
		"apiVersion": ReportAPIVersion,
		"kind":       "ClusterPolicyReport",
		"metadata":   map[string]any{"name": name, "labels": labels},
		"summary":    summary,
		"results":    results,
	}
}

// applyReport writes the report into the region with `kubectl apply -f -`.
func applyReport(ctx context.Context, region kube.Region, report map[string]any) error {
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = region.Run(ctx, bytes.NewReader(b), "apply", "-f", "-")
	return err
}
