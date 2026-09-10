package external

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func sampleStatement() (store.Statement, store.Customer) {
	issued := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	due := issued.AddDate(0, 0, 30)
	src := "src-1"
	st := store.Statement{ID: "1a2b3c4d-0000-0000-0000-000000000000", CustomerID: "c-1", PeriodStart: "2026-05-01", PeriodEnd: "2026-05-31", Currency: "OMR",
		Subtotal: "1000.100000", TaxRate: "0.0500", Tax: "50.005000", Total: "1050.105000", Status: "issued", IssuedAt: &issued, DueAt: &due, CreatedAt: issued,
		Lines:       []store.RatedLine{{SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "744", UnitPrice: "1.344489", Amount: "1000.100000", ResourceCount: 2, SourceID: &src}},
		TaxSnapshot: &store.TaxSnapshot{Rate: "0.0500", CustomerTaxNumber: "OM2200000002"}}
	c := store.Customer{ID: "c-1", Slug: "omantel-corp", Name: "Corporate", ExternalAccountID: "BA-99001"}
	return st, c
}

// Every document type writes one CSV, named by type and idempotency key,
// with exact decimals and no floats; a redelivery overwrites its own file.
func TestCSVFileWritesOneFilePerDocument(t *testing.T) {
	dir := t.TempDir()
	e := &CSVFile{Dir: dir}
	st, c := sampleStatement()
	usage, _ := Wrap(DocRatedUsage, st.ID, BuildRatedUsage(st, c))
	summary, err := BuildSummaryCharge(st, c, "INV-2026-00007")
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := Wrap(DocSummaryCharge, st.ID, summary)
	pay, _ := Wrap(DocPayment, "payment-7", BuildPayment(store.Payment{ID: 7, Amount: "1050.105000", PaidAt: st.IssuedAt.AddDate(0, 0, 3), Method: "transfer", Reference: "BANK-1", Status: "received", Purpose: "collection",
		Allocations: []store.Allocation{{StatementID: st.ID, InvoiceNumber: "INV-2026-00007", Amount: "1050.105000"}}}, c, "OMR"))
	acc := BuildAccount(c, store.AccountBalance{Balance: "-10.500000", AvailableCredit: "10.500000", Outstanding: "0.000000"}, "OMR", *st.IssuedAt)
	account, _ := Wrap(DocAccount, acc.IdempotencyKey, acc)
	for _, env := range []Envelope{usage, sum, pay, account} {
		ref, err := e.DeliverDocument(context.Background(), env)
		if err != nil {
			t.Fatalf("%s: %v", env.Type, err)
		}
		if !strings.HasPrefix(ref, env.Type+"-") {
			t.Fatalf("%s ref = %s", env.Type, ref)
		}
		if _, err := os.Stat(filepath.Join(dir, ref+".csv")); err != nil {
			t.Fatalf("%s file: %v", env.Type, err)
		}
		// Twice is one file.
		if again, err := e.DeliverDocument(context.Background(), env); err != nil || again != ref {
			t.Fatalf("redelivery of %s = %s (err %v)", env.Type, again, err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.csv"))
	if len(files) != 4 {
		t.Fatalf("files = %v, want 4", files)
	}
	// The summary charge is ONE line quoting our invoice number and exact money.
	f, _ := os.Open(filepath.Join(dir, Ref(sum)+".csv"))
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil || len(rows) != 2 {
		t.Fatalf("summary rows = %v (err %v)", rows, err)
	}
	row := map[string]string{}
	for i, h := range rows[0] {
		row[h] = rows[1][i]
	}
	if row["reference"] != "INV-2026-00007" || row["billing_account_id"] != "BA-99001" || row["tax_included_amount"] != "1050.105000" || row["tax_amount"] != "50.005000" || row["currency"] != "OMR" || row["period_start"] != "2026-05-01" || row["customer_tax_registration_number"] != "OM2200000002" {
		t.Fatalf("summary row = %+v", row)
	}
	// The rated usage carries one row per line plus the total, exact.
	f, _ = os.Open(filepath.Join(dir, Ref(usage)+".csv"))
	rows, _ = csv.NewReader(f).ReadAll()
	f.Close()
	if len(rows) != 3 || rows[1][1] != "usage" || rows[1][14] != "1000.100000" || rows[2][1] != "total" {
		t.Fatalf("usage rows = %v", rows)
	}
	// Money never became a float on the way out.
	raw, _ := json.Marshal(summary)
	if strings.Contains(string(raw), "1050.105000000001") || !strings.Contains(string(raw), `"value":1050.105000`) {
		t.Fatalf("summary JSON = %s", raw)
	}
}

// A summary charge needs the account and the number: without either it is
// refused rather than exported half-attributed.
func TestSummaryChargeRefusesWithoutAccountOrNumber(t *testing.T) {
	st, c := sampleStatement()
	if _, err := BuildSummaryCharge(st, c, ""); err == nil {
		t.Fatal("no invoice number must be refused")
	}
	c.ExternalAccountID = ""
	if _, err := BuildSummaryCharge(st, c, "INV-1"); err == nil {
		t.Fatal("no billing account must be refused")
	}
}

type recApplier struct {
	cmds []Command
	fail string
}

func (r *recApplier) ApplyCommand(_ context.Context, cmd Command) error {
	r.cmds = append(r.cmds, cmd)
	if cmd.Kind == r.fail {
		return os.ErrInvalid
	}
	return nil
}

// The directory poller applies each command file once, in name order, and
// moves it aside: processed, or failed with its error beside it.
func TestDirectoryPollerAppliesEachCommandOnce(t *testing.T) {
	dir := t.TempDir()
	write := func(name, kind, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"kind":"`+kind+`","body":`+body+`}`), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("002-payment.json", CmdPaymentStatus, `{"external_account_id":"BA-1","amount":10,"reference":"R1","status":"settled"}`)
	write("001-enforce.json", CmdEnforcement, `{"external_account_id":"BA-1","action":"suspend","reason":"unpaid"}`)
	write("003-bad.json", CmdAccountBalance, `{"external_account_id":"BA-1","balance":5}`)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o640); err != nil {
		t.Fatal(err)
	}
	app := &recApplier{fail: CmdAccountBalance}
	p := &DirectoryPoller{Dir: dir, Applier: app}
	ok, failed, err := p.Poll(context.Background())
	if err != nil || ok != 2 || failed != 1 {
		t.Fatalf("poll = ok %d failed %d err %v", ok, failed, err)
	}
	if len(app.cmds) != 3 || app.cmds[0].Kind != CmdEnforcement || app.cmds[1].Kind != CmdPaymentStatus || app.cmds[2].Kind != CmdAccountBalance {
		t.Fatalf("applied in name order: %+v", app.cmds)
	}
	if _, err := os.Stat(filepath.Join(dir, "processed", "001-enforce.json")); err != nil {
		t.Fatal("processed file must be moved aside")
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "003-bad.json.error")); err != nil {
		t.Fatal("a failed file carries its error beside it")
	}
	// A second poll finds nothing: nothing is applied twice.
	ok, failed, err = p.Poll(context.Background())
	if err != nil || ok != 0 || failed != 0 || len(app.cmds) != 3 {
		t.Fatalf("second poll = ok %d failed %d cmds %d (err %v)", ok, failed, len(app.cmds), err)
	}
	// No directory configured is a named condition, not a crash.
	if _, _, err := (&DirectoryPoller{}).Poll(context.Background()); err != ErrPollerNotConfigured {
		t.Fatalf("unconfigured = %v", err)
	}
}

// The vocabularies a billing system uses map onto ours; anything else is
// refused with the word it sent.
func TestInboundVocabularies(t *testing.T) {
	for in, want := range map[string]string{"settled": store.PaymentReceived, "SUCCEEDED": store.PaymentReceived, "pending": store.PaymentPending, "declined": store.PaymentFailed, "charged_back": store.PaymentRefunded, "": store.PaymentReceived} {
		if got, err := MapPaymentStatus(in); err != nil || got != want {
			t.Errorf("MapPaymentStatus(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	if _, err := MapPaymentStatus("teleported"); err == nil {
		t.Error("an unknown payment status must be refused")
	}
	for in, want := range map[string]string{"suspend": "suspend", "Barred": "suspend", "resume": "resume", "reactivate": "resume"} {
		if got, err := MapEnforcement(in); err != nil || got != want {
			t.Errorf("MapEnforcement(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	if _, err := MapEnforcement("paid"); err == nil {
		t.Error("a payment status is never an enforcement command")
	}
}
