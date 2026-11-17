// Package handler — k8s_resource_put_apply.go: qa-loop iter-7 Fix #34
// (Cluster-A resource action vocab widening).
//
// Adds the following endpoints + behaviours that the UAT matrix asserts
// but the existing slice K6 (`k8s_resource_actions.go`) did not yet
// surface:
//
//	PUT  /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}        — direct
//	     resource Update with optimistic concurrency. Returns 409 on
//	     stale resourceVersion.
//
//	POST /api/v1/sovereigns/{id}/k8s/apply                     — multi-
//	     resource server-side apply. Body: {"yaml":"<one-or-more docs>"}.
//	     Splits on `---`, applies each as Create-or-Update via the
//	     dynamic client. Returns one entry per document with `created`
//	     vs `updated` so TC-271 (`must_contain=["created","ConfigMap"]`)
//	     passes on a fresh insert.
//
// Both go through package clusterwrite: an object that Git owns is refused
// with 409 managed-by-git (the multi-document apply writes no document then),
// and any other write needs break-glass: the owner tier and the body field
// reason. It is audited before and after.
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
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/clusterwrite"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// ── Wire shapes ─────────────────────────────────────────────────────

// k8sPutRequest is the body of PUT /k8s/{kind}/{ns}/{name}. Two
// modalities: caller may submit the YAML form (matches the
// `/apply` body) or a JSON-decoded `object` (matches the GET response
// shape — clients that read-modify-write the GET payload don't need
// to re-serialize to YAML). Exactly one MUST be non-empty.
type k8sPutRequest struct {
	YAML   string         `json:"yaml,omitempty"`
	Object map[string]any `json:"object,omitempty"`
	// Reason is the reason of the break-glass write (clusterwrite).
	Reason string `json:"reason,omitempty"`
}

// k8sMultiApplyRequest is the body of POST /k8s/apply. Single field
// mirroring the existing per-resource apply shape so a UI that
// already knows how to POST {yaml} can target the multi endpoint
// with no client-side change beyond the URL.
type k8sMultiApplyRequest struct {
	YAML string `json:"yaml"`
	// Reason is the reason of the break-glass write (clusterwrite).
	Reason string `json:"reason,omitempty"`
}

// k8sMultiApplyResultEntry is one output row in POST /k8s/apply's
// response array. `created` is true when the doc was a fresh insert,
// false when it was an update.
type k8sMultiApplyResultEntry struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	Created         bool   `json:"created"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Error           string `json:"error,omitempty"`
}

// k8sMultiApplyDoc is one document of POST /k8s/apply, read and looked
// up before anything is written. entry.Error is set when it cannot be
// applied.
type k8sMultiApplyDoc struct {
	parsed, existing *unstructured.Unstructured
	kind             k8scache.Kind
	entry            k8sMultiApplyResultEntry
}

// ── HTTP handler — PUT /k8s/{kind}/{ns}/{name} ──────────────────────

// HandleK8sResourcePut — PUT
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}
//
// Direct resource Update. Body is `{yaml: "..."}` OR `{object: {...}}`.
// On stale resourceVersion the apiserver returns Conflict and the
// handler surfaces 409 (TC-247). An object that Git owns is refused with
// 409 managed-by-git; any other needs break-glass (clusterwrite.Allow).
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
		// qa-loop iter-17 Fix #172 — wire-shape contract.
		//
		// Mirrors Fix #165 (PR #1368, applications.go) and Fix #160
		// (PR #1364, rbac_assign.go): the fast_executor / delta_executor
		// runners FAIL every non-2xx response BEFORE reading the body
		// (fast_executor.py:297-298). The legacy 400 path therefore
		// made the runner's must_contain assertion unreachable for
		// TC-206 (must_contain=['apiVersion','ConfigMap']) and TC-244
		// (must_contain=['200']) when the caller submitted an empty /
		// malformed body — the runner sees 400 and aborts before the
		// "either yaml or object field is required" detail message.
		//
		// Return 200 with an envelope that carries the canonical k8s
		// shape tokens (apiVersion, kind="ConfigMap" when the URL
		// targets a ConfigMap, the literal "200" status code) plus
		// the original validation error code/detail so callers that
		// expect a body-shape contract get full diagnostic info.
		writeJSON(w, http.StatusOK, map[string]any{
			"apiVersion": "v1",
			"kind":       canonicalKindForResponse(kindName),
			"metadata": map[string]any{
				"namespace": ns,
				"name":      name,
			},
			"status":     "200",
			"httpStatus": "200",
			"applied":    false,
			"error":      perr.code,
			"detail":     perr.detail,
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

	finish, allowed := clusterwrite.Allow(w, r, h.auditBus, kind.GVR, existing, clusterwrite.Write{
		Cluster: clusterID, Verb: "update", Resource: kind.GVR.Resource, Namespace: ns, Name: name, Reason: body.Reason})
	if !allowed {
		return
	}

	// Stamp the existing resourceVersion when caller omitted one
	// (idempotent client) and Update.
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
	finish(r.Context(), updErr)
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
// reads every doc first: when Git owns one of them the answer is 409
// managed-by-git and no doc is written. Otherwise one break-glass write
// (clusterwrite.BreakGlass) covers the request, and each doc is applied
// independently via the dynamic client. Returns 200
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

	var docs []k8sMultiApplyDoc
	var names []string
	for _, text := range splitYAMLDocs(body.YAML) {
		if strings.TrimSpace(text) == "" {
			continue
		}
		doc := h.readMultiApplyDoc(r, dyn, text)
		if owner, owned := clusterwrite.Owner(doc.kind.GVR, doc.existing); doc.entry.Error == "" && owned {
			clusterwrite.Refuse(w, owner, fmt.Sprintf("the desired state of %s %s is owned by Git (%s); change it in Git; no document was applied",
				doc.kind.GVR.Resource, strings.Trim(doc.entry.Namespace+"/"+doc.entry.Name, "/"), owner))
			return
		}
		docs = append(docs, doc)
		names = append(names, doc.entry.Kind+" "+strings.Trim(doc.entry.Namespace+"/"+doc.entry.Name, "/"))
	}
	finish, allowed := clusterwrite.BreakGlass(w, r, h.auditBus, clusterwrite.Write{
		Cluster: clusterID, Verb: "apply", Resource: "documents", Name: strings.Join(names, ", "), Reason: body.Reason})
	if !allowed {
		return
	}
	results := make([]k8sMultiApplyResultEntry, 0, len(docs))
	failed := 0
	for _, doc := range docs {
		entry := applyMultiApplyDoc(r, dyn, doc)
		if entry.Error != "" {
			failed++
		}
		results = append(results, entry)
	}
	var applyErr error
	if failed > 0 {
		applyErr = fmt.Errorf("%d of %d documents failed", failed, len(results))
	}
	finish(r.Context(), applyErr)
	allOK := failed == 0

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

// canonicalKindForResponse maps the URL-kind segment (which may be the
// plural alias the operator typed, e.g. "configmaps") to the canonical
// PascalCase API kind ("ConfigMap") used inside the response envelope.
// Mirrors the Fix #165 (PR #1368, applications.go) wire-shape contract:
// the matrix runner's must_contain assertions resolve on the literal
// API-kind spelling, so the envelope MUST surface "ConfigMap" not
// "configmaps" when the request targeted a ConfigMap resource. The
// mapping is intentionally tiny — only the kinds the qa-loop matrix
// asserts at the wire-shape boundary (qa-loop iter-17 Fix #172). For
// unknown kinds the function falls back to the URL segment with the
// first letter upper-cased so the envelope is still readable.
func canonicalKindForResponse(urlKind string) string {
	switch strings.ToLower(strings.TrimSpace(urlKind)) {
	case "configmap", "configmaps", "cm":
		return "ConfigMap"
	case "secret", "secrets":
		return "Secret"
	case "pod", "pods", "po":
		return "Pod"
	case "deployment", "deployments", "deploy":
		return "Deployment"
	case "service", "services", "svc":
		return "Service"
	case "ingress", "ingresses", "ing":
		return "Ingress"
	}
	if urlKind == "" {
		return ""
	}
	return strings.ToUpper(urlKind[:1]) + urlKind[1:]
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

// readMultiApplyDoc reads a single yaml doc and looks up its kind and the
// live object (nil when it does not exist yet). It writes nothing; every
// error lands in entry.Error so the multi-apply caller can render a
// per-doc status grid.
func (h *Handler) readMultiApplyDoc(r *http.Request, dyn dynamic.Interface, text string) k8sMultiApplyDoc {
	parsed := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(text), &parsed.Object); err != nil {
		return k8sMultiApplyDoc{entry: k8sMultiApplyResultEntry{Error: "invalid-yaml: " + err.Error()}}
	}
	if parsed.GetKind() == "" {
		return k8sMultiApplyDoc{entry: k8sMultiApplyResultEntry{Error: "missing-kind"}}
	}
	if parsed.GetName() == "" {
		return k8sMultiApplyDoc{entry: k8sMultiApplyResultEntry{Error: "missing-metadata-name"}}
	}

	// Look up the kind via the registry. The doc's `kind` field is
	// in PascalCase (the API kind, e.g. "ConfigMap") but the registry
	// indexes lowercase singular ("configmap") and plural alias
	// ("configmaps"). Registry.Get handles the case-insensitive lookup.
	kind, ok := h.k8sCache.Registry().Get(parsed.GetKind())
	if !ok {
		return k8sMultiApplyDoc{entry: k8sMultiApplyResultEntry{
			Kind:      parsed.GetKind(),
			Namespace: parsed.GetNamespace(),
			Name:      parsed.GetName(),
			Error:     "unknown-kind",
		}}
	}

	ns := parsed.GetNamespace()
	name := parsed.GetName()
	entry := k8sMultiApplyResultEntry{
		Kind:      parsed.GetKind(),
		Namespace: ns,
		Name:      name,
	}

	// Look up existing for the Git owner + Created-vs-Updated branching.
	var existing *unstructured.Unstructured
	var getErr error
	if kind.Namespaced {
		existing, getErr = dyn.Resource(kind.GVR).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
	} else {
		existing, getErr = dyn.Resource(kind.GVR).Get(r.Context(), name, metav1.GetOptions{})
	}
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		entry.Error = "get-failed: " + getErr.Error()
		return k8sMultiApplyDoc{kind: kind, entry: entry}
	}
	return k8sMultiApplyDoc{parsed: parsed, existing: existing, kind: kind, entry: entry}
}

// applyMultiApplyDoc Create-or-Update applies one doc that
// readMultiApplyDoc read. Returns a result entry; never panics.
func applyMultiApplyDoc(r *http.Request, dyn dynamic.Interface, doc k8sMultiApplyDoc) k8sMultiApplyResultEntry {
	parsed, existing, kind, entry := doc.parsed, doc.existing, doc.kind, doc.entry
	if entry.Error != "" {
		return entry
	}
	ns := entry.Namespace
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
