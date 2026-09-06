package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Saved views (#6867, DESIGN.md §3.8): a signed-in user's named explorer
// states. Any role may save views; each user only ever sees its own, so a
// customer's saved filters never reach another customer or the operator.

const defaultViewPage = "explore"

func (h *Handler) listViews(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	page := strings.TrimSpace(r.URL.Query().Get("page"))
	if page == "" {
		page = defaultViewPage
	}
	views, err := h.Store.ListViews(r.Context(), s.Email, page)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": views})
}

func (h *Handler) createView(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	var in struct {
		Name   string          `json:"name"`
		Page   string          `json:"page"`
		Params json.RawMessage `json:"params"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 {
		writeErr(w, http.StatusBadRequest, "name is required (at most 120 characters)")
		return
	}
	in.Page = strings.TrimSpace(in.Page)
	if in.Page == "" {
		in.Page = defaultViewPage
	}
	params := []byte(strings.TrimSpace(string(in.Params)))
	if len(params) == 0 || string(params) == "null" {
		params = []byte("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(params, &obj); err != nil {
		writeErr(w, http.StatusBadRequest, "params must be a JSON object")
		return
	}
	v, err := h.Store.CreateView(r.Context(), s.Email, in.Name, in.Page, params)
	if err != nil {
		if store.IsConflict(err) {
			writeErr(w, http.StatusConflict, "a view named "+in.Name+" already exists for this page")
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, s.CustomerID, "view.create", map[string]any{"view_id": v.ID, "name": v.Name, "page": v.Page})
	writeJSON(w, http.StatusCreated, v)
}

func (h *Handler) deleteView(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.Store.DeleteView(r.Context(), s.Email, id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, s.CustomerID, "view.delete", map[string]any{"view_id": id})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}
