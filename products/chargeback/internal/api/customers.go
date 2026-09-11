package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
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
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	AdminEmail string `json:"admin_email"`
	// PriceBookID is DEPRECATED and IGNORED (DESIGN.md §4.1): the price
	// book is assigned per source (PATCH /sources/{id} price_book_id). The
	// key is still decoded so an older client is not answered 400.
	PriceBookID *string `json:"price_book_id"`
	// BillingMode is DEPRECATED and IGNORED (DESIGN.md §8): showback /
	// chargeback / real were three labels for three different questions and
	// are replaced by charging + payment_model + payment_method. The key is
	// still decoded so an older client is not answered 400, and the value on
	// the customer document is derived from the four fields.
	BillingMode *string `json:"billing_mode"`
	StartDate   *string `json:"start_date"`
	Status      *string `json:"status"`
	OrgSlug     *string `json:"org_slug"`
	Kind        *string `json:"kind"`
	// PlanSlug is the catalog plan (s, m, l, xl, flexi; "" = none). Settable
	// on external customers; an Organization customer's plan is read from
	// its Organization CR by OrgSync and a PATCH is refused.
	PlanSlug *string `json:"plan_slug"`
	// The commercial model (DESIGN.md §8). Charging says whether anything is
	// collected (billed | informational); PaymentModel when (prepaid |
	// postpaid) and PaymentMethod how (gateway | transfer | internal), both
	// meaningful only when billed; GatewayName which gateway implementation
	// collects. PORef is the customer's standing purchase-order reference
	// and PaymentTermsDays the net terms an invoice falls due in; both are
	// copied onto each statement at issue.
	Charging         *string `json:"charging"`
	PaymentModel     *string `json:"payment_model"`
	PaymentMethod    *string `json:"payment_method"`
	GatewayName      *string `json:"gateway_name"`
	PORef            *string `json:"po_reference"`
	PaymentTermsDays *int    `json:"payment_terms_days"`
	// ExternalAccountID is this customer's account in the operator's own
	// billing system (DESIGN.md §8.10). It is the one commercial field that
	// stays writable in external mode — it is how a rated bill is attributed
	// over there, and only we know which of our customers is which.
	ExternalAccountID *string `json:"external_account_id"`
	// The tax profile (DESIGN.md §9.4). tax_rate is a fraction (0.05 = 5 %);
	// an empty string clears the override back to the Sovereign default.
	TaxRegistrationNumber *string        `json:"tax_registration_number"`
	TaxExempt             *bool          `json:"tax_exempt"`
	TaxExemptReason       *string        `json:"tax_exempt_reason"`
	TaxRate               *store.Decimal `json:"tax_rate"`
	// DESIGN.md §17 — what a tax RULE needs: where the customer is
	// registered, whether it is a registered BUSINESS (reverse charge
	// applies to a business and never to a consumer), and the exemption
	// certificate behind tax_exempt. An empty tax_exemption_expires_on
	// clears the expiry back to "no expiry recorded".
	TaxCountry            *string `json:"tax_country"`
	TaxRegion             *string `json:"tax_region"`
	TaxBusiness           *bool   `json:"tax_business"`
	TaxExemptionNumber    *string `json:"tax_exemption_number"`
	TaxExemptionExpiresOn *string `json:"tax_exemption_expires_on"`
	TaxExemptionScanRef   *string `json:"tax_exemption_scan_ref"`
	// Account credit (DESIGN.md §9.5): apply credit at issue; the prepaid
	// wallet's low-balance alert (empty string = off) and suspend-at-zero.
	AutoApplyCredit     *bool          `json:"auto_apply_credit"`
	LowBalanceThreshold *store.Decimal `json:"low_balance_threshold"`
	SuspendAtZero       *bool          `json:"suspend_at_zero"`
	// PartnerID assigns this customer to a partner (DESIGN.md §13);
	// "" makes it direct again. One partner per customer, and it needs
	// partners.manage — a customer never sets who resells to it.
	PartnerID *string `json:"partner_id"`
}

// validPlanSlug accepts a catalog plan slug or "" (no plan), case-folded.
func validPlanSlug(p string) bool {
	n := store.NormalizePlanSlug(p)
	return n == "" || store.ValidPlanSlug(n)
}

const planSlugHelp = "plan_slug must be s, m, l, xl, flexi or empty"

// validBillingMode is still used by the CSV importer, whose documented
// columns include billing_mode; the importer maps it through
// store.CommercialFromBillingMode, exactly as the migration did.
func validBillingMode(m string) bool { return m == "real" || m == "chargeback" || m == "showback" }

const paymentTermsHelp = "payment_terms_days must be a whole number of days between 0 and 365"

// externallyOwnedHelp is what a write to a commercial field is answered with
// when the operator's billing system is the system of record (DESIGN.md
// §8.10). The fields are still shown — they are what the export carries —
// but they are theirs to change, not ours.
const externallyOwnedHelp = "this Sovereign invoices through the operator's billing system; charging, payment model, payment method, terms and the tax profile are owned there and are read-only here"

// commercialWriteRefused reports whether the body touches a field the
// external billing system owns, and answers 400 when it does. The customer
// master — the tax registration and exemption included — is theirs too
// (DESIGN.md §9.1); the account-credit knobs stay ours in every mode.
func (h *Handler) commercialWriteRefused(w http.ResponseWriter, r *http.Request, in customerBody) bool {
	if in.Charging == nil && in.PaymentModel == nil && in.PaymentMethod == nil && in.GatewayName == nil && in.PORef == nil && in.PaymentTermsDays == nil &&
		in.TaxRegistrationNumber == nil && in.TaxExempt == nil && in.TaxExemptReason == nil && in.TaxRate == nil &&
		in.TaxCountry == nil && in.TaxRegion == nil && in.TaxBusiness == nil &&
		in.TaxExemptionNumber == nil && in.TaxExemptionExpiresOn == nil && in.TaxExemptionScanRef == nil {
		return false
	}
	settings, err := h.Store.GetBillingSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return true
	}
	if !settings.ExternalCommercial() {
		return false
	}
	writeErr(w, http.StatusBadRequest, externallyOwnedHelp)
	return true
}

// commercialFrom reads the four commercial keys off the body. Absent keys
// stay nil, which the store reads as "unchanged" on a patch and "not given"
// on a create.
func commercialFrom(in customerBody) store.CustomerPatch {
	return store.CustomerPatch{Charging: in.Charging, PaymentModel: in.PaymentModel, PaymentMethod: in.PaymentMethod, GatewayName: in.GatewayName}
}

// deref returns the pointed-to string, or "".
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func validStatus(s string) bool { return s == "pending" || s == "active" || s == "suspended" }

func (h *Handler) createCustomer(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CustomersManage); !ok {
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
	if h.commercialWriteRefused(w, r, in) {
		return
	}
	ci := store.CustomerInput{Slug: in.Slug, Name: in.Name, AdminEmail: in.AdminEmail}
	if in.PriceBookID != nil {
		slog.Info("customer create: price_book_id is deprecated and ignored; assign the book on the customer's sources (DESIGN.md §4.1)", "slug", in.Slug)
	}
	if in.BillingMode != nil {
		slog.Info("customer create: billing_mode is deprecated and ignored; set charging, payment_model and payment_method (DESIGN.md §8)", "slug", in.Slug)
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
	if in.PlanSlug != nil {
		if !validPlanSlug(*in.PlanSlug) {
			writeErr(w, http.StatusBadRequest, planSlugHelp)
			return
		}
		ci.PlanSlug = *in.PlanSlug
	}
	// The commercial position, validated as a whole by the store: an
	// informational customer may carry no payment model or method, and a
	// billed one must carry both.
	ci.Commercial = store.Commercial{Charging: deref(in.Charging), PaymentModel: deref(in.PaymentModel), PaymentMethod: deref(in.PaymentMethod), GatewayName: deref(in.GatewayName)}
	if in.PORef != nil {
		ci.PORef = *in.PORef
	}
	if in.ExternalAccountID != nil {
		ci.ExternalAccountID = *in.ExternalAccountID
	}
	if in.PaymentTermsDays != nil {
		if *in.PaymentTermsDays < 0 || *in.PaymentTermsDays > store.MaxPaymentTermsDays {
			writeErr(w, http.StatusBadRequest, paymentTermsHelp)
			return
		}
		ci.PaymentTermsDays = in.PaymentTermsDays
	}
	ci.Tax = store.TaxProfile{TaxRegistrationNumber: deref(in.TaxRegistrationNumber), TaxExemptReason: deref(in.TaxExemptReason), TaxRate: in.TaxRate,
		TaxCountry: deref(in.TaxCountry), TaxRegion: deref(in.TaxRegion),
		TaxExemptionNumber: deref(in.TaxExemptionNumber), TaxExemptionExpiresOn: deref(in.TaxExemptionExpiresOn), TaxExemptionScanRef: deref(in.TaxExemptionScanRef)}
	if in.TaxExempt != nil {
		ci.Tax.TaxExempt = *in.TaxExempt
	}
	if in.TaxBusiness != nil {
		ci.Tax.TaxBusiness = *in.TaxBusiness
	}
	if in.AutoApplyCredit != nil {
		ci.AutoApplyCredit = *in.AutoApplyCredit
	}
	ci.LowBalanceThreshold = in.LowBalanceThreshold
	if in.SuspendAtZero != nil {
		ci.SuspendAtZero = *in.SuspendAtZero
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

// ownerPatchFields are the customer fields an owner may set on its own
// customer (DESIGN.md §10, customer.self.manage): its purchase-order
// reference and tax registration number. Everything else — the commercial
// model, the plan, the status, the admin email — stays with customers.manage.
var ownerPatchFields = map[string]bool{"po_reference": true, "tax_registration_number": true}

// ownerPatchRefused names the first field an owner's patch touches outside
// ownerPatchFields, or "" when the patch is within them.
func ownerPatchRefused(in customerBody) string {
	for _, f := range patchedFields(in) {
		if !ownerPatchFields[f] {
			return f
		}
	}
	if in.PriceBookID != nil {
		return "price_book_id"
	}
	if in.BillingMode != nil {
		return "billing_mode"
	}
	if in.PartnerID != nil {
		return "partner_id"
	}
	return ""
}

func (h *Handler) patchCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// customers.manage edits everything; an owner (customer.self.manage on
	// its own customer) may set its PO reference and tax registration only.
	s, ok := h.requireAnyPermission(w, r, id, access.CustomersManage, access.CustomerSelfManage)
	if !ok {
		return
	}
	var in customerBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if !access.Has(access.Bindings(s), access.CustomersManage, id) {
		if f := ownerPatchRefused(in); f != "" {
			writeErr(w, http.StatusForbidden, "a customer owner may change po_reference and tax_registration_number only; "+f+" needs permission customers.manage")
			return
		}
	}
	if h.commercialWriteRefused(w, r, in) {
		return
	}
	p := commercialFrom(in)
	p.OrgSlug = in.OrgSlug
	p.ExternalAccountID = in.ExternalAccountID
	if in.PriceBookID != nil {
		slog.Info("customer patch: price_book_id is deprecated and ignored; assign the book on the customer's sources", "customer", id)
	}
	if in.BillingMode != nil {
		slog.Info("customer patch: billing_mode is deprecated and ignored; set charging, payment_model and payment_method (DESIGN.md §8)", "customer", id)
	}
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
	if in.PlanSlug != nil {
		if !validPlanSlug(*in.PlanSlug) {
			writeErr(w, http.StatusBadRequest, planSlugHelp)
			return
		}
		cur, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, id)
		if err != nil {
			storeErr(w, err)
			return
		}
		if cur.Kind == "organization" {
			writeErr(w, http.StatusBadRequest, "plan_slug of an Organization customer is read from its Organization CR (spec.planSlug); change the plan there")
			return
		}
		p.PlanSlug = in.PlanSlug
	}
	p.PORef = in.PORef
	if in.PaymentTermsDays != nil {
		if *in.PaymentTermsDays < 0 || *in.PaymentTermsDays > store.MaxPaymentTermsDays {
			writeErr(w, http.StatusBadRequest, paymentTermsHelp)
			return
		}
		p.PaymentTermsDays = in.PaymentTermsDays
	}
	p.TaxRegistrationNumber, p.TaxExempt, p.TaxExemptReason, p.TaxRate = in.TaxRegistrationNumber, in.TaxExempt, in.TaxExemptReason, in.TaxRate
	p.TaxCountry, p.TaxRegion, p.TaxBusiness = in.TaxCountry, in.TaxRegion, in.TaxBusiness
	p.TaxExemptionNumber, p.TaxExemptionExpiresOn, p.TaxExemptionScanRef = in.TaxExemptionNumber, in.TaxExemptionExpiresOn, in.TaxExemptionScanRef
	p.AutoApplyCredit, p.LowBalanceThreshold, p.SuspendAtZero = in.AutoApplyCredit, in.LowBalanceThreshold, in.SuspendAtZero
	// Assigning a customer to a partner is a partner decision (DESIGN.md
	// §13): partners.manage, never customers.manage alone.
	if in.PartnerID != nil {
		if !access.Has(access.Bindings(s), access.PartnersManage, "") {
			writeErr(w, http.StatusForbidden, "permission partners.manage required at the Sovereign to assign a customer to a partner")
			return
		}
		if *in.PartnerID != "" {
			if _, err := h.Store.GetPartner(r.Context(), *in.PartnerID); err != nil {
				writeErr(w, http.StatusBadRequest, "partner_id does not exist")
				return
			}
		}
		p.PartnerID = in.PartnerID
	}
	before, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	c, err := h.Store.UpdateCustomer(r.Context(), id, p)
	if err != nil {
		storeErr(w, err)
		return
	}
	// The customer's list book may now (or no longer) feed a partner's
	// derived retail book, so both partners are re-derived.
	if in.PartnerID != nil {
		for _, pid := range []*string{before.PartnerID, c.PartnerID} {
			if pid == nil {
				continue
			}
			if _, err := rating.DeriveRetailBooks(r.Context(), h.Store, *pid); err != nil {
				storeErr(w, err)
				return
			}
		}
	}
	h.audit(r, &c.ID, "customer.update", map[string]any{"fields": patchedFields(in), "partner_id": c.PartnerID})
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
	if in.Charging != nil {
		f = append(f, "charging")
	}
	if in.PaymentModel != nil {
		f = append(f, "payment_model")
	}
	if in.PaymentMethod != nil {
		f = append(f, "payment_method")
	}
	if in.GatewayName != nil {
		f = append(f, "gateway_name")
	}
	if in.ExternalAccountID != nil {
		f = append(f, "external_account_id")
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
	if in.PlanSlug != nil {
		f = append(f, "plan_slug")
	}
	if in.PORef != nil {
		f = append(f, "po_reference")
	}
	if in.PaymentTermsDays != nil {
		f = append(f, "payment_terms_days")
	}
	if in.TaxRegistrationNumber != nil {
		f = append(f, "tax_registration_number")
	}
	if in.TaxExempt != nil {
		f = append(f, "tax_exempt")
	}
	if in.TaxExemptReason != nil {
		f = append(f, "tax_exempt_reason")
	}
	if in.TaxRate != nil {
		f = append(f, "tax_rate")
	}
	for _, pair := range []struct {
		name string
		set  bool
	}{
		{"tax_country", in.TaxCountry != nil},
		{"tax_region", in.TaxRegion != nil},
		{"tax_business", in.TaxBusiness != nil},
		{"tax_exemption_number", in.TaxExemptionNumber != nil},
		{"tax_exemption_expires_on", in.TaxExemptionExpiresOn != nil},
		{"tax_exemption_scan_ref", in.TaxExemptionScanRef != nil},
	} {
		if pair.set {
			f = append(f, pair.name)
		}
	}
	if in.AutoApplyCredit != nil {
		f = append(f, "auto_apply_credit")
	}
	if in.LowBalanceThreshold != nil {
		f = append(f, "low_balance_threshold")
	}
	if in.SuspendAtZero != nil {
		f = append(f, "suspend_at_zero")
	}
	if in.PartnerID != nil {
		f = append(f, "partner_id")
	}
	return f
}

// inviteCustomer mints an activation link and mails it to the admin.
func (h *Handler) inviteCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireSovereign(w, r, access.CustomersManage); !ok {
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
	if err := decode(r, &in); err != nil || !validEmail(in.Email) {
		writeErr(w, http.StatusBadRequest, "email and role (customer-owner | customer-billing | customer-viewer; admin | viewer accepted) are required")
		return
	}
	// The role is a customer role (DESIGN.md §10); the legacy admin | viewer
	// pair is still accepted and means owner | viewer.
	bound, okRole := store.CustomerRoleFromLegacy(in.Role)
	if !okRole {
		writeErr(w, http.StatusBadRequest, "role must be customer-owner, customer-billing or customer-viewer (admin | viewer accepted)")
		return
	}
	if _, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, id); err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.UpsertCustomerUser(r.Context(), id, in.Email, bound); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &id, "customer.user.add", map[string]any{"email": normEmail(in.Email), "role": store.LegacyCustomerRole(bound), "binding_role": bound})
	h.audit(r, &id, "access.binding", map[string]any{"op": "grant", "subject_email": normEmail(in.Email), "role": bound, "scope_kind": store.ScopeKindCustomer, "customer_id": id})
	writeJSON(w, http.StatusCreated, store.CustomerUser{CustomerID: id, Email: normEmail(in.Email), Role: store.LegacyCustomerRole(bound), BindingRole: bound})
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
	h.audit(r, &id, "access.binding", map[string]any{"op": "revoke", "subject_email": email, "scope_kind": store.ScopeKindCustomer, "customer_id": id})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// customerAudit — GET /customers/{id}/audit. audit.read is a Sovereign
// permission (DESIGN.md §10): a customer principal on the customer is
// answered 403, one that is not 404.
func (h *Handler) customerAudit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requirePermission(w, r, access.AuditRead, id)
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

// activateIfPending flips a pending customer to active after a source
// verification succeeded outside the invite flow: at that moment the intent
// to start collecting is unambiguous, and leaving the customer pending would
// silently keep the collector away from its verified sources. Invite
// activation keeps its own explicit rule (every project must verify).
func (h *Handler) activateIfPending(r *http.Request, customerID string) {
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, customerID)
	if err != nil || c.Status != "pending" {
		return
	}
	if err := h.Store.SetCustomerStatus(r.Context(), customerID, "active"); err != nil {
		slog.Error("activate customer after source verification", "customer", customerID, "error", err)
		return
	}
	h.audit(r, &customerID, "customer.activate", map[string]any{"via": "source verification"})
}

// deleteCustomer removes a customer and everything hanging off it (sources,
// credentials, usage, drafts) through the FK cascades. Refused with 409
// while an issued statement exists — that bill is a permanent record.
func (h *Handler) deleteCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireSovereign(w, r, access.CustomersManage); !ok {
		return
	}
	c, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteCustomer(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &id, "customer.delete", map[string]any{"slug": c.Slug, "name": c.Name})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id, "slug": c.Slug})
}
