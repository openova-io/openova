// flux.go — real handlers for flux.status / flux.reconcile /
// flux.suspend / flux.resume.
//
// Scope (architecture.md §3 + line 104): agents shipping apps from a
// Sandbox routinely need to inspect + nudge Flux's reconcile loop in
// the Sandbox's tenant namespaces — "is my HelmRelease ready", "force
// a reconcile of my Kustomization", "suspend that drifting HR before
// I patch the values". The canonical Flux v2 API surfaces this via
// HelmRelease (`helm.toolkit.fluxcd.io/v2`) and Kustomization
// (`kustomize.toolkit.fluxcd.io/v1`) custom resources.
//
// Tools shipped here:
//
//   - flux.status               → LIST HRs + Kustomizations across the
//                                 Sandbox's tenant namespaces, distil
//                                 the Ready condition + last applied
//                                 revision into a flat table the agent
//                                 can iterate.
//   - flux.reconcile {kind, name, namespace}
//                               → Annotate the resource with
//                                 `reconcile.fluxcd.io/requestedAt=<now>`.
//                                 Flux's controller picks this up on
//                                 the next watch tick and re-runs the
//                                 reconcile loop. Idempotent: the
//                                 annotation is a timestamp, so a
//                                 second call simply refreshes it.
//   - flux.suspend {kind, name, namespace}
//                               → Patch `spec.suspend=true`.
//                                 Flux stops reconciling until the
//                                 field is flipped back.
//   - flux.resume {kind, name, namespace}
//                               → Patch `spec.suspend=false`.
//
// Hard-rule note (Wave 12 task brief): the broader k8s.write.* surface
// stays READ-ONLY, but the flux.reconcile/suspend/resume trio is
// explicitly carved out because each mutation targets the agent's OWN
// tenant namespaces and the mutated fields are the canonical Flux
// "kick" / "pause" knobs every operator already uses.
//
// Namespace scoping:
//
//   - Every mutation tool defaults `namespace` to env.SandboxNamespace
//     when the caller omits it. An explicit namespace argument is
//     accepted (a Sandbox often sees multiple workload namespaces
//     inside the Org vcluster) but the SA's RoleBinding gates which
//     namespaces it can actually touch — we don't second-guess it
//     client-side.
//   - flux.status enumerates the well-known set of tenant namespaces
//     (the Sandbox's own ns + every entry the caller passes as
//     `namespaces`). We do NOT list cluster-wide HRs/Kustomizations
//     because that leaks across tenants on a multi-tenant vcluster.
//
// Auth: same shape as sandbox.db.* — claims.OrgID must match env.OrgID,
// RequiredCapability = "flux", and every mutation is gated by the
// dynamic client's per-namespace RBAC (the SA can only patch what its
// RoleBinding allows).
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Flux GVRs. Pinned to the latest stable (v2 for HelmRelease per the
// flux 2.4 release, v1 for Kustomization). Centralised so a future
// API-version bump is a one-line edit.
var (
	fluxHelmReleaseGVR   = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	fluxKustomizationGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
)

// fluxReconcileAnnotation is the canonical "kick" annotation Flux's
// notification controller honours to force-run a reconcile loop. The
// value MUST be a fresh timestamp (any RFC3339 / Unix-seconds string
// works in practice — the controller only checks for a delta).
const fluxReconcileAnnotation = "reconcile.fluxcd.io/requestedAt"

// fluxKindRE constrains the `kind` arg of flux.reconcile / suspend /
// resume to the two we support.
var fluxKindRE = regexp.MustCompile(`^(?i)(helmrelease|kustomization)$`)

// fluxNameRE matches RFC-1123 labels (k8s metadata.name format).
var fluxNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// fluxKindToGVR resolves the user-friendly kind string to a GVR.
// Case-insensitive; returns an error on anything outside the supported
// set so an unknown kind is rejected before we touch the API server.
func fluxKindToGVR(kind string) (schema.GroupVersionResource, string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "helmrelease":
		return fluxHelmReleaseGVR, "HelmRelease", nil
	case "kustomization":
		return fluxKustomizationGVR, "Kustomization", nil
	default:
		return schema.GroupVersionResource{}, "", fmt.Errorf("flux: unknown kind %q (must be one of: HelmRelease, Kustomization)", kind)
	}
}

// fluxScopedNamespace mirrors scopeNamespace from k8s_read.go but is
// kept local so a future change to flux's defaulting (e.g. "all tenant
// namespaces by default") doesn't leak into the broader k8s.read.*
// surface.
func fluxScopedNamespace(env *Env, want string) (string, error) {
	if want == "" {
		want = env.SandboxNamespace
	}
	if want == "" {
		return "", errors.New("flux: namespace not supplied and Sandbox Env has no SANDBOX_NAMESPACE default")
	}
	return want, nil
}

// fluxResourceSummary is the per-row shape flux.status returns. We
// distil the Ready condition + last applied revision into stable
// snake_case keys so the agent doesn't have to parse the noisy
// Unstructured status sub-object.
type fluxResourceSummary struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Ready       string `json:"ready"` // "True" | "False" | "Unknown"
	Suspended   bool   `json:"suspended"`
	Reason      string `json:"reason,omitempty"`
	Message     string `json:"message,omitempty"`
	Revision    string `json:"revision,omitempty"` // status.lastAppliedRevision
	LastApplied string `json:"last_applied_at,omitempty"`
	Age         string `json:"age,omitempty"`
}

// extractReadyCondition pulls the Ready condition out of the standard
// metav1.Condition slice on Flux resources. Returns ("Unknown", "", "")
// when the slice is empty (resource still admitted, controller hasn't
// observed it yet).
func extractReadyCondition(obj map[string]any) (status, reason, message string) {
	conds, found, _ := unstructured.NestedSlice(obj, "status", "conditions")
	if !found {
		return "Unknown", "", ""
	}
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cm["type"] == "Ready" {
			status, _ = cm["status"].(string)
			reason, _ = cm["reason"].(string)
			message, _ = cm["message"].(string)
			return
		}
	}
	return "Unknown", "", ""
}

// summariseFluxResource renders one Unstructured into the flat
// fluxResourceSummary shape.
func summariseFluxResource(kind string, obj *unstructured.Unstructured) fluxResourceSummary {
	status, reason, message := extractReadyCondition(obj.Object)
	suspended, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend")
	revision, _, _ := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
	lastApplied, _, _ := unstructured.NestedString(obj.Object, "status", "lastHandledReconcileAt")
	age := ""
	if ts := obj.GetCreationTimestamp().Time; !ts.IsZero() {
		age = ts.UTC().Format(time.RFC3339)
	}
	return fluxResourceSummary{
		Kind:        kind,
		Name:        obj.GetName(),
		Namespace:   obj.GetNamespace(),
		Ready:       status,
		Suspended:   suspended,
		Reason:      reason,
		Message:     message,
		Revision:    revision,
		LastApplied: lastApplied,
		Age:         age,
	}
}

// --- flux.status -------------------------------------------------------

type fluxStatusArgs struct {
	// Namespaces — optional list to expand the scan beyond the
	// Sandbox's own namespace. When empty we scan only
	// env.SandboxNamespace; otherwise the union of the supplied
	// names + the Sandbox namespace.
	Namespaces []string `json:"namespaces,omitempty"`
}

func fluxStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var args fluxStatusArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("flux.status: invalid arguments: %w", err)
		}
	}
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("flux.status: server env missing on context")
	}
	// Build the deduped namespace set (Sandbox NS + caller-supplied).
	nsSet := map[string]struct{}{}
	if env.SandboxNamespace != "" {
		nsSet[env.SandboxNamespace] = struct{}{}
	}
	for _, n := range args.Namespaces {
		n = strings.TrimSpace(n)
		if n != "" {
			nsSet[n] = struct{}{}
		}
	}
	if len(nsSet) == 0 {
		return nil, errors.New("flux.status: no namespaces in scope (env.SandboxNamespace empty and `namespaces` not supplied)")
	}
	namespaces := make([]string, 0, len(nsSet))
	for n := range nsSet {
		namespaces = append(namespaces, n)
	}
	sort.Strings(namespaces)

	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	summaries := make([]fluxResourceSummary, 0, 32)
	// We swallow per-namespace 404s on the GVR (e.g. Flux CRDs not
	// installed in a fresh ns) — surfacing an empty result is friendlier
	// to the agent than a noisy multi-error response. Any other error
	// (RBAC, transport) aborts the call.
	listOne := func(ns string, gvr schema.GroupVersionResource, kind string) error {
		list, err := client.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("flux.status: LIST %s in %s: %w", gvr.Resource, ns, err)
		}
		for i := range list.Items {
			summaries = append(summaries, summariseFluxResource(kind, &list.Items[i]))
		}
		return nil
	}
	for _, ns := range namespaces {
		if err := listOne(ns, fluxHelmReleaseGVR, "HelmRelease"); err != nil {
			return nil, err
		}
		if err := listOne(ns, fluxKustomizationGVR, "Kustomization"); err != nil {
			return nil, err
		}
	}
	// Stable sort: namespace, kind, name. Lets the agent diff status
	// across calls without re-ordering noise.
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].Namespace != summaries[j].Namespace {
			return summaries[i].Namespace < summaries[j].Namespace
		}
		if summaries[i].Kind != summaries[j].Kind {
			return summaries[i].Kind < summaries[j].Kind
		}
		return summaries[i].Name < summaries[j].Name
	})
	return map[string]any{
		"namespaces": namespaces,
		"count":      len(summaries),
		"items":      summaries,
	}, nil
}

func schemaFluxStatus() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespaces": map[string]any{
				"type":        "array",
				"description": "Optional extra namespaces to scan. The Sandbox namespace is always included.",
				"items":       map[string]any{"type": "string"},
			},
		},
		"additionalProperties": false,
	}
}

// --- flux.reconcile ----------------------------------------------------

type fluxMutationArgs struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

func validateFluxMutationArgs(a *fluxMutationArgs) error {
	if a.Kind == "" {
		return errors.New("`kind` is required (HelmRelease | Kustomization)")
	}
	if !fluxKindRE.MatchString(a.Kind) {
		return fmt.Errorf("`kind` must match %s", fluxKindRE.String())
	}
	if a.Name == "" {
		return errors.New("`name` is required")
	}
	if !fluxNameRE.MatchString(a.Name) {
		return fmt.Errorf("`name` must be a valid RFC-1123 label (matched %s)", fluxNameRE.String())
	}
	return nil
}

// patchFluxAnnotation applies the kick annotation as a strategic-merge
// patch. We use Patch rather than Update so a concurrent reconcile
// loop doesn't race us on resourceVersion.
func patchFluxAnnotation(ctx context.Context, env *Env, args fluxMutationArgs) (any, error) {
	ns, err := fluxScopedNamespace(env, args.Namespace)
	if err != nil {
		return nil, err
	}
	gvr, canonicalKind, err := fluxKindToGVR(args.Kind)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				fluxReconcileAnnotation: now,
			},
		},
	}
	patchBytes, _ := json.Marshal(patch)
	patched, err := client.Resource(gvr).Namespace(ns).
		Patch(ctx, args.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("flux.reconcile: PATCH %s/%s in %s: %w", canonicalKind, args.Name, ns, err)
	}
	return map[string]any{
		"status":           "Reconciling",
		"kind":             canonicalKind,
		"name":             args.Name,
		"namespace":        ns,
		"requested_at":     now,
		"resource_version": patched.GetResourceVersion(),
		"note":             "Flux's controller will pick up the annotation on the next watch tick.",
	}, nil
}

func fluxReconcile(ctx context.Context, raw json.RawMessage) (any, error) {
	var args fluxMutationArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("flux.reconcile: invalid arguments: %w", err)
	}
	if err := validateFluxMutationArgs(&args); err != nil {
		return nil, fmt.Errorf("flux.reconcile: %w", err)
	}
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("flux.reconcile: server env missing on context")
	}
	return patchFluxAnnotation(ctx, env, args)
}

// --- flux.suspend / flux.resume ----------------------------------------

// patchFluxSuspend applies the spec.suspend=<want> patch. Same
// rationale as patchFluxAnnotation — strategic-merge to avoid the
// resourceVersion race on a concurrent reconcile loop.
func patchFluxSuspend(ctx context.Context, env *Env, args fluxMutationArgs, want bool) (any, error) {
	ns, err := fluxScopedNamespace(env, args.Namespace)
	if err != nil {
		return nil, err
	}
	gvr, canonicalKind, err := fluxKindToGVR(args.Kind)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	patch := map[string]any{
		"spec": map[string]any{
			"suspend": want,
		},
	}
	patchBytes, _ := json.Marshal(patch)
	patched, err := client.Resource(gvr).Namespace(ns).
		Patch(ctx, args.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		verb := "suspend"
		if !want {
			verb = "resume"
		}
		return nil, fmt.Errorf("flux.%s: PATCH %s/%s in %s: %w", verb, canonicalKind, args.Name, ns, err)
	}
	verb := "Suspended"
	note := "Flux will stop reconciling this resource until spec.suspend is flipped to false."
	if !want {
		verb = "Resumed"
		note = "Flux will resume reconciling on the next watch tick."
	}
	return map[string]any{
		"status":           verb,
		"kind":             canonicalKind,
		"name":             args.Name,
		"namespace":        ns,
		"suspend":          want,
		"resource_version": patched.GetResourceVersion(),
		"note":             note,
	}, nil
}

func fluxSuspend(ctx context.Context, raw json.RawMessage) (any, error) {
	var args fluxMutationArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("flux.suspend: invalid arguments: %w", err)
	}
	if err := validateFluxMutationArgs(&args); err != nil {
		return nil, fmt.Errorf("flux.suspend: %w", err)
	}
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("flux.suspend: server env missing on context")
	}
	return patchFluxSuspend(ctx, env, args, true)
}

func fluxResume(ctx context.Context, raw json.RawMessage) (any, error) {
	var args fluxMutationArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("flux.resume: invalid arguments: %w", err)
	}
	if err := validateFluxMutationArgs(&args); err != nil {
		return nil, fmt.Errorf("flux.resume: %w", err)
	}
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("flux.resume: server env missing on context")
	}
	return patchFluxSuspend(ctx, env, args, false)
}

func schemaFluxMutation() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"kind", "name"},
		"properties": map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"description": "Flux resource kind. One of: HelmRelease, Kustomization.",
				"pattern":     fluxKindRE.String(),
			},
			"name": map[string]any{
				"type":        "string",
				"description": "metadata.name of the Flux resource.",
				"pattern":     fluxNameRE.String(),
			},
			"namespace": map[string]any{
				"type":        "string",
				"description": "Defaults to the Sandbox namespace.",
			},
		},
		"additionalProperties": false,
	}
}
