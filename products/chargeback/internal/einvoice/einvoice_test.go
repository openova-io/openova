package einvoice

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

// The e-invoicing contract (DESIGN.md §17).

func ecKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func rsaKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func sampleInput() Input {
	return Input{
		Seller: Party{
			Name:               "Sovereign Cloud Operator LLC",
			RegistrationNumber: "OM1100012345",
			Country:            "OM",
			Address:            "Knowledge Oasis Muscat\nMuscat 130, Oman",
		},
		Buyer: Party{
			Name:               "Acme Trading LLC",
			RegistrationNumber: "OM1100098765",
			Country:            "OM",
		},
		IssuedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Statement: Statement{
			ID:            "3f9c1b2e-7a4d-4c1e-9b0a-1d2e3f4a5b6c",
			InvoiceNumber: "INV-2026-00042",
			Currency:      "OMR",
			PeriodStart:   "2026-08-01",
			PeriodEnd:     "2026-08-31",
			DueAt:         "2026-10-01",
			PORef:         "PO-2026-118",
			ListSubtotal:  "520.000000",
			DiscountTotal: "52.000000",
			NetSubtotal:   "468.000000",
			TaxTotal:      "18.000000",
			Total:         "486.000000",
			Lines: []Line{
				{SKU: "ecs.c7.large", Unit: "hour", Quantity: "1.000000", UnitPrice: "400.000000", Amount: "400.000000", TaxKind: "standard", TaxRate: "0.0500"},
				{SKU: "evs.ssd", Unit: "gb-hour", Quantity: "1.000000", UnitPrice: "120.000000", Amount: "120.000000", TaxCategory: "storage", TaxKind: "zero_rated", TaxRate: "0.0000"},
			},
			TaxSubtotals: []TaxSubtotal{
				{RuleID: "std", RuleName: "Oman VAT standard", Kind: "standard", Rate: "0.0500", Base: "360.000000", Tax: "18.000000"},
				{RuleID: "zero", RuleName: "Zero-rated storage", Kind: "zero_rated", Category: "storage", Rate: "0.0000", Base: "108.000000", Tax: "0.000000",
					Note: "Zero-rated supply under the Executive Regulation."},
			},
			Notes: []string{"Zero-rated supply under the Executive Regulation."},
		},
	}
}

func mustProfile(t *testing.T, key []byte) Profile {
	t.Helper()
	p, err := New(Config{Profile: ProfileOman, SigningKeyPEM: key, KeyID: "einvoice-2026"})
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("no profile was built for oman")
	}
	return p
}

// An empty profile name is OFF, not an error: unconfigured is a first-class
// state and a Sovereign that never enabled e-invoicing must not fail to start.
func TestEmptyProfileIsOffAndUnknownIsAnError(t *testing.T) {
	p, err := New(Config{})
	if err != nil || p != nil {
		t.Fatalf("empty profile = (%v, %v), want (nil, nil)", p, err)
	}
	if _, err := New(Config{Profile: "narnia"}); err == nil {
		t.Fatal("an unknown profile was accepted")
	}
}

// THE QR. The payload must DECODE to the five fields with the right tags —
// decoded here, not compared against a recorded blob, because a blob compares
// equal to itself however wrong it is.
func TestQRPayloadDecodesToTheFiveTaggedFields(t *testing.T) {
	doc, err := mustProfile(t, ecKeyPEM(t)).Build(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if doc.QRPayload == "" {
		t.Fatal("the document carries no QR payload")
	}
	fields, err := DecodeTLVBase64(doc.QRPayload)
	if err != nil {
		t.Fatalf("the QR payload does not decode: %v", err)
	}
	if len(fields) != 5 {
		t.Fatalf("the QR carries %d fields, want 5: %+v", len(fields), fields)
	}
	want := []struct {
		tag   byte
		value string
	}{
		{TagSellerName, "Sovereign Cloud Operator LLC"},
		{TagSellerRegistration, "OM1100012345"},
		{TagTimestamp, "2026-09-01T00:00:00Z"},
		{TagTotalIncludingTax, "486.000000"},
		{TagTaxTotal, "18.000000"},
	}
	for i, w := range want {
		if fields[i].Tag != w.tag {
			t.Errorf("field %d has tag %d, want %d", i, fields[i].Tag, w.tag)
		}
		if fields[i].Value != w.value {
			t.Errorf("tag %d = %q, want %q", w.tag, fields[i].Value, w.value)
		}
	}
	// The tags are 1..5 in order, which is what a scanner expects.
	for i, f := range fields {
		if int(f.Tag) != i+1 {
			t.Errorf("tag order broke at field %d: tag %d", i, f.Tag)
		}
	}
}

// TLV length is the BYTE length, not the character count: a multi-byte value
// that recorded its rune count would decode as gibberish from the next field
// onwards.
func TestTLVLengthIsBytesNotCharacters(t *testing.T) {
	arabic := "شركة السحابة"
	raw, err := EncodeTLV([]TLVField{{Tag: 1, Value: arabic}, {Tag: 2, Value: "OM1"}})
	if err != nil {
		t.Fatal(err)
	}
	if int(raw[1]) != len([]byte(arabic)) {
		t.Fatalf("length byte = %d, want the byte length %d (not the %d runes)", raw[1], len([]byte(arabic)), len([]rune(arabic)))
	}
	back, err := DecodeTLV(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[0].Value != arabic || back[1].Value != "OM1" {
		t.Fatalf("round trip = %+v", back)
	}
	// A value a one-byte length cannot express is refused, not truncated.
	if _, err := EncodeTLV([]TLVField{{Tag: 1, Value: strings.Repeat("x", 256)}}); err == nil {
		t.Fatal("a 256-byte value was accepted by a one-byte length")
	}
}

// VALIDATION catches a missing registration number and names it. This is the
// problem an operator hits first and the one a tax authority rejects an
// invoice for.
func TestValidationCatchesAMissingRegistrationNumber(t *testing.T) {
	p := mustProfile(t, ecKeyPEM(t))
	in := sampleInput()
	in.Seller.RegistrationNumber = ""
	doc, err := p.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	problems := p.Validate(doc)
	if len(problems) == 0 {
		t.Fatal("an invoice with no seller registration number validated clean")
	}
	found := false
	for _, pr := range problems {
		if pr.Field == "seller_registration_number" {
			found = true
			if !strings.Contains(pr.Message, "registration number is not set") {
				t.Errorf("the message does not say what is wrong: %q", pr.Message)
			}
		}
	}
	if !found {
		t.Fatalf("no problem named seller_registration_number: %+v", problems)
	}
	// The error the issue is refused with names the field.
	err = ProblemsError(problems)
	if err == nil || !strings.Contains(err.Error(), "seller_registration_number") {
		t.Fatalf("the refusal does not name the field: %v", err)
	}
	// A complete document validates clean, so the test above cannot pass on
	// a validator that refuses everything.
	if got := p.Validate(mustBuild(t, p, sampleInput())); len(got) != 0 {
		t.Fatalf("a complete document was refused: %+v", got)
	}
}

// A zero tax line with no explanation is the defect a tax auditor looks for
// first, so validation refuses it.
func TestValidationRequiresANoteOnAZeroRatedLine(t *testing.T) {
	p := mustProfile(t, ecKeyPEM(t))
	in := sampleInput()
	in.Statement.TaxSubtotals[1].Note = ""
	problems := p.Validate(mustBuild(t, p, in))
	found := false
	for _, pr := range problems {
		if strings.HasPrefix(pr.Field, "tax_subtotals[1]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a zero-rated line with no note validated clean: %+v", problems)
	}
}

// NO SIGNING KEY is a validation problem, not a mystery at signing time: an
// issue is refused with a sentence naming the variable to set.
func TestNoSigningKeyIsAValidationProblem(t *testing.T) {
	p, err := New(Config{Profile: ProfileOman})
	if err != nil {
		t.Fatal(err)
	}
	problems := p.Validate(mustBuild(t, p, sampleInput()))
	found := false
	for _, pr := range problems {
		if pr.Field == "signature" && strings.Contains(pr.Message, "EINVOICE_SIGNER_PATH") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a keyless profile validated clean: %+v", problems)
	}
	if _, err := p.Sign(mustBuild(t, p, sampleInput())); err == nil {
		t.Fatal("a keyless profile signed something")
	}
}

// THE SIGNATURE VERIFIES against its public key — for both key types the
// profile accepts — and the ARCHIVE holds the hash of exactly what was
// signed.
func TestSignatureVerifiesAndTheHashCoversTheCanonicalBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  []byte
		alg  string
	}{
		{"ecdsa", ecKeyPEM(t), AlgECDSASHA256},
		{"rsa", rsaKeyPEM(t), AlgRSASHA256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustProfile(t, tc.key)
			signed, err := p.Sign(mustBuild(t, p, sampleInput()))
			if err != nil {
				t.Fatal(err)
			}
			if signed.Algorithm != tc.alg {
				t.Fatalf("algorithm = %q, want %q", signed.Algorithm, tc.alg)
			}
			if signed.KeyID != "einvoice-2026" {
				t.Errorf("key id = %q — a rotation must be traceable", signed.KeyID)
			}
			pubPEM, err := p.(*omanProfile).PublicKeyPEM()
			if err != nil {
				t.Fatal(err)
			}
			if err := Verify(pubPEM, signed.Algorithm, signed.Canonical, signed.Signature); err != nil {
				t.Fatalf("the signature does not verify against its own public key: %v", err)
			}
			// Tamper with one byte: the signature must stop verifying, or it
			// is not covering the document at all.
			tampered := append([]byte(nil), signed.Canonical...)
			tampered[len(tampered)/2] ^= 0x01
			if err := Verify(pubPEM, signed.Algorithm, tampered, signed.Signature); err == nil {
				t.Fatal("the signature verified over TAMPERED bytes")
			}

			// The hash is the SHA-256 of exactly the bytes that were signed.
			sum := sha256.Sum256(signed.Canonical)
			if signed.Hash != hex.EncodeToString(sum[:]) {
				t.Fatalf("hash %s is not the digest of the canonical bytes", signed.Hash)
			}
			if signed.Hash != HashHex(signed.Canonical) {
				t.Fatal("HashHex disagrees with the archived hash")
			}

			// THE ARCHIVAL COPY carries the document, the signature and the
			// hash — and NEVER the key. A private key that reached an
			// archive would be the whole compliance story undone.
			xml := string(signed.XML)
			for _, want := range []string{signed.Signature, signed.Hash, signed.Algorithm, "INV-2026-00042", signed.Document.QRPayload} {
				if !strings.Contains(xml, want) {
					t.Errorf("the archival XML does not carry %q", want)
				}
			}
			for _, forbidden := range []string{"PRIVATE KEY", "BEGIN EC", "BEGIN RSA"} {
				if strings.Contains(xml, forbidden) {
					t.Fatalf("the archival XML carries %q — key material must NEVER be archived", forbidden)
				}
			}
			// The signature cannot cover itself: the canonical bytes must not
			// contain it.
			if strings.Contains(string(signed.Canonical), signed.Signature) {
				t.Fatal("the canonical bytes contain the signature they are signed by")
			}
		})
	}
}

// The UBL document carries what a tax authority reads: the parties and their
// registrations, the totals BY RATE with the UBL category codes, and the
// per-line category and percent.
func TestTheDocumentCarriesTheUBLShapedFields(t *testing.T) {
	p := mustProfile(t, ecKeyPEM(t))
	signed, err := p.Sign(mustBuild(t, p, sampleInput()))
	if err != nil {
		t.Fatal(err)
	}
	xml := string(signed.XML)
	for _, want := range []string{
		"<cbc:ID>INV-2026-00042</cbc:ID>",
		"<cbc:IssueDate>2026-09-01</cbc:IssueDate>",
		"<cbc:InvoiceTypeCode>388</cbc:InvoiceTypeCode>",
		"<cbc:DocumentCurrencyCode>OMR</cbc:DocumentCurrencyCode>",
		"OM1100012345", "OM1100098765",
		// Two tax subtotals, standard (S) and zero-rated (Z).
		"<cbc:ID>S</cbc:ID>", "<cbc:ID>Z</cbc:ID>",
		"<cbc:Percent>5.00</cbc:Percent>", "<cbc:Percent>0.00</cbc:Percent>",
		`<cbc:TaxableAmount currencyID="OMR">360.000000</cbc:TaxableAmount>`,
		`<cbc:TaxAmount currencyID="OMR">18.000000</cbc:TaxAmount>`,
		`<cbc:TaxInclusiveAmount currencyID="OMR">486.000000</cbc:TaxInclusiveAmount>`,
		"Zero-rated supply under the Executive Regulation.",
		"ecs.c7.large", "evs.ssd",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("the e-invoice XML does not carry %q\n---\n%s", want, xml)
		}
	}
}

// A percent is a percent: 0.0500 renders as 5.00, not as 0.05 and not as
// 0.0500 — a UBL Percent of 0.05 would read as five hundredths of a percent.
func TestPercentRendersTheRateAsAPercentage(t *testing.T) {
	for _, tc := range []struct{ rate, want string }{
		{"0.05", "5.00"}, {"0.0500", "5.00"}, {"0", "0.00"}, {"", "0.00"},
		{"0.15", "15.00"}, {"0.075", "7.50"}, {"1", "100.00"}, {"0.0825", "8.25"},
	} {
		if got := percentOf(tc.rate); got != tc.want {
			t.Errorf("percentOf(%q) = %q, want %q", tc.rate, got, tc.want)
		}
	}
}

// SUBMIT is an honest no-op. Oman's model has no endpoint an issuer posts to,
// and the receipt says so rather than inventing one or failing silently.
func TestSubmitReportsNotSubmittedWithTheReason(t *testing.T) {
	p := mustProfile(t, ecKeyPEM(t))
	signed, err := p.Sign(mustBuild(t, p, sampleInput()))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Submit(context.Background(), signed)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != StatusNotSubmitted {
		t.Fatalf("status = %q, want %q", receipt.Status, StatusNotSubmitted)
	}
	if strings.TrimSpace(receipt.Reason) == "" {
		t.Fatal("not submitted with NO reason — the reason is the whole point")
	}
	for _, want := range []string{"accredited service provider", "publishes no endpoint"} {
		if !strings.Contains(receipt.Reason, want) {
			t.Errorf("the reason does not mention %q: %q", want, receipt.Reason)
		}
	}
}

func mustBuild(t *testing.T, p Profile, in Input) Document {
	t.Helper()
	doc, err := p.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}
