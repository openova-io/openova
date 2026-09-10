package commercial

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The import side of the external system of record: in external mode the
// lifecycle after issue is driven ONLY by what the operator's billing system
// tells us, and this is how it tells us.
//
// The body is the TMF678/TMF676 subset that matters — which invoice, what
// state it is in, how much has been paid, and when — and it is authenticated
// by an HMAC over the raw body, because the caller is a machine in the
// operator's estate rather than a signed-in user.

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
// `paid_amount` is CUMULATIVE, so a repeated import is harmless.
type InvoiceStatusImport struct {
	ExternalRef string        `json:"external_ref"`
	State       string        `json:"state"`
	PaidAmount  store.Decimal `json:"paid_amount"`
	PaidAt      string        `json:"paid_at"`
	Reference   string        `json:"reference"`
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

// Importer applies invoice-status imports. It is usable only in external
// mode: in internal mode this product is the system of record and an import
// would be a second writer on the same ledger.
type Importer struct {
	Store  *store.Store
	Secret string
}

// Apply verifies nothing (the caller does that with VerifySignature over the
// raw body) and applies one already-decoded import.
func (im *Importer) Apply(ctx context.Context, in InvoiceStatusImport, actor string) (store.Statement, error) {
	if strings.TrimSpace(in.ExternalRef) == "" {
		return store.Statement{}, fmt.Errorf("%w: external_ref is required", store.ErrInvalid)
	}
	settings, err := im.Store.GetBillingSettings(ctx)
	if err != nil {
		return store.Statement{}, err
	}
	if !settings.ExternalCommercial() {
		return store.Statement{}, fmt.Errorf("%w: this Sovereign invoices internally; invoice status is not imported", store.ErrConflict)
	}
	state, err := MapState(in.State)
	if err != nil {
		return store.Statement{}, err
	}
	paidAt, err := ParsePaidAt(in.PaidAt)
	if err != nil {
		return store.Statement{}, err
	}
	return im.Store.ApplyExternalInvoiceStatus(ctx, store.ExternalInvoiceStatus{
		Ref: strings.TrimSpace(in.ExternalRef), Status: state, PaidAmount: in.PaidAmount,
		PaidAt: paidAt, Reference: in.Reference, Actor: actor,
	})
}
