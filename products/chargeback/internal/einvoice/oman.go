package einvoice

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// The OMAN profile — the first implementation of Profile, not a special case
// the rest of the package is shaped around (DESIGN.md §17).
//
// WHAT IS GROUNDED, and where from:
//
//   - FORMAT. Oman's Tax Authority (OTA) runs its e-invoicing programme as
//     **Fawtara**: the notified formats are XML (UBL 2.1) per the **PINT OM**
//     specification, and PDF/A-3. So the document this profile renders is
//     UBL-shaped: cbc/cac element names, an InvoiceLine per rated line with
//     its own TaxCategory and Percent, and a TaxTotal with one TaxSubtotal
//     per rate.
//
//   - QR, TLV. The Gulf authorities specify the QR payload as a TLV stream,
//     base64-encoded. The TAG NUMBERING implemented here is the five-field
//     set published by ZATCA (Saudi Arabia) in its "Guide to Developed
//     FATOORA Compliant QR Code" — tag 1 seller name, tag 2 VAT registration
//     number, tag 3 timestamp, tag 4 invoice total INCLUDING tax, tag 5 tax
//     total — which is the numbering the region's implementations follow.
//     ZATCA's own clearance extension adds tags 6-9 (XML hash, signature,
//     public key, authority stamp); those are NOT emitted here, because they
//     are a Saudi clearance artefact and Oman has published no equivalent.
//
// WHAT IS NOT GROUNDED, and is therefore configuration rather than a
// constant in this file:
//
//   - Oman's OWN tag numbering. The OTA's QR field list is defined in the
//     "Peppol Oman Architecture" document (§4 of v1.0.1), which is not
//     published openly; secondary sources describe SEVEN fields for a B2C
//     simplified invoice without naming their tags. This profile therefore
//     emits the five grounded fields and leaves the tag table as a VALUE
//     (omanTags) so the two additional fields can be added by changing one
//     table when the numbering is published. DESIGN.md §17 says exactly
//     what to fill in.
//
//   - The CLEARANCE channel. Oman's model is FIVE-CORNER: the issuer
//     transmits through an OTA-ACCREDITED SERVICE PROVIDER on the Peppol
//     network, and the OTA receives the tax data from that provider. There
//     is no published endpoint an issuer posts an invoice to. Submit
//     therefore does nothing and returns a receipt marked not submitted
//     WITH THE REASON. No endpoint is invented here.

// ProfileOman is the profile name (EINVOICE_PROFILE=oman).
const ProfileOman = "oman"

// The TLV tag numbers of the QR payload. Source: ZATCA's published QR code
// specification (tags 1-5), which is the numbering the Gulf implementations
// follow. Oman's own list — "Peppol Oman Architecture" §4 — is not public;
// when it is, the two further fields it describes are added HERE and
// nowhere else.
const (
	TagSellerName         byte = 1 // the seller's legal name
	TagSellerRegistration byte = 2 // the seller's VAT registration number
	TagTimestamp          byte = 3 // the invoice's issue timestamp, ISO 8601
	TagTotalIncludingTax  byte = 4 // the invoice total INCLUDING tax
	TagTaxTotal           byte = 5 // the tax total
)

// omanTags is the ordered tag table the QR payload is built from. It is a
// VALUE, not a sequence of literals scattered through Build, precisely so
// that a published Omani numbering is a one-line change.
var omanTags = []byte{TagSellerName, TagSellerRegistration, TagTimestamp, TagTotalIncludingTax, TagTaxTotal}

// omanSubmitReason is the exact, honest reason Submit returns. It names the
// model rather than blaming the operator, because there is nothing the
// operator can configure here today.
const omanSubmitReason = "Oman's Fawtara model is five-corner: an invoice reaches the Tax Authority through an OTA-accredited service provider on the Peppol network, and the Tax Authority publishes no endpoint an issuer submits to directly. The document is built, validated, signed and archived; transmission is configured when an accredited provider is engaged."

type omanProfile struct {
	signer *signer
}

func newOman(cfg Config) (Profile, error) {
	s, err := newSigner(cfg.SigningKeyPEM, cfg.KeyID)
	if err != nil {
		return nil, err
	}
	return &omanProfile{signer: s}, nil
}

func (p *omanProfile) Name() string { return ProfileOman }

// Build maps a statement onto the document. It NEVER computes money: every
// amount is the decimal string the ledger settled, passed through.
func (p *omanProfile) Build(in Input) (Document, error) {
	st := in.Statement
	issued := in.IssuedAt
	if issued.IsZero() {
		return Document{}, fmt.Errorf("issued_at is required to build an e-invoice")
	}
	doc := Document{
		Profile:              ProfileOman,
		ID:                   strings.TrimSpace(st.InvoiceNumber),
		IssuedAt:             issued.UTC().Format(time.RFC3339),
		Currency:             strings.ToUpper(strings.TrimSpace(st.Currency)),
		Seller:               in.Seller,
		Buyer:                in.Buyer,
		SellerName:           strings.TrimSpace(in.Seller.Name),
		SellerRegistration:   strings.TrimSpace(in.Seller.RegistrationNumber),
		SellerCountry:        strings.ToUpper(strings.TrimSpace(in.Seller.Country)),
		SellerAddress:        strings.TrimSpace(in.Seller.Address),
		BuyerName:            strings.TrimSpace(in.Buyer.Name),
		BuyerRegistration:    strings.TrimSpace(in.Buyer.RegistrationNumber),
		BuyerCountry:         strings.ToUpper(strings.TrimSpace(in.Buyer.Country)),
		BuyerAddress:         strings.TrimSpace(in.Buyer.Address),
		PORef:                strings.TrimSpace(st.PORef),
		PeriodStart:          st.PeriodStart,
		PeriodEnd:            st.PeriodEnd,
		DueAt:                st.DueAt,
		StatementID:          st.ID,
		Lines:                st.Lines,
		TaxSubtotals:         st.TaxSubtotals,
		TaxExclusiveAmount:   st.NetSubtotal,
		TaxAmount:            st.TaxTotal,
		TaxInclusiveAmount:   st.Total,
		LineExtensionAmount:  st.ListSubtotal,
		AllowanceTotalAmount: st.DiscountTotal,
		Notes:                append([]string(nil), st.Notes...),
	}
	qr, err := EncodeTLVBase64(omanQRFields(doc))
	if err != nil {
		return Document{}, err
	}
	doc.QRPayload = qr
	return doc, nil
}

// omanQRFields is the QR payload, field by field, in tag order.
func omanQRFields(doc Document) []TLVField {
	values := map[byte]string{
		TagSellerName:         doc.SellerName,
		TagSellerRegistration: doc.SellerRegistration,
		TagTimestamp:          doc.IssuedAt,
		TagTotalIncludingTax:  doc.TaxInclusiveAmount,
		TagTaxTotal:           doc.TaxAmount,
	}
	out := make([]TLVField, 0, len(omanTags))
	for _, tag := range omanTags {
		out = append(out, TLVField{Tag: tag, Value: values[tag]})
	}
	return out
}

// Validate reports EVERY problem at once. A missing registration number is
// the one an operator hits first and the one a tax authority rejects an
// invoice for, so it is named by field and by what to do about it.
func (p *omanProfile) Validate(doc Document) []Problem {
	var out []Problem
	add := func(field, format string, a ...any) {
		out = append(out, Problem{Field: field, Message: fmt.Sprintf(format, a...)})
	}
	if doc.ID == "" {
		add("id", "the invoice has no number; an e-invoice is built from an ISSUED statement, which is where the number is assigned")
	}
	if doc.SellerName == "" {
		add("seller_name", "the Sovereign's legal name is not set (Configure → Billing → legal name)")
	}
	if doc.SellerRegistration == "" {
		add("seller_registration_number", "the Sovereign's tax registration number is not set (Configure → Tax → the Sovereign's registration); a tax invoice cannot be issued without it")
	}
	if doc.Currency == "" || len(doc.Currency) != 3 {
		add("currency", "must be a three-letter ISO 4217 code")
	}
	if _, err := time.Parse(time.RFC3339, doc.IssuedAt); err != nil {
		add("issued_at", "must be an RFC 3339 timestamp")
	}
	if doc.BuyerName == "" {
		add("buyer_name", "the customer has no name")
	}
	if len(doc.Lines) == 0 {
		add("lines", "an invoice with no lines cannot be issued")
	}
	for i, l := range doc.Lines {
		if strings.TrimSpace(l.SKU) == "" && strings.TrimSpace(l.Description) == "" {
			add(fmt.Sprintf("lines[%d]", i), "needs a SKU or a description")
		}
		if strings.TrimSpace(l.Amount) == "" {
			add(fmt.Sprintf("lines[%d].amount", i), "is empty")
		}
	}
	if strings.TrimSpace(doc.TaxInclusiveAmount) == "" {
		add("tax_inclusive_amount", "is empty")
	}
	// A reverse-charge or exempt subtotal MUST carry the note the authority
	// requires; a zero line with no explanation is the defect a tax auditor
	// looks for first.
	for i, t := range doc.TaxSubtotals {
		if (t.Kind == "reverse_charge" || t.Kind == "exempt" || t.Kind == "zero_rated") && strings.TrimSpace(t.Note) == "" {
			add(fmt.Sprintf("tax_subtotals[%d].note", i), "a %s line must carry the note the tax authority requires; set it on the tax rule", t.Kind)
		}
	}
	if doc.QRPayload == "" {
		add("qr_payload", "the QR payload is empty")
	}
	if p.signer == nil || p.signer.key == nil {
		add("signature", "no signing key is configured; mount the key Secret and set EINVOICE_SIGNER_PATH")
	}
	return out
}

// Sign renders the canonical document, signs it, and returns the archival
// XML — the canonical document with the signature carried alongside it.
//
// CANONICAL here means, precisely: the bytes this renderer produces for this
// document, which are the bytes hashed, the bytes signed, and the bytes
// archived. It is not XML C14N — nothing in this build canonicalises XML —
// and it does not need to be, because the archive keeps the exact bytes
// rather than re-serialising them to check a signature.
func (p *omanProfile) Sign(doc Document) (Signed, error) {
	canonical, err := renderUBL(doc, nil)
	if err != nil {
		return Signed{}, err
	}
	signature, hash, err := p.signer.sign(canonical)
	if err != nil {
		return Signed{}, err
	}
	sig := &ublSignature{
		Algorithm: p.signer.alg,
		Digest:    hash,
		DigestAlg: digestAlgorithm,
		KeyID:     p.signer.keyID,
		Value:     signature,
	}
	archival, err := renderUBL(doc, sig)
	if err != nil {
		return Signed{}, err
	}
	return Signed{
		Document:  doc,
		Canonical: canonical,
		Hash:      hash,
		Signature: signature,
		Algorithm: p.signer.alg,
		KeyID:     p.signer.keyID,
		XML:       archival,
	}, nil
}

// PublicKeyPEM exposes the public half of the signing key so an operator —
// and the test — can verify an archived signature. The private half never
// leaves newSigner.
func (p *omanProfile) PublicKeyPEM() (string, error) { return p.signer.PublicKeyPEM() }

// Submit does NOTHING and says so. See omanSubmitReason: Oman's model has no
// endpoint for an issuer to post to, and inventing one would be worse than
// the honest no-op.
func (p *omanProfile) Submit(_ context.Context, _ Signed) (Receipt, error) {
	return Receipt{Status: StatusNotSubmitted, Reason: omanSubmitReason}, nil
}

// ---------------------------------------------------------------------------
// the UBL-shaped document
// ---------------------------------------------------------------------------

const (
	nsInvoice = "urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
	nsCAC     = "urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
	nsCBC     = "urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2"
)

type ublSignature struct {
	Algorithm string
	Digest    string
	DigestAlg string
	KeyID     string
	Value     string
}

type ublAmount struct {
	Currency string `xml:"currencyID,attr"`
	Value    string `xml:",chardata"`
}

type ublQuantity struct {
	Unit  string `xml:"unitCode,attr,omitempty"`
	Value string `xml:",chardata"`
}

// ublParty follows the UBL Party SEQUENCE — PartyName, PostalAddress,
// PartyTaxScheme — because a UBL document is validated against an ordered
// schema: the same elements in a different order do not validate, and a
// document that does not validate is one an accredited provider rejects.
type ublParty struct {
	XMLName       xml.Name
	Name          string `xml:"cac:PartyName>cbc:Name"`
	StreetName    string `xml:"cac:PostalAddress>cbc:StreetName,omitempty"`
	CountrySubent string `xml:"cac:PostalAddress>cbc:CountrySubentity,omitempty"`
	CountryCode   string `xml:"cac:PostalAddress>cac:Country>cbc:IdentificationCode,omitempty"`
	CompanyID     string `xml:"cac:PartyTaxScheme>cbc:CompanyID,omitempty"`
	TaxSchemeID   string `xml:"cac:PartyTaxScheme>cac:TaxScheme>cbc:ID,omitempty"`
}

type ublTaxSubtotal struct {
	XMLName       xml.Name  `xml:"cac:TaxSubtotal"`
	TaxableAmount ublAmount `xml:"cbc:TaxableAmount"`
	TaxAmount     ublAmount `xml:"cbc:TaxAmount"`
	// The UBL TaxCategory sequence is ID, Name, Percent, exemption reason,
	// then TaxScheme — every cbc element before the cac one, in that order.
	CategoryID     string `xml:"cac:TaxCategory>cbc:ID"`
	OpenovaRuleRef string `xml:"cac:TaxCategory>cbc:Name,omitempty"`
	Percent        string `xml:"cac:TaxCategory>cbc:Percent"`
	ExemptionCode  string `xml:"cac:TaxCategory>cbc:TaxExemptionReasonCode,omitempty"`
	ExemptionText  string `xml:"cac:TaxCategory>cbc:TaxExemptionReason,omitempty"`
	TaxSchemeID    string `xml:"cac:TaxCategory>cac:TaxScheme>cbc:ID"`
}

type ublLine struct {
	XMLName       xml.Name    `xml:"cac:InvoiceLine"`
	ID            string      `xml:"cbc:ID"`
	Quantity      ublQuantity `xml:"cbc:InvoicedQuantity"`
	LineExtension ublAmount   `xml:"cbc:LineExtensionAmount"`
	TaxAmount     *ublAmount  `xml:"cac:TaxTotal>cbc:TaxAmount,omitempty"`
	ItemName      string      `xml:"cac:Item>cbc:Name"`
	ItemSKU       string      `xml:"cac:Item>cac:SellersItemIdentification>cbc:ID,omitempty"`
	CategoryID    string      `xml:"cac:Item>cac:ClassifiedTaxCategory>cbc:ID"`
	Percent       string      `xml:"cac:Item>cac:ClassifiedTaxCategory>cbc:Percent"`
	TaxSchemeID   string      `xml:"cac:Item>cac:ClassifiedTaxCategory>cac:TaxScheme>cbc:ID"`
	PriceAmount   ublAmount   `xml:"cac:Price>cbc:PriceAmount"`
}

type ublInvoice struct {
	XMLName  xml.Name `xml:"Invoice"`
	Xmlns    string   `xml:"xmlns,attr"`
	XmlnsCAC string   `xml:"xmlns:cac,attr"`
	XmlnsCBC string   `xml:"xmlns:cbc,attr"`

	CustomizationID string   `xml:"cbc:CustomizationID"`
	ProfileID       string   `xml:"cbc:ProfileID"`
	ID              string   `xml:"cbc:ID"`
	UUID            string   `xml:"cbc:UUID,omitempty"`
	IssueDate       string   `xml:"cbc:IssueDate"`
	IssueTime       string   `xml:"cbc:IssueTime"`
	InvoiceTypeCode string   `xml:"cbc:InvoiceTypeCode"`
	Notes           []string `xml:"cbc:Note,omitempty"`
	CurrencyCode    string   `xml:"cbc:DocumentCurrencyCode"`

	PeriodStart string `xml:"cac:InvoicePeriod>cbc:StartDate,omitempty"`
	PeriodEnd   string `xml:"cac:InvoicePeriod>cbc:EndDate,omitempty"`
	OrderRef    string `xml:"cac:OrderReference>cbc:ID,omitempty"`

	Seller ublParty `xml:"cac:AccountingSupplierParty>cac:Party"`
	Buyer  ublParty `xml:"cac:AccountingCustomerParty>cac:Party"`

	DueDate string `xml:"cac:PaymentMeans>cbc:PaymentDueDate,omitempty"`

	AllowanceChargeIndicator *bool      `xml:"cac:AllowanceCharge>cbc:ChargeIndicator,omitempty"`
	AllowanceAmount          *ublAmount `xml:"cac:AllowanceCharge>cbc:Amount,omitempty"`

	TaxTotalAmount ublAmount        `xml:"cac:TaxTotal>cbc:TaxAmount"`
	TaxSubtotals   []ublTaxSubtotal `xml:"cac:TaxTotal>cac:TaxSubtotal"`

	LineExtensionAmount ublAmount `xml:"cac:LegalMonetaryTotal>cbc:LineExtensionAmount"`
	TaxExclusiveAmount  ublAmount `xml:"cac:LegalMonetaryTotal>cbc:TaxExclusiveAmount"`
	TaxInclusiveAmount  ublAmount `xml:"cac:LegalMonetaryTotal>cbc:TaxInclusiveAmount"`
	AllowanceTotal      ublAmount `xml:"cac:LegalMonetaryTotal>cbc:AllowanceTotalAmount"`
	PayableAmount       ublAmount `xml:"cac:LegalMonetaryTotal>cbc:PayableAmount"`

	Lines []ublLine

	// The QR payload, carried as an AdditionalDocumentReference the way the
	// Gulf implementations carry it.
	QRID    string `xml:"cac:AdditionalDocumentReference>cbc:ID,omitempty"`
	QRValue string `xml:"cac:AdditionalDocumentReference>cac:Attachment>cbc:EmbeddedDocumentBinaryObject,omitempty"`

	// The signature block, present only in the ARCHIVAL copy. The canonical
	// bytes that were signed never contain it — a signature cannot cover
	// itself.
	SignatureAlgorithm string `xml:"cac:Signature>cbc:ID,omitempty"`
	SignatureKeyID     string `xml:"cac:Signature>cbc:Note,omitempty"`
	SignatureDigestAlg string `xml:"cac:Signature>cac:DigitalSignatureAttachment>cbc:ID,omitempty"`
	SignatureDigest    string `xml:"cac:Signature>cac:DigitalSignatureAttachment>cbc:Note,omitempty"`
	SignatureValue     string `xml:"cac:Signature>cac:DigitalSignatureAttachment>cac:ExternalReference>cbc:URI,omitempty"`
}

// ublTaxCategoryCode maps a rule kind onto the UBL tax category code. The
// codes are the UN/CEFACT 5305 subset every UBL profile uses: S standard, Z
// zero-rated, E exempt, AE reverse charge, O outside the scope of tax.
func ublTaxCategoryCode(kind string) string {
	switch kind {
	case "zero_rated":
		return "Z"
	case "exempt":
		return "E"
	case "reverse_charge":
		return "AE"
	case "out_of_state":
		return "O"
	default:
		return "S"
	}
}

// percentOf renders a rate (0.0500) as a UBL percent (5.00).
func percentOf(rate string) string {
	r := strings.TrimSpace(rate)
	if r == "" {
		return "0.00"
	}
	neg := strings.HasPrefix(r, "-")
	r = strings.TrimPrefix(r, "-")
	whole, frac, _ := strings.Cut(r, ".")
	digits := strings.TrimLeft(whole, "0") + frac
	if digits == "" {
		digits = "0"
	}
	// Multiply by 100 = shift the decimal point two places right.
	point := len(whole) + 2
	padded := whole + frac
	for len(padded) < point {
		padded += "0"
	}
	intPart := strings.TrimLeft(padded[:point], "0")
	if intPart == "" {
		intPart = "0"
	}
	fracPart := padded[point:]
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	fracPart = strings.TrimRight(fracPart, "0")
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	out := intPart + "." + fracPart
	if neg {
		out = "-" + out
	}
	return out
}

// renderUBL serialises the document. sig == nil renders the CANONICAL bytes
// (no signature block); a non-nil sig renders the archival copy.
func renderUBL(doc Document, sig *ublSignature) ([]byte, error) {
	date, clock := doc.IssuedAt, ""
	if t, err := time.Parse(time.RFC3339, doc.IssuedAt); err == nil {
		date, clock = t.UTC().Format("2006-01-02"), t.UTC().Format("15:04:05")
	}
	inv := ublInvoice{
		Xmlns:    nsInvoice,
		XmlnsCAC: nsCAC,
		XmlnsCBC: nsCBC,
		// The customization and profile identifiers name the specification
		// the document claims to follow. PINT OM is Oman's notified UBL 2.1
		// customisation; the exact identifier string an accredited provider
		// requires is configuration on their side, so the value here names
		// the specification rather than asserting a registered identifier.
		CustomizationID: "urn:openova:einvoice:oman:pint-om",
		ProfileID:       "urn:openova:einvoice:profile:oman",
		ID:              doc.ID,
		IssueDate:       date,
		IssueTime:       clock,
		// 388 is the UN/EDIFACT 1001 code for a commercial tax invoice.
		InvoiceTypeCode: "388",
		Notes:           doc.Notes,
		CurrencyCode:    doc.Currency,
		PeriodStart:     doc.PeriodStart,
		PeriodEnd:       doc.PeriodEnd,
		OrderRef:        doc.PORef,
		DueDate:         doc.DueAt,
		Seller: ublParty{
			XMLName:       xml.Name{Local: "cac:Party"},
			Name:          doc.SellerName,
			CompanyID:     doc.SellerRegistration,
			TaxSchemeID:   "VAT",
			StreetName:    firstLine(doc.SellerAddress),
			CountrySubent: doc.Seller.Region,
			CountryCode:   doc.SellerCountry,
		},
		Buyer: ublParty{
			XMLName:       xml.Name{Local: "cac:Party"},
			Name:          doc.BuyerName,
			CompanyID:     doc.BuyerRegistration,
			TaxSchemeID:   taxSchemeOrEmpty(doc.BuyerRegistration),
			StreetName:    firstLine(doc.BuyerAddress),
			CountrySubent: doc.Buyer.Region,
			CountryCode:   doc.BuyerCountry,
		},
		TaxTotalAmount:      ublAmount{Currency: doc.Currency, Value: orZero(doc.TaxAmount)},
		LineExtensionAmount: ublAmount{Currency: doc.Currency, Value: orZero(doc.LineExtensionAmount)},
		TaxExclusiveAmount:  ublAmount{Currency: doc.Currency, Value: orZero(doc.TaxExclusiveAmount)},
		TaxInclusiveAmount:  ublAmount{Currency: doc.Currency, Value: orZero(doc.TaxInclusiveAmount)},
		AllowanceTotal:      ublAmount{Currency: doc.Currency, Value: orZero(doc.AllowanceTotalAmount)},
		PayableAmount:       ublAmount{Currency: doc.Currency, Value: orZero(doc.TaxInclusiveAmount)},
		QRID:                "QR",
		QRValue:             doc.QRPayload,
	}
	if doc.LineExtensionAmount == "" {
		inv.LineExtensionAmount.Value = orZero(doc.TaxExclusiveAmount)
	}
	for _, t := range doc.TaxSubtotals {
		sub := ublTaxSubtotal{
			TaxableAmount:  ublAmount{Currency: doc.Currency, Value: orZero(t.Base)},
			TaxAmount:      ublAmount{Currency: doc.Currency, Value: orZero(t.Tax)},
			CategoryID:     ublTaxCategoryCode(t.Kind),
			Percent:        percentOf(t.Rate),
			TaxSchemeID:    "VAT",
			OpenovaRuleRef: t.RuleName,
		}
		if t.Kind != "standard" && strings.TrimSpace(t.Note) != "" {
			sub.ExemptionText = t.Note
		}
		inv.TaxSubtotals = append(inv.TaxSubtotals, sub)
	}
	if len(inv.TaxSubtotals) == 0 {
		// A single-rate invoice still states its rate as one subtotal: a
		// UBL invoice with a TaxTotal and no TaxSubtotal is not readable.
		inv.TaxSubtotals = append(inv.TaxSubtotals, ublTaxSubtotal{
			TaxableAmount: ublAmount{Currency: doc.Currency, Value: orZero(doc.TaxExclusiveAmount)},
			TaxAmount:     ublAmount{Currency: doc.Currency, Value: orZero(doc.TaxAmount)},
			CategoryID:    "S",
			Percent:       percentOf(singleRateOf(doc)),
			TaxSchemeID:   "VAT",
		})
	}
	for i, l := range doc.Lines {
		line := ublLine{
			ID:            fmt.Sprint(i + 1),
			Quantity:      ublQuantity{Unit: l.Unit, Value: orZero(l.Quantity)},
			LineExtension: ublAmount{Currency: doc.Currency, Value: orZero(l.Amount)},
			ItemName:      firstNonEmpty(l.Description, l.SKU),
			ItemSKU:       l.SKU,
			CategoryID:    ublTaxCategoryCode(l.TaxKind),
			Percent:       percentOf(l.TaxRate),
			TaxSchemeID:   "VAT",
			PriceAmount:   ublAmount{Currency: doc.Currency, Value: orZero(l.UnitPrice)},
		}
		if strings.TrimSpace(l.TaxAmount) != "" {
			line.TaxAmount = &ublAmount{Currency: doc.Currency, Value: l.TaxAmount}
		}
		inv.Lines = append(inv.Lines, line)
	}
	if strings.TrimSpace(doc.AllowanceTotalAmount) != "" && doc.AllowanceTotalAmount != "0" && doc.AllowanceTotalAmount != "0.000000" {
		no := false
		inv.AllowanceChargeIndicator = &no
		inv.AllowanceAmount = &ublAmount{Currency: doc.Currency, Value: doc.AllowanceTotalAmount}
	}
	if sig != nil {
		inv.SignatureAlgorithm = sig.Algorithm
		inv.SignatureKeyID = sig.KeyID
		inv.SignatureDigestAlg = sig.DigestAlg
		inv.SignatureDigest = sig.Digest
		inv.SignatureValue = sig.Value
	}
	body, err := xml.MarshalIndent(inv, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render e-invoice: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
}

// singleRateOf derives the rate for the fallback single subtotal.
func singleRateOf(doc Document) string {
	if len(doc.Lines) > 0 && strings.TrimSpace(doc.Lines[0].TaxRate) != "" {
		return doc.Lines[0].TaxRate
	}
	return "0"
}

func taxSchemeOrEmpty(registration string) string {
	if strings.TrimSpace(registration) == "" {
		return ""
	}
	return "VAT"
}

func firstLine(s string) string {
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func orZero(s string) string {
	if strings.TrimSpace(s) == "" {
		return "0"
	}
	return s
}
