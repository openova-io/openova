package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// getInvite is public: it tells the activation page who is activating and
// which projects the operator pre-registered.
func (h *Handler) getInvite(w http.ResponseWriter, r *http.Request) {
	inv, err := h.Store.GetInvite(r.Context(), r.PathValue("token"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if !inv.Usable(h.Now()) {
		writeErr(w, http.StatusGone, "this invite has expired or was already used")
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, inv.CustomerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	sources, err := h.Store.ListSources(r.Context(), store.OperatorScope, c.ID)
	if err != nil {
		storeErr(w, err)
		return
	}
	region := ""
	var projects []string
	for _, s := range sources {
		if s.Kind != "huawei-project" {
			continue
		}
		if region == "" {
			region = s.Region
		}
		projects = append(projects, s.ProjectID)
	}
	if projects == nil {
		projects = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"customer":    map[string]any{"id": c.ID, "slug": c.Slug, "name": c.Name, "status": c.Status},
		"email":       inv.Email,
		"region":      region,
		"project_ids": projects,
		"expires_at":  inv.ExpiresAt,
	})
}

// activationResult reports one project's verification.
type activationResult struct {
	SourceID  string `json:"source_id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

// activateInvite stores the AK/SK, verifies every project, activates the
// customer when all verify, consumes the invite and signs the admin in.
func (h *Handler) activateInvite(w http.ResponseWriter, r *http.Request) {
	inv, err := h.Store.GetInvite(r.Context(), r.PathValue("token"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if !inv.Usable(h.Now()) {
		writeErr(w, http.StatusGone, "this invite has expired or was already used")
		return
	}
	var in struct {
		Region     string   `json:"region"`
		ProjectIDs []string `json:"project_ids"`
		AccessKey  string   `json:"access_key"`
		SecretKey  string   `json:"secret_key"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in.Region = strings.TrimSpace(in.Region)
	in.AccessKey = strings.TrimSpace(in.AccessKey)
	in.SecretKey = strings.TrimSpace(in.SecretKey)
	if in.Region == "" || len(in.ProjectIDs) == 0 || in.AccessKey == "" || in.SecretKey == "" {
		writeErr(w, http.StatusBadRequest, "region, project_ids, access_key and secret_key are required")
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, inv.CustomerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	results, allOK, err := h.attachAndVerify(r.Context(), c.ID, in.Region, in.ProjectIDs, in.AccessKey, in.SecretKey)
	if err != nil {
		storeErr(w, err)
		return
	}
	// The secret is no longer needed in memory; the encrypted copy is in the store.
	in.SecretKey = ""
	if !allOK {
		h.audit(r, &c.ID, "customer.activate.failed", map[string]any{"email": inv.Email, "results": results})
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"activated": false, "results": results})
		return
	}
	if err := h.Store.SetCustomerStatus(r.Context(), c.ID, "active"); err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.UpsertCustomerUser(r.Context(), c.ID, inv.Email, "admin"); err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.MarkInviteUsed(r.Context(), inv.Token); err != nil {
		storeErr(w, err)
		return
	}
	cid := c.ID
	sess, err := h.Store.CreateSession(r.Context(), inv.Email, store.RoleCustomerAdmin, &cid, sessionTTL)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.setSessionCookie(w, sess.Token, sess.ExpiresAt)
	h.audit(r, &c.ID, "customer.activate", map[string]any{"email": inv.Email, "region": in.Region, "projects": len(in.ProjectIDs)})
	me := h.mePayload(r.WithContext(withSession(r.Context(), sess)), sess)
	writeJSON(w, http.StatusOK, map[string]any{"activated": true, "results": results, "session": me})
}

// attachAndVerify stores one credential for the customer, links it to a
// source per project, and verifies each project. The credential is revoked
// again when no project verified.
func (h *Handler) attachAndVerify(ctx context.Context, customerID, region string, projectIDs []string, accessKey, secretKey string) ([]activationResult, bool, error) {
	enc, err := h.Keys.Seal([]byte(secretKey))
	if err != nil {
		return nil, false, err
	}
	cred, err := h.Store.CreateCredential(ctx, customerID, accessKey, enc)
	if err != nil {
		return nil, false, err
	}
	var results []activationResult
	allOK, anyOK := true, false
	for _, pid := range projectIDs {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		src, created, err := h.Store.UpsertSource(ctx, customerID, "huawei-project", region, pid)
		if err != nil {
			return nil, false, err
		}
		if src.CredentialID != nil && *src.CredentialID != cred.ID {
			_ = h.Store.MarkCredentialRotated(ctx, *src.CredentialID)
		}
		if err := h.Store.SetSourceCredential(ctx, src.ID, cred.ID); err != nil {
			return nil, false, err
		}
		res := activationResult{SourceID: src.ID, ProjectID: pid}
		if verr := h.verify(ctx, src.ID, region, pid, accessKey, secretKey); verr != nil {
			res.Status, res.Error = "failed", verr.Error()
			allOK = false
			if created {
				// A project typed in this attempt that does not verify is not
				// kept as a failed row; operator-registered ones keep their
				// failed status and error for the operator to see.
				_ = h.Store.DeleteSource(ctx, src.ID)
				res.SourceID = ""
			}
		} else {
			res.Status = "verified"
			anyOK = true
		}
		results = append(results, res)
	}
	if !anyOK {
		_ = h.Store.RevokeCredential(ctx, cred.ID)
	}
	return results, allOK && len(results) > 0, nil
}

// verify runs the activation check and records the outcome on the source.
// The returned error never contains the secret.
func (h *Handler) verify(ctx context.Context, sourceID, region, projectID, accessKey, secretKey string) error {
	if h.Verifier == nil {
		_ = h.Store.SetSourceFailed(ctx, sourceID, "no verifier configured")
		return errors.New("no verifier configured")
	}
	err := h.Verifier.VerifyProject(ctx, region, projectID, accessKey, secretKey)
	if err == nil {
		if serr := h.Store.SetSourceVerified(ctx, sourceID, ""); serr != nil {
			return serr
		}
		h.Metrics.Inc("chargeback_source_verifications_total", "Source verification outcomes", map[string]string{"result": "verified"}, 1)
		return nil
	}
	var ve *VerifyError
	msg := err.Error()
	if errors.As(err, &ve) && ve.Code != "" {
		msg = ve.Code + ": " + ve.Message
	}
	if strings.Contains(msg, secretKey) {
		msg = strings.ReplaceAll(msg, secretKey, "[redacted]")
	}
	_ = h.Store.SetSourceFailed(ctx, sourceID, msg)
	h.Metrics.Inc("chargeback_source_verifications_total", "Source verification outcomes", map[string]string{"result": "failed"}, 1)
	slog.Warn("source verification failed", "source", sourceID, "project", projectID, "error", msg)
	return errors.New(msg)
}
