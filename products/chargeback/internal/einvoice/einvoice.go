// Package einvoice is the e-invoicing seam: one interface, one profile per
// tax authority, and nothing in the issuing path that knows which authority
// it is talking to (DESIGN.md §17, EPIC #6867).
//
// Quoting, tax and the invoice document are OURS to produce. An operator
// running Catalyst BSS in internal mode IS the issuer as far as its tax
// authority is concerned, and the authority asks the issuer — not the
// operator's billing department — for a structured, signed, archived
// electronic invoice. Omantel's external billing seam is ONE configuration
// of that, not the design.
//
// So the shape here is deliberately generic:
//
//	Build     — a statement becomes a structured invoice document
//	Validate  — every problem at once, so an issue is refused with the list
//	Sign      — a signature over the canonical document, key from a Secret
//	Submit    — hand it to whatever the authority's model calls for
//
// Oman is the FIRST profile, not a hard-coded assumption. A second authority
// is a second file implementing the same four methods.
//
// KEY HANDLING, stated once and obeyed everywhere in this package: the
// signing key is read from a mounted Secret, parsed once, held only as a
// parsed key, and NEVER written to the archive, a log line, an error message
// or any response body. What is archived is the document and the HASH of the
// document.
package einvoice

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Profile is one tax authority's e-invoicing contract.
type Profile interface {
	// Name is the profile's configured name ("oman").
	Name() string
	// Build turns a statement into the authority's document shape.
	Build(in Input) (Document, error)
	// Validate reports EVERY problem, so one round of fixes clears them.
	Validate(doc Document) []Problem
	// Sign produces a signature over the canonical document.
	Sign(doc Document) (Signed, error)
	// Submit hands the signed document to the authority's channel. A
	// profile whose authority has published no endpoint returns a receipt
	// marked not submitted WITH THE REASON, and never invents one.
	Submit(ctx context.Context, s Signed) (Receipt, error)
}

// Input is what Build is given. It is the statement plus the two things a
// statement cannot carry on its own: who the seller is, and who the buyer is
// beyond a name.
type Input struct {
	Statement Statement
	Buyer     Party
	Seller    Party
	// IssuedAt is the invoice's issue timestamp. The QR payload and the
	// document both quote it, so it is passed in rather than read from the
	// clock twice.
	IssuedAt time.Time
}

// Statement is the subset of a rated, issued statement a document needs. It
// is a plain struct rather than the store type so this package can be tested,
// and read, without a database.
type Statement struct {
	ID            string
	InvoiceNumber string
	Currency      string
	PeriodStart   string
	PeriodEnd     string
	DueAt         string
	PORef         string
	// The waterfall, as decimal STRINGS at the ledger's own precision.
	// Nothing here parses or re-scales them.
	ListSubtotal  string
	DiscountTotal string
	NetSubtotal   string
	TaxTotal      string
	Total         string
	Lines         []Line
	// TaxSubtotals is the per-rule summary frozen on the statement: the
	// tax summary block by rate an invoice with several rates must show.
	TaxSubtotals []TaxSubtotal
	// Notes are the sentences the invoice must carry — the reverse-charge
	// wording, an exemption article.
	Notes []string
}

// Party is an identified party on the document.
type Party struct {
	Name string
	// RegistrationNumber is the tax registration (VATIN) of the party.
	RegistrationNumber string
	Country            string
	Region             string
	Address            string
	Email              string
}

// Line is one invoice line, with its OWN tax category and rate — which is
// what makes several rates on one invoice expressible at all.
type Line struct {
	SKU         string
	Description string
	Unit        string
	Quantity    string
	UnitPrice   string
	Amount      string
	TaxCategory string
	TaxKind     string
	TaxRate     string
	TaxAmount   string
}

// TaxSubtotal is one rule's row of the tax summary.
type TaxSubtotal struct {
	RuleID   string
	RuleName string
	Kind     string
	Category string
	Rate     string
	Base     string
	Tax      string
	Note     string
}

// Document is the structured invoice a profile built: the UBL-shaped fields
// plus the QR payload the printed invoice carries.
type Document struct {
	Profile  string `json:"profile"`
	ID       string `json:"id"`
	IssuedAt string `json:"issued_at"`
	Currency string `json:"currency"`

	Seller Party `json:"-"`
	Buyer  Party `json:"-"`

	// The JSON shape is flattened so GET /statements/{id}/einvoice reads as
	// a document rather than as this package's internals.
	SellerName           string        `json:"seller_name"`
	SellerRegistration   string        `json:"seller_registration_number"`
	SellerCountry        string        `json:"seller_country,omitempty"`
	SellerAddress        string        `json:"seller_address,omitempty"`
	BuyerName            string        `json:"buyer_name"`
	BuyerRegistration    string        `json:"buyer_registration_number,omitempty"`
	BuyerCountry         string        `json:"buyer_country,omitempty"`
	BuyerAddress         string        `json:"buyer_address,omitempty"`
	PORef                string        `json:"po_reference,omitempty"`
	PeriodStart          string        `json:"period_start,omitempty"`
	PeriodEnd            string        `json:"period_end,omitempty"`
	DueAt                string        `json:"due_at,omitempty"`
	StatementID          string        `json:"statement_id,omitempty"`
	Lines                []Line        `json:"lines"`
	TaxSubtotals         []TaxSubtotal `json:"tax_subtotals"`
	TaxExclusiveAmount   string        `json:"tax_exclusive_amount"`
	TaxAmount            string        `json:"tax_amount"`
	TaxInclusiveAmount   string        `json:"tax_inclusive_amount"`
	AllowanceTotalAmount string        `json:"allowance_total_amount,omitempty"`
	LineExtensionAmount  string        `json:"line_extension_amount,omitempty"`
	Notes                []string      `json:"notes,omitempty"`
	// QRPayload is the base64 TLV the printed invoice's QR carries.
	QRPayload string `json:"qr_payload,omitempty"`
}

// Signed is a document plus the signature over its canonical bytes.
type Signed struct {
	Document Document
	// Canonical is EXACTLY the bytes that were hashed and signed.
	Canonical []byte
	// Hash is the SHA-256 of Canonical, hex, lower case.
	Hash string
	// Signature is base64; Algorithm names how to verify it; KeyID names
	// which key, so a rotation is traceable. The KEY ITSELF is never here.
	Signature string
	Algorithm string
	KeyID     string
	// XML is the archival copy: the canonical document with the signature
	// carried alongside it.
	XML []byte
}

// Submission outcomes.
const (
	// StatusSubmitted — the authority's channel accepted the document.
	StatusSubmitted = "submitted"
	// StatusNotSubmitted — submission was not attempted, or it failed.
	// Reason always says which; it is never silently empty.
	StatusNotSubmitted = "not_submitted"
)

// Receipt is what Submit returns.
type Receipt struct {
	Status      string
	Reason      string
	Reference   string
	SubmittedAt time.Time
}

// Problem is one validation failure, by field.
type Problem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	if p.Field == "" {
		return p.Message
	}
	return p.Field + ": " + p.Message
}

// ProblemsError renders a list of problems as one error — what the issuing
// path refuses with, exactly as the commercial outbox refuses a malformed
// export: every problem named, in one message.
func ProblemsError(problems []Problem) error {
	if len(problems) == 0 {
		return nil
	}
	parts := make([]string, 0, len(problems))
	for _, p := range problems {
		parts = append(parts, p.String())
	}
	sort.Strings(parts)
	return fmt.Errorf("e-invoice is not valid: %s", strings.Join(parts, "; "))
}

// Config is how a Sovereign turns e-invoicing on.
type Config struct {
	// Profile is EINVOICE_PROFILE. Empty = OFF: no document is built, no
	// endpoint answers, and every statement behaves exactly as it did.
	Profile string
	// SigningKeyPEM is the private key, read from a mounted Secret. It is
	// parsed once by New and this field is not retained afterwards.
	SigningKeyPEM []byte
	// KeyID names the key on the document so a rotation is traceable. It is
	// a NAME, never key material.
	KeyID string
}

// Profiles lists the profile names this build carries.
var Profiles = []string{ProfileOman}

// New builds the configured profile. An empty Profile returns (nil, nil):
// unconfigured is a first-class state, not an error.
func New(cfg Config) (Profile, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Profile))
	switch name {
	case "":
		return nil, nil
	case ProfileOman:
		return newOman(cfg)
	default:
		return nil, fmt.Errorf("unknown e-invoicing profile %q (known: %s)", cfg.Profile, strings.Join(Profiles, ", "))
	}
}
