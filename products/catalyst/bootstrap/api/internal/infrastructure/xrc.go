// xrc.go — Crossplane Composite Resource Claim writer helpers.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 every Day-2 mutation MUST be
// expressed as a Crossplane XRC submission. This file is the single
// seam through which the catalyst-api writes those XRCs against the
// SOVEREIGN cluster (NOT contabo-mkt).
//
// The seam takes a dynamic.Interface (the Sovereign's dynamic client,
// built from the deployment's persisted kubeconfig) and a typed
// XRCSpec; it produces the unstructured object, performs the create
// against Crossplane's Composite Resource Claim API, and returns the
// XRC's namespace + name + GVK so the handler can surface them in
// the 202 response and write the audit-trail Job entry.
//
// # Why dynamic.Interface, not typed clients
//
// Crossplane Compositions are author-time; the catalyst-api is
// consumer-time. We write claims against API groups (e.g.
// `infra.openova.io/v1alpha1`) whose typed Go schemas DON'T live
// in this repo — the third-sibling agent owns them in their own
// chart. A dynamic client lets us write claims by group/version/kind
// without compiling against generated types.
//
// # When the Composition isn't ready yet
//
// If the Composition for a given XRC kind doesn't exist on the
// Sovereign cluster yet (the third-sibling agent hasn't finished
// authoring + applying their chart), the create still succeeds —
// Crossplane stores the claim and sits it as Pending. The catalyst-
// api emits a Job log line saying "Awaiting Crossplane Composition
// for <kind>" and returns the same 202.
package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// XRCAPIGroup — the canonical Crossplane API group catalyst-api
// writes claims under. The third-sibling chart's Compositions match
// this group + version + per-kind names. Per docs/INVIOLABLE-PRINCIPLES.md
// #4 the group name is centralised here so a future migration to a
// different group only changes one line.
const (
	XRCAPIGroup   = "infra.openova.io"
	XRCAPIVersion = "v1alpha1"
)

// XRC kind constants. Each maps to one Composition the third-sibling
// agent authors. Keep these in lockstep with the Composition manifest
// metadata.name values; mismatches surface as Pending claims with no
// reconciliation progress.
const (
	KindRegionClaim       = "RegionClaim"
	KindClusterClaim      = "ClusterClaim"
	KindVClusterClaim     = "VClusterClaim"
	KindNodePoolClaim     = "NodePoolClaim"
	KindLoadBalancerClaim = "LoadBalancerClaim"
	KindPeeringClaim      = "PeeringClaim"
	KindFirewallRuleClaim = "FirewallRuleClaim"
	KindNodeActionClaim   = "NodeActionClaim"
)

// XRCNamespace — the namespace catalyst-api submits all claims into.
// Crossplane Composite Resource Claims are namespace-scoped; the
// third-sibling agent's chart provisions ServiceAccount RBAC + the
// underlying ProviderConfig in this namespace.
const XRCNamespace = "catalyst-day2"

// LabelDeploymentID + LabelOwner — every claim catalyst-api writes
// carries these labels so an operator can trace a claim back to the
// deployment + the catalyst-api Pod that submitted it.
const (
	LabelDeploymentID = "catalyst.openova.io/deployment-id"
	LabelOwner        = "catalyst.openova.io/owner"
	LabelOwnerValue   = "catalyst-api"
	AnnotationAction  = "catalyst.openova.io/action"
	AnnotationDiff    = "catalyst.openova.io/diff"
)

// XRCSpec — the typed payload one CRUD endpoint passes to SubmitXRC.
// Spec is a free-form map matching the XRD's schema; the helper
// stamps apiVersion + kind + metadata. Keeping this loose lets the
// CRUD handlers compose the same shape the third-sibling Composition
// expects without a per-kind Go type explosion.
type XRCSpec struct {
	Kind         string
	Name         string
	DeploymentID string
	Action       string // human label e.g. "add-region", "remove-pool"
	Diff         string // unified-diff or short ASCII summary

	// Spec — the XRD's spec subtree as a map. The helper marshals it
	// under .spec on the unstructured object.
	Spec map[string]any
}

// ErrXRCNameConflict — surfaced when a claim with the same name +
// namespace already exists. The CRUD handler maps this onto HTTP 409
// so the wizard can surface "this region is already provisioned".
var ErrXRCNameConflict = errors.New("infrastructure: xrc name conflict")

// SubmitXRC writes the XRC to the Sovereign cluster. Returns the
// gvr + the unstructured object (with the cluster-stamped UID +
// resourceVersion populated) so the handler can include them in the
// 202 response. Caller MUST hold no locks.
//
// The helper is idempotent on retry: a second call with the same
// .metadata.name within the same namespace returns ErrXRCNameConflict
// — the handler surfaces 409 and instructs the operator to use PATCH
// for in-place updates instead.
func SubmitXRC(ctx context.Context, client dynamic.Interface, spec XRCSpec) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	if client == nil {
		return nil, schema.GroupVersionResource{}, errors.New("infrastructure: dynamic client is required (sovereign cluster unreachable)")
	}
	if strings.TrimSpace(spec.Kind) == "" {
		return nil, schema.GroupVersionResource{}, errors.New("infrastructure: XRC kind is required")
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, schema.GroupVersionResource{}, errors.New("infrastructure: XRC name is required")
	}

	gvr := gvrForKind(spec.Kind)
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(XRCAPIGroup + "/" + XRCAPIVersion)
	obj.SetKind(spec.Kind)
	obj.SetName(spec.Name)
	obj.SetNamespace(XRCNamespace)
	obj.SetLabels(map[string]string{
		LabelOwner:        LabelOwnerValue,
		LabelDeploymentID: spec.DeploymentID,
	})
	annotations := map[string]string{}
	if spec.Action != "" {
		annotations[AnnotationAction] = spec.Action
	}
	if spec.Diff != "" {
		annotations[AnnotationDiff] = spec.Diff
	}
	if len(annotations) > 0 {
		obj.SetAnnotations(annotations)
	}
	if spec.Spec != nil {
		// k8s.io/apimachinery's DeepCopyJSONValue only accepts the
		// JSON-typed scalars (string, bool, float64, int64, nil) and
		// recursively-typed slices/maps. Go `int` panics — so we
		// normalise the spec tree before SetNestedMap.
		_ = unstructured.SetNestedMap(obj.Object, normaliseJSONMap(spec.Spec), "spec")
	}

	created, err := client.Resource(gvr).Namespace(XRCNamespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, gvr, fmt.Errorf("%w: %s/%s", ErrXRCNameConflict, spec.Kind, spec.Name)
		}
		return nil, gvr, fmt.Errorf("infrastructure: create %s/%s: %w", spec.Kind, spec.Name, err)
	}
	return created, gvr, nil
}

// DeleteXRC marks the named XRC for deletion. Crossplane's
// Composition controller honours .spec.deletionPolicy=Delete and
// reaps the underlying cloud resources. Returns ErrXRCNameConflict
// when the claim doesn't exist (the operator should refresh the
// topology before retrying).
func DeleteXRC(ctx context.Context, client dynamic.Interface, kind, name string) (schema.GroupVersionResource, error) {
	if client == nil {
		return schema.GroupVersionResource{}, errors.New("infrastructure: dynamic client is required (sovereign cluster unreachable)")
	}
	gvr := gvrForKind(kind)
	policy := metav1.DeletePropagationForeground
	err := client.Resource(gvr).Namespace(XRCNamespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return gvr, fmt.Errorf("%w: %s/%s not found", ErrXRCNameConflict, kind, name)
		}
		return gvr, fmt.Errorf("infrastructure: delete %s/%s: %w", kind, name, err)
	}
	return gvr, nil
}

// gvrForKind — derives the plural resource segment from the Kind.
// All kinds in this surface follow the same suffix pattern: drop
// "Claim" → lowercase → pluralise. RegionClaim → regionclaims.
// The helper centralises the rule so a future kind that violates it
// surfaces here.
func gvrForKind(kind string) schema.GroupVersionResource {
	plural := strings.ToLower(kind)
	if !strings.HasSuffix(plural, "s") {
		plural += "s"
	}
	return schema.GroupVersionResource{
		Group:    XRCAPIGroup,
		Version:  XRCAPIVersion,
		Resource: plural,
	}
}

// XRCName composes the deterministic XRC name catalyst-api submits.
// The same shape (deployment-id-prefix + verb + slug) is used for
// every claim so the operator can grep across the cluster for a
// single deployment's claims.
//
// E.g. depID="ce476aaf80731a46", verb="region", slug="hel1" →
// "ce476aaf-region-hel1"
func XRCName(deploymentID, verb, slug string) string {
	dep := strings.TrimSpace(deploymentID)
	if len(dep) > 8 {
		dep = dep[:8]
	}
	verb = strings.ToLower(strings.TrimSpace(verb))
	slug = strings.ToLower(strings.TrimSpace(slug))
	parts := []string{}
	if dep != "" {
		parts = append(parts, dep)
	}
	if verb != "" {
		parts = append(parts, verb)
	}
	if slug != "" {
		parts = append(parts, slug)
	}
	out := strings.Join(parts, "-")
	// Crossplane claim names follow DNS-1123 subdomain rules — replace
	// any disallowed characters with '-' and clamp at 63 chars.
	out = sanitizeDNS1123(out)
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

func sanitizeDNS1123(in string) string {
	var b strings.Builder
	for i, r := range in {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '.':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			b.WriteRune('-')
		}
		_ = i
	}
	out := b.String()
	out = strings.Trim(out, "-.")
	if out == "" {
		out = "x"
	}
	return out
}

// SubmittedAt — UTC instant the helper stamps onto every claim's
// metadata.annotations and into the 202 response. Centralised so
// tests can inject a fake clock.
func SubmittedAt() time.Time {
	return time.Now().UTC()
}

// normaliseJSONMap walks a map[string]any and converts every leaf
// value to a JSON-compatible scalar (string, bool, int64, float64,
// nil) plus recursively normalised maps/slices. The k8s.io
// apimachinery's DeepCopyJSONValue panics on Go `int` (no JSON
// equivalent) — we hit that path through unstructured.SetNestedMap.
// Centralising the conversion here keeps the per-handler spec
// blocks readable (`map[string]any{"workerCount": body.WorkerCount}`)
// while the wire-level shape stays JSON-strict.
func normaliseJSONMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = normaliseJSONValue(v)
	}
	return out
}

func normaliseJSONValue(v any) any {
	switch x := v.(type) {
	case nil, bool, string, int64, float64:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case float32:
		return float64(x)
	case map[string]any:
		return normaliseJSONMap(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normaliseJSONValue(x[i])
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out
	default:
		// Fallback — render unknown types as their fmt.Sprint string
		// so the XRC carries a valid JSON value and the operator can
		// see what was attempted in the resulting Pending claim. The
		// alternative (drop the field) would silently corrupt the
		// audit trail.
		return fmt.Sprintf("%v", x)
	}
}
