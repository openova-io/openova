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
// #5140 part B — primary-client re-resolution. The Path-1 Factory
// client is constructed ONCE at AddCluster registration time and
// cached in the Factory's cluster map; DynamicClientFor never
// invalidates it. After a region-kill / DR failover the cached
// client can keep dialing a dead or stale endpoint (or present a
// token a peer apiserver rejects) — pre-fix, every request through
// it failed identically and the surface returned 503 FOREVER, even
// after the DR survivor was serving. sandboxCall fixes the recovery
// gap: on a connection-level (backend-unavailable) failure from the
// Path-1 client it re-resolves a FRESH client via sovereignDepsFor()
// — the local host apiserver, where the Sandbox CRD + RBAC are
// always valid (the remedy #5140 names) — and retries the operation
// once, so the first request after the survivor takes over returns
// 200 instead of a permanent 503.
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── NATS tenant-event publish (TBD-D35b / #1776) ────────────────────
//
// Per ADR-0001 §6 + core/services/shared/events/bridge.go's
// CanonicalSubject() rule, tenant lifecycle events publish to subjects
// under `catalyst.tenant.*`. Sandbox-controller already consumes this
// taxonomy (see core/controllers/sandbox/internal/controller/nats_bridge.go),
// so adding the publish on the catalyst-api side closes the audit-trail
// loop: every Sandbox CR materialised on the Sovereign apiserver leaves
// a corresponding NATS event the operator's audit log can replay.
//
// The Publisher interface is nil-tolerant by design: production main.go
// wires a real publisher when CATALYST_NATS_URL is set (mirroring the
// `newRBACAuditPublisherFromEnv` pattern); tests bind a recorder; CI
// without NATS wiring leaves it nil and the publish-side is a no-op so
// the Sandbox-create hot path never fails because audit isn't wired.

// SandboxRequestedSubject is the canonical NATS subject the catalyst-api
// emits to after a successful Sandbox CR Create. Sandbox-controller's
// consumer list MUST contain this subject so the controller can correlate
// FE-requested sandboxes with reconciler-observed CRs without scraping
// the apiserver.
const SandboxRequestedSubject = "catalyst.tenant.sandbox_requested"

// TenantEvent is the on-the-wire payload published to the canonical
// `catalyst.tenant.*` subjects. Field names mirror the shape downstream
// consumers (sandbox-controller, audit-trail UI, billing rollup) already
// parse from the audit.Event struct — but the subject is per-event so
// consumers can subscribe at the `catalyst.tenant.sandbox_requested`
// granularity without prefix matching.
type TenantEvent struct {
	// TenantID — the operator's Org slug (= namespace the CR lands in).
	// Empty on a single-tenant chroot when claims.Org is not set; the
	// payload falls back to the resolved namespace.
	TenantID string `json:"tenant_id"`

	// SandboxID — the Sandbox CR name (DNS-1123 label). Stable across
	// the CR's lifetime; consumers key by this.
	SandboxID string `json:"sandbox_id"`

	// RequestedBy — the operator identity that authored the create.
	// Email is preferred (operator-readable); falls back to the Keycloak
	// subject UUID when email is not in the claims set.
	RequestedBy string `json:"requested_by"`

	// Timestamp — RFC3339 UTC at the moment the catalyst-api wrote the
	// CR (NOT the apiserver creationTimestamp, which can lag by ms and
	// is read back from the unstructured response).
	Timestamp time.Time `json:"timestamp"`

	// SpecHash — SHA-256 of the canonical JSON-serialised spec, hex-
	// encoded. Lets consumers detect spec drift between the requested
	// event and a later updated event without re-fetching the CR.
	SpecHash string `json:"spec_hash"`
}

// TenantEventPublisher publishes tenant lifecycle events to NATS. The
// subject is passed explicitly per-call (vs the audit.Publisher pattern
// where the subject is fixed in the impl) so this one interface covers
// every `catalyst.tenant.*` event without needing a method per subject.
//
// Production main.go binds this to a real NATS-JetStream client when
// CATALYST_NATS_URL is set; tests bind a recorder. Nil-tolerant on the
// caller side — see HandleCreateSandboxSession's publish guard.
type TenantEventPublisher interface {
	// PublishTenantEvent writes ev to the given NATS subject. MUST NOT
	// panic on error; production callers log and continue so a
	// transient NATS outage never wedges the Sandbox-create hot path.
	PublishTenantEvent(ctx context.Context, subject string, ev TenantEvent) error
}

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

// sandboxRetryAfterSeconds — the Retry-After hint stamped on every
// sandbox 503 response (#5140). The transient windows the 503 covers
// (clustermesh peer-apiserver token rejection during a cutover /
// chart-roll / region-kill recovery, discovery races, apiserver
// restabilising) typically clear within a few minutes, so a 30s pace
// keeps retrying clients honest without hammering the degraded
// backend. Same convention as organization_provisioning.go's
// NS-flip-pending 503 (which uses 300s for its slower DNS window).
const sandboxRetryAfterSeconds = "30"

// writeSandboxJSON writes the JSON envelope for the sandbox handlers'
// error paths. When the status is 503 Service Unavailable it first
// stamps the Retry-After back-off hint so clients treat the degrade as
// retryable-with-pacing (#5140); genuine faults (500) and the
// object-level 404/409 keep their envelope untouched.
func writeSandboxJSON(w http.ResponseWriter, status int, body map[string]string) {
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", sandboxRetryAfterSeconds)
	}
	writeJSON(w, status, body)
}

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
//
// The list/get responses reflect controller-managed `.status` fields
// (Wave 14 follow-up to PR #1637 which shipped spec-only). Phase
// mapping is documented on `mapSandboxStatus` — the canonical CRD
// vocabulary (Pending|Provisioning|Ready|Failed) is preserved on
// `Phase` while the FE-facing `Status` carries the projected
// pending|running|stopped|failed|unknown.
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
	// Phase — raw controller phase (Pending|Provisioning|Ready|Failed).
	// Surfaced alongside the projected `Status` so the FE can render
	// finer-grained progress (e.g. "Provisioning" spinner vs. "Pending"
	// queued state) without re-implementing the mapping.
	Phase string `json:"phase,omitempty"`
	// CreatedAt — RFC3339 metadata.creationTimestamp.
	CreatedAt string `json:"createdAt"`
	// Repo — first repo entry's giteaRepo (`<org>/<repo>`). Empty
	// when no repos are configured.
	Repo string `json:"repo"`
	// Sessions — number of pty-server sessions live on the Sandbox.
	// Mirrors `.status.sessions`. Zero when the controller hasn't
	// reconciled yet OR when no sessions are open (the FE distinguishes
	// via `Phase`).
	Sessions int64 `json:"sessions"`
	// StorageUsed — human-readable storage usage (e.g. "12Gi"). Mirrors
	// `.status.storageUsed`. Empty when unknown.
	StorageUsed string `json:"storageUsed,omitempty"`
	// Spend30d — 30-day rolling spend in the operator's currency (e.g.
	// "$4.21"). Mirrors `.status.spend30d`. Empty when unknown.
	Spend30d string `json:"spend30d,omitempty"`
	// Previews — list of preview URLs (one per PR/branch). Mirrors
	// `.status.previews`. Empty slice when no previews are alive.
	Previews []sandboxPreview `json:"previews"`
	// Conditions — controller-managed K8s-style conditions. Surfaces
	// the `Type|Status|Reason|Message` tuple verbatim so the FE can
	// render actionable error states (e.g. `Ready=False` +
	// `Reason=TokenMintFailed` + `Message=...`) without
	// re-implementing the reconciler's failure taxonomy. Empty slice
	// when the controller hasn't observed the CR yet.
	Conditions []sandboxCondition `json:"conditions"`
}

// sandboxPreview mirrors sandboxapi.SandboxPreview (one preview URL
// per PR/branch, see core/controllers/sandbox/internal/sandboxapi/
// types.go). camelCase to match the FE wire convention.
type sandboxPreview struct {
	// PRNumber — the Gitea pull-request number that authored this
	// preview. Zero for the trunk preview.
	PRNumber int64 `json:"prNumber"`
	// URL — the public HTTPS URL serving the preview.
	URL string `json:"url"`
	// SHA — short commit SHA the preview was built from.
	SHA string `json:"sha,omitempty"`
}

// sandboxCondition mirrors sandboxapi.SandboxCondition. Reason +
// Message are operator-facing strings (see fail() in
// sandbox_controller.go for the canonical list:
// OwnerOrgRefMissing|OwnerEmailMissing|OwnerEmailInvalid|
// NoAllowedChannels|TokenMintFailed|ManifestRenderFailed|
// GitopsWriteFailed). LastTransitionTime is RFC3339 to match the
// rest of the catalyst-api wire format.
type sandboxCondition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
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
// sandboxBackendUnavailable reports whether err indicates the Sandbox
// backend is temporarily unreachable (as opposed to a genuine
// object-not-found, which each handler maps to its own 200/404/503).
// 401/403 arise when the catalyst-api dynamic client hits a clustermesh
// peer apiserver that rejects its token during a cutover / chart-roll /
// region-kill recovery window — observed live on hw261 after the
// region-kill DR failover flipped the primary region and the sandbox
// reflector alternated "Unauthorized" and "could not find the requested
// resource". Per this package's doc, backend-unavailable ⇒ 503 "API
// pending", never a raw 500.
// sandboxBackendUnavailable reports whether err indicates the sandbox backend
// (apiserver / sandbox-CRD discovery / the reflector's watch) is TRANSIENTLY
// unavailable — a retryable 503 — rather than a genuine 500 server fault.
//
// #5140: on hw261 during a region-kill recovery window, GET/POST
// /api/v1/sandbox/sessions returned 500. The typed apierrors below did NOT
// catch the real cause: the dynamic client's RESTMapper transiently could not
// resolve the sandbox GVR (a *meta.NoKindMatchError, "no matches for kind"),
// and the reflector's watch failed with connection-refused / "the server could
// not find the requested resource" while the region-a apiserver restabilised.
// Those are retryable — map them to 503 so the FE renders the "API pending"
// pill and retries, not a hard 500.
func sandboxBackendUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsUnauthorized(err) ||
		apierrors.IsForbidden(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsTimeout(err) ||
		meta.IsNoMatchError(err) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Untyped discovery / connection errors the reflector + RESTMapper wrap as
	// plain strings that the typed helpers above miss.
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"could not find the requested resource",
		"the server is currently unable to handle the request",
		"connection refused",
		"dial tcp",
		"unexpected eof",
		"tls handshake",
		"no route to host",
		"i/o timeout",
		"etcdserver: request timed out",
		"apiserver not ready",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func (h *Handler) HandleListSandboxSessions(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	hc, status, errResp := h.sandboxClient(r)
	if errResp != nil {
		writeSandboxJSON(w, status, errResp)
		return
	}
	ns := hc.ns

	var listIface *unstructured.UnstructuredList
	err := h.sandboxCall(r.Context(), hc, func(ctx context.Context, dyn dynamic.Interface) error {
		var cerr error
		listIface, cerr = dyn.Resource(SandboxGVR()).Namespace(ns).List(ctx, metav1.ListOptions{})
		return cerr
	})
	if err != nil {
		// CRD not installed → return empty list. The Sandbox CRD ships
		// via the catalyst chart's crds/ directory but a fresh chroot
		// may not have it applied yet (Wave 2 chart roll). The FE
		// renders the empty state in either case.
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, sandboxListResponse{Sandboxes: []sandboxItem{}})
			return
		}
		if sandboxBackendUnavailable(err) {
			h.log.Warn("sandbox: backend unavailable on list", "namespace", ns, "err", err)
			writeSandboxJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "sandbox-backend-unavailable",
				"detail": "sandbox backend temporarily unavailable",
			})
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

	hc, status, errResp := h.sandboxClient(r)
	if errResp != nil {
		writeSandboxJSON(w, status, errResp)
		return
	}
	ns := hc.ns

	var obj *unstructured.Unstructured
	err := h.sandboxCall(r.Context(), hc, func(ctx context.Context, dyn dynamic.Interface) error {
		var cerr error
		obj, cerr = dyn.Resource(SandboxGVR()).Namespace(ns).Get(ctx, id, metav1.GetOptions{})
		return cerr
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "sandbox-not-found",
				"detail": fmt.Sprintf("sandbox %q not found in namespace %q", id, ns),
			})
			return
		}
		if sandboxBackendUnavailable(err) {
			h.log.Warn("sandbox: backend unavailable on get", "id", id, "namespace", ns, "err", err)
			writeSandboxJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "sandbox-backend-unavailable",
				"detail": "sandbox backend temporarily unavailable",
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

	hc, status, errResp := h.sandboxClient(r)
	if errResp != nil {
		writeSandboxJSON(w, status, errResp)
		return
	}
	ns := hc.ns

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

	// Create retried through sandboxCall is safe: a first attempt that
	// FAILED at the connection level (the only class sandboxCall
	// retries) never reached the apiserver, so the retry cannot
	// double-create; a duplicate racing through anyway surfaces as
	// IsAlreadyExists and maps to 409 exactly like a double-click.
	var created *unstructured.Unstructured
	err := h.sandboxCall(r.Context(), hc, func(ctx context.Context, dyn dynamic.Interface) error {
		var cerr error
		created, cerr = dyn.Resource(SandboxGVR()).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
		return cerr
	})
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
			writeSandboxJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "sandbox-crd-not-installed",
				"detail": "sandboxes.sandbox.openova.io CRD missing on this cluster",
			})
			return
		}
		if sandboxBackendUnavailable(err) {
			h.log.Warn("sandbox: backend unavailable on create", "name", crName, "namespace", ns, "err", err)
			writeSandboxJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "sandbox-backend-unavailable",
				"detail": "sandbox backend temporarily unavailable",
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

	// Publish catalyst.tenant.sandbox_requested for the audit trail +
	// cross-component consumers (TBD-D35b / #1776). Best-effort: a NATS
	// outage MUST NOT fail the HTTP response — the CR has already been
	// written, so the operator's UI already shows the new sandbox. The
	// publish-side log captures publish failures for the
	// audit-completeness alert in the SRE runbook.
	h.publishSandboxRequested(r.Context(), obj, ns, claims, orgSlug)

	writeJSON(w, http.StatusCreated, unstructuredToSandboxItem(created))
}

// publishSandboxRequested emits a NATS event on
// `catalyst.tenant.sandbox_requested` after a successful Sandbox CR
// Create. Nil-tolerant: when the publisher isn't wired (CI / chroot
// without CATALYST_NATS_URL) this is a no-op. Errors are logged but
// never propagated — the CR write has already succeeded by the time
// this runs.
//
// `obj` is the unstructured the handler authored (NOT the apiserver
// response) so the spec_hash is deterministic across the read-back
// jitter the apiserver injects (defaults / managedFields / RV).
func (h *Handler) publishSandboxRequested(
	ctx context.Context,
	obj *unstructured.Unstructured,
	namespace string,
	claims *auth.Claims,
	orgSlug string,
) {
	if h == nil || h.tenantEvents == nil {
		return
	}
	if obj == nil {
		return
	}

	// Tenant identity resolution: claims.Org wins (multi-tenant
	// Sovereign), then the resolved namespace (single-tenant chroot
	// fallback — the namespace IS the tenant boundary). orgSlug is
	// the CRD-pattern-valid label stamped on `spec.owner.orgRef.slug`
	// and may collapse to "default" when the operator's Org doesn't
	// satisfy `^[a-z][a-z0-9-]{2,31}$`; using it as the tenant id
	// would conflate every non-pattern-matching Org under a single
	// downstream key, so we deliberately prefer the namespace.
	tenantID := ""
	if claims != nil {
		tenantID = strings.TrimSpace(claims.Org)
	}
	if tenantID == "" {
		tenantID = strings.TrimSpace(namespace)
	}
	if tenantID == "" {
		// Last-resort: the orgSlug (which itself falls back to
		// "default"). Keeps the field non-empty so consumers can index
		// by it without nil checks.
		tenantID = strings.TrimSpace(orgSlug)
	}

	requestedBy := ""
	if claims != nil {
		if e := strings.TrimSpace(claims.Email); e != "" {
			requestedBy = e
		} else {
			requestedBy = strings.TrimSpace(claims.Sub)
		}
	}

	ev := TenantEvent{
		TenantID:    tenantID,
		SandboxID:   obj.GetName(),
		RequestedBy: requestedBy,
		Timestamp:   time.Now().UTC(),
		SpecHash:    sandboxSpecHash(obj),
	}

	if err := h.tenantEvents.PublishTenantEvent(ctx, SandboxRequestedSubject, ev); err != nil {
		// Log at Warn — the CR is already created so this is non-fatal,
		// but operators need to know the audit-trail message was lost
		// (see #1776: zero `sandbox_requested` events in the stream
		// despite Sandbox CRs materialising was the original symptom).
		h.log.Warn("sandbox: tenant-event publish failed",
			"subject", SandboxRequestedSubject,
			"sandbox", obj.GetName(),
			"namespace", namespace,
			"err", err,
		)
		return
	}
	h.log.Debug("sandbox: tenant-event published",
		"subject", SandboxRequestedSubject,
		"sandbox", obj.GetName(),
		"tenant", tenantID,
	)
}

// sandboxSpecHash computes a stable SHA-256 over the Sandbox CR's
// `.spec` block. Stable across map-iteration order via the
// canonicaliseForHash sort. Returns "" when the spec is missing or
// not a map (defensive — the handler builds the spec as a map[string]any
// so this should always succeed in production, but a wrong-type spec
// MUST NOT panic).
func sandboxSpecHash(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	spec, found, err := unstructured.NestedFieldNoCopy(obj.Object, "spec")
	if err != nil || !found || spec == nil {
		return ""
	}
	buf, err := json.Marshal(canonicaliseForHash(spec))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// canonicaliseForHash recursively walks a map/slice tree and returns a
// representation where map keys are sorted. Lets json.Marshal produce a
// byte-stable output so the spec_hash is deterministic across runs.
// Pure helper — testable in isolation.
func canonicaliseForHash(in any) any {
	switch v := in.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// Use a slice of [key, value] pairs so the order is preserved
		// through json.Marshal (which sorts map keys but doesn't
		// distinguish nested map identity).
		pairs := make([][2]any, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, [2]any{k, canonicaliseForHash(v[k])})
		}
		return pairs
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = canonicaliseForHash(item)
		}
		return out
	default:
		return v
	}
}

// HandleDeleteSandboxSession — DELETE /api/v1/sandbox/sessions/{id}.
//
// Graceful: the apiserver delete fires finalizers and the
// sandbox-controller cleans up the per-Sandbox namespace + PVCs + RBAC
// inside the Org's vcluster. The handler returns 204 even when the CR
// is already gone (idempotent — repeated DELETEs from the FE never
// surface a 404 toast).
//
// #5140 follow-up: List/Get/Create already degrade a transient
// backend-unavailable error (401/403/timeout/discovery-404/etc, see
// sandboxBackendUnavailable) to 503 "API pending" instead of a raw
// 500. Delete shared the exact same client + error surface but had
// not been wired through the same classifier — fixed here so the
// whole CRUD surface is consistent: transient ⇒ 503, genuine fault ⇒
// 500.
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

	hc, status, errResp := h.sandboxClient(r)
	if errResp != nil {
		writeSandboxJSON(w, status, errResp)
		return
	}
	ns := hc.ns

	err := h.sandboxCall(r.Context(), hc, func(ctx context.Context, dyn dynamic.Interface) error {
		return dyn.Resource(SandboxGVR()).Namespace(ns).Delete(ctx, id, metav1.DeleteOptions{})
	})
	if err != nil && !apierrors.IsNotFound(err) {
		if sandboxBackendUnavailable(err) {
			h.log.Warn("sandbox: backend unavailable on delete", "id", id, "namespace", ns, "err", err)
			writeSandboxJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "sandbox-backend-unavailable",
				"detail": "sandbox backend temporarily unavailable",
			})
			return
		}
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

// sandboxClientHandle bundles the resolved dynamic client with the
// namespace it operates on plus the resolution path it came from.
//
// fromFactory marks a Path-1 (k8sCache Factory) client. That client is
// a REGISTRATION-TIME construction cached in the Factory's cluster map
// (k8scache.AddCluster builds it once from the kubeconfig on disk;
// DynamicClientFor never invalidates it), so across a region-kill /
// DR-failover window it can keep dialing a dead or stale endpoint —
// #5140 part B. Only that path is eligible for sandboxCall's
// re-resolve retry: Path-2 clients are already constructed fresh per
// request (sovereignDepsFromEnv), so retrying the same construction
// adds nothing but a duplicate call.
type sandboxClientHandle struct {
	dyn         dynamic.Interface
	ns          string
	fromFactory bool
}

// sandboxClient resolves the client handle the handlers operate on.
// Resolution order matches the package doc:
//
//  1. k8sCache.Factory.DynamicClientFor(resolveChrootClusterID(""))
//     — the canonical Catalyst-Zero / multi-Sovereign path.
//  2. sovereignDepsFor() — chroot in-cluster fallback.
//
// Returns (handle, status, errResp) with errResp non-nil on failure;
// the caller writes the JSON envelope verbatim.
func (h *Handler) sandboxClient(r *http.Request) (*sandboxClientHandle, int, map[string]string) {
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
				return &sandboxClientHandle{dyn: dyn, ns: ns, fromFactory: true}, 0, nil
			}
		}
	}

	// Path 2 — in-cluster fallback. The sovereignDeps factory
	// (sovereign.go) builds rest.InClusterConfig + dynamic.NewForConfig.
	// Tests inject a fake via SetSovereignDepsFactory.
	deps, err := h.sovereignDepsFor()
	if err != nil {
		return nil, http.StatusServiceUnavailable, map[string]string{
			"error":  "sandbox-cluster-unavailable",
			"detail": err.Error(),
		}
	}
	if deps == nil || deps.dyn == nil {
		return nil, http.StatusServiceUnavailable, map[string]string{
			"error":  "sandbox-cluster-unavailable",
			"detail": "no dynamic client available",
		}
	}
	return &sandboxClientHandle{dyn: deps.dyn, ns: ns}, 0, nil
}

// sandboxCall runs op against the resolved client with the per-request
// budget and — #5140 part B — re-resolves the backend on a
// connection-level failure of the Path-1 (Factory-cached) client.
//
// Root cause of the recovery gap (hw261, post-region-kill): after the
// DR failover flipped the primary region, the sandbox surface returned
// 503 on EVERY request and never recovered, because the Factory's
// dynamic client is built once at cluster registration and cached
// forever — a dead endpoint, a token a peer apiserver rejects (401), or
// a peer that does not serve the Sandbox CRD ("could not find the
// requested resource") fails identically on every call, and
// sandboxClient only falls through to Path 2 when Path 1 fails to
// RESOLVE, never when its client fails at request time. Part A
// (#5141/#5161/#5199) made those failures an honest retryable 503; this
// closes the loop so a retry can actually succeed.
//
// Mechanics:
//
//   - The first attempt runs against hc.dyn under its own
//     sandboxRequestBudget.
//   - When the attempt fails with a backend-unavailable class error
//     (sandboxBackendUnavailable — connection refused / dial / i-o
//     timeout / 401 / 403 / discovery-404 / NoKindMatch) AND the client
//     came from the Factory cache, a FRESH client is constructed via
//     sovereignDepsFor() — rest.InClusterConfig, i.e. the LOCAL host
//     apiserver where the Sandbox CRD + the cutover-driver RBAC are
//     always valid (the exact remedy #5140 part B names) — and op is
//     retried ONCE under a fresh budget. The fresh budget matters: a
//     blackholed endpoint eats the full first budget, so reusing the
//     original deadline would doom the retry to DeadlineExceeded.
//   - A genuine object-level NotFound ("sandboxes \"x\" not found")
//     is NOT in the backend-unavailable class, so ordinary 404s never
//     pay a second call. A CRD-level miss retries and, when the CRD is
//     genuinely absent locally too, returns the same error the caller
//     already maps (List → 200 empty, Create → 503 crd-not-installed).
//   - No sticky state: each request tries Path 1 first (it may have
//     healed via a kubeconfig re-registration) and pays at most one
//     extra call while the primary is stale. Worst case per request is
//     2× sandboxRequestBudget, still bounded for the FE, and the
//     response is a 200 from the survivor instead of a permanent 503.
func (h *Handler) sandboxCall(rctx context.Context, hc *sandboxClientHandle, op func(ctx context.Context, dyn dynamic.Interface) error) error {
	ctx, cancel := context.WithTimeout(rctx, sandboxRequestBudget)
	err := op(ctx, hc.dyn)
	cancel()
	if err == nil || !hc.fromFactory || !sandboxBackendUnavailable(err) {
		return err
	}

	deps, derr := h.sovereignDepsFor()
	if derr != nil || deps == nil || deps.dyn == nil {
		// No fresh local client available (mothership without in-cluster
		// SA, CI without the seam wired) — surface the original, honest
		// primary-path error; the caller degrades to 503 as before.
		return err
	}
	h.log.Warn("sandbox: primary client backend-unavailable — re-resolved local in-cluster client, retrying once (#5140)",
		"namespace", hc.ns, "err", err)

	retryCtx, retryCancel := context.WithTimeout(rctx, sandboxRequestBudget)
	defer retryCancel()
	return op(retryCtx, deps.dyn)
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
//
// Wave 14 (PR sandbox-wave14-sessions-status-reflect): reads
// controller-managed `.status` fields so list/get reflect the LIVE
// runtime (Ready/Provisioning/Failed + sessions + storage + spend +
// previews + conditions), not just the spec the handler authored.
// Empty slices (`previews`, `conditions`) are always materialised so
// the FE never has to `?? []` defensively.
func unstructuredToSandboxItem(u *unstructured.Unstructured) sandboxItem {
	if u == nil {
		return sandboxItem{
			Previews:   []sandboxPreview{},
			Conditions: []sandboxCondition{},
		}
	}
	rawPhase := readSandboxPhase(u)
	item := sandboxItem{
		ID:         u.GetName(),
		Name:       u.GetName(),
		Status:     mapSandboxStatus(rawPhase),
		Phase:      rawPhase,
		CreatedAt:  u.GetCreationTimestamp().UTC().Format(time.RFC3339),
		Previews:   []sandboxPreview{},
		Conditions: []sandboxCondition{},
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

	// ── .status reflection ──────────────────────────────────────
	//
	// All accessors tolerate type drift: the apiserver may serialise
	// an int as float64 (JSON round-trip) so we route every numeric
	// read through unstructured.NestedInt64 which handles the
	// float→int coercion.

	if sessions, found, _ := unstructured.NestedInt64(u.Object, "status", "sessions"); found {
		item.Sessions = sessions
	}
	if storage, found, _ := unstructured.NestedString(u.Object, "status", "storageUsed"); found {
		item.StorageUsed = strings.TrimSpace(storage)
	}
	if spend, found, _ := unstructured.NestedString(u.Object, "status", "spend30d"); found {
		item.Spend30d = strings.TrimSpace(spend)
	}

	if previews, found, _ := unstructured.NestedSlice(u.Object, "status", "previews"); found {
		for _, raw := range previews {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			p := sandboxPreview{}
			// prNumber may arrive as int64 (controller) OR float64
			// (post-JSON round-trip through the apiserver). Try both.
			switch v := m["prNumber"].(type) {
			case int64:
				p.PRNumber = v
			case float64:
				p.PRNumber = int64(v)
			case int:
				p.PRNumber = int64(v)
			}
			if u, ok := m["url"].(string); ok {
				p.URL = strings.TrimSpace(u)
			}
			if s, ok := m["sha"].(string); ok {
				p.SHA = strings.TrimSpace(s)
			}
			if p.URL == "" {
				// Drop incomplete entries — the FE keys rows by URL
				// for the preview list.
				continue
			}
			item.Previews = append(item.Previews, p)
		}
	}

	if conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions"); found {
		for _, raw := range conds {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			c := sandboxCondition{}
			if v, ok := m["type"].(string); ok {
				c.Type = strings.TrimSpace(v)
			}
			if v, ok := m["status"].(string); ok {
				c.Status = strings.TrimSpace(v)
			}
			if v, ok := m["reason"].(string); ok {
				c.Reason = strings.TrimSpace(v)
			}
			if v, ok := m["message"].(string); ok {
				c.Message = strings.TrimSpace(v)
			}
			if v, ok := m["lastTransitionTime"].(string); ok {
				// Already RFC3339 from metav1.Time JSON marshal.
				c.LastTransitionTime = strings.TrimSpace(v)
			}
			if c.Type == "" {
				continue
			}
			item.Conditions = append(item.Conditions, c)
		}
	}

	return item
}

// readSandboxPhase reads `.status.phase` verbatim, trimming
// whitespace. Returns "" when unset OR the controller hasn't observed
// the CR yet.
func readSandboxPhase(u *unstructured.Unstructured) string {
	if u == nil {
		return ""
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	return strings.TrimSpace(phase)
}

// mapSandboxStatus projects a raw `.status.phase` value (CRD
// vocabulary: Pending|Provisioning|Ready|Failed) to the FE vocabulary
// (pending|running|stopped|failed|unknown) per
// products/catalyst/bootstrap/ui/src/lib/sandbox.api.ts:normalizeStatus.
//
// Empty / unknown phases surface as `pending` so the FE renders the
// spinner rather than the red-text "unknown" pill — a fresh Sandbox
// typically transits Pending → Provisioning → Ready in <10s.
//
// Wave 14: signature takes the raw phase string (was: *Unstructured).
// The caller reads via readSandboxPhase exactly once and reuses the
// value for both the projected Status AND the raw Phase field on the
// wire payload.
func mapSandboxStatus(rawPhase string) string {
	switch strings.ToLower(rawPhase) {
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
