// otel.go — OTel auto-instrumentation evaluator.
//
// EPIC-1 (#1096) §4.3 row "OTel auto-instrumentation present".
//
// Logic (per `02-W-watcher-extension.md` brief):
//
//  1. For each Pod: check container list for an `otel-collector`
//     sidecar (heuristic: image substring matches
//     Config.OTelSidecarImageMatch — default `opentelemetry-collector`).
//     If found → result=pass.
//  2. OTel Operator auto-injection: check if the Pod has an annotation
//     with prefix `instrumentation.opentelemetry.io/inject-` whose
//     value is `true`. Combined with an Instrumentation CR existing in
//     the Pod's namespace → result=pass.
//  3. Neither → result=fail.
//
// The Instrumentation CR is a namespaced opentelemetry.io/v1alpha1
// resource. When the kind is not registered in the k8scache (the
// bp-otel-operator chart isn't installed on this Sovereign) the
// evaluator falls back to sidecar-only detection — annotation alone
// without the operator running is meaningless.
package evaluators

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// OTelEvaluator implements `policy=otel-injected`.
type OTelEvaluator struct {
	SidecarImageMatch       string
	InjectAnnotationPrefix  string
	InstrumentationKindName string
}

// NewOTelEvaluator builds an OTelEvaluator from cfg.
func NewOTelEvaluator(cfg Config) *OTelEvaluator {
	return &OTelEvaluator{
		SidecarImageMatch:       cfg.OTelSidecarImageMatch,
		InjectAnnotationPrefix:  cfg.OTelInjectAnnotationPrefix,
		InstrumentationKindName: cfg.OTelInstrumentationKind,
	}
}

func (OTelEvaluator) Name() string { return "otel-injected" }

func (o *OTelEvaluator) Evaluate(ctx context.Context, snap Snapshot, target *unstructured.Unstructured) []SyntheticReport {
	if !isPod(target) {
		return nil
	}

	// 1. Sidecar check — substring match against container images.
	for _, img := range containerImages(target) {
		if o.SidecarImageMatch != "" && strings.Contains(img, o.SidecarImageMatch) {
			return []SyntheticReport{{
				Policy:    o.Name(),
				Rule:      o.Name(),
				Result:    ResultPass,
				Resource:  resourceFor(target),
				Namespace: target.GetNamespace(),
				Message:   "OTel collector sidecar detected (image=" + img + ")",
				Properties: map[string]string{
					"detection": "sidecar",
					"image":     img,
				},
			}}
		}
	}

	// 2. Auto-inject path — Pod annotation + Instrumentation CR in
	//    the same namespace.
	annots := target.GetAnnotations()
	injectAnnotation := ""
	for k, v := range annots {
		if strings.HasPrefix(k, o.InjectAnnotationPrefix) && strings.EqualFold(v, "true") {
			injectAnnotation = k
			break
		}
	}
	if injectAnnotation != "" {
		// Look up Instrumentation CR in the same namespace.
		instList, err := snap.List(o.InstrumentationKindName, labels.Everything())
		if err == nil {
			for _, inst := range instList {
				if inst.GetNamespace() == target.GetNamespace() {
					return []SyntheticReport{{
						Policy:    o.Name(),
						Rule:      o.Name(),
						Result:    ResultPass,
						Resource:  resourceFor(target),
						Namespace: target.GetNamespace(),
						Message:   "OTel auto-inject annotation present + Instrumentation CR in namespace",
						Properties: map[string]string{
							"detection":           "auto-inject",
							"annotation":          injectAnnotation,
							"instrumentationName": inst.GetName(),
						},
					}}
				}
			}
		}
		// Annotation present but no Instrumentation CR — operator
		// not installed. Surface as fail with a hint.
		return []SyntheticReport{{
			Policy:    o.Name(),
			Rule:      o.Name(),
			Result:    ResultFail,
			Resource:  resourceFor(target),
			Namespace: target.GetNamespace(),
			Message:   "OTel auto-inject annotation set but no Instrumentation CR in namespace — operator missing",
			Properties: map[string]string{
				"detection":  "auto-inject-orphan",
				"annotation": injectAnnotation,
			},
		}}
	}

	// 3. Neither path matched.
	return []SyntheticReport{{
		Policy:    o.Name(),
		Rule:      o.Name(),
		Result:    ResultFail,
		Resource:  resourceFor(target),
		Namespace: target.GetNamespace(),
		Message:   "no OTel collector sidecar and no auto-inject annotation",
	}}
}
