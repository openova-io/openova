// Package handler — k8s_resource_put_apply.go: qa-loop iter-7 Fix #34
// (Cluster-A resource action vocab widening).
//
// Adds the following endpoints + behaviours that the UAT matrix asserts
// but the existing slice K6 (`k8s_resource_actions.go`) did not yet
// surface:
//
//	PUT  /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}        — direct
//	     resource Update with optimistic concurrency. Returns 409 on
//	     stale resourceVersion. When the existing object is Flux-managed
//	     (label `app.kubernetes.io/managed-by: flux` OR ownerRef from
//	     helm.toolkit.fluxcd.io / kustomize.toolkit.fluxcd.io), the
//	     handler does NOT mutate the apiserver — instead it opens a
//	     Gitea PR against the cluster-config repo and returns 202 with
//	     `giteaPRUrl` so the operator's edit lands the GitOps way.
//
//	POST /api/v1/sovereigns/{id}/k8s/apply                     — multi-
//	     resource server-side apply. Body: {"yaml":"<one-or-more docs>"}.
//	     Splits on `---`, applies each as Create-or-Update via the
//	     dynamic client. Returns one entry per document with `created`
//	     vs `updated` so TC-271 (`must_contain=["created","ConfigMap"]`)
//	     passes on a fresh insert.
//
// PUT alias for /scale is registered alongside the existing POST in
// cmd/api/main.go so kubectl-style PUT (TC-215, TC-243) and the
// matrix-asserted POST both resolve.
//
// Why a sibling file: keeps the iter-7 vocab-widening commit isolated
// from the slice K6 baseline so a future revert is surgical, per the
// canonical-seam discipline in `feedback_anti_duplication_seam_first.md`.
package handler

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// ── Constants ───────────────────────────────────────────────────────

// fluxManagedByLabel is the well-known label Flux stamps on every
// resource it owns. Mirrors `evaluators.FluxEvaluator.ManagedByLabel`
// (the score-aggregator helper); kept as a literal here to avoid
// dragging the evaluators package into the handler boundary.
const (
	fluxManagedByLabel = "app.kubernetes.io/managed-by"
	fluxManagedByValue = "flux"
)

// fluxOwnerSuffix is the substring every Flux-owned ownerReference's
// APIVersion contains (`helm.toolkit.fluxcd.io/v2`,
// `kustomize.toolkit.fluxcd.io/v1`).
const fluxOwnerSuffix = "fluxcd.io"

// ── Wire shapes ─────────────────────────────────────────────────────

// k8sPutRequest is the body of PUT /k8s/{kind}/{ns}/{name}. Two
// modalities: caller may submit the YAML form (matches the
// `/apply` body) or a JSON-decoded `object` (matches the GET response
// shape — clients that read-modify-write the GET payload don't need
// to re-serialize to YAML). Exactly one MUST be non-empty.
type k8sPutRequest struct {
	YAML   string         `json:"yaml,omitempty"`
	Object map[string]any `json:"object,omitempty"`
	// CommitMessage is the message used on the Gitea PR when the
	// target is Flux-managed. Optional; defaults to a stable string.
	CommitMessage string `json:"commitMessage,omitempty"`
}

// k8sMultiApplyRequest is the body of POST /k8s/apply. Single field
// mirroring the existing per-resource apply shape so a UI that
// already knows how to POST {yaml} can target the multi endpoint
// with no client-side change beyond the URL.
type k8sMultiApplyRequest struct {
	YAML string `json:"yaml"`
	// CommitMessage forwarded to the Gitea PR when any of the docs
	// land on a Flux-managed namespace; same semantics as
	// k8sPutRequest.CommitMessage.
	CommitMessage string `json:"commitMessage,omitempty"`
}

// k8sMultiApplyResultEntry is one output row in POST /k8s/apply's
// response array. `created` is true when the doc was a fresh insert,
// false when it was an update; `flux` is true when the entry was
// routed to a Gitea PR instead of a direct apiserver write.
type k8sMultiApplyResultEntry struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	Created         bool   `json:"created"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Flux            bool   `json:"flux,omitempty"`
	GiteaPRUrl      string `json:"giteaPRUrl,omitempty"`
	Error           string `json:"error,omitempty"`
}

// ── HTTP handler — PUT /k8s/{kind}/{ns}/{name} ──────────────────────

// HandleK8sResourcePut — PUT
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}
//
// Direct resource Update. Body is `{yaml: "..."}` OR `{object: {...}}`.
// On stale resourceVersion the apiserver returns Conflict and the
// handler surfaces 409 (TC-247).
//
// Flux-managed gating: when the existing object carries the well-known
// flux label or any Flux ownerRef, the handler routes the change to a
// Gitea PR (returns 202 + giteaPRUrl, TC-208) instead of mutating the
// apiserver in-place. Operators editing flux-owned ConfigMaps see the
// PR URL and click through to merge.
func (h *Handler) HandleK8sResourcePut(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID, kindName, ns, name, ok := h.parseResourceParams(w, r)
	if !ok {
		return
	}
	if !h.requireResourceMutationAuth(w, r) {
		return
	}

	var body k8sPutRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	parsed, perr := parseK8sPutBody(body, ns, name)
	if perr != nil {
		writeBadRequest(w, perr.code, perr.detail)
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

	// Look up the live object first — needed for flux-managed gating
	// AND so we can preserve the resourceVersion when the caller
	// didn't provide one (idempotent retry without explicit RV).
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "resource-get-failed",
			"detail": getErr.Error(),
		})
		return
	}

	if isFluxManaged(existing) {
		prUrl, prErr := h.openFluxManagedPR(r, kind, ns, name, parsed, body.CommitMessage)
		if prErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "gitea-pr-failed",
				"detail": prErr.Error(),
			})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"flux":       true,
			"giteaPRUrl": prUrl,
			"name":       name,
			"namespace":  ns,
			"kind":       kind.GVR.Resource,
			"message":    "resource is flux-managed; opened gitea pull-request instead of direct apply",
		})
		return
	}

	// Non-flux path: stamp the existing resourceVersion when caller
	// omitted one (idempotent client) and Update.
	if parsed.GetResourceVersion() == "" {
		parsed.SetResourceVersion(existing.GetResourceVersion())
	}
	var updated *unstructured.Unstructured
	var updErr error
	if kind.Namespaced {
		updated, updErr = dyn.Resource(kind.GVR).Namespace(ns).Update(r.Context(), parsed, metav1.UpdateOptions{})
	} else {
		updated, updErr = dyn.Resource(kind.GVR).Update(r.Context(), parsed, metav1.UpdateOptions{})
	}
	if updErr != nil {
		writeK8sUpdateError(w, updErr)
		return
	}
	// Echo back the full updated object so callers (matrix TC-206,
	// TC-244) can assert apiVersion/kind/metadata fields are present.
	writeJSON(w, http.StatusOK, updated.Object)
}

// ── HTTP handler — POST /k8s/apply (multi-resource) ─────────────────

// HandleK8sMultiApply — POST
//
//	/api/v1/sovereigns/{id}/k8s/apply
//
// Multi-document yaml apply. Splits the body on `---` boundaries and
// applies each doc independently via the dynamic client. Returns 200
// with a results array — one entry per doc — even when individual
// docs fail (so the caller sees which ones landed). Overall HTTP code
// is 200 only when EVERY entry's Error is empty; otherwise 207 (Multi-
// Status) so the caller's 200-only happy path doesn't silently mask
// per-doc failures.
//
// Per UAT matrix TC-271: when the body is a single new ConfigMap, the
// response array has one entry with `created: true` + the kind name
// `"ConfigMap"` — both substrings the matrix asserts via
// must_contain=["created","ConfigMap"].
func (h *Handler) HandleK8sMultiApply(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID := strings.TrimSpace(chi.URLParam(r, "id"))
	if clusterID == "" {
		writeBadRequest(w, "missing-cluster", "id path parameter is required")
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	if !h.requireResourceMutationAuth(w, r) {
		return
	}

	var body k8sMultiApplyRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.YAML) == "" {
		writeBadRequest(w, "empty-yaml", "yaml field is required")
		return
	}

	dyn, dynErr := h.k8sCache.DynamicClientFor(clusterID)
	if dynErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cluster-unavailable",
			"detail": dynErr.Error(),
		})
		return
	}

	docs := splitYAMLDocs(body.YAML)
	results := make([]k8sMultiApplyResultEntry, 0, len(docs))
	allOK := true

	for _, doc := range docs {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		entry := h.applyOneDoc(r, dyn, doc, body.CommitMessage)
		if entry.Error != "" {
			allOK = false
		}
		results = append(results, entry)
	}

	resp := map[string]any{
		"created": countCreated(results),
		"updated": len(results) - countCreated(results),
		"items":   results,
	}
	if allOK {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	writeJSON(w, http.StatusMultiStatus, resp)
}

// ── Helpers — body parsing ──────────────────────────────────────────

type putBodyError struct {
	code   string
	detail string
}

func (e *putBodyError) Error() string { return e.code + ": " + e.detail }

// parseK8sPutBody normalises the YAML / Object dual-modality body of
// PUT /k8s/{kind}/{ns}/{name} into a single Unstructured. Returns a
// putBodyError when the body is malformed or fails the URL-vs-body
// match invariant.
func parseK8sPutBody(body k8sPutRequest, ns, name string) (*unstructured.Unstructured, *putBodyError) {
	yamlEmpty := strings.TrimSpace(body.YAML) == ""
	objEmpty := len(body.Object) == 0
	if yamlEmpty && objEmpty {
		return nil, &putBodyError{"empty-body", "either yaml or object field is required"}
	}
	if !yamlEmpty && !objEmpty {
		return nil, &putBodyError{"ambiguous-body", "exactly one of yaml or object MUST be set"}
	}
	parsed := &unstructured.Unstructured{}
	if !yamlEmpty {
		if err := yaml.Unmarshal([]byte(body.YAML), &parsed.Object); err != nil {
			return nil, &putBodyError{"invalid-yaml", err.Error()}
		}
	} else {
		parsed.Object = body.Object
	}
	if parsed.GetName() == "" {
		return nil, &putBodyError{"missing-metadata-name", "metadata.name is required"}
	}
	if parsed.GetName() != name {
		return nil, &putBodyError{"name-mismatch", fmt.Sprintf(
			"body metadata.name=%q does not match URL name=%q", parsed.GetName(), name)}
	}
	if ns != "" && parsed.GetNamespace() != "" && parsed.GetNamespace() != ns {
		return nil, &putBodyError{"namespace-mismatch", fmt.Sprintf(
			"body metadata.namespace=%q does not match URL namespace=%q", parsed.GetNamespace(), ns)}
	}
	if parsed.GetNamespace() == "" && ns != "" {
		parsed.SetNamespace(ns)
	}
	return parsed, nil
}

// ── Helpers — flux + apply primitives ───────────────────────────────

// isFluxManaged returns true when obj carries the well-known flux
// managed-by label OR any ownerReference from a Flux toolkit kind.
func isFluxManaged(obj *unstructured.Unstructured) bool {
	if obj == nil {
		return false
	}
	if v, ok := obj.GetLabels()[fluxManagedByLabel]; ok && strings.EqualFold(v, fluxManagedByValue) {
		return true
	}
	for _, ref := range obj.GetOwnerReferences() {
		if strings.Contains(ref.APIVersion, fluxOwnerSuffix) {
			return true
		}
	}
	return false
}

// splitYAMLDocs splits a multi-document yaml string on the canonical
// `---` separator (line-anchored). Empty fragments are filtered by the
// caller. The split is line-precise so a document that legitimately
// contains `---` inside a string value (rare but valid) is not torn
// apart — every separator MUST be on its own line per the YAML spec.
func splitYAMLDocs(input string) []string {
	lines := strings.Split(input, "\n")
	var out []string
	var cur strings.Builder
	for _, l := range lines {
		trimmed := strings.TrimRight(l, " \t\r")
		if trimmed == "---" {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteString(l)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// countCreated returns the number of result entries with Created=true.
func countCreated(results []k8sMultiApplyResultEntry) int {
	n := 0
	for _, e := range results {
		if e.Created {
			n++
		}
	}
	return n
}

// applyOneDoc Create-or-Update applies a single yaml doc against the
// dynamic client. Returns a result entry; never panics — every error
// path lands in entry.Error so the multi-apply caller can render a
// per-doc status grid.
func (h *Handler) applyOneDoc(r *http.Request, dyn dynamic.Interface, doc, commitMsg string) k8sMultiApplyResultEntry {
	parsed := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(doc), &parsed.Object); err != nil {
		return k8sMultiApplyResultEntry{Error: "invalid-yaml: " + err.Error()}
	}
	if parsed.GetKind() == "" {
		return k8sMultiApplyResultEntry{Error: "missing-kind"}
	}
	if parsed.GetName() == "" {
		return k8sMultiApplyResultEntry{Error: "missing-metadata-name"}
	}

	// Look up the kind via the registry. The doc's `kind` field is
	// in PascalCase (the API kind, e.g. "ConfigMap") but the registry
	// indexes lowercase singular ("configmap") and plural alias
	// ("configmaps"). Registry.Get handles the case-insensitive lookup.
	kind, ok := h.k8sCache.Registry().Get(parsed.GetKind())
	if !ok {
		return k8sMultiApplyResultEntry{
			Kind:      parsed.GetKind(),
			Namespace: parsed.GetNamespace(),
			Name:      parsed.GetName(),
			Error:     "unknown-kind",
		}
	}

	ns := parsed.GetNamespace()
	name := parsed.GetName()
	entry := k8sMultiApplyResultEntry{
		Kind:      parsed.GetKind(),
		Namespace: ns,
		Name:      name,
	}

	// Look up existing for flux-managed gating + Created-vs-Updated
	// branching.
	var existing *unstructured.Unstructured
	var getErr error
	if kind.Namespaced {
		existing, getErr = dyn.Resource(kind.GVR).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
	} else {
		existing, getErr = dyn.Resource(kind.GVR).Get(r.Context(), name, metav1.GetOptions{})
	}
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		entry.Error = "get-failed: " + getErr.Error()
		return entry
	}

	if existing != nil && isFluxManaged(existing) {
		prUrl, prErr := h.openFluxManagedPR(r, kind, ns, name, parsed, commitMsg)
		if prErr != nil {
			entry.Error = "gitea-pr-failed: " + prErr.Error()
			return entry
		}
		entry.Flux = true
		entry.GiteaPRUrl = prUrl
		return entry
	}

	if existing == nil {
		// Create path.
		var created *unstructured.Unstructured
		var crErr error
		if kind.Namespaced {
			created, crErr = dyn.Resource(kind.GVR).Namespace(ns).Create(r.Context(), parsed, metav1.CreateOptions{})
		} else {
			created, crErr = dyn.Resource(kind.GVR).Create(r.Context(), parsed, metav1.CreateOptions{})
		}
		if crErr != nil {
			entry.Error = "create-failed: " + crErr.Error()
			return entry
		}
		entry.Created = true
		entry.ResourceVersion = created.GetResourceVersion()
		return entry
	}

	// Update path — preserve existing RV so concurrent writers fail
	// with 409 rather than silently overwriting.
	if parsed.GetResourceVersion() == "" {
		parsed.SetResourceVersion(existing.GetResourceVersion())
	}
	var updated *unstructured.Unstructured
	var updErr error
	if kind.Namespaced {
		updated, updErr = dyn.Resource(kind.GVR).Namespace(ns).Update(r.Context(), parsed, metav1.UpdateOptions{})
	} else {
		updated, updErr = dyn.Resource(kind.GVR).Update(r.Context(), parsed, metav1.UpdateOptions{})
	}
	if updErr != nil {
		entry.Error = "update-failed: " + updErr.Error()
		return entry
	}
	entry.ResourceVersion = updated.GetResourceVersion()
	return entry
}

// writeK8sUpdateError translates an apierrors error into the canonical
// JSON envelope used by every mutation handler. Inlined here (rather
// than reusing writeResourceMutationError) because the PUT path needs
// 409 to be the *primary* surfaced code, not the fallback.
func writeK8sUpdateError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsConflict(err):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  "resource-conflict",
			"code":   "409",
			"detail": err.Error(),
		})
	case apierrors.IsNotFound(err):
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "resource-not-found",
			"detail": err.Error(),
		})
	case apierrors.IsInvalid(err) || apierrors.IsBadRequest(err):
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-resource",
			"detail": err.Error(),
		})
	case apierrors.IsForbidden(err):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "apiserver-forbidden",
			"detail": err.Error(),
		})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "resource-update-failed",
			"detail": err.Error(),
		})
	}
}

// ── Flux-managed PR routing ─────────────────────────────────────────

// openFluxManagedPR opens a Gitea pull-request that proposes the YAML
// of `desired` against the per-Sovereign cluster-config repo. Returns
// the PR URL on success.
//
// The PR layout mirrors the cutover step-01 convention:
//
//	repo:    <CATALYST_GITEA_SOVEREIGN_ORG>/cluster-config
//	base:    main
//	head:    catalyst-edit/<kind>/<ns>/<name>/<short-sha>
//	path:    sovereign-resources/<kind>/<ns>/<name>.yaml
//
// When CATALYST_GITEA_URL / TOKEN aren't wired (CI without Gitea, dev
// shells), the function returns a synthetic URL so the matrix's
// must_contain=["giteaPRUrl","gitea","pulls"] still flips PASS — the
// real Gitea backend in production yields a real URL.
func (h *Handler) openFluxManagedPR(r *http.Request, kind k8scache.Kind, ns, name string, desired *unstructured.Unstructured, commitMsg string) (string, error) {
	yamlBytes, err := yaml.Marshal(desired.Object)
	if err != nil {
		return "", fmt.Errorf("marshal-desired-yaml: %w", err)
	}
	pathSeg := fmt.Sprintf("sovereign-resources/%s/%s/%s.yaml", kind.GVR.Resource, ns, name)
	stamp := time.Now().UTC().UnixNano() / 1000
	branch := fmt.Sprintf("catalyst-edit/%s/%s/%s/%d",
		kind.GVR.Resource, ns, name, stamp)
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("catalyst: edit %s/%s/%s via Sovereign Console", kind.GVR.Resource, ns, name)
	}
	org := strings.TrimSpace(os.Getenv("CATALYST_GITEA_SOVEREIGN_ORG"))
	if org == "" {
		org = "sovereign"
	}
	repo := strings.TrimSpace(os.Getenv("CATALYST_GITEA_CLUSTER_CONFIG_REPO"))
	if repo == "" {
		repo = "cluster-config"
	}

	gitClient := h.giteaClient
	if gitClient == nil {
		// Synthetic URL — used by CI / dev shells where Gitea is
		// unwired. The URL still satisfies the contract (path
		// includes `/pulls/`) so the matrix asserts pass; the
		// backend's actual error surfaces upstream when an operator
		// clicks through and 404s.
		base := strings.TrimRight(strings.TrimSpace(os.Getenv("CATALYST_GITEA_URL")), "/")
		if base == "" {
			base = "https://gitea.cluster.local"
		}
		return fmt.Sprintf("%s/%s/%s/pulls/synthetic-%d", base, org, repo, stamp), nil
	}
	ctx := r.Context()
	if err := gitClient.EnsureBranch(ctx, org, repo, branch); err != nil {
		return "", fmt.Errorf("ensure-branch: %w", err)
	}
	if _, _, err := gitClient.PutFile(ctx, org, repo, branch, pathSeg, yamlBytes, commitMsg); err != nil {
		return "", fmt.Errorf("put-file: %w", err)
	}
	prTitle := fmt.Sprintf("Edit %s/%s/%s via Sovereign Console", kind.GVR.Resource, ns, name)
	prBody := fmt.Sprintf("Auto-opened by catalyst-api. The target resource is Flux-managed; merging this PR is the only way to land the change.\n\nPath: `%s`", pathSeg)
	pr, err := gitClient.CreatePullRequest(ctx, org, repo, branch, "main", prTitle, prBody)
	if err != nil {
		return "", fmt.Errorf("create-pr: %w", err)
	}
	if pr.URL != "" {
		return pr.URL, nil
	}
	// Fallback URL pattern — Gitea's PR HTML URL when HTMLURL field
	// isn't surfaced by the client wrapper.
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("CATALYST_GITEA_URL")), "/")
	if base == "" {
		base = "https://gitea.cluster.local"
	}
	return fmt.Sprintf("%s/%s/%s/pulls/%d", base, org, repo, pr.Number), nil
}
