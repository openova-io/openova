package api

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"

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
	known := h.Config.IsOperator(email)
	if !known {
		if _, _, ok, err := h.Store.RoleForEmail(r.Context(), email); err != nil {
			storeErr(w, err)
			return
		} else if ok {
			known = true
		}
	}
	if known {
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
	role, customerID, err := h.resolveRole(r, email)
	if err != nil {
		storeErr(w, err)
		return
	}
	if role == "" {
		writeErr(w, http.StatusForbidden, "this email has no access")
		return
	}
	sess, err := h.Store.CreateSession(r.Context(), email, role, customerID, sessionTTL)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.setSessionCookie(w, sess.Token, sess.ExpiresAt)
	h.audit(r, customerID, "auth.signin", map[string]any{"email": email, "role": role})
	writeJSON(w, http.StatusOK, h.mePayload(r, sess))
}

// resolveRole maps an email to (role, customerID): operator emails from the
// environment, otherwise the customer_users grant.
func (h *Handler) resolveRole(r *http.Request, email string) (string, *string, error) {
	if h.Config.IsOperator(email) {
		return store.RoleOperator, nil, nil
	}
	cid, role, ok, err := h.Store.RoleForEmail(r.Context(), email)
	if err != nil || !ok {
		return "", nil, err
	}
	if role == "admin" {
		return store.RoleCustomerAdmin, &cid, nil
	}
	return store.RoleCustomerViewer, &cid, nil
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if s, ok := sessionFrom(r); ok {
		_ = h.Store.DeleteSession(r.Context(), s.Token)
	}
	h.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"signed_out": true})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.mePayload(r, s))
}

func (h *Handler) mePayload(r *http.Request, s store.Session) map[string]any {
	out := map[string]any{
		"email":      s.Email,
		"role":       s.Role,
		"expires_at": s.ExpiresAt,
		"profile":    h.Config.Profile,
		"version":    h.Version,
	}
	if s.CustomerID != nil {
		out["customer_id"] = *s.CustomerID
		if c, err := h.Store.GetCustomer(r.Context(), store.CustomerScope(*s.CustomerID), *s.CustomerID); err == nil {
			out["customer"] = map[string]any{"id": c.ID, "slug": c.Slug, "name": c.Name, "status": c.Status, "billing_mode": c.BillingMode}
		}
	}
	return out
}
