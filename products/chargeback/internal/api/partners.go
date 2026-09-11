package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The partners API (DESIGN.md §13, EPIC #6867): resellers and agents.
//
//	GET|POST   /partners                       the directory · create
//	GET|PATCH  /partners/{id}                  one partner
//	GET|POST   /partners/tiers                 the tiers and their discounts
//	PUT        /partners/tiers/{id}/discounts  replace a tier's discounts
//	PUT        /partners/{id}/retail-rule      re-derives the retail book
//	GET        /partners/{id}/retail-book      the derived book(s) + below-buy
//	GET        /partners/{id}/customers        the end customers assigned
//	GET        /partners/{id}/statements       its wholesale / commission statements
//	GET        /partners/{id}/margin?period=   customer net, partner buy, margin
//	GET        /partners/{id}/account          its party's ledger
//	GET|POST   /partners/{id}/users            its partner-scoped bindings
//	DELETE     /partners/{id}/users/{email}
//
// Sovereign writes need partners.manage; a partner owner holds
// partner.self.manage on its OWN partner (its retail rule and its users).
// Reads need metering.read at the partner scope, which a Sovereign binding
// also carries. Every write is audited.

// requirePartner is requirePermission at the PARTNER scope: 404 when the
// caller holds no binding on the partner (so ids of other partners are not
// confirmed), 403 naming the permission when it is on the scope without it.
func (h *Handler) requirePartner(w http.ResponseWriter, r *http.Request, perm access.Permission, partnerID string) (store.Session, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return s, false
	}
	bindings := access.Bindings(s)
	if !access.OnPartner(bindings, partnerID) {
		writeErr(w, http.StatusNotFound, "not found")
		return s, false
	}
	if access.HasPartner(bindings, perm, partnerID) {
		return s, true
	}
	writeErr(w, http.StatusForbidden, "permission "+string(perm)+" required on this partner")
	return s, false
}

// partnerBody is the create/patch document. Absent keys stay unchanged on a
// patch; commission_pct "" clears the agent commission and tier_id ""
// clears the tier.
type partnerBody struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// TierID names the partner tier whose discounts set the buy price.
	TierID *string `json:"tier_id"`
	// BillTo is the billing model: partner (resell — the partner is invoiced
	// the wholesale statement) or customer (agent — we invoice the end
	// customer and credit the partner a commission).
	BillTo *string `json:"bill_to"`
	// CommissionPct is the agent commission as a percent of the customer
	// net, used when the partner has no tier.
	CommissionPct *store.Decimal `json:"commission_pct"`
	Status        *string        `json:"status"`
	ContactEmail  *string        `json:"contact_email"`
}

func partnerAudit(p store.Partner) map[string]any {
	return map[string]any{
		"partner_id": p.ID, "slug": p.Slug, "name": p.Name, "tier_id": p.TierID, "tier": p.TierName,
		"bill_to": p.BillTo, "commission_pct": p.CommissionPct, "status": p.Status, "contact_email": p.ContactEmail,
		"party_customer_id": p.PartyCustomerID,
	}
}

// listPartners — GET /partners. A Sovereign principal sees every partner; a
// partner principal only the ones it is bound to.
func (h *Handler) listPartners(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	bindings := access.Bindings(s)
	var ids []string
	if !access.IsSovereign(bindings) {
		ids = access.PartnerIDs(bindings)
		if len(ids) == 0 {
			// A customer principal has no partner directory: the partner it
			// buys through is named on its own customer document.
			writeErr(w, http.StatusForbidden, "permission metering.read required at the Sovereign")
			return
		}
	}
	list, err := h.Store.ListPartners(r.Context(), ids)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"partners": list})
}

// createPartner — POST /partners. The partner's PARTY (its customers row,
// party_kind = partner) is created with it, so it has a balance, invoices
// and collections through the one ledger; its contact is granted
// partner-owner at the partner scope.
func (h *Handler) createPartner(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.PartnersManage)
	if !ok {
		return
	}
	var in partnerBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !validSlug(in.Slug) {
		writeErr(w, http.StatusBadRequest, "slug must be 2-63 lowercase letters, digits or dashes")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	email := normEmail(deref(in.ContactEmail))
	if email != "" && !validEmail(email) {
		writeErr(w, http.StatusBadRequest, "contact_email is invalid")
		return
	}
	if in.TierID != nil && strings.TrimSpace(*in.TierID) != "" {
		if _, err := h.Store.GetPartnerTier(r.Context(), *in.TierID); err != nil {
			writeErr(w, http.StatusBadRequest, "tier_id does not exist")
			return
		}
	}
	p, err := h.Store.CreatePartner(r.Context(), store.PartnerInput{
		Slug: in.Slug, Name: in.Name, TierID: in.TierID, BillTo: deref(in.BillTo),
		CommissionPct: in.CommissionPct, ContactEmail: email, GrantedBy: s.Email,
	})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, &p.PartyCustomerID, "partner.create", partnerAudit(p))
	if email != "" {
		h.audit(r, nil, "access.binding", map[string]any{"op": "grant", "subject_email": email, "role": store.RolePartnerOwner, "scope_kind": store.ScopeKindPartner, "partner_id": p.ID})
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handler) getPartner(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePartner(w, r, access.MeteringRead, id); !ok {
		return
	}
	p, err := h.Store.GetPartner(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// patchPartner — PATCH /partners/{id}. A tier or billing-model change moves
// the buy price, so the derived retail books are re-derived here.
func (h *Handler) patchPartner(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireSovereign(w, r, access.PartnersManage)
	if !ok {
		return
	}
	if _, err := h.Store.GetPartner(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	var in partnerBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.ContactEmail != nil && strings.TrimSpace(*in.ContactEmail) != "" && !validEmail(*in.ContactEmail) {
		writeErr(w, http.StatusBadRequest, "contact_email is invalid")
		return
	}
	if in.TierID != nil && strings.TrimSpace(*in.TierID) != "" {
		if _, err := h.Store.GetPartnerTier(r.Context(), *in.TierID); err != nil {
			writeErr(w, http.StatusBadRequest, "tier_id does not exist")
			return
		}
	}
	patch := store.PartnerPatch{TierID: in.TierID, BillTo: in.BillTo, CommissionPct: in.CommissionPct, Status: in.Status, ContactEmail: in.ContactEmail, GrantedBy: s.Email}
	if strings.TrimSpace(in.Name) != "" {
		patch.Name = &in.Name
	}
	p, err := h.Store.UpdatePartner(r.Context(), id, patch)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	if _, err := rating.DeriveRetailBooks(r.Context(), h.Store, id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &p.PartyCustomerID, "partner.update", partnerAudit(p))
	writeJSON(w, http.StatusOK, p)
}

// ---------------------------------------------------------------------------
// tiers
// ---------------------------------------------------------------------------

// listPartnerTiers — GET /partners/tiers. Sovereign-only: a tier is the
// provider's commercial structure and a partner never sees another's.
func (h *Handler) listPartnerTiers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	list, err := h.Store.ListPartnerTiers(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tiers": list})
}

func (h *Handler) createPartnerTier(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.PartnersManage); !ok {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	t, err := h.Store.CreatePartnerTier(r.Context(), in.Name, in.Description)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "partner.tier", map[string]any{"op": "create", "tier_id": t.ID, "name": t.Name})
	writeJSON(w, http.StatusCreated, t)
}

// putTierDiscounts — PUT /partners/tiers/{id}/discounts. The given percent
// discounts become THE tier's discounts; every partner on the tier has its
// derived retail book re-derived, because the buy price just moved.
func (h *Handler) putTierDiscounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.PartnersManage); !ok {
		return
	}
	id := r.PathValue("id")
	var in struct {
		Discounts []discountBody `json:"discounts"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	inputs := make([]store.DiscountInput, 0, len(in.Discounts))
	for _, d := range in.Discounts {
		if d.Kind == "" {
			d.Kind = "percent"
		}
		// The SAME validation every discount goes through: a tier discount
		// is a discount, decided by the one combination engine.
		got, msg := validateDiscount(d)
		if msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		got.CustomerID = nil
		inputs = append(inputs, got)
	}
	list, err := h.Store.ReplaceTierDiscounts(r.Context(), id, inputs)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	affected, err := h.Store.PartnersOnTier(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	for _, pid := range affected {
		if _, err := rating.DeriveRetailBooks(r.Context(), h.Store, pid); err != nil {
			storeErr(w, err)
			return
		}
	}
	h.audit(r, nil, "partner.tier", map[string]any{"op": "discounts", "tier_id": id, "count": len(list), "partners_rederived": len(affected)})
	t, err := h.Store.GetPartnerTier(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---------------------------------------------------------------------------
// the retail rule and its derived book
// ---------------------------------------------------------------------------

type retailRuleBody struct {
	// Base is what the markup applies to: the Sovereign's list price, or the
	// partner's own buy price.
	Base string `json:"base"`
	// MarkupPct is the default markup; Overrides narrow it to a service
	// (a SKU's first segment) or one SKU — most specific wins.
	MarkupPct store.Decimal          `json:"markup_pct"`
	Overrides []store.RetailOverride `json:"overrides"`
}

// retailDocument is what the retail-rule and retail-book endpoints answer:
// the rule, the derived books with their items, and every line priced BELOW
// the partner's own buy price — a warning, never a refusal.
type retailDocument struct {
	PartnerID string                `json:"partner_id"`
	BillTo    string                `json:"bill_to"`
	Rule      *store.RetailRule     `json:"retail_rule"`
	Books     []rating.DerivedBook  `json:"books"`
	BelowBuy  []rating.BelowBuyLine `json:"below_buy"`
	// Note says why there is no retail book, when there is none.
	Note string `json:"note,omitempty"`
}

func (h *Handler) retailDoc(r *http.Request, p store.Partner, books []rating.DerivedBook) (retailDocument, error) {
	doc := retailDocument{PartnerID: p.ID, BillTo: p.BillTo, Books: books, BelowBuy: []rating.BelowBuyLine{}}
	if doc.Books == nil {
		doc.Books = []rating.DerivedBook{}
	}
	rule, ok, err := h.Store.GetRetailRule(r.Context(), p.ID)
	if err != nil {
		return doc, err
	}
	if ok {
		doc.Rule = &rule
	}
	for _, b := range doc.Books {
		doc.BelowBuy = append(doc.BelowBuy, b.BelowBuy...)
	}
	switch {
	case p.BillTo == store.BillToCustomer:
		doc.Note = "This partner is an agent: we invoice its end customers at our own books and credit the partner a commission, so there is no retail book."
	case !ok:
		doc.Note = "No retail rule yet — set a base and a markup to derive this partner's retail book from the list books its customers are priced by."
	case len(doc.Books) == 0:
		doc.Note = "No list book to derive from yet: assign a price book to the sources of this partner's customers."
	}
	return doc, nil
}

// putRetailRule — PUT /partners/{id}/retail-rule. Stores the rule, RE-DERIVES
// the partner's retail books from the list books its customers are priced by,
// and answers with the derived books and the below-buy lines.
func (h *Handler) putRetailRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePartner(w, r, access.PartnerSelfManage, id); !ok {
		return
	}
	p, err := h.Store.GetPartner(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	var in retailRuleBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	rule, err := h.Store.PutRetailRule(r.Context(), id, store.RetailRule{Base: in.Base, MarkupPct: in.MarkupPct, Overrides: in.Overrides})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	books, err := rating.DeriveRetailBooks(r.Context(), h.Store, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	doc, err := h.retailDoc(r, p, books)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &p.PartyCustomerID, "partner.retail_rule", map[string]any{
		"partner_id": id, "base": rule.Base, "markup_pct": rule.MarkupPct, "overrides": len(rule.Overrides),
		"books": len(books), "below_buy": len(doc.BelowBuy),
	})
	writeJSON(w, http.StatusOK, doc)
}

// getRetailBook — GET /partners/{id}/retail-book.
func (h *Handler) getRetailBook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePartner(w, r, access.MeteringRead, id); !ok {
		return
	}
	p, err := h.Store.GetPartner(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	books, err := rating.DeriveRetailBooks(r.Context(), h.Store, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	doc, err := h.retailDoc(r, p, books)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// ---------------------------------------------------------------------------
// what a partner reads about itself
// ---------------------------------------------------------------------------

func (h *Handler) listPartnerCustomers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePartner(w, r, access.MeteringRead, id); !ok {
		return
	}
	list, err := h.Store.PartnerCustomers(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"customers": list})
}

// listPartnerStatements — GET /partners/{id}/statements: the partner's OWN
// statements, wholesale (resell) or commission (agent).
func (h *Handler) listPartnerStatements(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePartner(w, r, access.MeteringRead, id); !ok {
		return
	}
	list, err := h.Store.ListPartnerStatements(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"statements": list})
}

// partnerMargin — GET /partners/{id}/margin?period=YYYY-MM: per end customer
// per service, the customer net, the partner buy, the margin and its
// percentage — all read from the per-line figures frozen on the customer
// statements of that period, never recomputed from a live price.
func (h *Handler) partnerMargin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePartner(w, r, access.MeteringRead, id); !ok {
		return
	}
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	if period == "" {
		period = h.Now().UTC().Format("2006-01")
	}
	if !periodShape.MatchString(period) {
		writeErr(w, http.StatusBadRequest, "period must be YYYY-MM")
		return
	}
	rep, err := h.Store.MarginReport(r.Context(), id, period)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// partnerAccount — GET /partners/{id}/account. The partner is a PARTY: its
// account is the customer account of its party row, through the same handler
// and the same ledger.
func (h *Handler) partnerAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePartner(w, r, access.MeteringRead, id); !ok {
		return
	}
	p, err := h.Store.GetPartner(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if p.PartyCustomerID == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	h.writeAccount(w, r, store.OperatorScope, p.PartyCustomerID)
}

// ---------------------------------------------------------------------------
// the partner's users
// ---------------------------------------------------------------------------

func (h *Handler) listPartnerUsers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requirePartner(w, r, access.MeteringRead, id); !ok {
		return
	}
	list, err := h.Store.PartnerUsers(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": list})
}

func (h *Handler) addPartnerUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requirePartner(w, r, access.PartnerSelfManage, id)
	if !ok {
		return
	}
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decode(r, &in); err != nil || !validEmail(in.Email) {
		writeErr(w, http.StatusBadRequest, "email and role (partner-owner | partner-viewer) are required")
		return
	}
	role := strings.ToLower(strings.TrimSpace(in.Role))
	if role == "" {
		role = store.RolePartnerViewer
	}
	if role != store.RolePartnerOwner && role != store.RolePartnerViewer {
		writeErr(w, http.StatusBadRequest, "role must be partner-owner or partner-viewer")
		return
	}
	if _, err := h.Store.GetPartner(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.UpsertPartnerUser(r.Context(), id, in.Email, role, s.Email); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "access.binding", map[string]any{"op": "grant", "subject_email": normEmail(in.Email), "role": role, "scope_kind": store.ScopeKindPartner, "partner_id": id})
	writeJSON(w, http.StatusCreated, store.RoleBinding{SubjectEmail: normEmail(in.Email), Role: role, ScopeKind: store.ScopeKindPartner, PartnerID: &id})
}

func (h *Handler) deletePartnerUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	email := normEmail(r.PathValue("email"))
	if _, ok := h.requirePartner(w, r, access.PartnerSelfManage, id); !ok {
		return
	}
	if err := h.Store.DeletePartnerUser(r.Context(), id, email); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "access.binding", map[string]any{"op": "revoke", "subject_email": email, "scope_kind": store.ScopeKindPartner, "partner_id": id})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// ---------------------------------------------------------------------------
// re-derivation triggers
// ---------------------------------------------------------------------------

// rederiveForBook re-derives the retail books of every resell partner whose
// customers are priced by the given LIST book — what a change to that book's
// items or divisor means for the partners that resell it.
func (h *Handler) rederiveForBook(r *http.Request, bookID string) error {
	partners, err := h.Store.PartnersDerivingFrom(r.Context(), bookID)
	if err != nil {
		return err
	}
	for _, pid := range partners {
		if _, err := rating.DeriveRetailBooks(r.Context(), h.Store, pid); err != nil {
			return err
		}
	}
	return nil
}

// derivedBookWriteRefused answers 409 on a write to a partner's DERIVED
// retail book: it is materialised from the rule, the tier and the list book,
// and an edit here would be overwritten by the next derivation.
func (h *Handler) derivedBookWriteRefused(w http.ResponseWriter, r *http.Request, bookID string) bool {
	derived, err := h.Store.IsDerivedBook(r.Context(), bookID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false // the handler's own 404 is the better message
		}
		storeErr(w, err)
		return true
	}
	if !derived {
		return false
	}
	writeErr(w, http.StatusConflict, "this is a partner's derived retail book and is read-only; change the partner's retail rule, its tier, or the list book it derives from")
	return true
}
