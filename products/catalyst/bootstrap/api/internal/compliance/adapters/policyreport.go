// Package adapters turns informer events into findings of the compliance read
// model: one adapter per source kind. An adapter reads only the event and the
// resolver; it keeps no state and never calls an API server.
package adapters

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// ReportGroup is the only API group whose reports PolicyReport reads.
const ReportGroup = "wgpolicyk8s.io"

// maxMessage is the longest Finding.Message an adapter keeps.
const maxMessage = 500

// PolicyReport reads the results of the PolicyReport and ClusterPolicyReport
// objects of group wgpolicyk8s.io: one finding per result, judged on the top
// controller of the object the result names.
type PolicyReport struct{}

// Kinds returns the informer kinds of the two report objects.
func (PolicyReport) Kinds() []string { return []string{"policyreport", "clusterpolicyreport"} }

// Apply returns one finding per result of the report; nil for a deletion or
// a report of another group.
func (PolicyReport) Apply(ev k8scache.Event, r compliance.Resolver) []compliance.Finding {
	obj := ev.Object
	if obj == nil || ev.Type == k8scache.EventDeleted || groupOf(obj.GetAPIVersion()) != ReportGroup {
		return nil
	}
	evidence := compliance.ObjectRef{APIVersion: obj.GetAPIVersion(), Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
	results, _, _ := unstructured.NestedSlice(obj.Object, "results")
	var out []compliance.Finding
	for _, ri := range results {
		res, ok := ri.(map[string]any)
		if !ok {
			continue
		}
		policy, _, _ := unstructured.NestedString(res, "policy")
		rule, _, _ := unstructured.NestedString(res, "rule")
		result, _, _ := unstructured.NestedString(res, "result")
		message, _, _ := unstructured.NestedString(res, "message")
		source, _, _ := unstructured.NestedString(res, "source")
		if policy == "" || result == "" {
			continue
		}
		kind, ns, name := ResourceFor(obj, res)
		if name == "" {
			continue
		}
		f := compliance.Finding{
			Check:      policy,
			Source:     sourceOf(source),
			Subject:    r.TopOwner(ev.Cluster, ns, kind, name),
			Result:     ResultOf(result),
			Message:    truncate(message),
			Evidence:   evidence,
			ObservedAt: ev.At,
		}
		if rule != "" {
			f.Properties = map[string]string{"rule": rule}
		}
		out = append(out, f)
	}
	return out
}

// ResourceFor returns the object one result of a report names: the first
// entry of the result's resources, else the report's scope, else the report
// itself.
func ResourceFor(report *unstructured.Unstructured, result map[string]any) (kind, ns, name string) {
	if rs, ok, _ := unstructured.NestedSlice(result, "resources"); ok && len(rs) > 0 {
		if r, ok := rs[0].(map[string]any); ok {
			kind, _, _ = unstructured.NestedString(r, "kind")
			ns, _, _ = unstructured.NestedString(r, "namespace")
			name, _, _ = unstructured.NestedString(r, "name")
			if name != "" {
				return
			}
		}
	}
	scope, _, _ := unstructured.NestedMap(report.Object, "scope")
	if len(scope) > 0 {
		kind, _ = scope["kind"].(string)
		ns, _ = scope["namespace"].(string)
		name, _ = scope["name"].(string)
		if name != "" {
			return
		}
	}
	return "PolicyReport", report.GetNamespace(), report.GetName()
}

// ResultOf maps a producer result to a Result: skip is na, error and any
// unknown word are not-measured.
func ResultOf(result string) compliance.Result {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "pass":
		return compliance.Pass
	case "fail":
		return compliance.Fail
	case "warn":
		return compliance.Warn
	case "skip", "na":
		return compliance.NA
	}
	return compliance.NotMeasured
}

// sourceOf maps the source field of a report result: empty or "kyverno" (a
// legacy ClusterPolicy) and "KyvernoValidatingPolicy" (a ValidatingPolicy,
// measured by spike CMP-S1) are the engine; any other producer keeps its own
// name, which no check of the fallback model names.
func sourceOf(s string) compliance.Source {
	if s == "" || strings.EqualFold(s, "kyverno") || strings.EqualFold(s, "KyvernoValidatingPolicy") {
		return compliance.SourceKyverno
	}
	return compliance.Source(s)
}

func groupOf(apiVersion string) string {
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		return apiVersion[:i]
	}
	return ""
}

func truncate(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	return s[:maxMessage]
}
