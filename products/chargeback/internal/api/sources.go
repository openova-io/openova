package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func (h *Handler) listSources(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListSources(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": list})
}

func validSourceKind(k string) bool {
	switch k {
	case "huawei-project", "openova-org", "k8s-namespace", "file":
		return true
	}
	return false
}

func (h *Handler) createSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, true); !ok {
		return
	}
	var in struct {
		Kind      string `json:"kind"`
		Region    string `json:"region"`
		ProjectID string `json:"project_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Kind == "" {
		in.Kind = "huawei-project"
	}
	if !validSourceKind(in.Kind) {
		writeErr(w, http.StatusBadRequest, "kind must be huawei-project, openova-org, k8s-namespace or file")
		return
	}
	if in.Kind == "huawei-project" && (strings.TrimSpace(in.Region) == "" || strings.TrimSpace(in.ProjectID) == "") {
		writeErr(w, http.StatusBadRequest, "region and project_id are required for a huawei-project source")
		return
	}
	if _, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, id); err != nil {
		storeErr(w, err)
		return
	}
	src, created, err := h.Store.UpsertSource(r.Context(), id, in.Kind, in.Region, in.ProjectID)
	if err != nil {
		storeErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		h.audit(r, &id, "source.create", map[string]any{"source_id": src.ID, "kind": src.Kind, "region": src.Region, "project_id": src.ProjectID})
	}
	writeJSON(w, status, src)
}

// rotateCredential stores a new AK/SK for one source and re-verifies it.
func (h *Handler) rotateCredential(w http.ResponseWriter, r *http.Request) {
	src, ok := h.sourceForWrite(w, r)
	if !ok {
		return
	}
	var in struct {
		AccessKey string `json:"access_key"`
		SecretKey string `json:"secret_key"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.AccessKey) == "" || strings.TrimSpace(in.SecretKey) == "" {
		writeErr(w, http.StatusBadRequest, "access_key and secret_key are required")
		return
	}
	results, allOK, err := h.attachAndVerify(r.Context(), src.CustomerID, src.Region, []string{src.ProjectID}, strings.TrimSpace(in.AccessKey), strings.TrimSpace(in.SecretKey))
	in.SecretKey = ""
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &src.CustomerID, "source.credential.rotate", map[string]any{"source_id": src.ID, "verified": allOK})
	updated, err := h.Store.GetSource(r.Context(), store.OperatorScope, src.ID)
	if err != nil {
		storeErr(w, err)
		return
	}
	status := http.StatusOK
	if !allOK {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{"source": updated, "results": results})
}

// verifySource re-runs the activation check with the stored credential.
func (h *Handler) verifySource(w http.ResponseWriter, r *http.Request) {
	src, ok := h.sourceForWrite(w, r)
	if !ok {
		return
	}
	if src.CredentialID == nil {
		writeErr(w, http.StatusConflict, "source has no credential; rotate one first")
		return
	}
	ak, enc, err := h.Store.GetCredentialSecret(r.Context(), *src.CredentialID)
	if err != nil {
		storeErr(w, err)
		return
	}
	sk, err := h.Keys.Open(enc)
	if err != nil {
		slog.Error("open credential", "source", src.ID, "error", err)
		writeErr(w, http.StatusInternalServerError, "credential cannot be decrypted with the current APP_ENCRYPTION_KEY")
		return
	}
	verr := h.verify(r.Context(), src.ID, src.Region, src.ProjectID, ak, string(sk))
	for i := range sk {
		sk[i] = 0
	}
	updated, err := h.Store.GetSource(r.Context(), store.OperatorScope, src.ID)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &src.CustomerID, "source.verify", map[string]any{"source_id": src.ID, "status": updated.Status})
	status := http.StatusOK
	if verr != nil {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, updated)
}

func (h *Handler) deleteSource(w http.ResponseWriter, r *http.Request) {
	src, ok := h.sourceForWrite(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteSource(r.Context(), src.ID); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &src.CustomerID, "source.delete", map[string]any{"source_id": src.ID, "project_id": src.ProjectID})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// sourceForWrite loads a source and checks the session may modify it.
func (h *Handler) sourceForWrite(w http.ResponseWriter, r *http.Request) (store.CostSource, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return store.CostSource{}, false
	}
	src, err := h.Store.GetSource(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return store.CostSource{}, false
	}
	if _, ok := h.requireCustomer(w, r, src.CustomerID, true); !ok {
		return store.CostSource{}, false
	}
	return src, true
}
