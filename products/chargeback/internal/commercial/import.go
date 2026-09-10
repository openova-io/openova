package commercial

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The import side of the external system of record: in external mode the
// lifecycle after issue is driven ONLY by what the operator's billing system
// tells us, and this is how it tells us.
//
// Four documents (DESIGN.md §9.1), one webhook each under
// POST /commercial/import/…, all authenticated by an HMAC over the raw
// body because the caller is a machine in the operator's estate rather
// than a signed-in user — and one directory poller for a system that
// batches instead of calling, applying the same commands through the same
// Importer:
//
//	invoice-status    TMF678 — what the invoice is now, how much was paid
//	payment-status    TMF676 — a payment against the account or an invoice
//	account-balance   TMF666 — the balance as the billing system holds it
//	enforcement       suspend / resume the Organization: EXPLICIT, never
//	                  inferred from a payment status (DESIGN.md §9.7)

// SignatureHeader is the header the sender puts the HMAC in.
const SignatureHeader = "X-Signature"

// ErrUnsigned is returned when the request carries no signature.
var ErrUnsigned = errors.New("the import must be signed")

// ErrBadSignature is returned when the signature does not match the body.
var ErrBadSignature = errors.New("the import signature does not match the body")

// ErrImportNotConfigured is returned when no shared secret is set: an
// unauthenticated write into the billing ledger is never the fallback.
var ErrImportNotConfigured = errors.New("invoice-status import is not configured")

// Sign returns the value SignatureHeader should carry for a body: the
// lower-case hex HMAC-SHA256 of the raw bytes, prefixed `sha256=`. Exported
// so an operator's sender — and this product's own tests — compute it the
// same way.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// VerifySignature checks the header against the raw body in constant time.
func VerifySignature(secret, header string, body []byte) error {
	if strings.TrimSpace(secret) == "" {
		return ErrImportNotConfigured
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return ErrUnsigned
	}
	want := Sign(secret, body)
	if !hmac.Equal([]byte(want), []byte(header)) {
		return ErrBadSignature
	}
	return nil
}

// InvoiceStatusImport is the body of POST /commercial/import/invoice-status.
//
//	{
//	  "external_ref": "2026-08-omantel-corp-1a2b3c4d",
//	  "state":        "paid",
//	  "paid_amount":  1200.000000,
//	  "paid_at":      "2026-09-19",
//	  "reference":    "BANK-88213"
//	}
//
// `state` takes the billing system's own vocabulary and is mapped below;
// `paid_amount` is CUMULATIVE, so a repeated import is harmless. In
// summary-charge mode `invoice_number` may name our invoice instead of the
// external reference.
type InvoiceStatusImport struct {
	ExternalRef   string        `json:"external_ref"`
	InvoiceNumber string        `json:"invoice_number,omitempty"`
	State         string        `json:"state"`
	PaidAmount    store.Decimal `json:"paid_amount"`
	PaidAt        string        `json:"paid_at"`
	Reference     string        `json:"reference"`
}

// stateMap translates the vocabularies a billing system is likely to use
// into the three statuses an import may set. TMF678's own bill states are
// `new`, `onHold`, `validated`, `sent`, `settled`, `partiallyPaid`; the rest
// are the words operators use in practice.
var stateMap = map[string]string{
	"sent":          store.StatusSent,
	"validated":     store.StatusSent,
	"issued":        store.StatusSent,
	"open":          store.StatusSent,
	"outstanding":   store.StatusSent,
	"overdue":       store.StatusSent,
	"partiallypaid": store.StatusSent,
	"partial":       store.StatusSent,
	"paid":          store.StatusPaid,
	"settled":       store.StatusPaid,
	"closed":        store.StatusPaid,
	"cancelled":     store.StatusCancelled,
	"canceled":      store.StatusCancelled,
	"void":          store.StatusCancelled,
	"voided":        store.StatusCancelled,
	"written-off":   store.StatusCancelled,
}

// MapState translates one external state. An empty state is "no change".
// `overdue` deliberately maps to `sent`: overdue is derived here from the due
// date and the outstanding balance, so accepting it as a stored status would
// put two sources of truth on the same fact.
func MapState(state string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(state))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return "", nil
	}
	if mapped, ok := stateMap[s]; ok {
		return mapped, nil
	}
	return "", fmt.Errorf("%w: unknown invoice state %q", store.ErrInvalid, state)
}

// ParsePaidAt accepts a YYYY-MM-DD day or an RFC3339 instant; empty is now.
func ParsePaidAt(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%w: paid_at must be YYYY-MM-DD or an RFC3339 timestamp", store.ErrInvalid)
}

// Enforcer executes an imported suspend / resume; internal/collections
// implements it over the platform seam. Kept as an interface so the import
// package does not depend on the collections package.
type Enforcer interface {
	Suspend(ctx context.Context, customerID, reason, source, actor string) (store.Suspension, error)
	Resume(ctx context.Context, customerID, reason, source, actor string) (store.Suspension, error)
}

// Importer applies imports from the operator's billing system. It is usable
// only in external mode: in internal mode this product is the system of
// record and an import would be a second writer on the same ledger.
type Importer struct {
	Store  *store.Store
	Secret string
	// Enforcer executes enforcement commands; nil refuses them with a
	// message rather than pretending.
	Enforcer Enforcer
}

func (im *Importer) external(ctx context.Context) error {
	settings, err := im.Store.GetBillingSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.ExternalCommercial() {
		return fmt.Errorf("%w: this Sovereign invoices internally; nothing is imported from a billing system", store.ErrConflict)
	}
	return nil
}

// Apply verifies nothing (the caller does that with VerifySignature over the
// raw body) and applies one already-decoded invoice-status import.
func (im *Importer) Apply(ctx context.Context, in InvoiceStatusImport, actor string) (store.Statement, error) {
	if strings.TrimSpace(in.ExternalRef) == "" && strings.TrimSpace(in.InvoiceNumber) == "" {
		return store.Statement{}, fmt.Errorf("%w: external_ref (or invoice_number) is required", store.ErrInvalid)
	}
	if err := im.external(ctx); err != nil {
		return store.Statement{}, err
	}
	state, err := MapState(in.State)
	if err != nil {
		return store.Statement{}, err
	}
	paidAt, err := ParsePaidAt(in.PaidAt)
	if err != nil {
		return store.Statement{}, err
	}
	ref := strings.TrimSpace(in.ExternalRef)
	if ref == "" {
		// Summary-charge mode: the billing system quotes OUR number.
		st, err := im.Store.GetStatementByInvoiceNumber(ctx, in.InvoiceNumber)
		if err != nil {
			return store.Statement{}, err
		}
		if st.ExternalInvoiceRef == "" {
			if err := im.Store.SetStatementExternalRef(ctx, st.ID, in.InvoiceNumber); err != nil {
				return store.Statement{}, err
			}
			ref = in.InvoiceNumber
		} else {
			ref = st.ExternalInvoiceRef
		}
	}
	return im.Store.ApplyExternalInvoiceStatus(ctx, store.ExternalInvoiceStatus{
		Ref: ref, Status: state, PaidAmount: in.PaidAmount,
		PaidAt: paidAt, Reference: in.Reference, Actor: actor,
	})
}

// resolveCustomer finds the customer an import names, by billing-account id
// first, then by our own slug.
func (im *Importer) resolveCustomer(ctx context.Context, externalAccountID, slug string) (store.Customer, error) {
	if strings.TrimSpace(externalAccountID) != "" {
		c, err := im.Store.GetCustomerByExternalAccount(ctx, externalAccountID)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return store.Customer{}, err
		}
	}
	if strings.TrimSpace(slug) != "" {
		return im.Store.GetCustomerBySlug(ctx, strings.ToLower(strings.TrimSpace(slug)))
	}
	return store.Customer{}, fmt.Errorf("%w: no customer carries external_account_id %q", store.ErrNotFound, externalAccountID)
}

// ApplyPaymentStatus books a payment the billing system reports (TMF676):
// against the exported invoice it names, or as credit on the account. The
// reference is unique per customer, so a repeated report books nothing
// twice; a `refunded` status refunds the payment with that reference.
func (im *Importer) ApplyPaymentStatus(ctx context.Context, in external.PaymentStatus, actor string) (store.Payment, error) {
	if err := im.external(ctx); err != nil {
		return store.Payment{}, err
	}
	c, err := im.resolveCustomer(ctx, in.ExternalAccountID, in.CustomerSlug)
	if err != nil {
		return store.Payment{}, err
	}
	status, err := external.MapPaymentStatus(in.Status)
	if err != nil {
		return store.Payment{}, err
	}
	if strings.TrimSpace(in.Reference) == "" {
		return store.Payment{}, fmt.Errorf("%w: a payment report carries the reference that proves it", store.ErrInvalid)
	}
	paidAt, err := ParsePaidAt(in.PaidAt)
	if err != nil {
		return store.Payment{}, err
	}
	if status == store.PaymentRefunded {
		existing, err := im.Store.FindPaymentByReference(ctx, c.ID, in.Reference)
		if err != nil {
			return store.Payment{}, err
		}
		if existing.Status == store.PaymentRefunded {
			return existing, nil
		}
		return im.Store.RefundPayment(ctx, existing.ID, "refunded in the billing system", actor)
	}
	if existing, err := im.Store.FindPaymentByReference(ctx, c.ID, in.Reference); err == nil {
		// Already booked: the report is a repeat.
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Payment{}, err
	}
	var allocations []store.AllocationInput
	var stmtID string
	switch {
	case strings.TrimSpace(in.ExternalRef) != "":
		st, err := im.Store.GetStatementByExternalRef(ctx, in.ExternalRef)
		if err != nil {
			return store.Payment{}, err
		}
		stmtID = st.ID
	case strings.TrimSpace(in.InvoiceNumber) != "":
		st, err := im.Store.GetStatementByInvoiceNumber(ctx, in.InvoiceNumber)
		if err != nil {
			return store.Payment{}, err
		}
		stmtID = st.ID
	}
	if stmtID != "" && status == store.PaymentReceived {
		st, err := im.Store.GetStatement(ctx, store.OperatorScope, stmtID)
		if err != nil {
			return store.Payment{}, err
		}
		// Whatever the invoice still carries settles it; the rest is credit.
		apply := in.Amount
		if r := ratCmp(in.Amount, st.Balance); r > 0 {
			apply = st.Balance
		}
		if ratSign(apply) > 0 {
			allocations = []store.AllocationInput{{StatementID: stmtID, Amount: apply}}
		}
	}
	method := c.PaymentMethod
	if strings.TrimSpace(in.Method) != "" {
		method = strings.ToLower(strings.TrimSpace(in.Method))
	}
	if method == "" {
		method = store.PaymentMethodGateway
	}
	return im.Store.RecordCustomerPayment(ctx, store.CustomerPaymentInput{
		CustomerID: c.ID, Purpose: store.PurposeCollection, Allocations: allocations, Note: "reported by the billing system",
		Payment: store.PaymentInput{Amount: in.Amount, PaidAt: paidAt, Method: method, Reference: in.Reference, Status: status, Gateway: "external", Actor: actor},
	})
}

// ApplyAccountBalance records the balance the billing system holds for the
// account (TMF666). Ours is never authoritative in external mode; this is
// what the customer page shows.
func (im *Importer) ApplyAccountBalance(ctx context.Context, in external.AccountBalanceImport) (store.Customer, error) {
	if err := im.external(ctx); err != nil {
		return store.Customer{}, err
	}
	c, err := im.resolveCustomer(ctx, in.ExternalAccountID, in.CustomerSlug)
	if err != nil {
		return store.Customer{}, err
	}
	if _, err := in.Balance.MarshalJSON(); err != nil || strings.TrimSpace(string(in.Balance)) == "" {
		return store.Customer{}, fmt.Errorf("%w: balance must be a number", store.ErrInvalid)
	}
	at, err := ParsePaidAt(in.AsOf)
	if err != nil {
		return store.Customer{}, err
	}
	if err := im.Store.SetExternalBalance(ctx, c.ID, in.Balance, at); err != nil {
		return store.Customer{}, err
	}
	return im.Store.GetCustomer(ctx, store.OperatorScope, c.ID)
}

// ApplyEnforcement executes an EXPLICIT suspend / resume command. This is
// the only way the platform is touched in external mode: a payment status
// never implies it (DESIGN.md §9.7).
func (im *Importer) ApplyEnforcement(ctx context.Context, in external.Enforcement, actor string) (store.Suspension, error) {
	if err := im.external(ctx); err != nil {
		return store.Suspension{}, err
	}
	action, err := external.MapEnforcement(in.Action)
	if err != nil {
		return store.Suspension{}, err
	}
	c, err := im.resolveCustomer(ctx, in.ExternalAccountID, in.CustomerSlug)
	if err != nil {
		return store.Suspension{}, err
	}
	if im.Enforcer == nil {
		return store.Suspension{}, fmt.Errorf("%w: no enforcement is wired on this Sovereign", store.ErrConflict)
	}
	if action == "suspend" {
		return im.Enforcer.Suspend(ctx, c.ID, in.Reason, store.SuspendSourceImport, actor)
	}
	return im.Enforcer.Resume(ctx, c.ID, in.Reason, store.SuspendSourceImport, actor)
}

// ApplyCommand applies one polled command through the same paths the
// webhooks take (external.Applier).
func (im *Importer) ApplyCommand(ctx context.Context, cmd external.Command) error {
	actor := "billing-system"
	if cmd.File != "" {
		actor = "billing-system:" + cmd.File
	}
	switch strings.ToLower(strings.TrimSpace(cmd.Kind)) {
	case external.CmdInvoiceStatus:
		var in InvoiceStatusImport
		if err := json.Unmarshal(cmd.Body, &in); err != nil {
			return err
		}
		_, err := im.Apply(ctx, in, actor)
		return err
	case external.CmdPaymentStatus:
		var in external.PaymentStatus
		if err := json.Unmarshal(cmd.Body, &in); err != nil {
			return err
		}
		_, err := im.ApplyPaymentStatus(ctx, in, actor)
		return err
	case external.CmdAccountBalance:
		var in external.AccountBalanceImport
		if err := json.Unmarshal(cmd.Body, &in); err != nil {
			return err
		}
		_, err := im.ApplyAccountBalance(ctx, in)
		return err
	case external.CmdEnforcement:
		var in external.Enforcement
		if err := json.Unmarshal(cmd.Body, &in); err != nil {
			return err
		}
		_, err := im.ApplyEnforcement(ctx, in, actor)
		return err
	}
	return fmt.Errorf("%w: unknown command kind %q", store.ErrInvalid, cmd.Kind)
}

// ratCmp and ratSign are the exact comparisons the import uses.
func ratCmp(a, b store.Decimal) int { return store.CompareDecimal(a, b) }
func ratSign(a store.Decimal) int   { return store.CompareDecimal(a, "0") }
