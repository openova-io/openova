// Package handler — sandbox_sessions.go: catalyst-api CRUD for the
// Sovereign-side Sandbox surface (Wave 7 follow-up to PR #1621 which
// shipped the FE — getSandboxes() / createSandbox() / getByosStatus()
// in products/catalyst/bootstrap/ui/src/lib/sandbox.api.ts — without a
// backend wire, so /sandbox could only render the empty target-state
// chrome and every action 404'd at the catalyst-api ingress).
//
// REST surface (registered by main.go inside the RequireSession group,
// mirroring byos_claude_code.go):
//
//	GET    /api/v1/sandbox/sessions          — list Sandbox CRs scoped to operator's org
//	POST   /api/v1/sandbox/sessions          — create Sandbox CR from {agent, name?, repo?}
//	GET    /api/v1/sandbox/sessions/{id}     — fetch single Sandbox CR
//	DELETE /api/v1/sandbox/sessions/{id}     — delete Sandbox CR (controller cleans up)
//
// ── Architecture ─────────────────────────────────────────────────────
//
// Sandbox CRD lives at sandbox.openova.io/v1 (Namespaced). Per
// products/sandbox/docs/architecture.md §7, each Sandbox CR is created
// in the Org's namespace (e.g. `acme`) — the sandbox-controller
// reconciles a `sandbox-<owner-uid>` namespace + RBAC + PVCs inside the
// Org's vcluster. catalyst-api never writes the inner namespace; it
// only authors the spec.* shape and lets the controller take over.
//
// On a chroot Sovereign (this is where the Sandbox surface lives —
// catalyst-ui runs at console.<sov-fqdn>) the catalyst-api Pod reads
// + writes via rest.InClusterConfig() through the cutover-driver SA.
// Per feedback_chroot_in_cluster_fallback.md every GVR the handler
// touches MUST have a matching rule on catalyst-api-cutover-driver
// ClusterRole (see clusterrole-cutover-driver.yaml). The
// `sandboxes.sandbox.openova.io` rules are added in the same PR as
// this handler.
//
// ── Client resolution ───────────────────────────────────────────────
//
// Two read/write paths are supported, in priority order:
//
//  1. k8sCache.Factory.DynamicClientFor(resolveChrootClusterID(...))
//     — the canonical seam every other CRD handler uses
//     (k8s_resource_actions.go, applications.go). The Factory's
//     dynamic client is the same one the informers use, so the cache
//     and the apiserver stay coherent on writes.
//
//  2. sovereignDepsFor() — in-cluster fallback that builds a fresh
//     dynamic.Interface from rest.InClusterConfig(). Used when
//     k8sCache is not wired (CI, mothership without a registered
//     cluster, fresh chroot before factory boot).
//
// Both paths share the same Sandbox GVR + namespace resolution. When
// neither path is available the handler returns 503 so the FE renders
// its target-state "API pending" pill rather than a 5xx spinner.
//
// ── Org-scoping ─────────────────────────────────────────────────────
//
// Every endpoint reads `claims.Org` (populated by RequireSession from
// the Keycloak `org_id` claim per PR #1619) to scope:
//
//   - the namespace the Sandbox CR is read from / written to,
//   - the `spec.owner.orgRef.slug` field on create,
//   - the list-filter (so a multi-tenant Sovereign never leaks a
//     cross-Org Sandbox into the operator's roster).
//
// On a chroot Sovereign with no `org_id` claim the handler falls back
// to a tenant-derived default (sandboxDefaultNamespace) so the
// single-tenant chroot case continues to render. Multi-tenant
// Sovereigns MUST emit `org_id`; the protocolMapper that does this is
// configured by configmap-{tenant,sovereign}-realm.yaml.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Sandbox CRD GVR ─────────────────────────────────────────────────
//
// Mirrors core/controllers/sandbox/internal/sandboxapi.GroupVersion +
// products/catalyst/chart/crds/sandbox.yaml. Redeclared here to avoid
// pulling the controller's runtime.Scheme into catalyst-api (the
// controller imports catalyst-api transitively via the shared blueprint
// catalog, so a reverse import would cycle).
const (
	sandboxGroup    = "sandbox.openova.io"
	sandboxVersion  = "v1"
	sandboxResource = "sandboxes"
)

// SandboxGVR — exposed for tests so the fake dynamic client can
// register the matching list-kind.
func SandboxGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    sandboxGroup,
		Version:  sandboxVersion,
		Resource: sandboxResource,
	}
}

// sandboxAllowedAgents — the canonical agent catalogue. Must stay in
// lock-step with products/catalyst/bootstrap/ui/src/lib/sandbox.api.ts
// `SANDBOX_AGENTS` + chart CRD `spec.agentCatalogue.items.enum`. Any
// agent not in this set is rejected at create time with 400.
var sandboxAllowedAgents = map[string]struct{}{
	"aider":        {},
	"claude-code":  {},
	"cursor-agent": {},
	"little-coder": {},
	"opencode":     {},
	"qwen-code":    {},
}

// sandboxNameSanitize is the K8s-name-safe transform applied to the
// derived Sandbox CR name (the FE-visible `id`). Matches DNS-1123 label
// rules (lowercase alphanumeric + hyphen, max 63 chars).
var sandboxNameSanitize = regexp.MustCompile(`[^a-z0-9-]+`)

// sandboxRequestBudget — short overall timeout per request so a
// wedged apiserver never wedges the FE. The FE tolerates 503 by
// rendering the "API pending" pill (see sandbox.api.ts:EMPTY_SANDBOXES).
const sandboxRequestBudget = 5 * time.Second

// sandboxDefaultNamespaceEnv — operator override for the namespace the
// handler reads/writes when claims.Org is empty (single-tenant chroot
// case). Defaults to `catalyst-system` so a fresh Sovereign with the
// CRD installed but no per-Org namespace still has somewhere to
// materialize the CR. Per docs/INVIOLABLE-PRINCIPLES.md #4 the
// namespace is configuration, not code.
const sandboxDefaultNamespaceEnv = "CATALYST_SANDBOX_DEFAULT_NAMESPACE"

// sandboxFallbackNamespace — final fallback when neither claims.Org
// nor the env override is set.
const sandboxFallbackNamespace = "catalyst-system"

// ── Wire shapes — match products/catalyst/bootstrap/ui/src/lib/
// sandbox.api.ts verbatim. The FE expects camelCase fields and a
// top-level `{sandboxes: [...]}` envelope on list. ─────────────────

// sandboxListResponse — GET /sandbox/sessions response body.
type sandboxListResponse struct {
	Sandboxes []sandboxItem `json:"sandboxes"`
}

// sandboxItem — one Sandbox row as the FE consumes it. The FE
// re-normalises every field defensively so the BE may omit unknown
// fields without breaking older clients.
type sandboxItem struct {
	// ID — the Sandbox CR name (DNS-1123 label, e.g. `sandbox-emrah`).
	ID string `json:"id"`
	// Name — operator-facing label. Defaults to ID when blank.
	Name string `json:"name"`
	// Agent — the chosen agent CLI binary id (must be in
	// sandboxAllowedAgents).
	Agent string `json:"agent"`
	// Status — lifecycle phase mapped from status.phase. The FE
	// vocabulary is {pending, running, stopped, failed, unknown};
	// the CRD vocabulary is {Pending, Provisioning, Ready, Failed}.
	// mapSandboxStatus handles the projection.
	Status string `json:"status"`
	// CreatedAt — RFC3339 metadata.creationTimestamp.
	CreatedAt string `json:"createdAt"`
	// Repo — first repo entry's giteaRepo (`<org>/<repo>`). Empty
	// when no repos are configured.
	Repo string `json:"repo"`
}

// sandboxCreateRequest — POST /sandbox/sessions body. Mirrors
// CreateSandboxRequest in sandbox.api.ts.
type sandboxCreateRequest struct {
	// Agent — the agent CLI binary id (REQUIRED; validated against
	// sandboxAllowedAgents).
	Agent string `json:"agent"`
	// Name — optional operator-facing label. When empty the handler
	// synthesises `<agent>-<short-id>` and uses that as both the CR
	// name and the FE-visible label.
	Name string `json:"name,omitempty"`
	// Repo — optional Gitea path `<org>/<repo>` to auto-clone.
	Repo string `json:"repo,omitempty"`
}

// ── HTTP handlers ────────────────────────────────────────────────────

// HandleListSandboxSessions — GET /api/v1/sandbox/sessions.
//
// Lists every Sandbox CR in the operator's Org namespace. Returns 200
// with `{sandboxes: []}` even when the CRD isn't installed yet — the
// FE renders the empty state plus its "API pending" pill in either
// case, and a 5xx would just churn the page state with a spinner.
func (h *Handler) HandleListSandboxSessions(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	client, ns, status, errResp := h.sandboxClient(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), sandboxRequestBudget)
	defer cancel()

	listIface, err := client.Resource(SandboxGVR()).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		// CRD not installed → return empty list. The Sandbox CRD ships
		// via the catalyst chart's crds/ directory but a fresh chroot
		// may not have it applied yet (Wave 2 chart roll). The FE
		// renders the empty state in either case.
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, sandboxListResponse{Sandboxes: []sandboxItem{}})
			return
		}
		h.log.Warn("sandbox: list failed", "namespace", ns, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "list-failed",
			"detail": err.Error(),
		})
		return
	}

	out := sandboxListResponse{Sandboxes: make([]sandboxItem, 0, len(listIface.Items))}
	for i := range listIface.Items {
		out.Sandboxes = append(out.Sandboxes, unstructuredToSandboxItem(&listIface.Items[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleGetSandboxSession — GET /api/v1/sandbox/sessions/{id}.
//
// Fetches a single Sandbox CR by name from the operator's Org
// namespace. 404 when the CR doesn't exist; 503 when the CRD isn't
// installed.
func (h *Handler) HandleGetSandboxSession(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing-id"})
		return
	}

	client, ns, status, errResp := h.sandboxClient(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), sandboxRequestBudget)
	defer cancel()

	obj, err := client.Resource(SandboxGVR()).Namespace(ns).Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "sandbox-not-found",
				"detail": fmt.Sprintf("sandbox %q not found in namespace %q", id, ns),
			})
			return
		}
		h.log.Warn("sandbox: get failed", "id", id, "namespace", ns, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "get-failed",
			"detail": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, unstructuredToSandboxItem(obj))
}

// HandleCreateSandboxSession — POST /api/v1/sandbox/sessions.
//
// Authors a Sandbox CR with the request body's {agent, name?, repo?}
// fields. The handler is intentionally thin: it validates the inbound
// shape, derives a DNS-1123 CR name, fills `spec.owner` from the
// operator's claims, picks sensible Wave 1 quota defaults, then writes
// the CR. The sandbox-controller takes over from there.
//
// Returns the created Sandbox row on success (mirrors the FE's
// expectation that createSandbox returns a Sandbox object).
func (h *Handler) HandleCreateSandboxSession(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	var body sandboxCreateRequest
	if r.ContentLength == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "empty-body",
			"detail": "request body required: {agent, name?, repo?}",
		})
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-json",
			"detail": err.Error(),
		})
		return
	}

	agent := strings.TrimSpace(body.Agent)
	if _, ok := sandboxAllowedAgents[agent]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-agent",
			"detail": fmt.Sprintf("agent %q not in catalogue {aider, claude-code, cursor-agent, little-coder, opencode, qwen-code}", agent),
		})
		return
	}

	client, ns, status, errResp := h.sandboxClient(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}

	// Derive the CR name. Operator-supplied Name is preferred (lets
	// the FE control the label); when empty we synthesise from the
	// agent id + the operator's user-sub prefix so two concurrent
	// "create with claude-code" clicks don't collide.
	crName := deriveSandboxName(body.Name, agent, claims.Sub)
	displayName := body.Name
	if strings.TrimSpace(displayName) == "" {
		displayName = crName
	}

	orgSlug := sandboxOrgSlug(claims)
	obj := buildSandboxUnstructured(crName, ns, displayName, agent, body.Repo, claims.Email, orgSlug)

	ctx, cancel := context.WithTimeout(r.Context(), sandboxRequestBudget)
	defer cancel()

	created, err := client.Resource(SandboxGVR()).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "sandbox-already-exists",
				"detail": fmt.Sprintf("sandbox %q already exists in namespace %q", crName, ns),
			})
			return
		}
		if apierrors.IsNotFound(err) {
			// CRD missing — the controller chart hasn't applied
			// the sandboxes.sandbox.openova.io CRD on this
			// cluster yet. Surface 503 so the FE renders the
			// "API pending" pill rather than a generic 500.
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "sandbox-crd-not-installed",
				"detail": "sandboxes.sandbox.openova.io CRD missing on this cluster",
			})
			return
		}
		h.log.Warn("sandbox: create failed", "name", crName, "namespace", ns, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "create-failed",
			"detail": err.Error(),
		})
		return
	}

	h.log.Info("sandbox: created",
		"name", crName,
		"namespace", ns,
		"agent", agent,
		"owner_sub", claims.Sub,
		"org", orgSlug,
	)
	writeJSON(w, http.StatusCreated, unstructuredToSandboxItem(created))
}

// HandleDeleteSandboxSession — DELETE /api/v1/sandbox/sessions/{id}.
//
// Graceful: the apiserver delete fires finalizers and the
// sandbox-controller cleans up the per-Sandbox namespace + PVCs + RBAC
// inside the Org's vcluster. The handler returns 204 even when the CR
// is already gone (idempotent — repeated DELETEs from the FE never
// surface a 404 toast).
func (h *Handler) HandleDeleteSandboxSession(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing-id"})
		return
	}

	client, ns, status, errResp := h.sandboxClient(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), sandboxRequestBudget)
	defer cancel()

	err := client.Resource(SandboxGVR()).Namespace(ns).Delete(ctx, id, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		h.log.Warn("sandbox: delete failed", "id", id, "namespace", ns, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "delete-failed",
			"detail": err.Error(),
		})
		return
	}

	h.log.Info("sandbox: deleted", "name", id, "namespace", ns, "owner_sub", claims.Sub)
	w.WriteHeader(http.StatusNoContent)
}

// ── Internal helpers ────────────────────────────────────────────────

// sandboxClient resolves the (dynamic.Interface, namespace) pair the
// handlers operate on. Resolution order matches the package doc:
//
//  1. k8sCache.Factory.DynamicClientFor(resolveChrootClusterID(""))
//     — the canonical Catalyst-Zero / multi-Sovereign path.
//  2. sovereignDepsFor() — chroot in-cluster fallback.
//
// Returns (..., status, errResp) with errResp non-nil on failure;
// the caller writes the JSON envelope verbatim.
func (h *Handler) sandboxClient(r *http.Request) (dynamic.Interface, string, int, map[string]string) {
	claims := auth.ClaimsFromContext(r.Context())
	ns := sandboxNamespaceFor(claims)

	// Path 1 — Factory dynamic client. The empty cluster id falls
	// through to resolveChrootClusterID's "first registered cluster"
	// branch when running on a chroot Sovereign (the catalyst-api
	// Pod self-registers exactly one cluster via FactoryFromEnv).
	if h.k8sCache != nil {
		clusterID := h.resolveChrootClusterID("")
		if clusterID != "" {
			if dyn, err := h.k8sCache.DynamicClientFor(clusterID); err == nil {
				return dyn, ns, 0, nil
			}
		}
	}

	// Path 2 — in-cluster fallback. The sovereignDeps factory
	// (sovereign.go) builds rest.InClusterConfig + dynamic.NewForConfig.
	// Tests inject a fake via SetSovereignDepsFactory.
	deps, err := h.sovereignDepsFor()
	if err != nil {
		return nil, ns, http.StatusServiceUnavailable, map[string]string{
			"error":  "sandbox-cluster-unavailable",
			"detail": err.Error(),
		}
	}
	if deps == nil || deps.dyn == nil {
		return nil, ns, http.StatusServiceUnavailable, map[string]string{
			"error":  "sandbox-cluster-unavailable",
			"detail": "no dynamic client available",
		}
	}
	return deps.dyn, ns, 0, nil
}

// sandboxNamespaceFor returns the namespace the handler reads/writes.
// Priority: claims.Org → CATALYST_SANDBOX_DEFAULT_NAMESPACE env →
// sandboxFallbackNamespace. Single-tenant chroots typically rely on
// the env override or the fallback; multi-tenant Sovereigns MUST emit
// the org_id claim so per-Org isolation holds.
func sandboxNamespaceFor(claims *auth.Claims) string {
	if claims != nil {
		if org := strings.TrimSpace(claims.Org); org != "" {
			return org
		}
	}
	if env := strings.TrimSpace(os.Getenv(sandboxDefaultNamespaceEnv)); env != "" {
		return env
	}
	return sandboxFallbackNamespace
}

// sandboxOrgSlug returns the org slug to stamp on
// spec.owner.orgRef.slug. Mirrors sandboxNamespaceFor but emits a
// CRD-pattern-valid slug (^[a-z][a-z0-9-]{2,31}$ per the chart's
// openAPIV3Schema). When claims.Org doesn't satisfy the pattern we
// fall back to a deterministic default so the apiserver accepts the
// CR regardless of how the operator's IDP names their orgs.
func sandboxOrgSlug(claims *auth.Claims) string {
	if claims != nil {
		org := strings.ToLower(strings.TrimSpace(claims.Org))
		if isValidSandboxOrgSlug(org) {
			return org
		}
	}
	// Fall back to a known-valid slug so the CRD's pattern matcher
	// accepts the create. The Org reconciliation step (Wave 4) maps
	// this back to the operator's real Organization record by
	// inspecting metadata.namespace.
	return "default"
}

// isValidSandboxOrgSlug enforces the CRD's
// `^[a-z][a-z0-9-]{2,31}$` pattern client-side so we surface 400
// before the apiserver does.
func isValidSandboxOrgSlug(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

// deriveSandboxName produces a DNS-1123-label-safe CR name. Priority:
// the operator-supplied Name (sanitised) → `<agent>-<sub-prefix>`. The
// sub-prefix is the first 8 chars of claims.Sub (typically a UUID), so
// two concurrent creates by the same operator with the same agent
// still collide — that's intentional; the FE shows 409 and the
// operator picks a distinct Name.
func deriveSandboxName(supplied, agent, sub string) string {
	name := strings.TrimSpace(supplied)
	if name == "" {
		subPrefix := sub
		if len(subPrefix) > 8 {
			subPrefix = subPrefix[:8]
		}
		name = fmt.Sprintf("%s-%s", agent, subPrefix)
	}
	name = strings.ToLower(name)
	name = sandboxNameSanitize.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = fmt.Sprintf("sandbox-%s", agent)
	}
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// buildSandboxUnstructured assembles the Sandbox CR's wire payload.
// Wave 1 quotas mirror the chart's docs/architecture.md §7 example
// values (4 CPU / 8Gi memory / 50Gi storage / 3 concurrent sessions);
// the FE doesn't yet expose a quota picker so these defaults stand.
func buildSandboxUnstructured(name, ns, displayName, agent, repo, ownerEmail, orgSlug string) *unstructured.Unstructured {
	spec := map[string]any{
		"owner": map[string]any{
			"email": ownerEmail,
			"orgRef": map[string]any{
				"slug": orgSlug,
			},
		},
		"quota": map[string]any{
			"cpu":                "4",
			"memory":             "8Gi",
			"storage":            "50Gi",
			"concurrentSessions": int64(3),
		},
		"agentCatalogue": []any{agent},
	}

	if r := strings.TrimSpace(repo); r != "" {
		spec["repos"] = []any{
			map[string]any{"giteaRepo": r},
		}
	}

	labels := map[string]any{
		"catalyst.openova.io/sandbox-agent": agent,
		"catalyst.openova.io/organization":  orgSlug,
	}
	annotations := map[string]any{}
	if dn := strings.TrimSpace(displayName); dn != "" && dn != name {
		annotations["catalyst.openova.io/sandbox-display-name"] = dn
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": sandboxGroup + "/" + sandboxVersion,
			"kind":       "Sandbox",
			"metadata": map[string]any{
				"name":      name,
				"namespace": ns,
				"labels":    labels,
			},
			"spec": spec,
		},
	}
	if len(annotations) > 0 {
		obj.Object["metadata"].(map[string]any)["annotations"] = annotations
	}
	return obj
}

// unstructuredToSandboxItem projects a Sandbox CR back into the FE's
// wire shape. Defensive against missing fields: every accessor
// tolerates a nil / wrong-type intermediate so a partial CR (e.g. one
// authored outside this handler by an operator's kubectl apply) still
// renders.
func unstructuredToSandboxItem(u *unstructured.Unstructured) sandboxItem {
	if u == nil {
		return sandboxItem{}
	}
	item := sandboxItem{
		ID:        u.GetName(),
		Name:      u.GetName(),
		Status:    mapSandboxStatus(u),
		CreatedAt: u.GetCreationTimestamp().UTC().Format(time.RFC3339),
	}

	// Display name lives on metadata.annotations when the operator
	// passed a Name distinct from the derived CR name.
	if ann := u.GetAnnotations(); ann != nil {
		if dn := strings.TrimSpace(ann["catalyst.openova.io/sandbox-display-name"]); dn != "" {
			item.Name = dn
		}
	}

	// Agent — first entry in spec.agentCatalogue. Per architecture.md
	// §7 the catalogue is a list but the FE picks exactly one agent
	// at create time, so [0] is the canonical projection.
	if agents, found, _ := unstructured.NestedStringSlice(u.Object, "spec", "agentCatalogue"); found && len(agents) > 0 {
		item.Agent = agents[0]
	}

	// Repo — first repos[*].giteaRepo. Multi-repo Sandboxes will need
	// a richer projection in a follow-up; today the FE only renders
	// one repo per row.
	if repos, found, _ := unstructured.NestedSlice(u.Object, "spec", "repos"); found && len(repos) > 0 {
		if first, ok := repos[0].(map[string]any); ok {
			if r, ok := first["giteaRepo"].(string); ok {
				item.Repo = strings.TrimSpace(r)
			}
		}
	}

	return item
}

// mapSandboxStatus projects status.phase (CRD vocabulary:
// Pending|Provisioning|Ready|Failed) to the FE vocabulary
// (pending|running|stopped|failed|unknown). Empty / unknown phases
// surface as `pending` so the FE renders the spinner rather than the
// red-text "unknown" pill — a fresh Sandbox typically transits
// Pending → Provisioning → Ready in <10s.
func mapSandboxStatus(u *unstructured.Unstructured) string {
	if u == nil {
		return "unknown"
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "pending", "provisioning":
		return "pending"
	case "ready":
		return "running"
	case "failed":
		return "failed"
	case "":
		// No status yet — the controller hasn't observed the CR.
		// Render as pending so the FE shows the spinner.
		return "pending"
	default:
		return "unknown"
	}
}
