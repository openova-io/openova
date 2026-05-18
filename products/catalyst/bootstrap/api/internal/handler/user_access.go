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

type userAccessRequest struct {
	Name string             `json:"name"`
	Spec userAccessSpecBody `json:"spec"`

	// Ergonomic top-level shape — collapsed into Spec.* on decode.
	// Matches the qa-loop matrix POST body for /admin/user-access:
	//   {"email": "qa-user2@openova.io", "tier": "viewer"}
	// The handler synthesizes:
	//   - Name = "useraccess-<sanitized-email-prefix>"
	//   - Spec.User.KeycloakSubject = email (Keycloak issues subjects
	//     equal to the email when the realm uses email-as-username,
	//     which is the default for OpenOva-shipped Sovereigns)
	//   - Spec.SovereignRef = depID-derived slug
	//   - Spec.Applications = [{app: "*", role: <tierToRole(tier)>}]
	// when these fields are present and the canonical Spec.* counterparts
	// are unset.
	Email string `json:"email,omitempty"`
	Tier  string `json:"tier,omitempty"`
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
	h.serveUserAccessList(w, r, dep, depID)
}

// HandleSovereignUsers — GET /api/v1/sovereign/users
//
// Chroot-friendly Sovereign-prefix wrapper around ListUserAccess
// (Refs TBD-E16). Same response shape; resolves the active Sovereign
// deployment from the in-cluster context via resolveSovereignDeploymentID,
// mirroring the canonical seam established by HandleSovereignRBACMatrix /
// HandleSovereignSelf / HandleSovereignApps. Lets operator-side smoke
// probes and external monitors hit a single stable URL without having
// to first round-trip /sovereign/self.
//
// The operator-facing /users UI page itself continues to use the
// canonical per-deployment endpoint
// (/api/v1/deployments/{depId}/admin/user-access) via the
// useResolvedDeploymentId() seam; this prefix endpoint exists for
// callers that want a single-hop probe.
func (h *Handler) HandleSovereignUsers(w http.ResponseWriter, r *http.Request) {
	depID := resolveSovereignDeploymentID(r)
	if depID == "" {
		// Mothership (no SOVEREIGN_FQDN env, no handover cookie). The
		// per-deployment surface is the right entry point on that side;
		// emit a structured 404 so callers can fall back.
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "not-a-sovereign",
			"detail": "this catalyst-api Pod is not running on a Sovereign cluster; use /api/v1/deployments/{depId}/admin/user-access instead",
		})
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	h.serveUserAccessList(w, r, dep, depID)
}

// serveUserAccessList is the shared body of ListUserAccess (per-deployment
// surface) and HandleSovereignUsers (chroot-prefix surface). Both return
// userAccessListResponse byte-byte-identical.
func (h *Handler) serveUserAccessList(w http.ResponseWriter, r *http.Request, dep *Deployment, depID string) {
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	// UserAccess Claims are NAMESPACED per the XRD's claimNames block
	// (Crossplane Claims are namespaced by construction — only the
	// underlying XR xuseraccesses is cluster-scoped). Listing with
	// Namespace("") asks the apiserver for all-namespaces and works
	// against a namespaced CRD; metadata.namespace is preserved on
	// every item for any caller that wants to filter. Refs t134 D21
	// fix in user_access_owner_seed.go + TBD-C6-006-followup.
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
	normalizeUserAccessErgonomicShape(&body, dep)
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
	// Stamp the canonical namespace on the Claim — Crossplane Claims
	// are namespaced; an empty namespace routes to the cluster-scoped
	// REST path which the apiserver doesn't serve for a namespaced
	// CRD (404 "the server could not find the requested resource").
	// Mirrors rbacAssignNamespace + userAccessOwnerNamespace.
	// Refs TBD-C6-006-followup.
	if obj.GetNamespace() == "" {
		obj.SetNamespace(rbacAssignNamespace)
	}
	created, err := client.Resource(UserAccessGVR()).Namespace(obj.GetNamespace()).Create(r.Context(), obj, metav1.CreateOptions{})
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
	normalizeUserAccessErgonomicShape(&body, dep)
	if msg, ok := validateUserAccess(body); !ok {
		writeBadRequest(w, "invalid-user-access", msg)
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeUserAccessUnavailable(w, err)
		return
	}
	// Find the existing Claim across all namespaces. UserAccess Claims
	// are namespaced; we walk the all-namespaces list to discover the
	// canonical namespace before issuing the Update against that exact
	// REST path. Refs TBD-C6-006-followup.
	current, currentNs, err := findUserAccessByName(r.Context(), client, name)
	if err != nil {
		if errors.Is(err, errUserAccessNotFound) {
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
	desired := userAccessToUnstructured(body)
	desired.SetResourceVersion(current.GetResourceVersion())
	desired.SetUID(current.GetUID())
	desired.SetNamespace(currentNs)
	updated, err := client.Resource(UserAccessGVR()).Namespace(currentNs).Update(r.Context(), desired, metav1.UpdateOptions{})
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

// findUserAccessByName walks the all-namespaces list and returns the
// first UserAccess Claim whose metadata.name matches. Returns the
// CR + its namespace, or errUserAccessNotFound on miss. Used by
// UpdateUserAccess and tryDeleteUserAccess to discover the canonical
// namespace before issuing a per-namespace REST call.
//
// UserAccess Claims are namespaced; an empty-namespace Get/Update/
// Delete routes to the cluster-scoped REST path which the apiserver
// returns 404 for ("the server could not find the requested
// resource"). Walk the list once to discover the namespace, then
// route the mutation to the canonical path. Refs TBD-C6-006-followup.
func findUserAccessByName(ctx context.Context, client dynamic.Interface, name string) (*unstructured.Unstructured, string, error) {
	listIface, err := client.Resource(UserAccessGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", errUserAccessNotFound
		}
		return nil, "", err
	}
	for i := range listIface.Items {
		it := &listIface.Items[i]
		if it.GetName() == name {
			return it, it.GetNamespace(), nil
		}
	}
	return nil, "", errUserAccessNotFound
}

// tryDeleteUserAccess deletes the named namespaced Claim. Walks the
// all-namespaces list to discover the canonical namespace before
// issuing the Delete (UserAccess Claims are namespaced per the XRD
// claimNames block — an empty-namespace Delete routes to the
// cluster-scoped REST path which the apiserver doesn't serve for a
// namespaced CRD). Returns errUserAccessNotFound on miss so the
// caller can map onto HTTP 404. Refs TBD-C6-006-followup.
func tryDeleteUserAccess(ctx context.Context, client dynamic.Interface, name string) error {
	_, ns, err := findUserAccessByName(ctx, client, name)
	if err != nil {
		return err
	}
	policy := metav1.DeletePropagationForeground
	err = client.Resource(UserAccessGVR()).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{
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

// userAccessTierToRole maps the 5-tier RBAC vocabulary used by the
// qa-loop matrix and the EPIC-3 access-matrix UI onto the 3-role
// vocabulary the UserAccess CRD's per-application grant accepts.
//
//   - viewer / developer  → viewer (read access; developer adds
//     exec/console/tickets via the tier-developer ClusterRole, which
//     the controller binds *in addition to* the per-app role)
//   - operator / admin    → admin
//   - owner               → admin (owner is global; no app-level form)
//
// Mapping is intentionally lossy — the canonical EPIC-3 path is the
// /rbac/assign endpoint, which writes the tier directly. The
// /admin/user-access endpoint pre-dates the 5-tier model and is kept
// alive for the issue-#323 sovereign-IAM editor; the ergonomic body
// shape lets the qa-loop matrix exercise it without inventing per-app
// grant rows.
func userAccessTierToRole(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "viewer", "developer":
		return "viewer"
	case "operator", "admin", "owner":
		return "admin"
	}
	return ""
}

// normalizeUserAccessErgonomicShape collapses the qa-loop matrix's
// {"email":"...","tier":"..."} POST body into the canonical
// userAccessRequest shape so validateUserAccess + userAccessToUnstructured
// can run unchanged. Idempotent: when Spec.* is already populated by
// the canonical shape, this is a no-op (canonical wins).
func normalizeUserAccessErgonomicShape(req *userAccessRequest, dep *Deployment) {
	email := strings.TrimSpace(req.Email)
	tier := strings.TrimSpace(req.Tier)

	// User identity: Keycloak issues subjects equal to the email when
	// the realm uses email-as-username (default for OpenOva-shipped
	// Sovereigns). Stamp both spec.user.keycloakSubject and the email
	// hint annotation upstream consumers (matrix, audit) read.
	if email != "" && strings.TrimSpace(req.Spec.User.KeycloakSubject) == "" &&
		len(req.Spec.User.KeycloakGroups) == 0 {
		req.Spec.User.KeycloakSubject = email
	}

	// SovereignRef: derive from the deployment's FQDN slug if unset.
	// This is the same slug rbacAssignSovereignRef computes for the
	// /rbac/assign path.
	if strings.TrimSpace(req.Spec.SovereignRef) == "" && dep != nil {
		req.Spec.SovereignRef = rbacAssignSovereignRef(dep)
	}

	// Applications: when the ergonomic body sets a tier but no
	// per-application grant, synthesize a single wildcard "*" grant
	// at the mapped role. The useraccess-controller treats "*" as
	// "every Application discovered on the Sovereign".
	if tier != "" && len(req.Spec.Applications) == 0 {
		role := userAccessTierToRole(tier)
		if role != "" {
			req.Spec.Applications = []userAccessAppGrantBody{{
				App:  "*",
				Role: role,
			}}
		}
	}

	// Name: synthesize a deterministic name from the email when unset.
	// The UI's create-form populates Name explicitly; the matrix
	// doesn't, so we fall back to "useraccess-<sanitized-email-prefix>".
	if strings.TrimSpace(req.Name) == "" && email != "" {
		req.Name = "useraccess-" + sanitizeK8sNameSegment(strings.SplitN(email, "@", 2)[0])
	}
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
	out := userAccessItem{
		Name: u.GetName(),
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
