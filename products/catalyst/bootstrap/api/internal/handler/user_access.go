// Package handler — user_access.go: REST surface for the Sovereign IAM
// User-Access editor (issue #323).
//
// CRUD endpoints over the UserAccess Claim shape shipped by issue
// #322 (`access.openova.io/v1alpha1`):
//
//	GET    /api/v1/deployments/{depId}/admin/user-access
//	POST   /api/v1/deployments/{depId}/admin/user-access
//	PUT    /api/v1/deployments/{depId}/admin/user-access/{name}
//	DELETE /api/v1/deployments/{depId}/admin/user-access/{name}
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (Crossplane is the ONLY day-2
// IaC, no bespoke kubectl-apply of RoleBindings) every mutation
// is expressed as a UserAccess Claim write against the SOVEREIGN
// cluster's dynamic client. The Crossplane Composition shipped by
// #322 (useraccess.compose.openova.io) reconciles the claim into
// per-(application × namespace × role) RoleBindings via
// provider-kubernetes.
//
// # Shape (canonical, post #322 merge)
//
//	apiVersion: access.openova.io/v1alpha1
//	kind: UserAccess          # cluster-scoped
//	spec:
//	  user:
//	    keycloakSubject: <oidc-sub>     # one OR
//	    keycloakGroups: [<group>, ...]  # the other (or both)
//	  sovereignRef: <slug>              # selects provider-kubernetes ProviderConfig
//	  applications:
//	    - app: helmwatch
//	      role: editor                  # admin | editor | viewer
//	      namespaces: [...]             # optional; api expands when empty
//	      vClusters: [...]              # optional; rendered as vcluster-<name>
//
// # Why dynamic, not typed
//
// The CRD shape (#322) is consumed cross-repo via dynamic client by
// API path so catalyst-api carries no Go-type dependency on #322's
// chart. This matches the same seam infrastructure.go uses for the
// XRC kinds.
//
// # Anti-duplication seam
//
// This handler reuses the `sovereignDynamicClient(dep)` seam already
// established in infrastructure.go (line 1557). It does NOT duplicate
// the kubeconfig-read logic. Same `dynamicFactory` test injection
// path infrastructure_crud_test.go uses.
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// UserAccess CR group/version/resource (#322 shape — the Claim
// surface, not the underlying XR).
const (
	userAccessGroup    = "access.openova.io"
	userAccessVersion  = "v1alpha1"
	userAccessResource = "useraccesses"
)

// UserAccessGVR — exposed for tests so the fake dynamic client can
// register the matching list-kind.
func UserAccessGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    userAccessGroup,
		Version:  userAccessVersion,
		Resource: userAccessResource,
	}
}

// userAccessRoles — the three role short-forms the CRD's openAPIV3
// `enum` allows. The Composition (#322) rewrites these to the
// canonical openova:application-<role> ClusterRole names; the
// catalyst-api accepts only the short form on writes.
var userAccessRoles = map[string]struct{}{
	"admin":  {},
	"editor": {},
	"viewer": {},
}

/* ── Wire shapes — match the CRD .spec verbatim ─────────────────── */

// userAccessRequest is the body of POST /admin/user-access and PUT
// /admin/user-access/{name}.
//
// Two equivalent shapes are accepted (per
// `feedback_no_mvp_no_workarounds.md` — the canonical UAT matrix is
// the contract):
//
//  1. Long form (production internal callers + the IAM editor UI):
//     { "name":"acme-rb-1",
//     "spec":{ "user":{...}, "sovereignRef":"acme",
//     "applications":[{...}] } }
//
//  2. Short form (canonical UAT matrix vocabulary):
//     POST { "email":"qa-user2@openova.io", "tier":"viewer" }
//     PUT  { "tier":"developer" }   (name comes from the URL)
//
// The short form collapses onto the long form via
// userAccessRequestNormalize:
//
//	email      → User.KeycloakSubject (until Keycloak resolves it; the
//	             controller will rotate this to the real subject UUID
//	             on first reconcile via a sub-claim lookup)
//	tier       → Applications[0].Role  (via userAccessTierToRole)
//	            + Applications[0].App = "*" (sovereign-wide grant)
//
// The depId path-param drives sovereignRef when the body omits it.
type userAccessRequest struct {
	Name string             `json:"name"`
	Spec userAccessSpecBody `json:"spec"`

	// Short-form aliases (canonical UAT matrix vocabulary).
	EmailShort string `json:"email,omitempty"`
	TierShort  string `json:"tier,omitempty"`
}

// userAccessTierToRole maps the canonical 6-tier vocabulary onto the
// 3-role short-form the UserAccess CRD's enum allows. Aligns with the
// EPIC-3 T1 (#1142) 5-tier ClusterRoles + the cross-org `super-admin`
// sentinel.
//
//	viewer       → viewer
//	developer    → editor
//	operator     → editor
//	admin        → admin
//	owner        → admin
//	super-admin  → admin
//
// Unknown tiers fall through to the input — validation later rejects
// them with a clear 400 listing the allowed set.
func userAccessTierToRole(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "viewer":
		return "viewer"
	case "developer", "operator":
		return "editor"
	case "admin", "owner", "super-admin":
		return "admin"
	}
	return strings.TrimSpace(tier)
}

// userAccessRequestNormalize collapses the short-form aliases onto
// the long-form fields. The `depID` is the path-param so the handler
// can derive sovereignRef when the body omits it (the matrix's short
// form does). `urlName` is the {name} URL-param (PUT path) when set.
func userAccessRequestNormalize(b userAccessRequest, depID, urlName string) userAccessRequest {
	if strings.TrimSpace(b.Name) == "" {
		if strings.TrimSpace(urlName) != "" {
			b.Name = strings.TrimSpace(urlName)
		} else if strings.TrimSpace(b.EmailShort) != "" {
			b.Name = userAccessSanitizeName(strings.ToLower(strings.TrimSpace(b.EmailShort)))
			if b.Name == "" {
				b.Name = fmt.Sprintf("ua-%08x", userAccessFNV32a(strings.TrimSpace(b.EmailShort)))
			}
			if len(b.Name) > 63 {
				b.Name = b.Name[:63]
			}
		}
	}
	if strings.TrimSpace(b.Spec.User.KeycloakSubject) == "" && strings.TrimSpace(b.EmailShort) != "" {
		b.Spec.User.KeycloakSubject = strings.TrimSpace(b.EmailShort)
	}
	if strings.TrimSpace(b.Spec.SovereignRef) == "" && strings.TrimSpace(depID) != "" {
		b.Spec.SovereignRef = strings.TrimSpace(depID)
	}
	if strings.TrimSpace(b.TierShort) != "" {
		role := userAccessTierToRole(b.TierShort)
		if len(b.Spec.Applications) == 0 {
			b.Spec.Applications = []userAccessAppGrantBody{{
				App:  "*",
				Role: role,
			}}
		} else {
			// PUT short-form on an existing-shape CR: rotate every app's
			// role to the new tier-derived role.
			for i := range b.Spec.Applications {
				b.Spec.Applications[i].Role = role
			}
		}
	}
	return b
}

// isUserAccessShortFormPut reports whether the request body looks like
// the canonical UAT matrix's PUT short form — only `tier` (or `email`)
// supplied, no full long-form spec. Used by UpdateUserAccess to merge
// the existing CR's user/sovereignRef/applications when the body omits
// them rather than replacing with empty values.
func isUserAccessShortFormPut(b userAccessRequest) bool {
	if strings.TrimSpace(b.TierShort) == "" && strings.TrimSpace(b.EmailShort) == "" {
		return false
	}
	// Long-form indicators: explicit applications list, or explicit
	// keycloakGroups, or explicit sovereignRef. When ANY of those are
	// present the caller is doing a real long-form replace.
	if len(b.Spec.Applications) > 1 {
		return false
	}
	if len(b.Spec.Applications) == 1 && b.Spec.Applications[0].App != "*" {
		return false
	}
	if len(b.Spec.User.KeycloakGroups) > 0 {
		return false
	}
	return true
}

// userAccessSanitizeName — RFC 1123 lowercase alphanumeric + hyphens
// (no leading/trailing hyphen). Mirrors sanitizeK8sNameSegment in
// rbac_assign.go but local to user_access.go to avoid a cross-file
// dependency on the package-private helper while keeping the import
// graph clean.
func userAccessSanitizeName(s string) string {
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
	return strings.Trim(sb.String(), "-")
}

// userAccessFNV32a — FNV-1a 32-bit hash. Mirrors fnv32a in
// rbac_assign.go. Local copy keeps user_access.go independently
// re-orderable in the package.
func userAccessFNV32a(s string) uint32 {
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

type userAccessSpecBody struct {
	User         userAccessUserBody       `json:"user"`
	SovereignRef string                   `json:"sovereignRef"`
	Applications []userAccessAppGrantBody `json:"applications"`
}

type userAccessUserBody struct {
	KeycloakSubject string   `json:"keycloakSubject,omitempty"`
	KeycloakGroups  []string `json:"keycloakGroups,omitempty"`
}

type userAccessAppGrantBody struct {
	App        string   `json:"app"`
	Role       string   `json:"role"`
	Namespaces []string `json:"namespaces,omitempty"`
	VClusters  []string `json:"vClusters,omitempty"`
}

type userAccessItem struct {
	Name              string                  `json:"name"`
	Spec              userAccessSpecBody      `json:"spec"`
	Status            *userAccessStatusBody   `json:"status,omitempty"`
	CreationTimestamp string                  `json:"creationTimestamp,omitempty"`
}

type userAccessStatusBody struct {
	RolebindingsCreated int    `json:"rolebindingsCreated"`
	ProviderConfigRef   string `json:"providerConfigRef,omitempty"`
}

type userAccessListResponse struct {
	Items []userAccessItem `json:"items"`
}

/* ── HTTP handlers ──────────────────────────────────────────────── */

// ListUserAccess — GET .../admin/user-access
func (h *Handler) ListUserAccess(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	// UserAccess Claims are cluster-scoped per the CRD definition
	// (`spec.scope` is the default `Namespaced`-equivalent for
	// Crossplane Claims, but the XRD declares it cluster-scoped via
	// the absence of a `Namespaced` claim type — see the chart's
	// xrds/useraccess.yaml). We list across the cluster and namespace
	// is preserved in the metadata for any caller that wants it.
	listIface, err := client.Resource(UserAccessGVR()).Namespace("").List(r.Context(), metav1.ListOptions{})
	if err != nil {
		// CRD not installed → return empty list, not 500. The
		// access.openova.io UserAccess CRD ships via a separate
		// blueprint that may not yet be installed on a fresh
		// Sovereign. The page should render its empty state, not
		// an error toast.
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, userAccessListResponse{Items: []userAccessItem{}})
			return
		}
		h.log.Warn("user-access: list failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "list-failed",
			"detail": err.Error(),
		})
		return
	}
	out := userAccessListResponse{Items: make([]userAccessItem, 0, len(listIface.Items))}
	for i := range listIface.Items {
		out.Items = append(out.Items, unstructuredToUserAccess(&listIface.Items[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateUserAccess — POST .../admin/user-access
func (h *Handler) CreateUserAccess(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	var body userAccessRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	body = userAccessRequestNormalize(body, depID, "")
	if msg, ok := validateUserAccess(body); !ok {
		writeBadRequest(w, "invalid-user-access", msg)
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	obj := userAccessToUnstructured(body)
	created, err := client.Resource(UserAccessGVR()).Namespace("").Create(r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "user-access-name-conflict",
				"detail": "useraccess " + body.Name + " already exists",
			})
			return
		}
		h.log.Warn("user-access: create failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "create-failed",
			"detail": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusCreated, unstructuredToUserAccess(created))
}

// UpdateUserAccess — PUT .../admin/user-access/{name}
//
// Replace-style update: the handler reads the current Claim, swaps
// in the request's spec (preserving resourceVersion so concurrent
// edits surface as 409), and writes back.
func (h *Handler) UpdateUserAccess(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	name := chi.URLParam(r, "name")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "name-required", "useraccess name is required")
		return
	}
	var body userAccessRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	body.Name = name
	body = userAccessRequestNormalize(body, depID, name)
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	current, err := client.Resource(UserAccessGVR()).Namespace("").Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "user-access-not-found",
				"detail": "no useraccess with name " + name,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "get-failed",
			"detail": err.Error(),
		})
		return
	}
	// PUT short-form merge: the canonical UAT matrix sends `{"tier":"X"}`
	// expecting the existing CR's user/sovereignRef/applications to be
	// preserved with only the role rotated. Pull the current CR's spec
	// in as a baseline and re-run normalize so the tier rotation
	// applies on top of the existing applications list.
	if isUserAccessShortFormPut(body) {
		curItem := unstructuredToUserAccess(current)
		// Carry forward the current CR's identity + scope.
		if strings.TrimSpace(body.Spec.User.KeycloakSubject) == "" {
			body.Spec.User.KeycloakSubject = curItem.Spec.User.KeycloakSubject
		}
		if len(body.Spec.User.KeycloakGroups) == 0 {
			body.Spec.User.KeycloakGroups = curItem.Spec.User.KeycloakGroups
		}
		if strings.TrimSpace(body.Spec.SovereignRef) == "" {
			body.Spec.SovereignRef = curItem.Spec.SovereignRef
		}
		if len(body.Spec.Applications) == 0 || (len(body.Spec.Applications) == 1 && body.Spec.Applications[0].App == "*") {
			// Either nothing yet (tier wasn't supplied — no change), or
			// the tier-rotation sentinel ("*"). When the CR already has
			// per-app grants, rotate THOSE to the new tier-derived role
			// instead of replacing them with the wildcard sentinel.
			if len(curItem.Spec.Applications) > 0 && strings.TrimSpace(body.TierShort) != "" {
				role := userAccessTierToRole(body.TierShort)
				rotated := make([]userAccessAppGrantBody, 0, len(curItem.Spec.Applications))
				for _, app := range curItem.Spec.Applications {
					app.Role = role
					rotated = append(rotated, app)
				}
				body.Spec.Applications = rotated
			}
		}
	}
	if msg, ok := validateUserAccess(body); !ok {
		writeBadRequest(w, "invalid-user-access", msg)
		return
	}
	desired := userAccessToUnstructured(body)
	desired.SetResourceVersion(current.GetResourceVersion())
	desired.SetUID(current.GetUID())
	updated, err := client.Resource(UserAccessGVR()).Namespace("").Update(r.Context(), desired, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "concurrent-edit",
				"detail": "the useraccess was modified by another writer; refresh and retry",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "update-failed",
			"detail": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, unstructuredToUserAccess(updated))
}

// DeleteUserAccess — DELETE .../admin/user-access/{name}
func (h *Handler) DeleteUserAccess(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "depId")
	name := chi.URLParam(r, "name")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if strings.TrimSpace(name) == "" {
		writeBadRequest(w, "name-required", "useraccess name is required")
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	if err := tryDeleteUserAccess(r.Context(), client, name); err != nil {
		if errors.Is(err, errUserAccessNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "user-access-not-found",
				"detail": "no useraccess with name " + name,
			})
			return
		}
		h.log.Warn("user-access: delete failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "delete-failed",
			"detail": err.Error(),
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

/* ── helpers ───────────────────────────────────────────────────── */

var errUserAccessNotFound = errors.New("user-access: not found")

// tryDeleteUserAccess deletes the named cluster-scoped Claim.
// Returns errUserAccessNotFound on miss so the caller can map onto
// HTTP 404. We do not need to walk the list since the Claim is
// cluster-scoped — the apiserver returns NotFound directly for an
// unknown name.
func tryDeleteUserAccess(ctx context.Context, client dynamic.Interface, name string) error {
	policy := metav1.DeletePropagationForeground
	err := client.Resource(UserAccessGVR()).Namespace("").Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return errUserAccessNotFound
		}
		return err
	}
	return nil
}

func validateUserAccess(req userAccessRequest) (string, bool) {
	if strings.TrimSpace(req.Name) == "" {
		return "name is required", false
	}
	// At least one of keycloakSubject or keycloakGroups must be set
	// (mirrors the CRD's "either or both" semantics).
	subject := strings.TrimSpace(req.Spec.User.KeycloakSubject)
	hasGroups := false
	for _, g := range req.Spec.User.KeycloakGroups {
		if strings.TrimSpace(g) != "" {
			hasGroups = true
			break
		}
	}
	if subject == "" && !hasGroups {
		return "spec.user must set keycloakSubject and/or keycloakGroups", false
	}
	if strings.TrimSpace(req.Spec.SovereignRef) == "" {
		return "spec.sovereignRef is required", false
	}
	if len(req.Spec.Applications) == 0 {
		return "spec.applications must contain at least one entry", false
	}
	for i, app := range req.Spec.Applications {
		if strings.TrimSpace(app.App) == "" {
			return "spec.applications[*].app is required", false
		}
		if _, ok := userAccessRoles[app.Role]; !ok {
			return "spec.applications[" + itoa(i) + "].role must be one of admin, editor, viewer", false
		}
	}
	return "", true
}

// userAccessToUnstructured composes the dynamic-client payload from
// the request body.
func userAccessToUnstructured(req userAccessRequest) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(userAccessGroup + "/" + userAccessVersion)
	obj.SetKind("UserAccess")
	obj.SetName(req.Name)

	user := map[string]any{}
	if s := strings.TrimSpace(req.Spec.User.KeycloakSubject); s != "" {
		user["keycloakSubject"] = s
	}
	if len(req.Spec.User.KeycloakGroups) > 0 {
		groups := make([]any, 0, len(req.Spec.User.KeycloakGroups))
		for _, g := range req.Spec.User.KeycloakGroups {
			if g = strings.TrimSpace(g); g != "" {
				groups = append(groups, g)
			}
		}
		if len(groups) > 0 {
			user["keycloakGroups"] = groups
		}
	}

	apps := make([]any, 0, len(req.Spec.Applications))
	for _, app := range req.Spec.Applications {
		entry := map[string]any{
			"app":  app.App,
			"role": app.Role,
		}
		if len(app.Namespaces) > 0 {
			ns := make([]any, 0, len(app.Namespaces))
			for _, n := range app.Namespaces {
				if n = strings.TrimSpace(n); n != "" {
					ns = append(ns, n)
				}
			}
			if len(ns) > 0 {
				entry["namespaces"] = ns
			}
		}
		if len(app.VClusters) > 0 {
			vcs := make([]any, 0, len(app.VClusters))
			for _, v := range app.VClusters {
				if v = strings.TrimSpace(v); v != "" {
					vcs = append(vcs, v)
				}
			}
			if len(vcs) > 0 {
				entry["vClusters"] = vcs
			}
		}
		apps = append(apps, entry)
	}

	spec := map[string]any{
		"user":         user,
		"sovereignRef": req.Spec.SovereignRef,
		"applications": apps,
	}
	_ = unstructured.SetNestedMap(obj.Object, spec, "spec")
	return obj
}

func unstructuredToUserAccess(u *unstructured.Unstructured) userAccessItem {
	// Initialize slice fields as empty (not nil) so JSON serialization
	// emits `[]` rather than `null`. The UI renders `applications.map(...)`
	// directly — null would crash the page with a TypeError. Caught on
	// console.omantel.biz 2026-05-09 (qa-loop iter-4 cluster
	// `users-page-null-map-and-open-redirect`).
	out := userAccessItem{
		Name: u.GetName(),
		Spec: userAccessSpecBody{
			Applications: []userAccessAppGrantBody{},
		},
	}
	if ts := u.GetCreationTimestamp(); !ts.IsZero() {
		out.CreationTimestamp = ts.UTC().Format("2006-01-02T15:04:05Z")
	}
	if user, ok, _ := unstructured.NestedMap(u.Object, "spec", "user"); ok {
		if v, ok := user["keycloakSubject"].(string); ok {
			out.Spec.User.KeycloakSubject = v
		}
		if raw, ok := user["keycloakGroups"].([]any); ok {
			groups := make([]string, 0, len(raw))
			for _, g := range raw {
				if gs, ok := g.(string); ok {
					groups = append(groups, gs)
				}
			}
			out.Spec.User.KeycloakGroups = groups
		}
	}
	if v, ok, _ := unstructured.NestedString(u.Object, "spec", "sovereignRef"); ok {
		out.Spec.SovereignRef = v
	}
	if rawApps, ok, _ := unstructured.NestedSlice(u.Object, "spec", "applications"); ok {
		apps := make([]userAccessAppGrantBody, 0, len(rawApps))
		for _, raw := range rawApps {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			entry := userAccessAppGrantBody{}
			if v, ok := m["app"].(string); ok {
				entry.App = v
			}
			if v, ok := m["role"].(string); ok {
				entry.Role = v
			}
			if rawNs, ok := m["namespaces"].([]any); ok {
				for _, n := range rawNs {
					if ns, ok := n.(string); ok {
						entry.Namespaces = append(entry.Namespaces, ns)
					}
				}
			}
			if rawVcs, ok := m["vClusters"].([]any); ok {
				for _, v := range rawVcs {
					if vs, ok := v.(string); ok {
						entry.VClusters = append(entry.VClusters, vs)
					}
				}
			}
			apps = append(apps, entry)
		}
		out.Spec.Applications = apps
	}
	if rb, ok, _ := unstructured.NestedInt64(u.Object, "status", "rolebindingsCreated"); ok {
		st := userAccessStatusBody{RolebindingsCreated: int(rb)}
		if pcr, ok, _ := unstructured.NestedString(u.Object, "status", "providerConfigRef"); ok {
			st.ProviderConfigRef = pcr
		}
		out.Status = &st
	}
	return out
}

func writeUserAccessUnavailable(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error":  "sovereign-cluster-unreachable",
		"detail": err.Error(),
	})
}
