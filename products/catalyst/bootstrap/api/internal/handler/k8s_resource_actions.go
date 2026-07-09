// Package handler — k8s_resource_actions.go: EPIC-4 Slice R6 (#1099)
// per-row resource actions (scale / restart / delete) + Slice R3 (YAML
// editor's apply / dry-run paths).
//
// REST surface added by this slice:
//
//	POST   /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale     — body {replicas:N}
//	POST   /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/restart   — kubectl-style rollout restart
//	POST   /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/dry-run   — body {yaml}; server-side validation
//	POST   /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/apply     — body {yaml}; live apply
//	DELETE /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}           — delete the resource
//
// Authorization: every mutating endpoint is gated tier-admin or higher
// (same shape as `applicationInstallCallerAuthorized` from slice I — the
// canonical seam for tier-admin gates). UI hides the button when the
// caller's session doesn't qualify, but the server is the authoritative
// gate (per INVIOLABLE-PRINCIPLES.md #5).
//
// All resource resolution flows through the per-cluster dynamic client
// owned by k8scache.Factory (DynamicClientFor). No second per-cluster
// pool, no fresh REST configs — same lifecycle as the informers per
// ADR-0001 §5.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// ── Wire shapes ─────────────────────────────────────────────────────

type k8sScaleRequest struct {
	Replicas int `json:"replicas"`
}

type k8sApplyRequest struct {
	YAML string `json:"yaml"`
}

// ── HTTP handler — scale (POST .../scale) ───────────────────────────

// HandleK8sResourceScale — POST
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale
//
// Body: {"replicas": <N>}. Allowed only on Deployment + StatefulSet
// kinds. Patches `spec.replicas` via merge-patch. Returns 200 + the
// patched object's `spec.replicas` echo.
func (h *Handler) HandleK8sResourceScale(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	// Authorization FIRST (qa-loop iter-9 Fix #43, Cluster-A): tier
	// gate runs before path-param parsing + kind validation so a
	// viewer/developer caller receives 403 regardless of whether the
	// kind/ns/name resolve. Matches the iter-9 contract for
	// /policy, /applications, /rbac/assign, /switchover. The {id}
	// chi param is read directly to avoid the resolveChrootClusterID
	// → registry round-trip which would 404 before the auth gate runs.
	if !h.requireResourceMutationAuth(w, r) {
		return
	}
	clusterID, kindName, ns, name, ok := h.parseResourceParams(w, r)
	if !ok {
		return
	}
	if !isScalableKind(kindName) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "kind-not-scalable",
			"detail": fmt.Sprintf("scale only supported on Deployment + StatefulSet (got %q)", kindName),
		})
		return
	}

	var body k8sScaleRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if body.Replicas < 0 {
		writeBadRequest(w, "invalid-replicas", "replicas must be >= 0")
		return
	}
	if body.Replicas > 1000 {
		writeBadRequest(w, "invalid-replicas", "replicas cannot exceed 1000")
		return
	}

	dyn, kind, err := h.resolveResourceClient(clusterID, kindName)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cluster-unavailable",
			"detail": err.Error(),
		})
		return
	}
	patch := map[string]any{"spec": map[string]any{"replicas": body.Replicas}}
	patchBytes, _ := json.Marshal(patch)
	updated, patchErr := dyn.Resource(kind.GVR).Namespace(ns).Patch(
		r.Context(), name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if patchErr != nil {
		writeResourceMutationError(w, patchErr, "scale")
		return
	}
	resp := map[string]any{
		"name":      updated.GetName(),
		"namespace": updated.GetNamespace(),
		"replicas":  body.Replicas,
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── HTTP handler — restart (POST .../restart) ───────────────────────

// HandleK8sResourceRestart — POST
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/restart
//
// Replicates `kubectl rollout restart` by stamping
// `kubectl.kubernetes.io/restartedAt` on the pod-template metadata.
// Allowed on Deployment, StatefulSet, DaemonSet.
func (h *Handler) HandleK8sResourceRestart(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	// Authorization FIRST (qa-loop iter-9 Fix #43, Cluster-A).
	if !h.requireResourceMutationAuth(w, r) {
		return
	}
	clusterID, kindName, ns, name, ok := h.parseResourceParams(w, r)
	if !ok {
		return
	}
	if !isRestartableKind(kindName) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "kind-not-restartable",
			"detail": fmt.Sprintf("restart only supported on Deployment / StatefulSet / DaemonSet (got %q)", kindName),
		})
		return
	}

	dyn, kind, err := h.resolveResourceClient(clusterID, kindName)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cluster-unavailable",
			"detail": err.Error(),
		})
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"kubectl.kubernetes.io/restartedAt": stamp,
					},
				},
			},
		},
	}
	patchBytes, _ := json.Marshal(patch)
	updated, patchErr := dyn.Resource(kind.GVR).Namespace(ns).Patch(
		r.Context(), name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if patchErr != nil {
		writeResourceMutationError(w, patchErr, "restart")
		return
	}
	resp := map[string]any{
		"name":        updated.GetName(),
		"namespace":   updated.GetNamespace(),
		"restartedAt": stamp,
		// `restarted: true` is the canonical contract field the UAT
		// matrix asserts against (TC-218 must_contain=["restarted",
		// "restartedAt"]).
		"restarted": true,
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── HTTP handler — delete (DELETE .../{name}) ───────────────────────

// HandleK8sResourceDelete — DELETE
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}
//
// Deletes the resource. Same tier-admin auth gate as scale/restart.
// Confirmation modal lives in the UI (R6); the server treats DELETE as
// the commit step.
func (h *Handler) HandleK8sResourceDelete(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	// Authorization FIRST (qa-loop iter-9 Fix #43, Cluster-A).
	if !h.requireResourceMutationAuth(w, r) {
		return
	}
	clusterID, kindName, ns, name, ok := h.parseResourceParams(w, r)
	if !ok {
		return
	}
	dyn, kind, err := h.resolveResourceClient(clusterID, kindName)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cluster-unavailable",
			"detail": err.Error(),
		})
		return
	}
	var delErr error
	if kind.Namespaced {
		delErr = dyn.Resource(kind.GVR).Namespace(ns).Delete(r.Context(), name, metav1.DeleteOptions{})
	} else {
		delErr = dyn.Resource(kind.GVR).Delete(r.Context(), name, metav1.DeleteOptions{})
	}
	if delErr != nil {
		writeResourceMutationError(w, delErr, "delete")
		return
	}
	// `deleted: true` is the canonical contract field every UAT-matrix
	// row asserts against (TC-080, TC-222 — both `must_contain=["deleted"]`).
	// `message: "delete requested"` is retained for human-readable
	// dashboards but is NOT what the UAT shells grep for.
	writeJSON(w, http.StatusOK, map[string]any{
		"name":      name,
		"namespace": ns,
		"kind":      kind.GVR.Resource,
		"deleted":   true,
		"message":   "delete requested",
	})
}

// ── HTTP handler — dry-run (POST .../dry-run) ───────────────────────

// HandleK8sResourceDryRun — POST
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/dry-run
//
// Body: {"yaml": "<full yaml document>"}. Performs a server-side
// dry-run validation (Update with `DryRun: [All]`) and returns the
// validated object on 200 or a 400 with the validation errors.
func (h *Handler) HandleK8sResourceDryRun(w http.ResponseWriter, r *http.Request) {
	h.handleApplyOrDryRun(w, r, true)
}

// HandleK8sResourceApply — POST
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/apply
//
// Body: {"yaml": "<full yaml document>"}. Performs a real Update.
// Caller has typically called dry-run first; the server still validates
// the YAML parses + matches the URL kind/name/ns before the write.
func (h *Handler) HandleK8sResourceApply(w http.ResponseWriter, r *http.Request) {
	h.handleApplyOrDryRun(w, r, false)
}

func (h *Handler) handleApplyOrDryRun(w http.ResponseWriter, r *http.Request, dryRun bool) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	// Authorization FIRST (qa-loop iter-9 Fix #43, Cluster-A).
	if !h.requireResourceMutationAuth(w, r) {
		return
	}
	clusterID, kindName, ns, name, ok := h.parseResourceParams(w, r)
	if !ok {
		return
	}

	var body k8sApplyRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.YAML) == "" {
		writeBadRequest(w, "empty-yaml", "yaml field is required")
		return
	}

	// Parse YAML → unstructured. Reject when the doc's metadata.name /
	// .namespace / .kind don't match the URL — protects against UI bugs
	// that submit the wrong resource against the wrong path.
	parsed := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(body.YAML), &parsed.Object); err != nil {
		writeBadRequest(w, "invalid-yaml", err.Error())
		return
	}
	if parsed.GetName() == "" {
		writeBadRequest(w, "missing-metadata-name", "metadata.name is required")
		return
	}
	if parsed.GetName() != name && !reconcileBlueprintBareName(parsed, kindName, name) {
		writeBadRequest(w, "name-mismatch", fmt.Sprintf(
			"yaml metadata.name=%q does not match URL name=%q", parsed.GetName(), name))
		return
	}
	if ns != "" && parsed.GetNamespace() != "" && parsed.GetNamespace() != ns {
		writeBadRequest(w, "namespace-mismatch", fmt.Sprintf(
			"yaml metadata.namespace=%q does not match URL namespace=%q", parsed.GetNamespace(), ns))
		return
	}
	// If yaml omits namespace and the URL has one, stamp it.
	if parsed.GetNamespace() == "" && ns != "" {
		parsed.SetNamespace(ns)
	}

	dyn, kind, err := h.resolveResourceClient(clusterID, kindName)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cluster-unavailable",
			"detail": err.Error(),
		})
		return
	}

	opts := metav1.UpdateOptions{}
	if dryRun {
		opts.DryRun = []string{"All"}
	}

	// #4860 (Refs #3668, #4844) — the Edit-IaC editor submits the CR YAML
	// without metadata.resourceVersion, but a plain Update requires one for
	// optimistic concurrency, so the apiserver 400s with
	//   metadata.resourceVersion: Invalid value: 0x0: must be specified for
	//   an update
	// (caught live on hw228, bp-wordpress Validate(dry-run)). The sibling
	// PUT-apply handler (k8s_resource_put_apply.go) already Gets the live
	// object and stamps its resourceVersion when the caller omits one; the
	// dry-run/apply path never did. Mirror that: when parsed omits an RV,
	// fetch the live object and stamp it so both the dry-run validation and
	// a same-RV apply carry a valid concurrency token.
	if parsed.GetResourceVersion() == "" {
		var existing *unstructured.Unstructured
		var getErr error
		if kind.Namespaced {
			existing, getErr = dyn.Resource(kind.GVR).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
		} else {
			existing, getErr = dyn.Resource(kind.GVR).Get(r.Context(), name, metav1.GetOptions{})
		}
		if getErr != nil {
			if apierrors.IsNotFound(getErr) {
				writeJSON(w, http.StatusNotFound, map[string]string{
					"error":  "resource-not-found",
					"detail": getErr.Error(),
				})
				return
			}
			// #4860 — an RBAC denial on the resourceVersion pre-fetch is a
			// CLIENT/permission error, not a server fault; surface 403 like
			// the sibling read path (k8s_resource_get.go) does. Before this
			// case a Forbidden fell through to the 500 below, which is the
			// exact "Validate(dry-run) → HTTP 500" symptom seen on hw228
			// when catalyst-api's SA lacked blueprints get/update (later
			// granted by #4862, but the handler must still classify the
			// error correctly for any cluster where the grant is absent).
			if apierrors.IsForbidden(getErr) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error":  "apiserver-forbidden",
					"code":   "403",
					"detail": getErr.Error(),
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error":  "resource-get-failed",
				"detail": getErr.Error(),
			})
			return
		}
		parsed.SetResourceVersion(existing.GetResourceVersion())
	}

	var updated *unstructured.Unstructured
	var updErr error
	if kind.Namespaced {
		updated, updErr = dyn.Resource(kind.GVR).Namespace(ns).Update(r.Context(), parsed, opts)
	} else {
		updated, updErr = dyn.Resource(kind.GVR).Update(r.Context(), parsed, opts)
	}
	if updErr != nil {
		// Validation errors and conflicts surface via apierrors; surface
		// 400 for invalid + 403 for forbidden + 409 for conflict + 404 for
		// not-found + 500 otherwise.
		if apierrors.IsInvalid(updErr) || apierrors.IsBadRequest(updErr) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":  "invalid-resource",
				"detail": updErr.Error(),
			})
			return
		}
		// #4860 — an apiserver RBAC denial on the dry-run/apply Update is a
		// permission (4xx) error, not a server fault. This case was the one
		// missing from the switch below, so a Forbidden bubbled to the 500
		// default and the console saw "Validate(dry-run)/Commit → HTTP 500"
		// (caught live on hw228, bp-wordpress). Match the sibling PUT path
		// (writeK8sUpdateError) + scale/restart/delete (writeResourceMutationError).
		if apierrors.IsForbidden(updErr) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "apiserver-forbidden",
				"code":   "403",
				"detail": updErr.Error(),
			})
			return
		}
		if apierrors.IsConflict(updErr) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "resource-conflict",
				"detail": updErr.Error(),
			})
			return
		}
		if apierrors.IsNotFound(updErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "resource-not-found",
				"detail": updErr.Error(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "resource-update-failed",
			"detail": updErr.Error(),
		})
		return
	}
	resp := map[string]any{
		"name":            updated.GetName(),
		"namespace":       updated.GetNamespace(),
		"dryRun":          dryRun,
		"resourceVersion": updated.GetResourceVersion(),
	}
	writeJSON(w, http.StatusOK, resp)
}

// reconcileBlueprintBareName bridges the Blueprint `bp-` prefix convention in
// the catalog Edit-IaC (YamlEditor) dry-run/apply path (#4896).
//
// The catalog IaC editor seeds itself from the AUTHORED `blueprint.yaml`, whose
// `metadata.name` is the BARE slug (e.g. "alloy"), while the in-cluster
// Blueprint CR — and therefore the request URL — use the canonical `bp-` prefix
// (e.g. "bp-alloy") per docs/ARCHITECTURE §4 (Naming) + the chart's
// catalog-seed. A dry-run/apply of the UNCHANGED blueprint.yaml would otherwise
// fail the name-match guard with
//
//	400 "yaml metadata.name='alloy' does not match URL name='bp-alloy'"
//
// even though it names the very same CR, blocking every full-CR IaC commit for
// bp-prefixed blueprints (hw232, UAT rows 145/148/154).
//
// When (and only when) the resource is a Blueprint AND the URL name is exactly
// "bp-"+parsed.name, the two are the SAME identity: we stamp the URL form onto
// the parsed object so the downstream k8s Update targets the real `bp-<slug>`
// CR, and report the match as reconciled. Every other case — a different kind,
// or a genuine rename (bare `alloy`→`loki`, or prefixed `bp-alloy`→`bp-loki`) —
// still fails the guard, so its protective purpose (rejecting a mis-targeted /
// renamed CR) is preserved.
func reconcileBlueprintBareName(parsed *unstructured.Unstructured, kindName, urlName string) bool {
	// kindName is the canonical registry Kind.Name (see parseResourceParams);
	// the Blueprint CRD registers as "blueprint".
	if kindName != "blueprint" {
		return false
	}
	if urlName == "bp-"+parsed.GetName() {
		parsed.SetName(urlName)
		return true
	}
	return false
}

// ── Internal helpers ────────────────────────────────────────────────

// parseResourceParams extracts the canonical (clusterID, kind, ns, name)
// tuple from a chi-routed request. Returns (..., false) when any
// required path-param is missing — caller should early-return.
//
// The returned `kind` string is the CANONICAL singular Kind.Name
// (resolved through k8scache.Registry.Get → byName/byPlural/short
// alias), NOT the raw URL segment. This is what every downstream
// helper (isScalableKind, isRestartableKind, resolveResourceClient,
// flux-managed gating) MUST receive — the matrix and `kubectl` muscle
// memory both surface plurals (`/k8s/deployments/...`,
// `/k8s/configmaps/...`) and the helpers were silently rejecting
// every plural URL with `kind-not-restartable`/`kind-not-scalable`
// before this canonicalisation step landed (qa-loop iter-7 TC-215,
// TC-218, TC-243 root cause).
func (h *Handler) parseResourceParams(w http.ResponseWriter, r *http.Request) (string, string, string, string, bool) {
	clusterID := chi.URLParam(r, "id")
	rawKind := chi.URLParam(r, "kind")
	ns := chi.URLParam(r, "ns")
	name := chi.URLParam(r, "name")
	if clusterID == "" || rawKind == "" || name == "" {
		http.Error(w, "missing path parameters", http.StatusBadRequest)
		return "", "", "", "", false
	}
	if ns == "_" {
		ns = ""
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	kind, ok := h.k8sCache.Registry().Get(rawKind)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":          "unknown-kind",
			"kind":           rawKind,
			"availableKinds": h.k8sCache.Registry().Names(),
		})
		return "", "", "", "", false
	}
	return clusterID, kind.Name, ns, name, true
}

// requireResourceMutationAuth enforces the tier-admin gate. Returns
// false (after writing a 403) when the caller's session lacks the
// required tier — caller early-returns.
//
// Same authorization shape as applicationInstallCallerAuthorized
// (canonical seam from slice I/T/X/A) — realm-role check OR custom
// `tier` claim ∈ {admin, owner}.
func (h *Handler) requireResourceMutationAuth(w http.ResponseWriter, r *http.Request) bool {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		// No auth wired in test → permit (mirrors the slice I/U pattern).
		return true
	}
	if !applicationInstallCallerAuthorized(claims) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden",
			"code":   "403",
			"detail": "K8s resource mutations require tier-admin or higher",
		})
		return false
	}
	return true
}

// resolveResourceClient returns the (dynamic.Interface, k8scache.Kind)
// tuple for the given kind on the given cluster. Wraps DynamicClientFor
// + Registry lookup so callers don't repeat the boilerplate. The kind
// is guaranteed-found because parseResourceParams already validated it.
func (h *Handler) resolveResourceClient(clusterID, kindName string) (dynamic.Interface, k8scache.Kind, error) {
	dyn, err := h.k8sCache.DynamicClientFor(clusterID)
	if err != nil {
		return nil, k8scache.Kind{}, err
	}
	kind, ok := h.k8sCache.Registry().Get(kindName)
	if !ok {
		return nil, k8scache.Kind{}, fmt.Errorf("kind %q not registered", kindName)
	}
	return dyn, kind, nil
}

// writeResourceMutationError translates an apierror into the canonical
// JSON envelope. Centralised so every action handler returns the same
// shape on the same kind of failure.
//
// Per UAT matrix conventions every error envelope carries an explicit
// `code` string field (the HTTP status code as a literal) so a body-
// substring matcher can assert `"403"` / `"409"` / `"404"` directly
// (TC-243 must_contain=["403"], TC-247 must_contain=["409"]).
func writeResourceMutationError(w http.ResponseWriter, err error, op string) {
	switch {
	case apierrors.IsNotFound(err):
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "resource-not-found",
			"code":   "404",
			"detail": err.Error(),
		})
	case apierrors.IsForbidden(err):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "apiserver-forbidden",
			"code":   "403",
			"detail": err.Error(),
		})
	case apierrors.IsConflict(err):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "resource-conflict",
			"code":   "409",
			"detail": err.Error(),
		})
	case apierrors.IsInvalid(err) || apierrors.IsBadRequest(err):
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-resource",
			"detail": err.Error(),
		})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  fmt.Sprintf("resource-%s-failed", op),
			"detail": err.Error(),
		})
	}
}

// normalizeKindName lower-cases the URL kind segment AND normalises
// plural / short-name / Kind-PascalCase variants to a canonical
// singular lowercase form. Per the k8scache.Registry alias map every
// caller-supplied form ("deployment", "deployments", "deploy", "Deployment")
// resolves to the same canonical kind for the {scalable, restartable}
// gates. Added qa-loop iter-7 Cluster-C (#1227) — TC-218 was
// posting kind="deployments" (plural) and getting kind-not-restartable
// because the gate's inner switch matched only the singular form.
func normalizeKindName(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	switch k {
	case "deployments", "deploy", "deploys":
		return "deployment"
	case "statefulsets", "sts":
		return "statefulset"
	case "daemonsets", "ds":
		return "daemonset"
	case "replicasets", "rs":
		return "replicaset"
	case "configmaps", "cm":
		return "configmap"
	case "secrets":
		return "secret"
	case "services", "svc":
		return "service"
	case "pods", "po":
		return "pod"
	case "namespaces", "ns":
		return "namespace"
	}
	return k
}

// isScalableKind — Deployment + StatefulSet only. ReplicaSet and
// DaemonSet (the latter doesn't take replicas at all) are excluded by
// design.
func isScalableKind(kind string) bool {
	switch normalizeKindName(kind) {
	case "deployment", "statefulset":
		return true
	}
	return false
}

// isRestartableKind — Deployment + StatefulSet + DaemonSet. Other
// workload kinds use other rollout mechanisms.
func isRestartableKind(kind string) bool {
	switch normalizeKindName(kind) {
	case "deployment", "statefulset", "daemonset":
		return true
	}
	return false
}
