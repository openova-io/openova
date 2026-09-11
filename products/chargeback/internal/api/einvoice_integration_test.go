package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/einvoice"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// E-invoicing at issue (DESIGN.md §17): the pre-flight refusal, the archived
// document, and the two read routes.

// signingKeyFile writes a throwaway EC key to a temp file — the same shape
// the chart mounts as a Secret. A key generated per test never leaves the
// process and never reaches a fixture.
func signingKeyFile(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "einvoice.key")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setupEInvoiceAPI(t *testing.T, keyFile string) (http.Handler, *store.Store) {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{7}, 32))
	h := New(Deps{
		Store:   st,
		Keys:    keys,
		Mail:    &recMail{},
		Metrics: metrics.New(),
		Version: "test",
		Config: config.Config{
			PublicURL: "https://billing.t99.omani.works", Profile: "operator-central", OperatorEmails: []string{opEmail},
			EInvoiceProfile: einvoice.ProfileOman, EInvoiceSigningKeyFile: keyFile, EInvoiceKeyID: "einvoice-2026",
		},
	})
	return h, st
}

// sellerIdentity sets the Sovereign's legal identity, minus whatever the
// caller wants missing.
func sellerIdentity(t *testing.T, st *store.Store, registration string) {
	t.Helper()
	ctx := context.Background()
	cur, err := st.GetBillingSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := cur
	next.LegalName = "Sovereign Cloud Operator LLC"
	next.Address = "Knowledge Oasis Muscat\nMuscat 130, Oman"
	next.TaxCountry = "OM"
	next.TaxRegistrationNumber = registration
	if _, err := st.UpdateBillingSettings(ctx, next); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := st.UpdateBillingSettings(context.Background(), store.DefaultBillingSettings()); err != nil {
			t.Errorf("reset billing settings: %v", err)
		}
	})
}

func eInvoiceCustomer(t *testing.T, st *store.Store, slug string) store.Customer {
	t.Helper()
	c, err := st.CreateCustomer(context.Background(), store.CustomerInput{
		Slug: slug, Name: strings.ToUpper(slug), AdminEmail: slug + "@acme.omani.homes",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer},
		Tax:        store.TaxProfile{TaxRegistrationNumber: "OM1100098765", TaxCountry: "OM"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// THE REFUSAL. An issue that cannot yield a compliant e-invoice is refused
// BEFORE anything is numbered, naming every problem — exactly as the
// commercial outbox refuses a malformed export.
func TestIntegrationIssueIsRefusedWhenTheEInvoiceIsNotValid(t *testing.T) {
	h, st := setupEInvoiceAPI(t, signingKeyFile(t))
	ctx := context.Background()
	op := operatorSession()
	sellerIdentity(t, st, "") // the Sovereign's registration number is NOT set
	c := eInvoiceCustomer(t, st, "refused")
	d := draftFor(t, st, c.ID, "2026-08-01", "100.000000")

	rec := do(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", `{"notify":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("issue = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "seller_registration_number") {
		t.Fatalf("the refusal does not name the missing field: %s", body)
	}
	if !strings.Contains(body, "tax registration number is not set") {
		t.Fatalf("the refusal does not say what to do: %s", body)
	}
	// NOTHING happened: the statement is still a draft, unnumbered, and the
	// gapless per-year sequence was not spent.
	still, err := st.GetStatement(ctx, store.OperatorScope, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != store.StatusDraft || still.InvoiceNumber != "" {
		t.Fatalf("a refused issue changed the statement: status %q number %q", still.Status, still.InvoiceNumber)
	}
	if !hasAction(customerAuditActions(t, st, c.ID), "statement.einvoice.refused") {
		t.Errorf("the refusal was not audited")
	}

	// Fix the gap and the SAME statement issues.
	sellerIdentity(t, st, "OM1100012345")
	if rec := do(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", `{"notify":false}`); rec.Code != http.StatusOK {
		t.Fatalf("issue after the fix = %d: %s", rec.Code, rec.Body.String())
	}
}

// THE HAPPY PATH: built, validated, signed, archived — and, because Oman
// publishes no clearance endpoint, NOT SUBMITTED with the reason. Both read
// routes answer.
func TestIntegrationIssueBuildsSignsAndArchivesTheEInvoice(t *testing.T) {
	h, st := setupEInvoiceAPI(t, signingKeyFile(t))
	ctx := context.Background()
	op := operatorSession()
	sellerIdentity(t, st, "OM1100012345")
	c := eInvoiceCustomer(t, st, "archived")
	d := draftFor(t, st, c.ID, "2026-08-01", "100.000000")

	if rec := do(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", `{"notify":false}`); rec.Code != http.StatusOK {
		t.Fatalf("issue = %d: %s", rec.Code, rec.Body.String())
	}
	issued, err := st.GetStatement(ctx, store.OperatorScope, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := issued.EInvoice
	if state == nil {
		t.Fatal("the issued statement carries no e-invoice state")
	}
	if state.Profile != einvoice.ProfileOman {
		t.Errorf("profile = %q", state.Profile)
	}
	if state.State != store.EInvoiceNotSubmitted {
		t.Fatalf("state = %q, want not_submitted — Oman has published no clearance endpoint", state.State)
	}
	if strings.TrimSpace(state.SubmitReason) == "" {
		t.Fatal("not submitted with NO reason")
	}
	if !strings.Contains(state.SubmitReason, "accredited service provider") {
		t.Errorf("the reason does not name the model: %q", state.SubmitReason)
	}
	if state.Hash == "" || len(state.Hash) != 64 {
		t.Errorf("hash = %q, want a SHA-256 hex digest", state.Hash)
	}
	if state.Signature == "" || state.SignatureAlgorithm != einvoice.AlgECDSASHA256 || state.KeyID != "einvoice-2026" {
		t.Errorf("signature = %q alg %q key %q", state.Signature, state.SignatureAlgorithm, state.KeyID)
	}
	if state.QRPayload == "" {
		t.Fatal("no QR payload was attached to the statement")
	}
	// The QR payload DECODES to the five tagged fields, with the issued
	// invoice's own total — not a stored blob nobody has read.
	fields, err := einvoice.DecodeTLVBase64(state.QRPayload)
	if err != nil {
		t.Fatalf("the attached QR payload does not decode: %v", err)
	}
	if len(fields) != 5 {
		t.Fatalf("the QR carries %d fields, want 5", len(fields))
	}
	if fields[einvoice.TagSellerName-1].Value != "Sovereign Cloud Operator LLC" {
		t.Errorf("tag 1 = %q", fields[0].Value)
	}
	if fields[einvoice.TagSellerRegistration-1].Value != "OM1100012345" {
		t.Errorf("tag 2 = %q", fields[1].Value)
	}
	if fields[einvoice.TagTotalIncludingTax-1].Value != string(issued.Total) {
		t.Errorf("tag 4 = %q, want the invoice total %q", fields[3].Value, issued.Total)
	}
	if fields[einvoice.TagTaxTotal-1].Value != string(issued.Tax) {
		t.Errorf("tag 5 = %q, want the tax %q", fields[4].Value, issued.Tax)
	}

	// GET /statements/{id}/einvoice — the structured document.
	rec := do(t, h, op, "GET", "/api/v1/statements/"+d.ID+"/einvoice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET einvoice = %d: %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		StatementID string `json:"statement_id"`
		State       struct {
			State     string `json:"state"`
			Hash      string `json:"hash"`
			QRPayload string `json:"qr_payload"`
		} `json:"state"`
		Document map[string]any `json:"document"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.StatementID != d.ID || doc.State.Hash != state.Hash {
		t.Fatalf("document = %+v", doc)
	}
	if doc.Document["id"] != issued.InvoiceNumber {
		t.Errorf("the document's id = %v, want the invoice number %q", doc.Document["id"], issued.InvoiceNumber)
	}
	if doc.Document["seller_registration_number"] != "OM1100012345" {
		t.Errorf("the document's seller registration = %v", doc.Document["seller_registration_number"])
	}
	// The key NEVER appears in a response.
	if strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatal("the e-invoice response carries key material")
	}

	// GET /statements/{id}/einvoice.xml — the signed archival copy.
	xml := do(t, h, op, "GET", "/api/v1/statements/"+d.ID+"/einvoice.xml", "")
	if xml.Code != http.StatusOK {
		t.Fatalf("GET einvoice.xml = %d: %s", xml.Code, xml.Body.String())
	}
	if ct := xml.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content type = %q", ct)
	}
	if cd := xml.Header().Get("Content-Disposition"); !strings.Contains(cd, issued.InvoiceNumber) {
		t.Errorf("content disposition = %q", cd)
	}
	body := xml.Body.String()
	for _, want := range []string{"<Invoice", issued.InvoiceNumber, state.Signature, state.Hash, state.QRPayload} {
		if !strings.Contains(body, want) {
			t.Errorf("the archival XML does not carry %q", want)
		}
	}
	if strings.Contains(body, "PRIVATE KEY") {
		t.Fatal("the archival XML carries key material")
	}

	// Re-issuing is idempotent and leaves ONE archive row.
	if rec := do(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", `{"notify":false}`); rec.Code != http.StatusOK {
		t.Fatalf("re-issue = %d: %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM einvoice_documents WHERE statement_id = $1`, d.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("archive rows after a re-issue = %d err=%v", n, err)
	}
}

// WITHOUT a profile configured, nothing changes: the statement issues, no
// document is built, and the two routes answer 404 rather than inventing one.
func TestIntegrationNoProfileLeavesIssuingExactlyAsItWas(t *testing.T) {
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{7}, 32))
	h := New(Deps{Store: st, Keys: keys, Mail: &recMail{}, Metrics: metrics.New(), Version: "test",
		Config: config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "operator-central", OperatorEmails: []string{opEmail}}})
	op := operatorSession()
	c := eInvoiceCustomer(t, st, "noprofile")
	d := draftFor(t, st, c.ID, "2026-08-01", "100.000000")

	if rec := do(t, h, op, "POST", "/api/v1/statements/"+d.ID+"/issue", `{"notify":false}`); rec.Code != http.StatusOK {
		t.Fatalf("issue = %d: %s", rec.Code, rec.Body.String())
	}
	issued, err := st.GetStatement(context.Background(), store.OperatorScope, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Status != store.StatusIssued || issued.InvoiceNumber == "" {
		t.Fatalf("issued = %+v", issued)
	}
	if issued.EInvoice != nil {
		t.Fatalf("an e-invoice was built with no profile configured: %+v", issued.EInvoice)
	}
	if rec := do(t, h, op, "GET", "/api/v1/statements/"+d.ID+"/einvoice", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET einvoice with no profile = %d, want 404", rec.Code)
	}
}

// The tax rules API: read is metering.read, every write is settings.manage,
// and the messages are the store's.
func TestIntegrationTaxRulesAPI(t *testing.T) {
	h, st := setupEInvoiceAPI(t, signingKeyFile(t))
	op := operatorSession()
	_ = st

	rec := do(t, h, op, "POST", "/api/v1/tax/rules", `{"name":"Oman VAT standard","country":"om","rate":"0.05","kind":"standard","effective_from":"2021-04-16"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created store.TaxRule
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Country != "OM" || string(created.Rate) != "0.0500" {
		t.Fatalf("created = %+v", created)
	}
	// A duplicate key is 409, an invalid rule 400 with the reason.
	if rec := do(t, h, op, "POST", "/api/v1/tax/rules", `{"name":"Dup","country":"OM","rate":"0.10","kind":"standard","effective_from":"2021-04-16"}`); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d: %s", rec.Code, rec.Body.String())
	}
	bad := do(t, h, op, "POST", "/api/v1/tax/rules", `{"name":"Exempt with a rate","country":"AE","rate":"0.05","kind":"exempt","effective_from":"2021-04-16"}`)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "rate must be 0") {
		t.Fatalf("exempt with a rate = %d: %s", bad.Code, bad.Body.String())
	}

	list := do(t, h, op, "GET", "/api/v1/tax/rules", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "reverse_charge") {
		t.Fatalf("list = %d: %s", list.Code, list.Body.String())
	}

	if rec := do(t, h, op, "PUT", "/api/v1/tax/rules/"+created.ID,
		`{"name":"Renamed","country":"OM","rate":"0.05","kind":"standard","note":"Standard-rated supply.","effective_from":"2021-04-16"}`); rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, op, "DELETE", "/api/v1/tax/rules/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, op, "DELETE", "/api/v1/tax/rules/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete twice = %d", rec.Code)
	}

	// Categories.
	if rec := do(t, h, op, "PUT", "/api/v1/tax/categories", `{"sku":"evs.*","category":"storage"}`); rec.Code != http.StatusOK {
		t.Fatalf("put category = %d: %s", rec.Code, rec.Body.String())
	}
	cats := do(t, h, op, "GET", "/api/v1/tax/categories", "")
	if cats.Code != http.StatusOK || !strings.Contains(cats.Body.String(), `"storage"`) {
		t.Fatalf("categories = %d: %s", cats.Code, cats.Body.String())
	}
	if rec := do(t, h, op, "DELETE", "/api/v1/tax/categories/evs.*", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete category = %d: %s", rec.Code, rec.Body.String())
	}

	// A CUSTOMER principal may not read or write the rules.
	cust := &store.Session{Email: "u@acme.example", Role: store.RoleCustomerAdmin, CustomerID: strPtrTest("11111111-1111-1111-1111-111111111111"), ExpiresAt: time.Now().Add(time.Hour)}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/tax/rules", ""},
		{"POST", "/api/v1/tax/rules", `{"name":"x","country":"OM","rate":"0","kind":"standard"}`},
		{"PUT", "/api/v1/tax/categories", `{"sku":"x","category":"y"}`},
	} {
		if rec := do(t, h, cust, tc.method, tc.path, tc.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a customer = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func strPtrTest(s string) *string { return &s }
