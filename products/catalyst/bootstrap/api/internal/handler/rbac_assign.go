// Package handler — rbac_assign.go: EPIC-3 (#1098) slice A1 find-or-create
// role assignment endpoint.
//
// REST surface:
//
//	POST /api/v1/sovereigns/{id}/rbac/assign
//
// The endpoint is the ergonomic wrapper the multi-grant editor (slice U1)
// calls when an operator picks a tier and a set of scopes for a target
// user. It is functionally a `kubectl apply -f` over a UserAccess CR
// — the find-or-create semantic means an idempotent re-assign with the
// SAME (user, scope, tier) is a no-op, a re-assign with a DIFFERENT tier
// rotates the existing CR's tier label + spec.tierRoleRef, and a brand
// new (user, scope) pair creates a new UserAccess CR.
//
// Per ADR-0001 §2.7 the UserAccess CR is the audit trail. The endpoint
// MUST NOT bypass the CR by writing RoleBindings directly.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every label key (org / tier /
// scope) is in the canonical NAMING-CONVENTION.md §6 vocabulary —
// the constants below match openova.io/* keys; never invented.
//
// ── Authorization ─────────────────────────────────────────────────────
//
// Only callers with `tier-admin` or higher (or the legacy
// `application-admin` ClusterRole) may grant tiers. The middleware
// already validates the JWT; we additionally check the parsed Claims'
// realm-role list for one of the privileged tier names. The check is
// inline (not a separate decorator) because A1 is the first endpoint
// that consumes catalog-tier roles — a follow-up slice will lift this
// into a chi middleware once N>1 endpoints share the contract.
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Canonical label keys + role names ────────────────────────────────

const (
	// labelTier is the canonical label key the useraccess-controller
	// reads to determine the tier on a UserAccess CR. Source-of-truth
	// per docs/EPICS-1-6-unified-design.md §6.2 + slice T1 (#1142).
	labelTier = "catalyst.openova.io/tier"

	// rbacAssignFinalizer prefixes the auto-generated UserAccess CR
	// names emitted by the find-or-create logic. The deterministic
	// shape (rbac-<keycloak-subject-prefix>-<scope-hash>) lets a
	// re-run of /rbac/assign find the existing CR by name in O(1)
	// without scanning the full UserAccess list — the `rbac-` prefix
	// makes the audit trail grep-able.
	rbacAssignNamePrefix = "rbac"

	// tierClusterRolePrefix is the prefix for the 5 tier ClusterRoles
	// shipped by EPIC-3 slice T1 (#1142). The full name is
	// `openova:tier-<tier>` per platform/crossplane-claims/chart/
	// templates/tier-clusterroles.yaml.
	tierClusterRolePrefix = "openova:tier-"

	// rbacAssignNamespace is the namespace UserAccess CRs are written
	// into. The CRD is `Namespaced` (per chart/crds/useraccess.yaml +
	// the live cluster verification: `kubectl get crd
	// useraccesses.access.openova.io -o jsonpath='{.spec.scope}'`
	// returns `Namespaced`). For namespaced CRDs the apiserver returns
	// the confusing 404 `the server could not find the requested
	// resource` when Create is called with an empty namespace string —
	// it reads the empty path as a request for the cluster-scoped
	// REST endpoint, which doesn't exist for a namespaced CR.
	//
	// Hardcoded to `catalyst-system` because that is the canonical
	// namespace the catalyst-platform Helm release ships into on every
	// Sovereign + chroot, and is the same namespace the
	// useraccess-controller watches for its reconcile loop. Mirrors
	// the SMTP-seed handler's hardcoded `sovereignSMTPSeedNamespace`
	// pattern (sovereign_smtp_seed.go) — a Sovereign without
	// catalyst-system is not a Sovereign at all.
	rbacAssignNamespace = "catalyst-system"
)

// rbacAssignAllowedTiers is the canonical tier catalog. Any other
// value on the request body is rejected with 400. The list is the
// public contract docs/EPICS-1-6-unified-design.md §6.2 declares.
//
// `super-admin` is the cross-org sentinel the canonical UAT matrix
// uses for the "global escalation" scope (TC-168 et al.) — it
// resolves to the same ClusterRole as `owner` (openova:tier-owner)
// but is reserved for grants that span multiple Sovereigns / orgs.
// Per `feedback_no_mvp_no_workarounds.md` the matrix vocabulary is
// the contract; the handler accepts and audits it as a first-class
// tier rather than rejecting with 400.
var rbacAssignAllowedTiers = map[string]struct{}{
	"viewer":      {},
	"developer":   {},
	"operator":    {},
	"admin":       {},
	"owner":       {},
	"super-admin": {},
}

// rbacAssignTierResolved maps a request-body tier value to the
// underlying ClusterRole name suffix. `super-admin` resolves to
// `owner` so the existing 5-tier ClusterRole shipped by EPIC-3 T1
// (#1142) provides the binding without a chart change. The audit
// trail still records the request-vocabulary tier (`super-admin`)
// for grep-ability.
func rbacAssignTierResolved(tier string) string {
	t := strings.ToLower(strings.TrimSpace(tier))
	if t == "super-admin" {
		return "owner"
	}
	return t
}

// rbacAssignPrivilegedRoles is the set of realm roles a caller MUST
// hold to call /rbac/assign. Includes both the new tier-admin /
// tier-owner roles (post-EPIC-3 T2) and the legacy
// application-admin role (pre-EPIC-3) so the rollout can be staged
// without breaking existing SREs.
var rbacAssignPrivilegedRoles = []string{
	"catalyst-admin",
	"catalyst-owner",
	"application-admin",
}

// ── Wire shapes ──────────────────────────────────────────────────────

type rbacAssignUserBody struct {
	Email           string `json:"email,omitempty"`
	KeycloakSubject string `json:"keycloakSubject,omitempty"`
}

type rbacAssignScopeBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// rbacAssignRequest is the body of POST /rbac/assign.
//
// Two equivalent body shapes are accepted (per
// `feedback_no_mvp_no_workarounds.md` the matrix is the contract;
// the handler conforms):
//
//  1. Long form (production internal callers, multi-grant editor):
//     { "user":{"email":"...","keycloakSubject":"..."},
//     "tier":"developer",
//     "scope":[{"key":"openova.io/application","value":"qa-wp"}] }
//
//  2. Short form (canonical UAT matrix):
//     { "email":"qa-user1@openova.io",
//     "tier":"developer",
//     "scopeType":"application",
//     "scopeName":"qa-wp" }
//
// The short form collapses onto the long form via
// rbacAssignRequestNormalize:
//
//	email      → User.Email
//	scopeType  → Scope[0].Key  ("application" → openova.io/application,
//	                            "organization" → openova.io/org,
//	                            "env-type"    → openova.io/env-type;
//	                            else passes through unchanged)
//	scopeName  → Scope[0].Value
//
// Bare `{"email":"x","tier":"super-admin"}` (TC-168 — global
// escalation) collapses to a global grant: empty Scope set + tier
// "super-admin" → tier-owner ClusterRole binding scoped to no
// resource label, which the useraccess-controller treats as
// "applies to all".
type rbacAssignRequest struct {
	User  rbacAssignUserBody    `json:"user"`
	Tier  string                `json:"tier"`
	Scope []rbacAssignScopeBody `json:"scope"`

	// Short-form aliases (canonical UAT matrix vocabulary).
	EmailShort     string `json:"email,omitempty"`
	ScopeTypeShort string `json:"scopeType,omitempty"`
	ScopeNameShort string `json:"scopeName,omitempty"`
}

// rbacAssignScopeKeyForType maps the matrix's friendly scope-type
// vocabulary to the canonical NAMING-CONVENTION.md §6 label key.
// Unknown scope-types pass through unchanged so a future addition
// (e.g. `cluster`, `region`) doesn't require a code change.
func rbacAssignScopeKeyForType(scopeType string) string {
	switch strings.ToLower(strings.TrimSpace(scopeType)) {
	case "application", "app":
		return scopeKeyApplication
	case "organization", "org":
		return scopeKeyOrg
	case "env-type", "envtype", "environment-type", "environmenttype":
		return scopeKeyEnvType
	}
	return strings.TrimSpace(scopeType)
}

// rbacAssignRequestNormalize collapses the short-form aliases onto
// the long-form fields. Long form wins on conflict so a body that
// supplies BOTH shapes ends up with the long-form values.
func rbacAssignRequestNormalize(b rbacAssignRequest) rbacAssignRequest {
	if strings.TrimSpace(b.User.Email) == "" && strings.TrimSpace(b.EmailShort) != "" {
		b.User.Email = strings.TrimSpace(b.EmailShort)
	}
	scopeType := strings.TrimSpace(b.ScopeTypeShort)
	scopeName := strings.TrimSpace(b.ScopeNameShort)
	if len(b.Scope) == 0 && (scopeType != "" || scopeName != "") {
		// Both empty short-form -> nothing to add. Both set -> single
		// scope entry. Either-only -> single entry with the empty
		// counterpart trimmed (validation later catches a half-set
		// scope as a clear 400).
		key := rbacAssignScopeKeyForType(scopeType)
		b.Scope = []rbacAssignScopeBody{{Key: key, Value: scopeName}}
	}
	return b
}

type rbacAssignUserAccessRef struct {
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Namespace string `json:"namespace"`
}

// rbacAssignResponse is the body returned by POST /rbac/assign.
type rbacAssignResponse struct {
	UserAccess      rbacAssignUserAccessRef `json:"userAccess"`
	TierClusterRole string                  `json:"tierClusterRole"`
	Applied         string                  `json:"applied"` // created | updated | no-op
}

// ── HTTP handler ─────────────────────────────────────────────────────

// HandleRBACAssign — POST /api/v1/sovereigns/{id}/rbac/assign
//
// Find-or-create-role: idempotent assignment of a tier to a user at a
// given scope. See file-level doc for the full semantic.
func (h *Handler) HandleRBACAssign(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body rbacAssignRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	body = rbacAssignRequestNormalize(body)
	if msg, ok := validateRBACAssignRequest(body); !ok {
		writeBadRequest(w, "invalid-rbac-assign", msg)
		return
	}

	// Authorization: caller must hold one of the privileged realm roles.
	// Nil-claims (Sovereign clusters with no Keycloak wired, or test
	// harnesses) are allowed through — the middleware decision is the
	// single source of truth for whether auth was required.
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !rbacAssignCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "/rbac/assign requires tier-admin or higher",
			})
			return
		}
	}

	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	resp, status, prevTier, err := rbacAssignFindOrCreate(r.Context(), client, body, dep)
	if err != nil {
		h.log.Warn("rbac.assign: find-or-create failed", "depId", depID, "err", err)
		writeJSON(w, status, map[string]string{
			"error":  "rbac-assign-failed",
			"detail": err.Error(),
		})
		return
	}

	// Emit audit event after a successful CR write. Per ADR-0001 §3 the
	// canonical transport is the catalyst.audit JetStream subject; the
	// audit Bus mirrors to NATS when CATALYST_NATS_URL is set and serves
	// the in-process /audit/rbac listing endpoint regardless. Nil-tolerant:
	// when the Bus isn't wired (tests, no-NATS dev) the call is a no-op.
	h.publishRBACAssignAudit(r.Context(), depID, body, resp, prevTier)

	writeJSON(w, status, resp)
}

// publishRBACAssignAudit emits one audit event per find-or-create
// outcome:
//
//   - applied=created  → AuditTypeRBACGrantCreated
//   - applied=updated  → AuditTypeRBACGrantUpdated  (+ AuditTypeRBACTierChanged if tier moved)
//   - applied=no-op    → no event (no state change)
//
// Splitting tier-changed from grant-updated lets the audit-trail UI
// render a distinct rotation pill without scanning Detail. Both events
// share the same target identity / scopes so consumers can correlate
// them.
func (h *Handler) publishRBACAssignAudit(
	ctx context.Context,
	depID string,
	req rbacAssignRequest,
	resp rbacAssignResponse,
	prevTier string,
) {
	if h == nil || h.auditBus == nil {
		return
	}
	if resp.Applied == "no-op" {
		return
	}
	actor := rbacAuditActorFromClaims(auth.ClaimsFromContext(ctx))
	target := strings.TrimSpace(req.User.KeycloakSubject)
	if target == "" {
		target = strings.TrimSpace(req.User.Email)
	}
	scopes := make([]audit.EventScope, 0, len(req.Scope))
	for _, s := range req.Scope {
		scopes = append(scopes, audit.EventScope{Key: s.Key, Value: s.Value})
	}
	app := ""
	for _, s := range req.Scope {
		if s.Key == scopeKeyApplication {
			app = s.Value
			break
		}
	}
	base := audit.Event{
		SovereignID:       depID,
		Actor:             actor,
		TargetUser:        target,
		TargetUserEmail:   strings.TrimSpace(req.User.Email),
		TargetApplication: app,
		Tier:              strings.ToLower(req.Tier),
		Scopes:            scopes,
		UserAccessRef:     resp.UserAccess.Name,
	}
	switch resp.Applied {
	case "created":
		ev := base
		ev.AuditType = audit.AuditTypeRBACGrantCreated
		ev.Detail = fmt.Sprintf("granted %s tier on UserAccess %s", base.Tier, base.UserAccessRef)
		h.auditBus.Publish(ctx, ev)
	case "updated":
		ev := base
		ev.AuditType = audit.AuditTypeRBACGrantUpdated
		ev.PreviousTier = strings.ToLower(strings.TrimSpace(prevTier))
		ev.Detail = fmt.Sprintf("rotated UserAccess %s to %s tier", base.UserAccessRef, base.Tier)
		h.auditBus.Publish(ctx, ev)
		// Emit a sibling tier-changed event when the tier actually moved.
		if ev.PreviousTier != "" && ev.PreviousTier != base.Tier {
			tev := base
			tev.AuditType = audit.AuditTypeRBACTierChanged
			tev.PreviousTier = ev.PreviousTier
			tev.Detail = fmt.Sprintf("tier rotated %s → %s on UserAccess %s",
				ev.PreviousTier, base.Tier, base.UserAccessRef)
			h.auditBus.Publish(ctx, tev)
		}
	}
}

// ── Core find-or-create logic ─────────────────────────────────────────

// rbacAssignMaxRetries is the cap on the race-tolerant 409 retry loop.
// The EnsureUser pattern uses the same shape: try once, on 409 re-list
// + re-evaluate, retry once more. Bounded retries avoid live-locking
// on a hot-path CR.
const rbacAssignMaxRetries = 2

// rbacAssignFindOrCreate runs the 3-path logic:
//
//  1. List UserAccess CRs filtered by tier-label + keycloak-subject
//  2. Find one with the SAME scope set
//     - same tier  → no-op (HTTP 200)
//     - diff tier  → update tier label + spec.tierRoleRef (HTTP 200)
//  3. No match    → create new UserAccess (HTTP 201)
//
// Race-tolerant: on a 409 conflict during create or update, the loop
// re-lists and re-evaluates, retrying once. Two concurrent creators
// for the same (user, scope) thus converge to the no-op or update path
// rather than surfacing a 409 to the operator.
//
// Returns the response body, the HTTP status code, the previous tier
// (when the path was Updated; "" otherwise — the audit-emit on the
// handler side surfaces tier rotation as a sibling rbac-tier-changed
// event), and any error.
// On success, status is 200 (no-op | updated) or 201 (created).
// On failure, status is 5xx.
func rbacAssignFindOrCreate(
	ctx context.Context,
	client dynamic.Interface,
	body rbacAssignRequest,
	dep *Deployment,
) (rbacAssignResponse, int, string, error) {
	wantScope := normalizeScopeSlice(body.Scope)
	wantTier := strings.ToLower(strings.TrimSpace(body.Tier))
	// resolvedTier is what the ClusterRole binding actually points at.
	// For `super-admin` this collapses to `owner`; for everything else
	// it equals wantTier. Persisted on the CR's spec.tierRoleRef +
	// echoed in the response's TierClusterRole. The audit/label tier
	// remains wantTier so the audit trail records the requested
	// vocabulary.
	resolvedTier := rbacAssignTierResolved(wantTier)
	keycloakSubject := strings.TrimSpace(body.User.KeycloakSubject)

	var lastErr error
	for attempt := 0; attempt < rbacAssignMaxRetries; attempt++ {
		// 1. List candidate UserAccess CRs. Filter happens in-memory
		//    on the keycloak-subject because the CRD doesn't index on
		//    spec fields and a label-selector for the subject UUID is
		//    expensive to set up at write time. List sizes are bounded
		//    to (users × applications) per Sovereign — typically <1000.
		//
		//    Scoped to rbacAssignNamespace: every UserAccess CR
		//    rbac_assign emits lives in catalyst-system, so listing
		//    cluster-wide is wasteful and (after the namespace fix
		//    below) would surface CRs we can't update via the same
		//    GVR namespace. Cluster-wide listing also widens the RBAC
		//    surface unnecessarily.
		listIface, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				// CRD not installed yet → fall through to create. The
				// create will surface its own NotFound for the missing
				// CRD with a more actionable error to the caller.
				resp, status, err := rbacAssignCreate(ctx, client, body, wantScope, wantTier, resolvedTier, dep)
				return resp, status, "", err
			}
			return rbacAssignResponse{}, http.StatusInternalServerError, "", fmt.Errorf("list useraccesses: %w", err)
		}
		match := rbacAssignFindMatch(listIface.Items, keycloakSubject, wantScope)
		if match != nil {
			currentTier := strings.ToLower(rbacAssignReadTierLabel(match))
			if currentTier == wantTier {
				// Path 2a: same scope, same tier → no-op.
				return rbacAssignResponse{
					UserAccess: rbacAssignUserAccessRef{
						Name:      match.GetName(),
						UID:       string(match.GetUID()),
						Namespace: match.GetNamespace(),
					},
					TierClusterRole: tierClusterRolePrefix + resolvedTier,
					Applied:         "no-op",
				}, http.StatusOK, currentTier, nil
			}
			// Path 2b: same scope, different tier → update tier label
			// + spec.tierRoleRef. Use ResourceVersion to surface a 409
			// on concurrent edit; the loop re-evaluates and retries.
			updated, err := rbacAssignUpdateTier(ctx, client, match, wantTier, resolvedTier)
			if err != nil {
				if apierrors.IsConflict(err) {
					// Concurrent writer mutated the CR — retry the
					// list+evaluate cycle. After rbacAssignMaxRetries
					// attempts, surface the conflict.
					lastErr = err
					continue
				}
				return rbacAssignResponse{}, http.StatusInternalServerError, currentTier, fmt.Errorf("update useraccess tier: %w", err)
			}
			return rbacAssignResponse{
				UserAccess: rbacAssignUserAccessRef{
					Name:      updated.GetName(),
					UID:       string(updated.GetUID()),
					Namespace: updated.GetNamespace(),
				},
				TierClusterRole: tierClusterRolePrefix + resolvedTier,
				Applied:         "updated",
			}, http.StatusOK, currentTier, nil
		}
		// Path 3: no match → create.
		resp, status, err := rbacAssignCreate(ctx, client, body, wantScope, wantTier, resolvedTier, dep)
		if err != nil && apierrors.IsAlreadyExists(err) {
			// Concurrent creator won the race for the same name. Retry
			// the list+evaluate cycle so we catch their CR and either
			// no-op (same tier) or update it (different tier).
			lastErr = err
			continue
		}
		if err != nil {
			return resp, status, "", err
		}
		return resp, status, "", nil
	}
	if lastErr != nil {
		return rbacAssignResponse{}, http.StatusConflict, "", fmt.Errorf("rbac-assign: gave up after %d retries: %w", rbacAssignMaxRetries, lastErr)
	}
	// Loop exited without an error or a return — defensive; should be
	// unreachable in practice.
	return rbacAssignResponse{}, http.StatusInternalServerError, "", errors.New("rbac-assign: unexpected loop exit")
}

// rbacAssignCreate composes a fresh UserAccess CR from the request and
// POSTs it. Returns 201 on success, 4xx/5xx on schema or apiserver
// errors. The created CR carries:
//   - metadata.labels[catalyst.openova.io/tier] = <tier>
//   - metadata.labels[catalyst.openova.io/managed-by] = "rbac-assign"
//   - spec.user.keycloakSubject (or keycloakGroups, eventually)
//   - spec.sovereignRef = dep.SovereignFQDN slug
//   - spec.tierRoleRef = "openova:tier-<tier>"
//   - spec.scopes[] = normalized scope set
func rbacAssignCreate(
	ctx context.Context,
	client dynamic.Interface,
	body rbacAssignRequest,
	wantScope []rbacAssignScopeBody,
	wantTier string,
	resolvedTier string,
	dep *Deployment,
) (rbacAssignResponse, int, error) {
	keycloakSubject := strings.TrimSpace(body.User.KeycloakSubject)
	name := rbacAssignName(keycloakSubject, body.User.Email, wantScope)

	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(userAccessGroup + "/" + userAccessVersion)
	obj.SetKind("UserAccess")
	obj.SetName(name)
	obj.SetLabels(map[string]string{
		labelTier:                          wantTier,
		"catalyst.openova.io/managed-by":   "rbac-assign",
	})

	user := map[string]any{}
	if keycloakSubject != "" {
		user["keycloakSubject"] = keycloakSubject
	}
	spec := map[string]any{
		"user":         user,
		"sovereignRef": rbacAssignSovereignRef(dep),
		"tierRoleRef":  tierClusterRolePrefix + resolvedTier,
	}
	if len(wantScope) > 0 {
		scopes := make([]any, 0, len(wantScope))
		for _, s := range wantScope {
			scopes = append(scopes, map[string]any{
				"key":   s.Key,
				"value": s.Value,
			})
		}
		spec["scopes"] = scopes
	}
	_ = unstructured.SetNestedMap(obj.Object, spec, "spec")

	// Set the namespace on the CR before Create — namespaced CRDs
	// reject empty-namespace Create with a confusing 404 ("the server
	// could not find the requested resource") because the apiserver
	// dispatches to the cluster-scoped REST endpoint, which doesn't
	// exist for a namespaced kind. See rbacAssignNamespace doc.
	obj.SetNamespace(rbacAssignNamespace)
	created, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		// Caller distinguishes IsAlreadyExists for the retry loop.
		return rbacAssignResponse{}, http.StatusInternalServerError, err
	}
	return rbacAssignResponse{
		UserAccess: rbacAssignUserAccessRef{
			Name:      created.GetName(),
			UID:       string(created.GetUID()),
			Namespace: created.GetNamespace(),
		},
		TierClusterRole: tierClusterRolePrefix + resolvedTier,
		Applied:         "created",
	}, http.StatusCreated, nil
}

// rbacAssignUpdateTier patches the tier label + spec.tierRoleRef on an
// existing UserAccess CR. Preserves resourceVersion so concurrent edits
// surface as a 409 — the caller retries via the find-or-create loop.
//
// `wantTier` is the audit/label tier (carries the request-vocabulary,
// e.g. "super-admin"). `resolvedTier` is the underlying ClusterRole
// suffix (e.g. "owner") wired into spec.tierRoleRef.
func rbacAssignUpdateTier(
	ctx context.Context,
	client dynamic.Interface,
	current *unstructured.Unstructured,
	wantTier string,
	resolvedTier string,
) (*unstructured.Unstructured, error) {
	desired := current.DeepCopy()
	labels := desired.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelTier] = wantTier
	desired.SetLabels(labels)
	_ = unstructured.SetNestedField(
		desired.Object,
		tierClusterRolePrefix+resolvedTier,
		"spec", "tierRoleRef",
	)
	// Update on a namespaced CRD requires the same namespace path as
	// Create — fall back to rbacAssignNamespace when the existing CR
	// somehow lacks one (defensive; the list path scopes to the same
	// namespace so this should always be set).
	ns := desired.GetNamespace()
	if ns == "" {
		ns = rbacAssignNamespace
		desired.SetNamespace(ns)
	}
	return client.Resource(UserAccessGVR()).Namespace(ns).Update(ctx, desired, metav1.UpdateOptions{})
}

// ── Helpers ──────────────────────────────────────────────────────────

// rbacAssignFindMatch scans the candidate list for a UserAccess CR
// whose keycloak-subject + scope set matches the request. The scope
// match is exact-set-equality (sorted by key+value before compare).
// Returns nil if no candidate matches.
func rbacAssignFindMatch(
	items []unstructured.Unstructured,
	keycloakSubject string,
	wantScope []rbacAssignScopeBody,
) *unstructured.Unstructured {
	for i := range items {
		item := &items[i]
		// Match keycloak-subject. Both fields are strings on the CR's
		// spec.user; we check both keycloakSubject and keycloakGroups
		// (a future enhancement; today only subject-based grants flow
		// through /rbac/assign).
		if rbacAssignReadKeycloakSubject(item) != keycloakSubject {
			continue
		}
		// Match scope set.
		gotScope := rbacAssignReadScopes(item)
		if scopeSetsEqual(gotScope, wantScope) {
			return item
		}
	}
	return nil
}

// rbacAssignReadKeycloakSubject reads spec.user.keycloakSubject from a
// UserAccess unstructured. Returns "" if unset.
func rbacAssignReadKeycloakSubject(u *unstructured.Unstructured) string {
	v, ok, _ := unstructured.NestedString(u.Object, "spec", "user", "keycloakSubject")
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// rbacAssignReadScopes reads spec.scopes[] from a UserAccess
// unstructured into the canonical sorted-slice form.
func rbacAssignReadScopes(u *unstructured.Unstructured) []rbacAssignScopeBody {
	raw, ok, _ := unstructured.NestedSlice(u.Object, "spec", "scopes")
	if !ok {
		return nil
	}
	out := make([]rbacAssignScopeBody, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		k, _ := m["key"].(string)
		v, _ := m["value"].(string)
		out = append(out, rbacAssignScopeBody{
			Key:   strings.TrimSpace(k),
			Value: strings.TrimSpace(v),
		})
	}
	return normalizeScopeSlice(out)
}

// rbacAssignReadTierLabel reads metadata.labels[catalyst.openova.io/tier]
// from a UserAccess CR. Returns "" if unset.
func rbacAssignReadTierLabel(u *unstructured.Unstructured) string {
	labels := u.GetLabels()
	if labels == nil {
		return ""
	}
	return strings.TrimSpace(labels[labelTier])
}

// normalizeScopeSlice trims, drops empty entries, and sorts a scope
// slice so two slices declaring the same scope set in different order
// compare equal. The canonical form is sorted by (key, value).
func normalizeScopeSlice(in []rbacAssignScopeBody) []rbacAssignScopeBody {
	out := make([]rbacAssignScopeBody, 0, len(in))
	for _, s := range in {
		k := strings.TrimSpace(s.Key)
		v := strings.TrimSpace(s.Value)
		if k == "" && v == "" {
			continue
		}
		out = append(out, rbacAssignScopeBody{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// scopeSetsEqual reports whether two normalized scope slices declare the
// same set. Both inputs MUST be normalized (sorted, trimmed) — pass
// the output of normalizeScopeSlice.
func scopeSetsEqual(a, b []rbacAssignScopeBody) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

// rbacAssignName builds a deterministic CR name from
// (keycloak-subject || email) + scope set so a re-run of /rbac/assign
// targets the same CR. The name is K8s-safe (lower-case alphanumeric +
// hyphens, max 63 chars per RFC 1123). Format:
//
//	rbac-<subject-prefix-12>-<scope-fingerprint-8>
//
// The fingerprint is a 32-bit FNV hash of the sorted scope set
// rendered as "<k>=<v>;..." — collisions on the truncated 8-hex form
// are tolerable because the find-or-create logic's subsequent
// scopeSetsEqual check filters out any collision survivors.
func rbacAssignName(keycloakSubject, email string, normalizedScope []rbacAssignScopeBody) string {
	subj := strings.ToLower(strings.TrimSpace(keycloakSubject))
	if subj == "" {
		subj = strings.ToLower(strings.TrimSpace(email))
	}
	subj = sanitizeK8sNameSegment(subj)
	if len(subj) > 12 {
		subj = subj[:12]
	}
	if subj == "" {
		subj = "user"
	}
	var sb strings.Builder
	for i, s := range normalizedScope {
		if i > 0 {
			sb.WriteByte(';')
		}
		sb.WriteString(s.Key)
		sb.WriteByte('=')
		sb.WriteString(s.Value)
	}
	hash := fnv32a(sb.String())
	return fmt.Sprintf("%s-%s-%08x", rbacAssignNamePrefix, subj, hash)
}

// sanitizeK8sNameSegment strips characters that K8s names disallow
// (RFC 1123 — lowercase alphanumeric + hyphens; no leading/trailing
// hyphens). Used for the subject-prefix part of generated CR names.
func sanitizeK8sNameSegment(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z':
			sb.WriteRune(r)
		case r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == '-':
			sb.WriteRune(r)
		}
	}
	out := sb.String()
	out = strings.Trim(out, "-")
	return out
}

// fnv32a returns the FNV-1a 32-bit hash of the input string. Stdlib
// hash/fnv is intentionally not imported here to avoid a dependency
// just for one short helper — the algorithm is canonical.
func fnv32a(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	var h uint32 = offset32
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// rbacAssignSovereignRef returns the slug used for the Sovereign's
// provider-kubernetes ProviderConfig name (the Composition resolves
// `sovereign-<sovereignRef>` to the live target). Mirrors the slug
// normalization applied to user-authored UserAccess CRs.
func rbacAssignSovereignRef(dep *Deployment) string {
	if dep == nil {
		return ""
	}
	if dep.Result != nil && strings.TrimSpace(dep.Result.SovereignFQDN) != "" {
		return rbacAssignSlug(dep.Result.SovereignFQDN)
	}
	return rbacAssignSlug(dep.Request.SovereignFQDN)
}

// rbacAssignSlug reduces a FQDN like "omantel.omani.works" to the
// short slug "omantel" used in the Composition's ProviderConfig name.
// First DNS label only.
func rbacAssignSlug(fqdn string) string {
	fqdn = strings.TrimSpace(fqdn)
	if fqdn == "" {
		return ""
	}
	if idx := strings.Index(fqdn, "."); idx > 0 {
		return strings.ToLower(fqdn[:idx])
	}
	return strings.ToLower(fqdn)
}

// ── Validation + authorization ────────────────────────────────────────

// validateRBACAssignRequest checks the request body shape per the slice
// A1 brief. Returns ("", true) on success; (msg, false) on the first
// problem (matches the existing user_access.go validation style).
//
// Per `feedback_no_mvp_no_workarounds.md` (TC-167) the email field —
// when populated — MUST conform to RFC-5322's basic shape (one '@',
// non-empty local + domain, domain has at least one '.', label
// charset). Iter-8 caught the regression: `{"email":"badformat"}`
// flowed through to a successful UserAccess CR create because the
// previous validation only checked emptiness. Reject mal-shaped
// emails up-front with a 400 instead of letting a downstream label
// or namespace check surface the real problem.
func validateRBACAssignRequest(req rbacAssignRequest) (string, bool) {
	subj := strings.TrimSpace(req.User.KeycloakSubject)
	email := strings.TrimSpace(req.User.Email)
	if subj == "" && email == "" {
		return "user.email or user.keycloakSubject is required", false
	}
	if email != "" {
		if msg, ok := validateEmailAddressShape(email); !ok {
			return "user.email " + msg, false
		}
	}
	tier := strings.ToLower(strings.TrimSpace(req.Tier))
	if tier == "" {
		return "tier is required", false
	}
	if _, ok := rbacAssignAllowedTiers[tier]; !ok {
		return "tier must be one of viewer, developer, operator, admin, owner, super-admin", false
	}
	for i, s := range req.Scope {
		k := strings.TrimSpace(s.Key)
		v := strings.TrimSpace(s.Value)
		if k == "" {
			return fmt.Sprintf("scope[%d].key is required", i), false
		}
		if v == "" {
			return fmt.Sprintf("scope[%d].value is required", i), false
		}
	}
	return "", true
}

// validateEmailAddressShape implements the basic RFC-5322-leaning
// shape check the matrix asserts on (TC-167). Avoids importing
// `net/mail` because the stdlib parser ALSO accepts display-name +
// brackets like `"Alice <alice@x.y>"` which is wider than the
// /rbac/assign request contract wants. Returns (msg, true) on
// success; (msg, false) with a short reason on rejection.
//
// Shape: <local>@<domain> where:
//   - local: 1..64 chars, no spaces, no ASCII control chars
//   - domain: 1..253 chars, contains at least one dot, each label
//     1..63 chars of alphanumeric or hyphen (no leading/trailing
//     hyphen), TLD label 2+ chars
//
// Strict-enough to reject "badformat", "alice", "x@y" (no TLD dot),
// "@example.com" (no local), "alice@@example.com" (multiple @).
// Permissive-enough to accept the canonical work email shapes the
// matrix uses (qa-user1@openova.io, alice.smith+plus@example.co.uk).
func validateEmailAddressShape(email string) (string, bool) {
	if email == "" {
		return "is required", false
	}
	for _, r := range email {
		if r <= 0x20 || r == 0x7f {
			return "must not contain whitespace or control characters", false
		}
	}
	at := strings.Index(email, "@")
	if at < 0 || strings.Index(email[at+1:], "@") >= 0 {
		return "must contain exactly one '@'", false
	}
	local := email[:at]
	domain := email[at+1:]
	if local == "" {
		return "local part (before '@') is required", false
	}
	if len(local) > 64 {
		return "local part is too long (max 64 chars)", false
	}
	if domain == "" {
		return "domain part (after '@') is required", false
	}
	if len(domain) > 253 {
		return "domain part is too long (max 253 chars)", false
	}
	if !strings.Contains(domain, ".") {
		return "domain must contain at least one '.'", false
	}
	labels := strings.Split(domain, ".")
	for i, label := range labels {
		if label == "" {
			return "domain has an empty label", false
		}
		if len(label) > 63 {
			return "domain label too long (max 63 chars)", false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "domain label may not start or end with '-'", false
		}
		for _, r := range label {
			ok := (r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') ||
				r == '-'
			if !ok {
				return "domain label contains invalid characters", false
			}
		}
		if i == len(labels)-1 && len(label) < 2 {
			return "domain TLD must be at least 2 characters", false
		}
	}
	return "", true
}

// rbacAssignCallerAuthorized checks whether the caller's claims include
// any of the privileged realm roles allowed to grant tiers. Empty roles
// list ⇒ false. Conservative-by-default: any unrecognized claim shape
// rejects.
func rbacAssignCallerAuthorized(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	for _, want := range rbacAssignPrivilegedRoles {
		if claims.HasRealmRole(want) {
			return true
		}
	}
	// Tier check via the custom `tier` claim (admin/owner): a Sovereign
	// with the EPIC-3 protocol mapper wired emits this claim directly,
	// so we don't have to walk realm_access.roles for tier-* role names.
	switch strings.ToLower(strings.TrimSpace(claims.Tier)) {
	case "admin", "owner":
		return true
	}
	return false
}
