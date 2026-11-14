package sandbox

import ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"

// Scheme is the identity scheme of the sandbox's customers. ResolveCustomer maps this scheme and
// a customer's subject (alice, bob or carol) to the customer.
const Scheme = "sandbox"

// The data set: three customers and five accounts, all made up. Sort codes start with 0000 and
// the bank code of the IBANs is SAND, so no value names a real account. Each account's balance
// is its opening amount plus its transactions; New computes it and each transaction's
// balance_after.
//
//	subject  customer   account      currency  type             balance
//	alice    cust-0001  acc-0001-01  GBP       Personal CACC    2443.20
//	alice    cust-0001  acc-0001-02  GBP       Personal SVGS    5204.17
//	bob      cust-0002  acc-0002-01  GBP       Personal CACC    1284.01
//	bob      cust-0002  acc-0002-02  EUR       Personal CACC     370.00
//	carol    cust-0003  acc-0003-01  GBP       Business CACC   20450.00
type customerData struct {
	id, subject string
	party       *ledgerv1.Party
	accounts    []accountData
}

type accountData struct {
	info      *ledgerv1.Account
	opening   string
	party     *ledgerv1.Party
	txns      []*ledgerv1.Transaction
	benefs    []*ledgerv1.Beneficiary
	standing  []*ledgerv1.StandingOrder
	debits    []*ledgerv1.DirectDebit
	scheduled []*ledgerv1.ScheduledPayment
}

const (
	sortCode = "UK.OBIE.SortCodeAccountNumber"
	iban     = "UK.OBIE.IBAN"
)

func money(amount, currency string) *ledgerv1.Money {
	return &ledgerv1.Money{Amount: amount, Currency: currency}
}

// tx is one booked transaction in the account's currency, booked and valued at 08:00 UTC of day.
func tx(id, day, amount, currency, creditDebit, information, reference, counterparty, counterpartyID string) *ledgerv1.Transaction {
	at := day + "T08:00:00Z"
	return &ledgerv1.Transaction{
		TransactionId: id, Amount: money(amount, currency), CreditDebit: creditDebit, Status: "BOOK",
		BookedAt: at, ValueAt: at, Information: information, Reference: reference,
		CounterpartyName: counterparty, CounterpartyIdentification: counterpartyID,
	}
}

func personal(id, currency, subType, nickname, scheme, identification, holder, opened string) *ledgerv1.Account {
	return &ledgerv1.Account{
		AccountId: id, Currency: currency, Type: "Personal", SubType: subType, Nickname: nickname,
		SchemeName: scheme, Identification: identification, HolderName: holder, Status: "Enabled", OpenedAt: opened,
	}
}

// dataSet returns a fresh copy of the data set.
func dataSet() []customerData {
	return []customerData{
		{
			id: "cust-0001", subject: "alice",
			party: &ledgerv1.Party{PartyId: "party-0001", Type: "Sole", Name: "Alice Sample", Email: "alice@example.invalid", Phone: "+440000000001"},
			accounts: []accountData{
				{
					info:    personal("acc-0001-01", "GBP", "CACC", "Everyday", sortCode, "00000110000001", "Alice Sample", "2019-04-01T09:00:00Z"),
					opening: "1000.00",
					party:   &ledgerv1.Party{PartyId: "party-0001-01", Type: "Sole", Name: "Alice Sample"},
					txns: []*ledgerv1.Transaction{
						tx("tx-0001-01-1", "2026-01-02", "2500.00", "GBP", "Credit", "Salary", "JAN SALARY", "Sample Employer Ltd", "00000390000001"),
						tx("tx-0001-01-2", "2026-01-05", "750.00", "GBP", "Debit", "Standing order", "RENT", "Sample Lettings Ltd", "00000290000001"),
						tx("tx-0001-01-3", "2026-01-09", "42.50", "GBP", "Debit", "Direct debit", "ENERGY", "Sample Energy plc", "00000490000001"),
						tx("tx-0001-01-4", "2026-01-12", "64.30", "GBP", "Debit", "Card payment", "GROCER", "Sample Grocer", ""),
						tx("tx-0001-01-5", "2026-01-15", "200.00", "GBP", "Debit", "Transfer to savings", "SAVINGS", "Alice Sample", "00000110000002"),
					},
					benefs: []*ledgerv1.Beneficiary{
						{BeneficiaryId: "ben-0001-01-1", Name: "Sample Lettings Ltd", SchemeName: sortCode, Identification: "00000290000001", Reference: "RENT"},
						{BeneficiaryId: "ben-0001-01-2", Name: "Bob Sample", SchemeName: sortCode, Identification: "00000120000001", Reference: "DINNER"},
					},
					standing: []*ledgerv1.StandingOrder{{
						StandingOrderId: "so-0001-01-1", Frequency: "MNTH", Reference: "RENT", NextAmount: money("750.00", "GBP"),
						NextAt: "2026-02-05T00:00:00Z", Status: "ACTV", CreditorName: "Sample Lettings Ltd",
						CreditorSchemeName: sortCode, CreditorIdentification: "00000290000001",
					}},
					debits: []*ledgerv1.DirectDebit{{
						DirectDebitId: "dd-0001-01-1", MandateId: "MANDATE-0001", Name: "Sample Energy plc", Status: "ACTV",
						PreviousAt: "2026-01-09T00:00:00Z", PreviousAmount: money("42.50", "GBP"),
					}},
					scheduled: []*ledgerv1.ScheduledPayment{{
						ScheduledPaymentId: "sp-0001-01-1", ExecutionAt: "2026-03-01T00:00:00Z", Amount: money("99.00", "GBP"),
						Reference: "INSURANCE", CreditorName: "Sample Insurance Ltd", CreditorSchemeName: sortCode,
						CreditorIdentification: "00000590000001",
					}},
				},
				{
					info:    personal("acc-0001-02", "GBP", "SVGS", "Savings", sortCode, "00000110000002", "Alice Sample", "2021-06-01T09:00:00Z"),
					opening: "5000.00",
					party:   &ledgerv1.Party{PartyId: "party-0001-02", Type: "Sole", Name: "Alice Sample"},
					txns: []*ledgerv1.Transaction{
						tx("tx-0001-02-1", "2026-01-15", "200.00", "GBP", "Credit", "Transfer from current account", "SAVINGS", "Alice Sample", "00000110000001"),
						tx("tx-0001-02-2", "2026-01-31", "4.17", "GBP", "Credit", "Interest", "INTEREST", "", ""),
					},
				},
			},
		},
		{
			id: "cust-0002", subject: "bob",
			party: &ledgerv1.Party{PartyId: "party-0002", Type: "Sole", Name: "Bob Sample", Email: "bob@example.invalid"},
			accounts: []accountData{
				{
					info:    personal("acc-0002-01", "GBP", "CACC", "", sortCode, "00000120000001", "Bob Sample", "2022-03-10T09:00:00Z"),
					opening: "120.00",
					party:   &ledgerv1.Party{PartyId: "party-0002-01", Type: "Sole", Name: "Bob Sample"},
					txns: []*ledgerv1.Transaction{
						tx("tx-0002-01-1", "2026-01-03", "1800.00", "GBP", "Credit", "Salary", "JAN SALARY", "Sample Employer Ltd", "00000390000001"),
						tx("tx-0002-01-2", "2026-01-06", "35.99", "GBP", "Debit", "Direct debit", "PHONE", "Sample Telecom plc", "00000690000001"),
						tx("tx-0002-01-3", "2026-01-20", "600.00", "GBP", "Debit", "Faster payment", "RENT", "Sample Lettings Ltd", "00000290000001"),
					},
					debits: []*ledgerv1.DirectDebit{{
						DirectDebitId: "dd-0002-01-1", MandateId: "MANDATE-0002", Name: "Sample Telecom plc", Status: "ACTV",
						PreviousAt: "2026-01-06T00:00:00Z", PreviousAmount: money("35.99", "GBP"),
					}},
				},
				{
					info:    personal("acc-0002-02", "EUR", "CACC", "Euro", iban, "GB81SAND00000220000002", "Bob Sample", "2023-09-18T09:00:00Z"),
					opening: "300.00",
					party:   &ledgerv1.Party{PartyId: "party-0002-02", Type: "Sole", Name: "Bob Sample"},
					txns: []*ledgerv1.Transaction{
						tx("tx-0002-02-1", "2026-01-10", "150.00", "EUR", "Credit", "Transfer", "GIFT", "Sample Relative", "GB20SAND00000990000001"),
						tx("tx-0002-02-2", "2026-01-22", "80.00", "EUR", "Debit", "Card payment", "TRAVEL", "Sample Rail", ""),
					},
					benefs: []*ledgerv1.Beneficiary{
						{BeneficiaryId: "ben-0002-02-1", Name: "Sample Relative", SchemeName: iban, Identification: "GB20SAND00000990000001", Reference: "GIFT"},
					},
				},
			},
		},
		{
			id: "cust-0003", subject: "carol",
			party: &ledgerv1.Party{PartyId: "party-0003", Type: "Sole", Name: "Carol Sample", Email: "carol@example.invalid"},
			accounts: []accountData{
				{
					info: &ledgerv1.Account{
						AccountId: "acc-0003-01", Currency: "GBP", Type: "Business", SubType: "CACC", Nickname: "Trading",
						SchemeName: sortCode, Identification: "00000130000001", HolderName: "Sample Trading Ltd",
						Status: "Enabled", OpenedAt: "2018-11-05T09:00:00Z",
					},
					opening: "20000.00",
					party:   &ledgerv1.Party{PartyId: "party-0003-01", Type: "Sole", Name: "Sample Trading Ltd"},
					txns: []*ledgerv1.Transaction{
						tx("tx-0003-01-1", "2026-01-04", "4800.00", "GBP", "Credit", "Invoice paid", "INV-1001", "Sample Customer Ltd", "00000790000001"),
						tx("tx-0003-01-2", "2026-01-08", "1250.00", "GBP", "Debit", "Supplier payment", "PO-2001", "Sample Supplies Ltd", "00000890000001"),
						tx("tx-0003-01-3", "2026-01-25", "3100.00", "GBP", "Debit", "Payroll", "PAYROLL JAN", "Sample Payroll Services", "00000990000001"),
					},
					benefs: []*ledgerv1.Beneficiary{
						{BeneficiaryId: "ben-0003-01-1", Name: "Sample Supplies Ltd", SchemeName: sortCode, Identification: "00000890000001", Reference: "SUPPLIES"},
						{BeneficiaryId: "ben-0003-01-2", Name: "Sample Payroll Services", SchemeName: sortCode, Identification: "00000990000001", Reference: "PAYROLL"},
					},
					scheduled: []*ledgerv1.ScheduledPayment{{
						ScheduledPaymentId: "sp-0003-01-1", ExecutionAt: "2026-02-28T00:00:00Z", Amount: money("1500.00", "GBP"),
						Reference: "VAT Q4", CreditorName: "Sample Tax Office", CreditorSchemeName: sortCode,
						CreditorIdentification: "00000990000002",
					}},
				},
			},
		},
	}
}

// Identity is a scheme and a subject, as ResolveCustomer receives them.
type Identity struct {
	Scheme  string `json:"scheme"`
	Subject string `json:"subject"`
}

// Fixture describes the data set to the ledger conformance kit. Its JSON form is the fixture
// file that fingate-ledger-conformance reads with --fixture.
type Fixture struct {
	Customer               Identity `json:"customer"`
	OtherCustomer          Identity `json:"other_customer"`
	DebtorAccountID        string   `json:"debtor_account_id"`
	Amount                 string   `json:"amount"`
	RefusedAmount          string   `json:"refused_amount"`
	CreditorName           string   `json:"creditor_name"`
	CreditorSchemeName     string   `json:"creditor_scheme_name"`
	CreditorIdentification string   `json:"creditor_identification"`
}

// KitFixture is the kit's view of the data set: alice pays one pound from acc-0001-01 to a
// beneficiary of hers, cannot pay a million, and bob is the other customer.
func KitFixture() Fixture {
	return Fixture{
		Customer:               Identity{Scheme: Scheme, Subject: "alice"},
		OtherCustomer:          Identity{Scheme: Scheme, Subject: "bob"},
		DebtorAccountID:        "acc-0001-01",
		Amount:                 "1.00",
		RefusedAmount:          "1000000.00",
		CreditorName:           "Sample Lettings Ltd",
		CreditorSchemeName:     sortCode,
		CreditorIdentification: "00000290000001",
	}
}
