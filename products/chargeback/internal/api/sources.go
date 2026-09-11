package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
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

// listAllSources — GET /sources — is the operator-wide directory of every
// source with its layer, book and customer; `?internal=true` adds the
// Sovereign's own internal platform source.
func (h *Handler) listAllSources(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	list, err := h.Store.ListAllSources(r.Context(), r.URL.Query().Get("internal") == "true")
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": list})
}

// getSource — GET /sources/{id} and GET /customers/{id}/sources/{sid} — one
// source inside the session's scope, with the collecting flag.
func (h *Handler) getSource(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	src, err := h.Store.GetSource(r.Context(), s.Scope(), sourceIDOf(r))
	if err != nil {
		storeErr(w, err)
		return
	}
	if cid := r.PathValue("id"); cid != "" && r.PathValue("sid") != "" && src.CustomerID != cid {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		store.CostSource
		Collecting bool `json:"collecting"`
	}{src, h.collectingFor(r, src)})
}

// sourceIDOf reads the source id from either route shape: /sources/{id} or
// /customers/{id}/sources/{sid}.
func sourceIDOf(r *http.Request) string {
	if sid := r.PathValue("sid"); sid != "" {
		return sid
	}
	return r.PathValue("id")
}

// validCreateKind accepts the kinds an operator may create by hand: the
// CLOUD kinds. Platform sources (openova-org) are created by the
// Organization sync, the internal one by the platform collector.
func validCreateKind(k string) bool {
	for _, c := range store.CloudSourceKinds {
		if c == k {
			return true
		}
	}
	return false
}

const createKindHelp = "kind must be huawei-project or file; platform sources are created by the Organization sync"

func (h *Handler) createSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireCustomer(w, r, id, true); !ok {
		return
	}
	var in struct {
		Kind      string `json:"kind"`
		Region    string `json:"region"`
		ProjectID string `json:"project_id"`
		// ScopeToken narrows a project-scoped source to ONE deployment's
		// resources (#6855/#6859). Without it the source bills every resource
		// in the project, including shared infrastructure and any other
		// Sovereign sharing it. Empty = bill the whole project (the prior
		// behaviour), so an existing integration is unaffected.
		ScopeToken string `json:"scope_token"`
		// PriceBookID assigns the cloud book that rates this source; may be
		// set later with PATCH. Its scope must be cloud.
		PriceBookID string `json:"price_book_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Kind == "" {
		in.Kind = store.SourceKindHuaweiProject
	}
	if !validCreateKind(in.Kind) {
		writeErr(w, http.StatusBadRequest, createKindHelp)
		return
	}
	if in.Kind == store.SourceKindHuaweiProject && (strings.TrimSpace(in.Region) == "" || strings.TrimSpace(in.ProjectID) == "") {
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
	if tok := strings.TrimSpace(in.ScopeToken); tok != "" || src.ScopeToken != "" {
		if err := h.Store.SetSourceScopeToken(r.Context(), src.ID, tok); err != nil {
			storeErr(w, err)
			return
		}
		src.ScopeToken = tok
	}
	if book := strings.TrimSpace(in.PriceBookID); book != "" {
		if err := h.Store.SetSourcePriceBook(r.Context(), src.ID, book); err != nil {
			if errors.Is(err, store.ErrInvalid) {
				writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
				return
			}
			storeErr(w, err)
			return
		}
		if src, err = h.Store.GetSource(r.Context(), store.OperatorScope, src.ID); err != nil {
			storeErr(w, err)
			return
		}
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		h.audit(r, &id, "source.create", map[string]any{"source_id": src.ID, "kind": src.Kind, "layer": src.Layer, "region": src.Region, "project_id": src.ProjectID, "price_book_id": src.PriceBookID})
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
	if allOK {
		h.activateIfPending(r, src.CustomerID)
	}
	updated, err := h.Store.GetSource(r.Context(), store.OperatorScope, src.ID)
	if err != nil {
		storeErr(w, err)
		return
	}
	status := http.StatusOK
	if !allOK {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{"source": updated, "results": results, "collecting": h.collectingFor(r, updated)})
}

// verifySource re-runs the activation check with the stored credential.
func (h *Handler) verifySource(w http.ResponseWriter, r *http.Request) {
	src, ok := h.sourceForWrite(w, r)
	if !ok {
		return
	}
	if src.Status == store.StatusDisabled {
		writeErr(w, http.StatusConflict, "source is disabled; enable it first")
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
	if verr == nil {
		h.activateIfPending(r, src.CustomerID)
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
	writeJSON(w, status, struct {
		store.CostSource
		Collecting bool `json:"collecting"`
	}{updated, h.collectingFor(r, updated)})
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

// sourceForWrite loads a source and checks the session may modify it. The
// internal platform source belongs to no customer and is never written
// through the API.
func (h *Handler) sourceForWrite(w http.ResponseWriter, r *http.Request) (store.CostSource, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return store.CostSource{}, false
	}
	src, err := h.Store.GetSource(r.Context(), s.Scope(), sourceIDOf(r))
	if err != nil {
		storeErr(w, err)
		return store.CostSource{}, false
	}
	if src.Internal {
		writeErr(w, http.StatusBadRequest, "the internal platform source is maintained by the platform collector and is not edited through the API")
		return store.CostSource{}, false
	}
	if cid := r.PathValue("id"); cid != "" && r.PathValue("sid") != "" && src.CustomerID != cid {
		writeErr(w, http.StatusNotFound, "not found")
		return store.CostSource{}, false
	}
	if _, ok := h.requireCustomer(w, r, src.CustomerID, true); !ok {
		return store.CostSource{}, false
	}
	return src, true
}

// collectingFor derives the collecting flag for one source: its customer is
// active and the source itself is verified — the exact gate the collector's
// source listing applies. The internal source collects while the adapter
// runs; it has no customer to be active.
func (h *Handler) collectingFor(r *http.Request, src store.CostSource) bool {
	if src.Status != "verified" {
		return false
	}
	if src.Internal {
		return true
	}
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, src.CustomerID)
	return err == nil && c.Status == "active"
}

// patchSource edits a source's location, scope or price book (#6867). The
// operator may change everything; a customer admin of the owning customer
// may change ONLY scope_token — region and project_id decide what the
// customer is billed for, domain_id is what verification stamped, and the
// price book decides the rates, so those stay with the operator. Changing
// region or project_id resets the source to pending: the stored verification
// proved a different project. A price book whose scope is not the source's
// layer is refused with 400 ("price book scope X does not match source
// layer Y") and nothing else in the patch is applied.
func (h *Handler) patchSource(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	src, ok := h.sourceForWrite(w, r)
	if !ok {
		return
	}
	var in struct {
		Region      *string `json:"region"`
		ProjectID   *string `json:"project_id"`
		ScopeToken  *string `json:"scope_token"`
		DomainID    *string `json:"domain_id"`
		PriceBookID *string `json:"price_book_id"`
		// Disabled decommissions a source (true) or brings it back (false):
		// the collector skips it, it counts as neither verified nor live,
		// and its history keeps rating on every surface.
		Disabled *bool `json:"disabled"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	p := store.SourcePatch{Region: in.Region, ProjectID: in.ProjectID, ScopeToken: in.ScopeToken, DomainID: in.DomainID, PriceBookID: in.PriceBookID}
	fields := p.Fields()
	if in.Disabled != nil {
		fields = append(fields, "disabled")
	}
	if len(fields) == 0 {
		writeErr(w, http.StatusBadRequest, "nothing to update: give region, project_id, scope_token, domain_id, price_book_id or disabled")
		return
	}
	if !access.Has(access.Bindings(s), access.CustomersManage, src.CustomerID) && (in.Region != nil || in.ProjectID != nil || in.DomainID != nil || in.PriceBookID != nil || in.Disabled != nil) {
		writeErr(w, http.StatusForbidden, "a customer owner may change scope_token only; region, project_id, domain_id, price_book_id and disabled need permission customers.manage")
		return
	}
	if in.Region != nil && src.Kind == store.SourceKindHuaweiProject && strings.TrimSpace(*in.Region) == "" {
		writeErr(w, http.StatusBadRequest, "region is required for a huawei-project source")
		return
	}
	if in.ProjectID != nil && src.Kind == store.SourceKindHuaweiProject && strings.TrimSpace(*in.ProjectID) == "" {
		writeErr(w, http.StatusBadRequest, "project_id is required for a huawei-project source")
		return
	}
	updated, err := h.Store.UpdateSource(r.Context(), src.ID, p)
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
			return
		}
		storeErr(w, err)
		return
	}
	if in.Disabled != nil {
		// Applied last: a location change resets the status to pending, and
		// the operator's decommission decision must survive that.
		if updated, err = h.Store.SetSourceDisabled(r.Context(), src.ID, *in.Disabled); err != nil {
			if errors.Is(err, store.ErrInvalid) {
				writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
				return
			}
			storeErr(w, err)
			return
		}
	}
	details := map[string]any{"source_id": src.ID, "fields": fields, "status": updated.Status}
	if in.PriceBookID != nil {
		details["price_book_id"] = updated.PriceBookID
		details["price_book_name"] = updated.PriceBookName
		// A partner customer's source moving onto a list book brings that
		// book into the partner's retail derivation (DESIGN.md §Partners).
		if c, cerr := h.Store.GetCustomer(r.Context(), store.OperatorScope, src.CustomerID); cerr == nil && c.PartnerID != nil {
			if _, derr := rating.DeriveRetailBooks(r.Context(), h.Store, *c.PartnerID); derr != nil {
				storeErr(w, derr)
				return
			}
		}
	}
	h.audit(r, &src.CustomerID, "source.update", details)
	writeJSON(w, http.StatusOK, struct {
		store.CostSource
		Collecting bool `json:"collecting"`
	}{updated, h.collectingFor(r, updated)})
}
