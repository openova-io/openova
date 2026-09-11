package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The access API (DESIGN.md §10): who holds which role at which scope.
//
//	GET    /api/v1/access/roles            the six roles and their permissions
//	GET    /api/v1/access/bindings         explicit bindings (+ the implicit OPERATOR_EMAILS ones)
//	POST   /api/v1/access/bindings         grant {subject_email, role, customer_id?}
//	DELETE /api/v1/access/bindings/{id}    revoke
//	GET    /api/v1/access/group-mappings   directory group → role
//	PUT    /api/v1/access/group-mappings   replace the set
//
// Every write needs settings.manage at the Sovereign and is audited as
// access.binding or access.mapping.

// listRoles — the policy as a document, so the console labels roles and
// permissions from the same source the server decides by.
func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAuth(w, r); !ok {
		return
	}
	roles := make([]map[string]any, 0, len(store.Roles))
	for _, role := range store.Roles {
		perms := make([]string, 0, len(access.Permissions))
		for _, p := range access.Permissions {
			if access.RoleGrants(role, p) {
				perms = append(perms, string(p))
			}
		}
		roles = append(roles, map[string]any{
			"role":        role,
			"scope_kind":  store.ScopeKindOfRole(role),
			"permissions": perms,
			"description": access.Describe[role],
		})
	}
	perms := make([]string, len(access.Permissions))
	for i, p := range access.Permissions {
		perms[i] = string(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles, "permissions": perms})
}

// implicitBindings are the OPERATOR_EMAILS sovereign-admins: not rows, not
// revocable here, but shown so the Access page tells the whole truth.
func (h *Handler) implicitBindings() []store.RoleBinding {
	out := make([]store.RoleBinding, 0, len(h.Config.OperatorEmails))
	for _, e := range h.Config.OperatorEmails {
		out = append(out, store.RoleBinding{SubjectEmail: e, Role: store.RoleSovereignAdmin, ScopeKind: store.ScopeKindSovereign, GrantedBy: "OPERATOR_EMAILS", Source: store.BindingSourceConfig})
	}
	return out
}

// listBindings — GET /access/bindings[?email=&customer_id=].
func (h *Handler) listBindings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	f := store.RoleBindingFilter{SubjectEmail: r.URL.Query().Get("email"), CustomerID: strings.TrimSpace(r.URL.Query().Get("customer_id"))}
	list, err := h.Store.ListRoleBindings(r.Context(), f)
	if err != nil {
		storeErr(w, err)
		return
	}
	implicit := []store.RoleBinding{}
	if f.CustomerID == "" {
		for _, b := range h.implicitBindings() {
			if f.SubjectEmail == "" || normEmail(f.SubjectEmail) == b.SubjectEmail {
				implicit = append(implicit, b)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": list, "implicit": implicit})
}

type bindingBody struct {
	SubjectEmail string  `json:"subject_email"`
	Email        string  `json:"email"` // alias accepted for symmetry with /customers/{id}/users
	Role         string  `json:"role"`
	CustomerID   *string `json:"customer_id"`
	// PartnerID is what a partner role is bound to (DESIGN.md §13);
	// the role fixes which of the two ids the binding takes.
	PartnerID *string `json:"partner_id"`
}

// createBinding — POST /access/bindings. Idempotent: granting what is
// already granted answers 200 with the existing row; a new grant answers 201.
func (h *Handler) createBinding(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.SettingsManage)
	if !ok {
		return
	}
	var in bindingBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	email := normEmail(in.SubjectEmail)
	if email == "" {
		email = normEmail(in.Email)
	}
	if !validEmail(email) {
		writeErr(w, http.StatusBadRequest, "subject_email must be a valid email address")
		return
	}
	role := strings.ToLower(strings.TrimSpace(in.Role))
	if !store.ValidRole(role) {
		writeErr(w, http.StatusBadRequest, "role must be one of "+strings.Join(store.Roles, ", "))
		return
	}
	if in.CustomerID != nil && *in.CustomerID != "" {
		if _, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, *in.CustomerID); err != nil {
			storeErr(w, err)
			return
		}
	}
	if in.PartnerID != nil && *in.PartnerID != "" {
		if _, err := h.Store.GetPartner(r.Context(), *in.PartnerID); err != nil {
			storeErr(w, err)
			return
		}
	}
	before, err := h.Store.ListRoleBindings(r.Context(), store.RoleBindingFilter{SubjectEmail: email})
	if err != nil {
		storeErr(w, err)
		return
	}
	b, err := h.Store.UpsertRoleBinding(r.Context(), store.RoleBinding{SubjectEmail: email, Role: role, CustomerID: in.CustomerID, PartnerID: in.PartnerID, GrantedBy: s.Email})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	status := http.StatusCreated
	for _, x := range before {
		if x.ID == b.ID {
			status = http.StatusOK
		}
	}
	if status == http.StatusCreated {
		h.audit(r, b.CustomerID, "access.binding", map[string]any{"op": "grant", "binding_id": b.ID, "subject_email": b.SubjectEmail, "role": b.Role, "scope_kind": b.ScopeKind, "customer_id": b.CustomerID, "partner_id": b.PartnerID})
	}
	writeJSON(w, status, b)
}

// deleteBinding — DELETE /access/bindings/{id}. Refuses to revoke the last
// sovereign-admin when no OPERATOR_EMAILS back it up: a Sovereign with
// nobody who can grant access again is a locked door.
func (h *Handler) deleteBinding(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	id := r.PathValue("id")
	b, err := h.Store.GetRoleBinding(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if b.Role == store.RoleSovereignAdmin && len(h.Config.OperatorEmails) == 0 {
		n, err := h.Store.CountSovereignAdmins(r.Context())
		if err != nil {
			storeErr(w, err)
			return
		}
		if n <= 1 {
			writeErr(w, http.StatusConflict, "this is the last sovereign-admin binding and OPERATOR_EMAILS is empty; grant another sovereign-admin first")
			return
		}
	}
	if err := h.Store.DeleteRoleBinding(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, b.CustomerID, "access.binding", map[string]any{"op": "revoke", "binding_id": b.ID, "subject_email": b.SubjectEmail, "role": b.Role, "scope_kind": b.ScopeKind, "customer_id": b.CustomerID, "partner_id": b.PartnerID})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// listGroupMappings — GET /access/group-mappings.
func (h *Handler) listGroupMappings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	list, err := h.Store.ListGroupRoleMappings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mappings": list, "groups_header": h.groupsHeaderName()})
}

// groupsHeaderName is the header the mappings are read from, or "" when the
// gate is not configured (mappings are then inert, and the page says so).
func (h *Handler) groupsHeaderName() string {
	if h.Config.TrustedForwardAuthHeader == "" {
		return ""
	}
	return h.Config.TrustedForwardGroupsHeader
}

type groupMappingBody struct {
	GroupName  string  `json:"group_name"`
	Role       string  `json:"role"`
	CustomerID *string `json:"customer_id"`
	// PartnerID is what a partner role's mapping is bound to.
	PartnerID *string `json:"partner_id"`
}

// putGroupMappings — PUT /access/group-mappings {mappings: [...]}. The given
// set becomes THE set; nothing is applied when any entry is invalid.
func (h *Handler) putGroupMappings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	var in struct {
		Mappings []groupMappingBody `json:"mappings"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	mappings := make([]store.GroupRoleMapping, 0, len(in.Mappings))
	for _, m := range in.Mappings {
		if m.CustomerID != nil && *m.CustomerID != "" {
			if _, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, *m.CustomerID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeErr(w, http.StatusBadRequest, "group "+strings.TrimSpace(m.GroupName)+": customer "+*m.CustomerID+" does not exist")
					return
				}
				storeErr(w, err)
				return
			}
		}
		if m.PartnerID != nil && *m.PartnerID != "" {
			if _, err := h.Store.GetPartner(r.Context(), *m.PartnerID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeErr(w, http.StatusBadRequest, "group "+strings.TrimSpace(m.GroupName)+": partner "+*m.PartnerID+" does not exist")
					return
				}
				storeErr(w, err)
				return
			}
		}
		mappings = append(mappings, store.GroupRoleMapping{GroupName: m.GroupName, Role: strings.ToLower(strings.TrimSpace(m.Role)), CustomerID: m.CustomerID, PartnerID: m.PartnerID})
	}
	list, err := h.Store.ReplaceGroupRoleMappings(r.Context(), mappings)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	names := make([]map[string]any, 0, len(list))
	for _, m := range list {
		names = append(names, map[string]any{"group_name": m.GroupName, "role": m.Role, "scope_kind": m.ScopeKind, "customer_id": m.CustomerID, "partner_id": m.PartnerID})
	}
	h.audit(r, nil, "access.mapping", map[string]any{"op": "replace", "count": len(list), "mappings": names})
	writeJSON(w, http.StatusOK, map[string]any{"mappings": list, "groups_header": h.groupsHeaderName()})
}
