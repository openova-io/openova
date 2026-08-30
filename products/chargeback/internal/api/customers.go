package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func (h *Handler) listCustomers(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	list, err := h.Store.ListCustomers(r.Context(), s.Scope())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"customers": list})
}

type customerBody struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	AdminEmail  string  `json:"admin_email"`
	PriceBookID *string `json:"price_book_id"`
	BillingMode *string `json:"billing_mode"`
	StartDate   *string `json:"start_date"`
	Status      *string `json:"status"`
	OrgSlug     *string `json:"org_slug"`
	Kind        *string `json:"kind"`
}

func validBillingMode(m string) bool { return m == "real" || m == "chargeback" || m == "showback" }
func validStatus(s string) bool      { return s == "pending" || s == "active" || s == "suspended" }

func (h *Handler) createCustomer(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in customerBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !validSlug(in.Slug) {
		writeErr(w, http.StatusBadRequest, "slug must be 2-63 lowercase letters, digits or dashes")
		return
	}
	if strings.TrimSpace(in.Name) == "" || !validEmail(in.AdminEmail) {
		writeErr(w, http.StatusBadRequest, "name and a valid admin_email are required")
		return
	}
	ci := store.CustomerInput{Slug: in.Slug, Name: in.Name, AdminEmail: in.AdminEmail}
	if in.PriceBookID != nil {
		ci.PriceBookID = *in.PriceBookID
	}
	if in.BillingMode != nil {
		if !validBillingMode(*in.BillingMode) {
			writeErr(w, http.StatusBadRequest, "billing_mode must be real, chargeback or showback")
			return
		}
		ci.BillingMode = *in.BillingMode
	}
	if in.StartDate != nil && *in.StartDate != "" {
		if !store.ValidDate(*in.StartDate) {
			writeErr(w, http.StatusBadRequest, "start_date must be YYYY-MM-DD")
			return
		}
		ci.StartDate = *in.StartDate
	}
	if in.OrgSlug != nil {
		ci.OrgSlug = *in.OrgSlug
	}
	if in.Kind != nil {
		if *in.Kind != "external" && *in.Kind != "organization" {
			writeErr(w, http.StatusBadRequest, "kind must be external or organization")
			return
		}
		ci.Kind = *in.Kind
	}
	c, err := h.Store.CreateCustomer(r.Context(), ci)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &c.ID, "customer.create", map[string]any{"slug": c.Slug})
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) getCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) patchCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in customerBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	p := store.CustomerPatch{PriceBookID: in.PriceBookID, OrgSlug: in.OrgSlug}
	if in.Name != "" {
		p.Name = &in.Name
	}
	if in.AdminEmail != "" {
		if !validEmail(in.AdminEmail) {
			writeErr(w, http.StatusBadRequest, "admin_email is invalid")
			return
		}
		p.AdminEmail = &in.AdminEmail
	}
	if in.BillingMode != nil {
		if !validBillingMode(*in.BillingMode) {
			writeErr(w, http.StatusBadRequest, "billing_mode must be real, chargeback or showback")
			return
		}
		p.BillingMode = in.BillingMode
	}
	if in.Status != nil {
		if !validStatus(*in.Status) {
			writeErr(w, http.StatusBadRequest, "status must be pending, active or suspended")
			return
		}
		p.Status = in.Status
	}
	if in.StartDate != nil {
		if *in.StartDate != "" && !store.ValidDate(*in.StartDate) {
			writeErr(w, http.StatusBadRequest, "start_date must be YYYY-MM-DD")
			return
		}
		p.StartDate = in.StartDate
	}
	c, err := h.Store.UpdateCustomer(r.Context(), id, p)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &c.ID, "customer.update", map[string]any{"fields": patchedFields(in)})
	writeJSON(w, http.StatusOK, c)
}

func patchedFields(in customerBody) []string {
	var f []string
	if in.Name != "" {
		f = append(f, "name")
	}
	if in.AdminEmail != "" {
		f = append(f, "admin_email")
	}
	if in.PriceBookID != nil {
		f = append(f, "price_book_id")
	}
	if in.BillingMode != nil {
		f = append(f, "billing_mode")
	}
	if in.Status != nil {
		f = append(f, "status")
	}
	if in.StartDate != nil {
		f = append(f, "start_date")
	}
	if in.OrgSlug != nil {
		f = append(f, "org_slug")
	}
	return f
}

// inviteCustomer mints an activation link and mails it to the admin.
func (h *Handler) inviteCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	inv, err := h.Store.CreateInvite(r.Context(), c.ID, c.AdminEmail, inviteTTL)
	if err != nil {
		storeErr(w, err)
		return
	}
	url := fmt.Sprintf("%s/activate/%s", h.Config.PublicURL, inv.Token)
	body := fmt.Sprintf("Hello %s,\n\nActivate your chargeback account and connect your cloud projects:\n\n%s\n\nThe link expires on %s.\n", c.Name, url, inv.ExpiresAt.Format("2006-01-02 15:04 UTC"))
	if err := h.Mail.Send(r.Context(), c.AdminEmail, "Activate your chargeback account", body); err != nil {
		slog.Error("send invite mail", "error", err)
	}
	h.audit(r, &c.ID, "customer.invite", map[string]any{"email": c.AdminEmail, "expires_at": inv.ExpiresAt})
	writeJSON(w, http.StatusCreated, map[string]any{"invite_url": url, "expires_at": inv.ExpiresAt})
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, false); !ok {
		return
	}
	users, err := h.Store.ListCustomerUsers(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Handler) addUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, true); !ok {
		return
	}
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decode(r, &in); err != nil || !validEmail(in.Email) || (in.Role != "admin" && in.Role != "viewer") {
		writeErr(w, http.StatusBadRequest, "email and role (admin|viewer) are required")
		return
	}
	if _, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, id); err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.UpsertCustomerUser(r.Context(), id, in.Email, in.Role); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &id, "customer.user.add", map[string]any{"email": normEmail(in.Email), "role": in.Role})
	writeJSON(w, http.StatusCreated, store.CustomerUser{CustomerID: id, Email: normEmail(in.Email), Role: in.Role})
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	email := normEmail(r.PathValue("email"))
	if _, ok := h.requireCustomer(w, r, id, true); !ok {
		return
	}
	if err := h.Store.DeleteCustomerUser(r.Context(), id, email); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &id, "customer.user.remove", map[string]any{"email": email})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h *Handler) customerAudit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	entries, err := h.Store.ListAudit(r.Context(), s.Scope(), id, 200)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
