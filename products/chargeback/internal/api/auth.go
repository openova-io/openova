package api

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"sort"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func newPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// pinRequest issues a sign-in code. It answers 202 for every syntactically
// valid email so the endpoint does not reveal which emails have access.
func (h *Handler) pinRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if err := decode(r, &in); err != nil || !validEmail(in.Email) {
		writeErr(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	email := normEmail(in.Email)
	recent, err := h.Store.PINIssuedRecently(r.Context(), email, pinTTL, pinThrottle)
	if err != nil {
		storeErr(w, err)
		return
	}
	if recent {
		w.Header().Set("Retry-After", fmt.Sprint(int(pinThrottle.Seconds())))
		writeErr(w, http.StatusTooManyRequests, "a code was sent recently; wait before requesting another")
		return
	}
	// Only known principals receive a code; unknown emails get the same 202.
	// A PIN sign-in carries no directory groups, so only explicit bindings
	// and OPERATOR_EMAILS count here.
	bindings, err := h.resolveBindings(r, email, nil)
	if err != nil {
		storeErr(w, err)
		return
	}
	if len(bindings) > 0 {
		code, err := newPIN()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := h.Store.PutPIN(r.Context(), email, code, pinTTL); err != nil {
			storeErr(w, err)
			return
		}
		body := fmt.Sprintf("Your chargeback sign-in code is %s. It expires in %d minutes.", code, int(pinTTL.Minutes()))
		if err := h.Mail.Send(r.Context(), email, "Your sign-in code", body); err != nil {
			slog.Error("send PIN mail", "error", err)
			writeErr(w, http.StatusBadGateway, "could not send the code")
			return
		}
	} else {
		slog.Info("PIN requested for unknown email", "email", email)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"sent": true, "expires_in_seconds": int(pinTTL.Seconds())})
}

// pinVerify exchanges a code for a session cookie.
func (h *Handler) pinVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := decode(r, &in); err != nil || !validEmail(in.Email) || len(in.Code) < 4 {
		writeErr(w, http.StatusBadRequest, "email and code are required")
		return
	}
	email := normEmail(in.Email)
	ok, err := h.Store.VerifyPIN(r.Context(), email, in.Code, pinMaxTries)
	if err != nil {
		storeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	resolved, err := h.resolveSession(r, email, nil)
	if err != nil {
		storeErr(w, err)
		return
	}
	if len(resolved.Roles) == 0 {
		writeErr(w, http.StatusForbidden, "this email has no access")
		return
	}
	sess, err := h.Store.CreateSession(r.Context(), email, resolved.Role, resolved.CustomerID, sessionTTL)
	if err != nil {
		storeErr(w, err)
		return
	}
	resolved.Token, resolved.ExpiresAt = sess.Token, sess.ExpiresAt
	h.setSessionCookie(w, sess.Token, sess.ExpiresAt)
	h.audit(r, resolved.CustomerID, "auth.signin", map[string]any{"email": email, "role": resolved.Role, "roles": roleNames(resolved.Roles)})
	writeJSON(w, http.StatusOK, h.mePayload(r, resolved))
}

// resolveBindings is the union of everything that grants an email a role
// (DESIGN.md §10): the implicit sovereign-admin of an OPERATOR_EMAILS
// address, its explicit role_bindings rows, and the mappings of the
// directory groups the gate forwarded. Order is stable: config, bindings,
// groups.
func (h *Handler) resolveBindings(r *http.Request, email string, groups []string) ([]store.RoleBinding, error) {
	var out []store.RoleBinding
	if h.Config.IsOperator(email) {
		out = append(out, store.RoleBinding{SubjectEmail: email, Role: store.RoleSovereignAdmin, ScopeKind: store.ScopeKindSovereign, Source: store.BindingSourceConfig})
	}
	explicit, err := h.Store.BindingsForEmail(r.Context(), email)
	if err != nil {
		return nil, err
	}
	out = append(out, explicit...)
	if len(groups) > 0 {
		viaGroups, err := h.Store.BindingsForGroups(r.Context(), groups)
		if err != nil {
			return nil, err
		}
		for i := range viaGroups {
			viaGroups[i].SubjectEmail = email
		}
		out = append(out, viaGroups...)
	}
	return out, nil
}

// resolveSession builds the session of an email from its bindings: Roles is
// the full set, Role + CustomerID the highest-power one in the legacy
// vocabulary (access.Primary). An email with no binding resolves to a
// session with no Roles, which every caller treats as unauthenticated.
func (h *Handler) resolveSession(r *http.Request, email string, groups []string) (store.Session, error) {
	bindings, err := h.resolveBindings(r, email, groups)
	if err != nil {
		return store.Session{}, err
	}
	s := store.Session{Email: email, Roles: bindings, Groups: groups}
	if role, cid, ok := access.Primary(bindings); ok {
		s.Role, s.CustomerID = role, cid
	}
	return s, nil
}

func roleNames(bs []store.RoleBinding) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Role)
	}
	return out
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if s, ok := sessionFrom(r); ok && s.Token != "" {
		_ = h.Store.DeleteSession(r.Context(), s.Token)
	}
	h.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"signed_out": true})
}

// me — GET /api/v1/auth/me and GET /api/v1/me.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.mePayload(r, s))
}

// mePayload is the signed-in principal as the console reads it. `role` and
// `customer_id` are the legacy pair (the highest-power binding, in the old
// vocabulary); `roles` is every binding with where it came from;
// `permissions` the effective list per scope key ("sovereign" or
// "customer:<id>"), which is what the console hides and shows by; `scopes`
// those keys in order. `customer` is the primary customer's card.
func (h *Handler) mePayload(r *http.Request, s store.Session) map[string]any {
	bindings := access.Bindings(s)
	roles := make([]map[string]any, 0, len(bindings))
	for _, b := range bindings {
		row := map[string]any{"role": b.Role, "scope_kind": b.ScopeKind, "source": b.Source}
		if b.Source == "" {
			row["source"] = store.BindingSourceExplicit
		}
		if b.CustomerID != nil {
			row["customer_id"] = *b.CustomerID
			if b.CustomerName != "" {
				row["customer_name"] = b.CustomerName
			}
		}
		roles = append(roles, row)
	}
	perms := access.Effective(bindings)
	permsOut := make(map[string][]string, len(perms))
	for k, list := range perms {
		names := make([]string, len(list))
		for i, p := range list {
			names[i] = string(p)
		}
		sort.Strings(names)
		permsOut[k] = names
	}
	out := map[string]any{
		"email":       s.Email,
		"role":        s.Role,
		"roles":       roles,
		"permissions": permsOut,
		"scopes":      access.Scopes(bindings),
		"expires_at":  s.ExpiresAt,
		"profile":     h.Config.Profile,
		"version":     h.Version,
	}
	if s.CustomerID != nil {
		out["customer_id"] = *s.CustomerID
		if c, err := h.Store.GetCustomer(r.Context(), store.CustomerScope(*s.CustomerID), *s.CustomerID); err == nil {
			out["customer"] = map[string]any{"id": c.ID, "slug": c.Slug, "name": c.Name, "status": c.Status, "billing_mode": c.BillingMode, "payment_method": c.PaymentMethod, "gateway_name": c.GatewayName}
		}
	}
	return out
}
