package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The contracts API (DESIGN.md §15, EPIC #6867): the agreement a customer's
// commercial terms hang on.
//
//	GET|POST    /contracts                    the directory · create
//	GET         /contracts/renewals           the renewals-due list
//	GET|PATCH   /contracts/{id}               one contract · edit
//	DELETE      /contracts/{id}
//	PUT         /contracts/{id}/items         replace the committed-use and allowance lines
//	POST        /contracts/{id}/sla-credit    credit an availability breach
//	GET         /customers/{id}/contracts     one customer's contracts
//
// PERMISSIONS (DESIGN.md §15.7). Writing a contract is `customers.manage` at
// the Sovereign — a contract is a commercial fact about a customer, and the
// role that owns customers owns it. Reading is `metering.read` AT THE SCOPE:
// a customer principal reads its OWN contract and nothing else, a partner
// principal its customers', a Sovereign principal every one. Issuing an SLA
// credit is `billing.issue`, like every other credit note, because that is
// what it is. Every write is audited.

// requireContractWriter is the one write gate: customers.manage at the
// Sovereign.
func (h *Handler) requireContractWriter(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	return h.requireSovereign(w, r, access.CustomersManage)
}

// contractBody is the create / patch document. On a PATCH an absent key stays
// unchanged; `minimum_commitment: null` clears the minimum and
// `signed_at: null` un-signs the contract.
type contractBody struct {
	CustomerID        string         `json:"customer_id"`
	Name              *string        `json:"name"`
	StartsOn          *string        `json:"starts_on"`
	EndsOn            *string        `json:"ends_on"`
	TermMonths        *int           `json:"term_months"`
	AutoRenew         *bool          `json:"auto_renew"`
	RenewalNoticeDays *int           `json:"renewal_notice_days"`
	MinimumCommitment *store.Decimal `json:"minimum_commitment"`
	Currency          *string        `json:"currency"`
	Status            *string        `json:"status"`
	SignedAt          *time.Time     `json:"signed_at"`
	PORef             *string        `json:"po_reference"`
	Notes             *string        `json:"notes"`
}

// input turns the body into the store's document. rawKeys says which keys the
// caller actually sent, so `null` can be told from "absent": clearing the
// minimum and clearing the signature are both real edits.
func (b contractBody) input(sent map[string]bool) store.ContractInput {
	in := store.ContractInput{
		CustomerID: strings.TrimSpace(b.CustomerID), Name: b.Name, StartsOn: b.StartsOn, EndsOn: b.EndsOn,
		TermMonths: b.TermMonths, AutoRenew: b.AutoRenew, RenewalNoticeDays: b.RenewalNoticeDays,
		MinimumCommitment: b.MinimumCommitment, Currency: b.Currency, Status: b.Status, SignedAt: b.SignedAt,
		PORef: b.PORef, Notes: b.Notes,
	}
	if sent["minimum_commitment"] && (b.MinimumCommitment == nil || strings.TrimSpace(string(*b.MinimumCommitment)) == "") {
		in.ClearMinimum, in.MinimumCommitment = true, nil
	}
	if sent["signed_at"] && b.SignedAt == nil {
		in.ClearSignedAt = true
	}
	return in
}

func contractAudit(c store.Contract) map[string]any {
	return map[string]any{
		"contract_id": c.ID, "customer_id": c.CustomerID, "name": c.Name, "starts_on": c.StartsOn, "ends_on": c.EndsOn,
		"term_months": c.TermMonths, "auto_renew": c.AutoRenew, "renewal_notice_days": c.RenewalNoticeDays,
		"minimum_commitment": c.MinimumCommitment, "currency": c.Currency, "status": c.Status, "po_reference": c.PORef,
	}
}

// listContracts — GET /contracts?customer_id=&status=. A Sovereign principal
// sees every contract, a partner principal its customers', a customer
// principal its own.
func (h *Handler) listContracts(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	f := store.ContractFilter{CustomerID: strings.TrimSpace(qs.Get("customer_id")), Status: strings.TrimSpace(qs.Get("status"))}
	if f.CustomerID != "" {
		if _, ok := h.requirePermission(w, r, access.MeteringRead, f.CustomerID); !ok {
			return
		}
	} else if s.Scope().Operator {
		if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
			return
		}
	}
	list, err := h.Store.ListContracts(r.Context(), s.Scope(), f)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contracts": list})
}

// customerContracts — GET /customers/{id}/contracts, the Contract tab of the
// customer page. A customer's own principal reads it too.
func (h *Handler) customerContracts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	list, err := h.Store.ListContracts(r.Context(), s.Scope(), store.ContractFilter{CustomerID: id})
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contracts": list})
}

// renewalsDue — GET /contracts/renewals?on=YYYY-MM-DD. Every ACTIVE contract
// inside its renewal notice window: `renewal_notice_days` before the end date
// and not yet past it. This is the list an operator acts on — there is no
// mail and no scheduler of its own; the collections evaluator only decides,
// on the end date itself, between renewing and expiring (DESIGN.md §15.6).
func (h *Handler) renewalsDue(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	on := strings.TrimSpace(r.URL.Query().Get("on"))
	if on == "" {
		on = time.Now().UTC().Format("2006-01-02")
	}
	if !store.ValidDate(on) {
		writeErr(w, http.StatusBadRequest, "on must be YYYY-MM-DD")
		return
	}
	list, err := h.Store.ListContracts(r.Context(), s.Scope(), store.ContractFilter{RenewalsDueOn: on})
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"on": on, "contracts": list})
}

// getContract — GET /contracts/{id}. Outside the caller's scope it reads as
// 404, so ids of other customers' contracts are not confirmed.
func (h *Handler) getContract(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	c, err := h.Store.GetContract(r.Context(), s.Scope(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if _, ok := h.requirePermission(w, r, access.MeteringRead, c.CustomerID); !ok {
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// createContract — POST /contracts.
func (h *Handler) createContract(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireContractWriter(w, r); !ok {
		return
	}
	var in contractBody
	sent, err := decodeKeys(r, &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.CustomerID) == "" {
		writeErr(w, http.StatusBadRequest, "customer_id is required")
		return
	}
	c, err := h.Store.CreateContract(r.Context(), in.input(sent))
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &c.CustomerID, "contract.create", contractAudit(c))
	writeJSON(w, http.StatusCreated, c)
}

// patchContract — PATCH /contracts/{id}.
func (h *Handler) patchContract(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireContractWriter(w, r); !ok {
		return
	}
	var in contractBody
	sent, err := decodeKeys(r, &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	c, err := h.Store.UpdateContract(r.Context(), r.PathValue("id"), in.input(sent))
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &c.CustomerID, "contract.update", contractAudit(c))
	writeJSON(w, http.StatusOK, c)
}

// deleteContract — DELETE /contracts/{id}. Statements already rated under it
// keep their lines; they simply stop naming a contract.
func (h *Handler) deleteContract(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireContractWriter(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	c, err := h.Store.GetContract(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteContract(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &c.CustomerID, "contract.delete", contractAudit(c))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// putContractItems — PUT /contracts/{id}/items. The list sent is the WHOLE
// list: committed-use lines and contract allowances together.
func (h *Handler) putContractItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireContractWriter(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	var in struct {
		Items []store.ContractItem `json:"items"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	items, err := h.Store.PutContractItems(r.Context(), id, in.Items)
	if err != nil {
		storeErr(w, err)
		return
	}
	c, err := h.Store.GetContract(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &c.CustomerID, "contract.items.put", map[string]any{"contract_id": id, "items": len(items), "lines": store.ContractItemsJSON(items)})
	writeJSON(w, http.StatusOK, c)
}

// slaCreditBody is what POST /contracts/{id}/sla-credit carries.
type slaCreditBody struct {
	StatementID  string        `json:"statement_id"`
	Pct          store.Decimal `json:"pct"`
	Availability store.Decimal `json:"measured_availability"`
	Reason       string        `json:"reason"`
}

// issueSLACredit — POST /contracts/{id}/sla-credit. An availability breach
// credited against a named statement: a real credit note, numbered and
// posted to the ledger like every other, that additionally records the
// contract, the percentage and the availability measured.
func (h *Handler) issueSLACredit(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.BillingIssue)
	if !ok {
		return
	}
	var in slaCreditBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.StatementID) == "" {
		writeErr(w, http.StatusBadRequest, "statement_id is required: an SLA credit is issued against a named statement")
		return
	}
	note, err := h.Store.IssueSLACredit(r.Context(), r.PathValue("id"), store.SLACreditInput{
		StatementID: strings.TrimSpace(in.StatementID), Pct: in.Pct, Availability: in.Availability,
		Reason: strings.TrimSpace(in.Reason), Actor: s.Email,
	})
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &note.CustomerID, "contract.sla_credit", map[string]any{
		"contract_id": r.PathValue("id"), "statement_id": in.StatementID, "credit_note": note.Number,
		"pct": string(in.Pct), "measured_availability": string(in.Availability), "total": string(note.Total),
	})
	writeJSON(w, http.StatusCreated, note)
}
