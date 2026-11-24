package adapters

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// Evaluator reads one synthetic report of an in-process evaluator: the flat
// fields policy, rule, result, message, resource.kind, resource.name and
// namespace of the event's object. No evaluator is registered today; the
// adapter stays so that one added later is judged like a report.
type Evaluator struct{}

// Kinds returns the informer kind of the synthetic reports.
func (Evaluator) Kinds() []string { return []string{k8scache.KindComplianceEvaluator} }

// SourceKey names the synthetic report by the evaluator's check and the
// object it judged: the event object has no name of its own.
func (Evaluator) SourceKey(ev k8scache.Event) compliance.ObjectRef {
	policy, kind, ns, name := syntheticFields(ev.Object)
	return compliance.ObjectRef{APIVersion: k8scache.KindComplianceEvaluator + "/" + policy, Kind: kind, Namespace: ns, Name: name}
}

// Apply returns the one finding of the report; nil for a deletion or a
// report without a policy, a result or a resource name.
func (Evaluator) Apply(ev k8scache.Event, r compliance.Resolver) []compliance.Finding {
	if ev.Object == nil || ev.Type == k8scache.EventDeleted {
		return nil
	}
	obj := ev.Object.Object
	policy, kind, ns, name := syntheticFields(ev.Object)
	rule, _, _ := unstructured.NestedString(obj, "rule")
	result, _, _ := unstructured.NestedString(obj, "result")
	message, _, _ := unstructured.NestedString(obj, "message")
	if policy == "" || result == "" || name == "" {
		return nil
	}
	f := compliance.Finding{
		Check:      policy,
		Source:     compliance.SourceEvaluator,
		Subject:    r.TopOwner(ev.Cluster, ns, kind, name),
		Result:     ResultOf(result),
		Message:    truncate(message),
		Evidence:   compliance.ObjectRef{Kind: kind, Namespace: ns, Name: name},
		ObservedAt: ev.At,
	}
	if rule != "" {
		f.Properties = map[string]string{"rule": rule}
	}
	return []compliance.Finding{f}
}

func syntheticFields(o *unstructured.Unstructured) (policy, kind, ns, name string) {
	if o == nil {
		return
	}
	policy, _, _ = unstructured.NestedString(o.Object, "policy")
	kind, _, _ = unstructured.NestedString(o.Object, "resource", "kind")
	name, _, _ = unstructured.NestedString(o.Object, "resource", "name")
	ns, _, _ = unstructured.NestedString(o.Object, "namespace")
	return
}
