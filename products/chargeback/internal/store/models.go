package store

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Decimal is an exact numeric value carried as its Postgres text form and
// emitted as a JSON number. Money and quantities never pass through float64
// between the database and the API.
type Decimal string

var decimalShape = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// MarshalJSON emits the value as a bare JSON number ("0" when empty).
func (d Decimal) MarshalJSON() ([]byte, error) {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return []byte("0"), nil
	}
	if !decimalShape.MatchString(s) {
		return nil, errors.New("store: decimal is not numeric: " + s)
	}
	return []byte(s), nil
}

// UnmarshalJSON accepts a JSON number or a numeric string.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*d = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.TrimSpace(str)
	}
	if s == "" {
		*d = ""
		return nil
	}
	if !decimalShape.MatchString(s) {
		return errors.New("store: decimal is not numeric: " + s)
	}
	*d = Decimal(s)
	return nil
}

// Source kinds and the layer each belongs to (DESIGN.md §2). A source is
// either a CLOUD source — a cloud project whose resource kinds are cloud
// SKUs, priced by a cloud book — or a PLATFORM source — an Organization on
// this Sovereign whose resource kinds are platform SKUs, priced by a platform
// book. The two layers never meet in billing: a book is assigned per source
// and its scope must equal the source's layer.
const (
	SourceKindHuaweiProject = "huawei-project"
	SourceKindFile          = "file"
	SourceKindOrg           = "openova-org"
	// SourceKindPlatform is the ONE internal source the Sovereign's own
	// platform footprint is recorded on (customer_id NULL, internal = true).
	// It is never billed and only Allocation reads it.
	SourceKindPlatform  = "openova-platform"
	SourceKindNamespace = "k8s-namespace"

	LayerCloud    = "cloud"
	LayerPlatform = "platform"
)

// LayerOfKind derives a source's layer from its kind — the same CASE the
// generated column cost_sources.layer stores.
func LayerOfKind(kind string) string {
	switch kind {
	case SourceKindHuaweiProject, SourceKindFile:
		return LayerCloud
	}
	return LayerPlatform
}

// ValidLayer reports whether s is cloud or platform.
func ValidLayer(s string) bool { return s == LayerCloud || s == LayerPlatform }

// CloudSourceKinds are the kinds an operator may create by hand; platform
// sources are created by the Organization sync and the platform collector.
var CloudSourceKinds = []string{SourceKindHuaweiProject, SourceKindFile}

// The platform meters the platform collector writes, one hourly record each
// (the request is the entitlement the plan quota enforces, so the request is
// what is metered). They are declared here rather than in the adapter because
// the "Organization PAYG" rate card prices them (planbook.go) and the store
// must name the same SKUs the collector emits; the adapter aliases these.
const (
	SKUVCPU  = "k8s.vcpu"
	UnitVCPU = "vcpu-hour"
	SKUMem   = "k8s.mem_gb"
	UnitMem  = "gib-hour"
	SKUPVC   = "k8s.pvc_gb"
	UnitPVC  = "gb-hour"
)

// PlatformMeterSKUs are the k8s.* meters the platform collector writes. Under
// a platform book that prices none of them they are "not sold per use" —
// the allocation basis, not unpriced revenue. Under the pay-per-use book they
// ARE the bill.
var PlatformMeterSKUs = []string{SKUVCPU, SKUMem, SKUPVC}

// IsPlatformMeter reports whether sku is one of PlatformMeterSKUs.
func IsPlatformMeter(sku string) bool {
	for _, s := range PlatformMeterSKUs {
		if s == sku {
			return true
		}
	}
	return false
}

// Customer is a buyer: an external account or a synced Organization. A
// customer owns one or more sources; the price book is assigned per SOURCE.
type Customer struct {
	ID         string  `json:"id"`
	Slug       string  `json:"slug"`
	Name       string  `json:"name"`
	AdminEmail string  `json:"admin_email"`
	Kind       string  `json:"kind"`
	OrgSlug    *string `json:"org_slug,omitempty"`
	// PriceBookID is DEPRECATED (DESIGN.md §4.1): the book is assigned per
	// source since the two-layer migration. The column is read for
	// compatibility and never written by the API or the Organization sync.
	PriceBookID *string `json:"price_book_id,omitempty"`
	// BillingMode is DEPRECATED (DESIGN.md §8): showback / chargeback / real
	// were three labels standing in for three different questions. It is
	// DERIVED from Charging + PaymentMethod on every write and never set
	// directly, and it is kept on the wire only so a reader written against
	// it keeps working.
	BillingMode string  `json:"billing_mode"`
	Status      string  `json:"status"`
	StartDate   *string `json:"start_date,omitempty"`
	// PlanSlug is the catalog plan the customer pays for (s, m, l, xl,
	// flexi; "" = no plan). For an Organization customer OrgSync reads it
	// from the Organization CR's spec.planSlug; the platform collector
	// meters it as plan.<slug> (DESIGN.md §2.8 "Plan revenue").
	PlanSlug string `json:"plan_slug"`
	// The commercial model (DESIGN.md §8) — four orthogonal fields that
	// replaced billing_mode. Charging says whether anything is collected;
	// the other three are meaningful only when it is billed.
	Charging string `json:"charging"`
	// PaymentModel is prepaid (paid ahead, or a balance debited on issue) or
	// postpaid (invoiced after the period, paid on terms).
	PaymentModel string `json:"payment_model,omitempty"`
	// PaymentMethod is gateway (a pluggable gateway collects), transfer
	// (bank transfer against the invoice, recorded by the operator) or
	// internal (a cost-centre recharge, no external money).
	PaymentMethod string `json:"payment_method,omitempty"`
	// GatewayName selects the gateway implementation when PaymentMethod is
	// gateway: "stripe" today, another name when one is registered.
	GatewayName string `json:"gateway_name,omitempty"`
	// PORef is the customer's standing purchase-order reference, copied onto
	// each statement at issue and quotable on the invoice.
	PORef string `json:"po_reference,omitempty"`
	// PaymentTermsDays is the net terms an invoice for this customer falls
	// due in (default 30), overridable per statement before it is issued.
	PaymentTermsDays int `json:"payment_terms_days"`
	// ExternalAccountID is this customer's account in the operator's own
	// billing system (a TMF666 billing account id). Used only when the
	// Sovereign's commercial provider is external (DESIGN.md §8.10).
	ExternalAccountID string `json:"external_account_id,omitempty"`

	// The tax profile (DESIGN.md §9.4): the customer's registration number,
	// an exemption with its reason, and an optional rate overriding the
	// Sovereign default (nil = the default). An issued invoice snapshots
	// these; changing them afterwards changes the NEXT invoice only.
	TaxRegistrationNumber string   `json:"tax_registration_number,omitempty"`
	TaxExempt             bool     `json:"tax_exempt"`
	TaxExemptReason       string   `json:"tax_exempt_reason,omitempty"`
	TaxRate               *Decimal `json:"tax_rate,omitempty"`
	// Account credit (DESIGN.md §9.5). AutoApplyCredit applies available
	// credit to every invoice at issue; LowBalanceThreshold and
	// SuspendAtZero are what payment_model = prepaid adds: an alert when the
	// balance falls below the threshold (nil = off) and a platform
	// suspension when it reaches zero.
	AutoApplyCredit     bool     `json:"auto_apply_credit"`
	LowBalanceThreshold *Decimal `json:"low_balance_threshold,omitempty"`
	SuspendAtZero       bool     `json:"suspend_at_zero"`
	// PlatformSuspendedAt is set while this product has the Organization
	// suspended at the platform (DESIGN.md §9.7), with why and by which
	// path — collections, wallet, operator, or an imported command.
	PlatformSuspendedAt *time.Time `json:"platform_suspended_at,omitempty"`
	SuspensionReason    string     `json:"suspension_reason,omitempty"`
	SuspensionSource    string     `json:"suspension_source,omitempty"`
	// ExternalBalance is the balance the operator's billing system last
	// reported (external mode); ours is never authoritative there.
	ExternalBalance   *Decimal   `json:"external_balance,omitempty"`
	ExternalBalanceAt *time.Time `json:"external_balance_at,omitempty"`
	// Balance is the customer's account balance from the ledger (DESIGN.md
	// §9.8): positive is owed, negative is credit; AvailableCredit is what
	// the customer could still apply. Both are sums at read time, never
	// stored, read-only on the wire.
	Balance         Decimal   `json:"balance"`
	AvailableCredit Decimal   `json:"available_credit"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`

	// List-view aggregates.
	SourceCount         int `json:"source_count"`
	VerifiedSourceCount int `json:"verified_source_count"`
	// Sources per layer — what the customer list shows in its Sources column
	// ("1 cloud · 1 platform").
	CloudSourceCount    int        `json:"cloud_source_count"`
	PlatformSourceCount int        `json:"platform_source_count"`
	LastCollectedAt     *time.Time `json:"last_collected_at,omitempty"`
	LastStatementPeriod *string    `json:"last_statement_period,omitempty"`

	// Collecting reports whether the collector picks this customer up at
	// all: the customer is active AND at least one source is verified. It
	// exists so the UI can say why nothing flows for a pending customer.
	Collecting bool `json:"collecting"`

	// PartyKind says what this row IS (DESIGN.md §13): a customer, or
	// the account (party) of a partner — the row that gives a partner a
	// balance, payments, invoices and collections through the one ledger.
	// A party row is never listed in the customer directory and is never
	// rated by the customer pass of a run.
	PartyKind string `json:"party_kind"`
	// PartnerID assigns this customer to a partner (nil = direct). One
	// partner per customer; a party row never carries one.
	PartnerID   *string `json:"partner_id,omitempty"`
	PartnerName string  `json:"partner_name,omitempty"`
}

// Party kinds of a customers row.
const (
	PartyKindCustomer = "customer"
	PartyKindPartner  = "partner"
)

// CustomerUser grants an email a role on a customer. It is the customer-
// scoped view of a RoleBinding: Role is the LEGACY name an older reader
// expects (admin = customer-owner, viewer = every other customer role) and
// BindingRole the role actually bound (DESIGN.md §10).
type CustomerUser struct {
	CustomerID  string `json:"customer_id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	BindingRole string `json:"binding_role,omitempty"`
}

// CostSource is one metered origin of usage: a cloud project (layer cloud)
// or an Organization on this Sovereign (layer platform). The price book that
// rates its usage is assigned HERE, per source, and must have the matching
// scope. CustomerID is empty only for the internal platform source.
type CostSource struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	// CustomerName is joined for operator-wide listings (empty on the
	// customer-scoped list, where it is redundant).
	CustomerName string `json:"customer_name,omitempty"`
	Kind         string `json:"kind"`
	// Layer is cloud or platform, derived from Kind (LayerOfKind).
	Layer string `json:"layer"`
	// PriceBookID is the book that rates this source's usage; nil = none
	// (its SKUs are unpriced). PriceBookName is joined for display.
	PriceBookID   *string `json:"price_book_id"`
	PriceBookName string  `json:"price_book_name,omitempty"`
	// Internal marks the Sovereign's own platform source (SourceKindPlatform):
	// no customer, never billed, excluded from every customer-facing query,
	// read by Allocation as the platform-overhead row.
	Internal        bool       `json:"internal"`
	Region          string     `json:"region"`
	ProjectID       string     `json:"project_id"`
	DomainID        *string    `json:"domain_id,omitempty"`
	CredentialID    *string    `json:"credential_id,omitempty"`
	Status          string     `json:"status"`
	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
	LastCollectedAt *time.Time `json:"last_collected_at,omitempty"`
	LastError       *string    `json:"last_error,omitempty"`
	// AccessKey is the (non-secret) key id of the linked credential, for display.
	AccessKey string `json:"access_key,omitempty"`
	// ScopeToken narrows a project-scoped source to ONE deployment's
	// resources (#6855). A Huawei project can hold shared infrastructure and
	// more than one Sovereign, and without this the customer is billed for
	// all of it — measured on hw307, `bastion-openova` was on the statement.
	// Empty = no filtering, which is the pre-#6855 behaviour: billing too much
	// is a bug, but silently dropping a customer's own resources because a
	// scope was never configured would be a worse one.
	ScopeToken string `json:"scope_token,omitempty"`
}

// Credential is the API view of a stored AK/SK: the secret never leaves the
// store in this shape (it has no field for it).
type Credential struct {
	ID         string     `json:"id"`
	CustomerID string     `json:"customer_id"`
	Kind       string     `json:"kind"`
	AccessKey  string     `json:"access_key"`
	CreatedAt  time.Time  `json:"created_at"`
	RotatedAt  *time.Time `json:"rotated_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// InventoryItem is one observed cloud resource.
type InventoryItem struct {
	SourceID   string          `json:"source_id"`
	ResourceID string          `json:"resource_id"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Attrs      json.RawMessage `json:"attrs"`
	FirstSeen  time.Time       `json:"first_seen"`
	LastSeen   time.Time       `json:"last_seen"`
	DeletedAt  *time.Time      `json:"deleted_at,omitempty"`
}

// UsageRecord is one fact in the ledger.
type UsageRecord struct {
	ID           int64           `json:"id,omitempty"`
	CustomerID   string          `json:"customer_id"`
	SourceID     string          `json:"source_id"`
	ResourceID   string          `json:"resource_id"`
	ResourceKind string          `json:"resource_kind"`
	SKU          string          `json:"sku"`
	Quantity     Decimal         `json:"quantity"`
	Unit         string          `json:"unit"`
	WindowStart  time.Time       `json:"window_start"`
	WindowEnd    time.Time       `json:"window_end"`
	Region       string          `json:"region"`
	Labels       json.RawMessage `json:"labels,omitempty"`
	RawRef       string          `json:"raw_ref,omitempty"`
	CollectedAt  time.Time       `json:"collected_at,omitempty"`
}

// UsageRow is one aggregated usage line.
type UsageRow struct {
	Key           string  `json:"key"`
	SKU           string  `json:"sku"`
	Unit          string  `json:"unit"`
	Quantity      Decimal `json:"quantity"`
	ResourceCount int     `json:"resource_count"`
	ResourceKind  string  `json:"resource_kind,omitempty"`
	ResourceName  string  `json:"resource_name,omitempty"`
}

// PriceBook is a rate card. Scope says which layer of source it may be
// assigned to: a cloud book prices cloud SKUs, a platform book prices
// platform SKUs (plan.<slug>, and k8s.* only if sold per use).
type PriceBook struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Scope         string  `json:"scope"`
	Currency      string  `json:"currency"`
	AnnualDivisor int     `json:"annual_divisor"`
	BillStopped   string  `json:"bill_stopped"`
	EffectiveFrom *string `json:"effective_from,omitempty"`
	// Description is the operator-editable note on the book itself: what it
	// is for and, for the two books the Organization sync owns, where every
	// rate in it came from. Before it existed the only place to write that
	// was an item description, which cannot explain a book as a whole.
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// Public marks the ONE cloud book the unauthenticated calculator prices
	// from (DESIGN.md §12); UpdatedAt moves with the header and the items,
	// and dates the public catalog.
	Public    bool        `json:"public"`
	UpdatedAt time.Time   `json:"updated_at"`
	Items     []PriceItem `json:"items,omitempty"`
	// A partner's DERIVED retail book (DESIGN.md §13): owned by the
	// partner, materialised from one list book by its retail rule, read-only
	// in the editor and re-derived on every list, tier or rule change.
	PartnerID         *string `json:"partner_id,omitempty"`
	DerivedFromRule   bool    `json:"derived_from_rule"`
	DerivedFromBookID *string `json:"derived_from_book_id,omitempty"`
}

// PriceItem prices one SKU. UnitPrice is derived from AnnualPrice and the
// book's divisor when the item came from an annual list price.
type PriceItem struct {
	PriceBookID string   `json:"price_book_id,omitempty"`
	SKU         string   `json:"sku"`
	Unit        string   `json:"unit"`
	UnitPrice   Decimal  `json:"unit_price"`
	AnnualPrice *Decimal `json:"annual_price,omitempty"`
	Description string   `json:"description,omitempty"`
}

// Discount reduces a customer's rated total (#6862).
//
// Two shapes cover the commercial cases the founder named — a negotiated
// percentage off, and a time-boxed campaign:
//
//	kind=percent  value=15      -> 15% off the matching lines
//	kind=fixed    value=500     -> 500 off the matching subtotal, never below 0
//
// Scope narrows what it applies to. An empty SKU means the whole bill; a set
// SKU means only that meter (e.g. compute discounted, storage not).
// StartsAt/EndsAt bound a campaign; both nil means always active.
//
// A discount is NOT a price change: the price book stays the list price, so a
// statement can show what the customer would have paid and what they saved.
// Overwriting the rate would destroy that, and destroy the audit trail with it.
//
// CustomerID nil is a GLOBAL campaign (#6867): it applies to every customer.
// The JSON key is always present (null for global) so a reader can tell "all
// customers" from "field missing".
type Discount struct {
	ID         string  `json:"id"`
	CustomerID *string `json:"customer_id"`
	// CustomerName is the joined display name; nil for a global campaign.
	CustomerName *string    `json:"customer_name,omitempty"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind"` // percent | fixed
	Value        Decimal    `json:"value"`
	SKU          string     `json:"sku,omitempty"`
	StartsAt     *time.Time `json:"starts_at,omitempty"`
	EndsAt       *time.Time `json:"ends_at,omitempty"`
	Active       bool       `json:"active"`
	// Stackable (DESIGN.md §2.11): under the most-specific and highest
	// combination rules this discount is added on top of the winning
	// percent instead of competing with it. No effect under stack/compound.
	Stackable bool      `json:"stackable"`
	CreatedAt time.Time `json:"created_at"`
	// TierID makes this a PARTNER TIER discount (DESIGN.md §13): a
	// percent off list that sets the partner buy price, decided by the same
	// combination engine as every other discount. Never on a customer's
	// list; never on a customer's bill.
	TierID   *string `json:"tier_id,omitempty"`
	TierName string  `json:"tier_name,omitempty"`
}

// AppliesAt reports whether the discount is live at t. A campaign that has not
// started, or has ended, must not silently keep discounting.
func (d Discount) AppliesAt(t time.Time) bool {
	if !d.Active {
		return false
	}
	if d.StartsAt != nil && t.Before(*d.StartsAt) {
		return false
	}
	if d.EndsAt != nil && !t.Before(*d.EndsAt) {
		return false
	}
	return true
}

// AppliesToSKU reports whether the discount covers a given meter. An empty
// SKU on the discount means "the whole bill".
func (d Discount) AppliesToSKU(sku string) bool {
	return d.SKU == "" || d.SKU == sku
}

// Statement is one customer's rated period.
type Statement struct {
	ID           string      `json:"id"`
	CustomerID   string      `json:"customer_id"`
	PeriodStart  string      `json:"period_start"`
	PeriodEnd    string      `json:"period_end"`
	Currency     string      `json:"currency"`
	Subtotal     Decimal     `json:"subtotal"`
	TaxRate      Decimal     `json:"tax_rate"`
	Tax          Decimal     `json:"tax"`
	Total        Decimal     `json:"total"`
	Status       string      `json:"status"`
	IssuedAt     *time.Time  `json:"issued_at,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	Lines        []RatedLine `json:"lines,omitempty"`
	CustomerName string      `json:"customer_name,omitempty"`
	// #6862/#6867 — what discounts took off the list subtotal, frozen with the
	// statement. Subtotal is the NET; list = Subtotal + DiscountTotal.
	DiscountTotal  Decimal         `json:"discount_total"`
	DiscountDetail json.RawMessage `json:"discount_detail,omitempty"`
	// DiscountRule (DESIGN.md §2.11) names the combination rule in force
	// when the statement was rated, so an issued bill states which rule
	// produced its numbers. Empty for statements rated before the rule
	// existed that carried no discount.
	DiscountRule string `json:"discount_rule,omitempty"`

	// Post-paid invoicing (DESIGN.md §8). Every key is additive and absent
	// on a draft that has never been issued, so a reader written against the
	// pre-invoicing document keeps working unchanged.
	//
	// InvoiceNumber is assigned inside the transaction that flips the status
	// to issued: gapless per calendar year, unique across the table. It is
	// EMPTY when the Sovereign's commercial provider is external — the
	// operator's billing system numbers its own invoices (DESIGN.md §8.10).
	InvoiceNumber string `json:"invoice_number,omitempty"`
	// ExternalInvoiceRef is what the operator's billing system knows this
	// invoice by, set at issue in external mode; the lifecycle after that is
	// driven by imports against this reference.
	ExternalInvoiceRef string `json:"external_invoice_ref,omitempty"`
	// PORef is the purchase-order reference this invoice quotes; copied from
	// the customer at issue, editable on the draft before then.
	PORef string `json:"po_reference,omitempty"`
	// PaymentTermsDays is the net terms the due date was computed from.
	PaymentTermsDays *int `json:"payment_terms_days,omitempty"`
	// DueAt is issued_at + terms.
	DueAt       *time.Time `json:"due_at,omitempty"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	PaidAt      *time.Time `json:"paid_at,omitempty"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	// CancelReason is why the statement was voided, when it was.
	CancelReason string `json:"cancel_reason,omitempty"`
	// Paid is the sum of the recorded payments and Balance is total − paid;
	// both are computed on read, never stored, so they cannot drift from the
	// payments ledger.
	Paid    Decimal `json:"paid_total,omitempty"`
	Balance Decimal `json:"balance,omitempty"`
	// Credited is what credit notes took off this invoice (DESIGN.md §9.3);
	// Balance is total − paid − credited. Computed on read like Paid.
	Credited Decimal `json:"credited_total,omitempty"`
	// TaxSnapshot is what the invoice carries about tax, frozen at issue:
	// the rate applied, the customer's registration and exemption, and the
	// seller's identity. Absent on a draft (DESIGN.md §9.4).
	TaxSnapshot *TaxSnapshot `json:"tax_snapshot,omitempty"`
	// CreditNotes are the notes issued against this invoice; present on the
	// single-statement document like Payments.
	CreditNotes []CreditNote `json:"credit_notes,omitempty"`
	// EffectiveStatus is Status, except that a sent statement past its due
	// date with money outstanding reads as "overdue". Derived from the clock
	// rather than stored, so no sweeper has to keep it true.
	EffectiveStatus string `json:"effective_status,omitempty"`
	// Payments is the ledger behind Paid; present on the single-statement
	// document, absent from list documents.
	Payments []StatementPayment `json:"payments,omitempty"`

	// The partner keys (DESIGN.md §13), all additive. PartnerID is the
	// partner of the statement's customer (a customer statement) or the
	// partner whose party this statement bills (wholesale / commission);
	// PartyKind says which; Kind is customer | wholesale | commission.
	// BuyTotal is what the partner pays us for these lines and MarginTotal
	// what the end customers pay minus that — derived, never entered, and
	// shown to Sovereign and partner roles only (RedactPartner).
	PartnerID   *string  `json:"partner_id,omitempty"`
	PartnerName string   `json:"partner_name,omitempty"`
	PartyKind   string   `json:"party_kind,omitempty"`
	Kind        string   `json:"statement_kind,omitempty"`
	BuyTotal    *Decimal `json:"buy_total,omitempty"`
	MarginTotal *Decimal `json:"margin_total,omitempty"`
}

// Statement kinds (statements.statement_kind).
const (
	StatementKindCustomer   = "customer"
	StatementKindWholesale  = "wholesale"
	StatementKindCommission = "commission"
)

// RedactPartner strips what a customer principal must not see about its
// partner: the buy figures and the margin. The partner assignment itself
// stays — it is the customer's own commercial relationship.
func (st *Statement) RedactPartner() {
	st.BuyTotal, st.MarginTotal = nil, nil
	for i := range st.Lines {
		st.Lines[i].BuyAmount = nil
		st.Lines[i].ListUnitPrice = nil
		st.Lines[i].ListAmount = nil
	}
}

// RatedLine is one priced aggregate on a statement.
type RatedLine struct {
	ID            int64   `json:"id"`
	StatementID   string  `json:"statement_id"`
	CustomerID    string  `json:"customer_id"`
	SourceID      *string `json:"source_id,omitempty"`
	SKU           string  `json:"sku"`
	Quantity      Decimal `json:"quantity"`
	Unit          string  `json:"unit"`
	UnitPrice     Decimal `json:"unit_price"`
	Amount        Decimal `json:"amount"`
	ResourceCount int     `json:"resource_count"`

	// The partner waterfall per line (DESIGN.md §13), set only on the
	// lines of a customer that has a partner and on partner statements. On
	// a customer statement: ListUnitPrice / ListAmount are the Sovereign's
	// list figures (Amount is the retail figure under resell), BuyAmount
	// what the partner pays for the line, NetAmount what the customer pays
	// after its own discounts. On a wholesale or commission statement
	// EndCustomerID names the end customer the line belongs to.
	EndCustomerID   *string  `json:"end_customer_id,omitempty"`
	EndCustomerName string   `json:"end_customer_name,omitempty"`
	ListUnitPrice   *Decimal `json:"list_unit_price,omitempty"`
	ListAmount      *Decimal `json:"list_amount,omitempty"`
	BuyAmount       *Decimal `json:"buy_amount,omitempty"`
	NetAmount       *Decimal `json:"net_amount,omitempty"`
}

// Invite is a one-time activation link.
type Invite struct {
	Token      string     `json:"token"`
	CustomerID string     `json:"customer_id"`
	Email      string     `json:"email"`
	ExpiresAt  time.Time  `json:"expires_at"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// AuditEntry records one mutation.
type AuditEntry struct {
	ID         int64           `json:"id"`
	CustomerID *string         `json:"customer_id,omitempty"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	Details    json.RawMessage `json:"details,omitempty"`
	At         time.Time       `json:"at"`
}

// Session is a signed-in principal.
//
// Role and CustomerID describe the HIGHEST-POWER binding in the legacy
// vocabulary (operator / customer-admin / customer-viewer) so a reader
// written before role bindings existed keeps working; Roles is the full
// set of bindings the principal holds (DESIGN.md §10), resolved on every
// request from role_bindings, the directory-group mappings and the
// OPERATOR_EMAILS configuration. Groups are the directory groups the SSO
// gate forwarded for this request (never persisted).
type Session struct {
	Token      string        `json:"-"`
	Email      string        `json:"email"`
	Role       string        `json:"role"`
	CustomerID *string       `json:"customer_id,omitempty"`
	Roles      []RoleBinding `json:"roles,omitempty"`
	Groups     []string      `json:"-"`
	ExpiresAt  time.Time     `json:"expires_at"`
}

// Legacy role names (the session's `role` key, kept for older readers).
const (
	RoleOperator       = "operator"
	RoleCustomerAdmin  = "customer-admin"
	RoleCustomerViewer = "customer-viewer"
)

// The six roles of the access model (DESIGN.md §10), each a fixed bundle of
// permissions bound at exactly one scope kind. RoleCustomerViewer
// ("customer-viewer") is both the legacy and the new name: its meaning did
// not change.
const (
	RoleSovereignAdmin  = "sovereign-admin"
	RoleBillingOperator = "billing-operator"
	RoleFinanceViewer   = "finance-viewer"
	RoleCustomerOwner   = "customer-owner"
	RoleCustomerBilling = "customer-billing"
	// The two partner roles (DESIGN.md §13), bound at one partner: an
	// owner reads its customers, edits its retail rule, manages its users
	// and tops up its own account; a viewer reads.
	RolePartnerOwner  = "partner-owner"
	RolePartnerViewer = "partner-viewer"
)

// Scope kinds a role is bound at.
const (
	ScopeKindSovereign = "sovereign"
	ScopeKindCustomer  = "customer"
	// ScopeKindPartner is one partner: the binding expands to the set of
	// customers assigned to it plus the partner's own party.
	ScopeKindPartner = "partner"
)

// Roles lists the eight roles in power order, highest first.
var Roles = []string{RoleSovereignAdmin, RoleBillingOperator, RoleFinanceViewer, RolePartnerOwner, RolePartnerViewer, RoleCustomerOwner, RoleCustomerBilling, RoleCustomerViewer}

// ValidRole reports whether r is one of the roles.
func ValidRole(r string) bool {
	for _, x := range Roles {
		if x == r {
			return true
		}
	}
	return false
}

// ScopeKindOfRole is the scope kind a role is bound at: the three operator
// roles at the Sovereign, the two partner roles at one partner, the three
// customer roles at one customer.
func ScopeKindOfRole(r string) string {
	switch r {
	case RoleSovereignAdmin, RoleBillingOperator, RoleFinanceViewer:
		return ScopeKindSovereign
	case RolePartnerOwner, RolePartnerViewer:
		return ScopeKindPartner
	case RoleCustomerOwner, RoleCustomerBilling, RoleCustomerViewer:
		return ScopeKindCustomer
	}
	return ""
}

// RoleBinding grants an email (or, through a GroupRoleMapping, a directory
// group) one role at one scope: the Sovereign (CustomerID nil) or one
// customer. Source says where the binding came from when it is carried on a
// session: "binding" (role_bindings), "group:<name>" (a directory group the
// gate forwarded) or "config" (OPERATOR_EMAILS).
type RoleBinding struct {
	ID           string     `json:"id,omitempty"`
	SubjectEmail string     `json:"subject_email,omitempty"`
	Role         string     `json:"role"`
	ScopeKind    string     `json:"scope_kind"`
	CustomerID   *string    `json:"customer_id,omitempty"`
	CustomerName string     `json:"customer_name,omitempty"`
	GrantedBy    string     `json:"granted_by,omitempty"`
	GrantedAt    *time.Time `json:"granted_at,omitempty"`
	Source       string     `json:"source,omitempty"`
	// PartnerID is set on a partner-scoped binding (DESIGN.md §13).
	// Customers is its expansion — the customers assigned to that partner
	// plus the partner's own party — resolved with the session and never
	// stored: it is what access.Has consults for a customer-scoped question.
	PartnerID   *string  `json:"partner_id,omitempty"`
	PartnerName string   `json:"partner_name,omitempty"`
	Customers   []string `json:"-"`
}

// GroupRoleMapping grants every member of a directory group (as forwarded
// by the SSO gate in the groups header) one role at one scope.
type GroupRoleMapping struct {
	ID           string    `json:"id,omitempty"`
	GroupName    string    `json:"group_name"`
	Role         string    `json:"role"`
	ScopeKind    string    `json:"scope_kind"`
	CustomerID   *string   `json:"customer_id,omitempty"`
	CustomerName string    `json:"customer_name,omitempty"`
	PartnerID    *string   `json:"partner_id,omitempty"`
	PartnerName  string    `json:"partner_name,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
}

// Sources a session binding may carry.
const (
	BindingSourceExplicit = "binding"
	BindingSourceConfig   = "config"
	// BindingSourceGroup is a prefix; the group name follows.
	BindingSourceGroup = "group:"
)

// Scope is the authorization boundary every query is filtered by: the operator
// sees everything, a customer principal sees only its own customer, and a
// PARTNER principal (DESIGN.md §13) sees the SET of customers assigned to
// its partner plus the partner's own party — CustomerIDs, with CustomerID the
// first of them for readers that take one.
type Scope struct {
	Operator   bool
	CustomerID string
	// CustomerIDs is the full set a multi-customer (partner) scope may read;
	// empty for the operator and for a single-customer scope, whose one id
	// is CustomerID.
	CustomerIDs []string
}

// OperatorScope sees all rows.
var OperatorScope = Scope{Operator: true}

// CustomerScope sees one customer.
func CustomerScope(id string) Scope { return Scope{CustomerID: id} }

// CustomersScope sees a set of customers (a partner principal). One id
// collapses to CustomerScope so every single-customer reader keeps working.
func CustomersScope(ids []string) Scope {
	var clean []string
	seen := map[string]bool{}
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return Scope{}
	}
	if len(clean) == 1 {
		return CustomerScope(clean[0])
	}
	return Scope{CustomerID: clean[0], CustomerIDs: clean}
}

// Allows reports whether the scope may see a row of the given customer.
func (s Scope) Allows(customerID string) bool {
	if s.Operator {
		return true
	}
	if customerID == "" {
		return false
	}
	if s.CustomerID == customerID {
		return true
	}
	for _, id := range s.CustomerIDs {
		if id == customerID {
			return true
		}
	}
	return false
}

// Set is every customer id a non-operator scope may read (nil for the
// operator, whose reads are not filtered). Readers that filter by
// `customer_id = ANY($n)` take this.
func (s Scope) Set() []string {
	if s.Operator {
		return nil
	}
	if len(s.CustomerIDs) > 0 {
		return s.CustomerIDs
	}
	if s.CustomerID != "" {
		return []string{s.CustomerID}
	}
	return []string{}
}

// Multi reports whether the scope spans more than one customer.
func (s Scope) Multi() bool { return !s.Operator && len(s.CustomerIDs) > 1 }

// Confine is THE customer predicate of this application: the set of customer
// ids a read may touch, given the scope and the one customer the caller asked
// for. Every scoped query filters on `customer_id = ANY(<this>)` and nothing
// else, so a customer principal is the one-element case of what a partner
// principal does with several — one predicate, not two code paths.
//
//	operator, no customer asked for → nil, no predicate at all
//	operator, one customer asked for → that one
//	customer principal → its own customer
//	partner principal → its partner's customers and its party
//	either of those naming ONE of their own → that one alone
//
// A customer OUTSIDE the scope, and a non-operator scope that expands to
// nothing, are ErrNotFound. nil therefore means "the operator, unfiltered"
// and can never mean "everything, for someone who may not see everything".
func (s Scope) Confine(customerID string) ([]string, error) {
	if s.Operator {
		if customerID == "" {
			return nil, nil
		}
		return []string{customerID}, nil
	}
	ids := s.Set()
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	if customerID != "" {
		if !s.Allows(customerID) {
			return nil, ErrNotFound
		}
		return []string{customerID}, nil
	}
	return ids, nil
}

// ErrNotFound is returned for absent rows and for rows outside the caller's scope.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique-constraint or state conflicts.
var ErrConflict = errors.New("conflict")

// IsConflict reports whether err is (or wraps) ErrConflict.
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }
