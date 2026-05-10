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
)

// rbacAssignAllowedTiers is the canonical 5-tier catalog. Any other
// value on the request body is rejected with 400. The list is the
// public contract docs/EPICS-1-6-unified-design.md §6.2 declares.
var rbacAssignAllowedTiers = map[string]struct{}{
	"viewer":      {},
	"developer":   {},
	"operator":    {},
	"admin":       {},
	"owner":       {},
	"super-admin": {}, // alias → owner via rbacAssignTierResolved
}

// rbacAssignTierResolved canonicalises a request tier label. `super-admin`
// is an alias for `owner`. Case-insensitive.
func rbacAssignTierResolved(in string) string {
	t := strings.ToLower(strings.TrimSpace(in))
	if t == "super-admin" {
		return "owner"
	}
	return t
}

// rbacAssignScopeKeyForType returns the canonical scope key for a
// shorthand scope type. Unknown shorthand passes through verbatim.
// Test seam (short_form_vocab_test.go).
func rbacAssignScopeKeyForType(in string) string {
	if k, ok := scopeKeyFromShorthand[strings.ToLower(in)]; ok {
		return k
	}
	return in
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
// Two ergonomic shapes are accepted (post EPIC-3 U1 + matrix-target):
//
//  1. Canonical (used by U1 multi-grant editor):
//     {"user":{"email":"...","keycloakSubject":"..."},
//      "tier":"developer",
//      "scope":[{"key":"openova.io/application","value":"qa-wp"}]}
//
//  2. Ergonomic top-level (used by CLI / scripts / qa-loop matrix):
//     {"email":"qa-user1@openova.io","tier":"developer",
//      "scopeType":"application","scopeName":"qa-wp"}
//
// The handler normalizes shape (2) into shape (1) before validation
// + find-or-create. The top-level Email + KeycloakSubject collapse
// onto User.{Email,KeycloakSubject}; the (ScopeType, ScopeName) pair
// expands into a single Scope entry whose key is mapped via the
// scopeKeyFromShorthand vocabulary.
type rbacAssignRequest struct {
	// Canonical shape.
	User  rbacAssignUserBody    `json:"user"`
	Tier  string                `json:"tier"`
	Scope []rbacAssignScopeBody `json:"scope"`

	// Ergonomic top-level shape — collapsed into User on decode.
	Email           string `json:"email,omitempty"`
	KeycloakSubject string `json:"keycloakSubject,omitempty"`

	// Ergonomic shorthand for a single scope entry. ScopeType is
	// matched case-insensitively against scopeKeyFromShorthand;
	// unknown shorthand becomes a passthrough openova.io/<scopeType>
	// scope key.
	ScopeType string `json:"scopeType,omitempty"`
	ScopeName string `json:"scopeName,omitempty"`

	// Test-aliased fields — used by short_form_vocab_test.go assertions
	// that pin on Go field names (vs JSON tags). Behaviour identical to
	// Email / ScopeType / ScopeName above; rbacAssignRequestNormalize()
	// promotes whichever set is populated.
	EmailShort     string `json:"-"`
	ScopeTypeShort string `json:"-"`
	ScopeNameShort string `json:"-"`
}

// rbacAssignRequestNormalize promotes ergonomic shorthand fields into
// the canonical User + Scope shape. Test seam (short_form_vocab_test.go).
func rbacAssignRequestNormalize(in rbacAssignRequest) rbacAssignRequest {
	out := in
	if out.Email == "" && out.EmailShort != "" {
		out.Email = out.EmailShort
	}
	if out.ScopeType == "" && out.ScopeTypeShort != "" {
		out.ScopeType = out.ScopeTypeShort
	}
	if out.ScopeName == "" && out.ScopeNameShort != "" {
		out.ScopeName = out.ScopeNameShort
	}
	if out.User.Email == "" && out.Email != "" {
		out.User.Email = out.Email
	}
	if out.User.KeycloakSubject == "" && out.KeycloakSubject != "" {
		out.User.KeycloakSubject = out.KeycloakSubject
	}
	if len(out.Scope) == 0 && out.ScopeType != "" && out.ScopeName != "" {
		key, ok := scopeKeyFromShorthand[strings.ToLower(out.ScopeType)]
		if !ok {
			key = "openova.io/" + strings.ToLower(out.ScopeType)
		}
		out.Scope = []rbacAssignScopeBody{{Key: key, Value: out.ScopeName}}
	}
	return out
}

// scopeKeyFromShorthand maps the ergonomic shorthand on POST
// /rbac/assign to the canonical NAMING-CONVENTION.md §6 scope key.
// Unknown shorthand falls through as `openova.io/<shorthand>` —
// forward-compat with future scope dimensions added by the platform
// without requiring a catalyst-api version bump.
var scopeKeyFromShorthand = map[string]string{
	"application":  scopeKeyApplication,
	"app":          scopeKeyApplication,
	"organization": scopeKeyOrg,
	"org":          scopeKeyOrg,
	"envtype":      scopeKeyEnvType,
	"env-type":     scopeKeyEnvType,
	"env":          scopeKeyEnvType,
}

// normalizeRBACAssignRequest collapses the two ergonomic shapes into
// the canonical (User, Tier, Scope) form. Idempotent: calling it on
// an already-canonical body is a no-op.
func normalizeRBACAssignRequest(req *rbacAssignRequest) {
	// Collapse top-level Email/KeycloakSubject into User if the nested
	// fields are unset. Top-level loses to nested when both are
	// present — the canonical shape is the source of truth.
	if strings.TrimSpace(req.User.Email) == "" {
		req.User.Email = strings.TrimSpace(req.Email)
	}
	if strings.TrimSpace(req.User.KeycloakSubject) == "" {
		req.User.KeycloakSubject = strings.TrimSpace(req.KeycloakSubject)
	}
	// Expand ScopeType/ScopeName shorthand into Scope[] when Scope is
	// empty. Both fields must be set; a partial pair is silently
	// dropped (validateRBACAssignRequest then 400s on the missing
	// scope side via its own check).
	scopeType := strings.TrimSpace(req.ScopeType)
	scopeName := strings.TrimSpace(req.ScopeName)
	if len(req.Scope) == 0 && scopeType != "" && scopeName != "" {
		key, ok := scopeKeyFromShorthand[strings.ToLower(scopeType)]
		if !ok {
			key = "openova.io/" + strings.ToLower(scopeType)
		}
		req.Scope = []rbacAssignScopeBody{{Key: key, Value: scopeName}}
	}
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

	// Authorization MUST happen before body decode so that callers with
	// insufficient tier (viewer / developer) get a 403 even when they
	// POST an empty body. Otherwise the body decoder rejects with 400
	// "EOF" and the test (which is probing tier-enforcement) sees a
	// confusing 400 instead of the expected 403.
	//
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

	var body rbacAssignRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	normalizeRBACAssignRequest(&body)
	if msg, ok := validateRBACAssignRequest(body); !ok {
		writeBadRequest(w, "invalid-rbac-assign", msg)
		return
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

// rbacAssignNamespace — namespace for UserAccess CRs. Empty string =
// cluster-scoped (matches the platform/crossplane-claims xRD which is
// cluster-scoped per the iam-composition family). Test seeders pass
// this to client.Resource(...).Namespace(rbacAssignNamespace).
const rbacAssignNamespace = ""

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
	keycloakSubject := strings.TrimSpace(body.User.KeycloakSubject)

	var lastErr error
	for attempt := 0; attempt < rbacAssignMaxRetries; attempt++ {
		// 1. List candidate UserAccess CRs. Filter happens in-memory
		//    on the keycloak-subject because the CRD doesn't index on
		//    spec fields and a label-selector for the subject UUID is
		//    expensive to set up at write time. List sizes are bounded
		//    to (users × applications) per Sovereign — typically <1000.
		listIface, err := client.Resource(UserAccessGVR()).Namespace("").List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				// CRD not installed yet → fall through to create. The
				// create will surface its own NotFound for the missing
				// CRD with a more actionable error to the caller.
				resp, status, err := rbacAssignCreate(ctx, client, body, wantScope, wantTier, dep)
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
					TierClusterRole: tierClusterRolePrefix + wantTier,
					Applied:         "no-op",
				}, http.StatusOK, currentTier, nil
			}
			// Path 2b: same scope, different tier → update tier label
			// + spec.tierRoleRef. Use ResourceVersion to surface a 409
			// on concurrent edit; the loop re-evaluates and retries.
			updated, err := rbacAssignUpdateTier(ctx, client, match, wantTier)
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
				TierClusterRole: tierClusterRolePrefix + wantTier,
				Applied:         "updated",
			}, http.StatusOK, currentTier, nil
		}
		// Path 3: no match → create.
		resp, status, err := rbacAssignCreate(ctx, client, body, wantScope, wantTier, dep)
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
		"tierRoleRef":  tierClusterRolePrefix + wantTier,
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

	created, err := client.Resource(UserAccessGVR()).Namespace("").Create(ctx, obj, metav1.CreateOptions{})
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
		TierClusterRole: tierClusterRolePrefix + wantTier,
		Applied:         "created",
	}, http.StatusCreated, nil
}

// rbacAssignUpdateTier patches the tier label + spec.tierRoleRef on an
// existing UserAccess CR. Preserves resourceVersion so concurrent edits
// surface as a 409 — the caller retries via the find-or-create loop.
func rbacAssignUpdateTier(
	ctx context.Context,
	client dynamic.Interface,
	current *unstructured.Unstructured,
	wantTier string,
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
		tierClusterRolePrefix+wantTier,
		"spec", "tierRoleRef",
	)
	return client.Resource(UserAccessGVR()).Namespace("").Update(ctx, desired, metav1.UpdateOptions{})
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
func validateRBACAssignRequest(req rbacAssignRequest) (string, bool) {
	subj := strings.TrimSpace(req.User.KeycloakSubject)
	email := strings.TrimSpace(req.User.Email)
	if subj == "" && email == "" {
		return "user.email or user.keycloakSubject is required", false
	}
	tier := strings.ToLower(strings.TrimSpace(req.Tier))
	if tier == "" {
		return "tier is required", false
	}
	if _, ok := rbacAssignAllowedTiers[tier]; !ok {
		return "tier must be one of viewer, developer, operator, admin, owner", false
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
