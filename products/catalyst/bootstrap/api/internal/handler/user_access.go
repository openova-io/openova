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
	if msg, ok := validateUserAccess(body); !ok {
		writeBadRequest(w, "invalid-user-access", msg)
		return
	}
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
