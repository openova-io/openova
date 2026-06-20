package handlers

// Tests for the catalyst.usage.recorded subscriber (#798 §B). The
// consumer is exercised through Handle() so the test does not need a
// running NATS server — that's a deliberate choice: bringing up
// embedded NATS for a unit test would slow CI by an order of magnitude
// and the broker-side semantics (durable consumer, AckWait, redelivery)
// are JetStream's contract, not ours. What we DO test is everything
// that lives inside our service boundary: payload validation,
// idempotency dedup, customer auto-create, balance computation, the
// "non-negative amount" guard, and the malformed-payload poison-pill
// path.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// fakeResolver returns a static customer (or error) without touching
// the store. Used to isolate the consumer's logic from the auto-create
// path's DB interactions.
type fakeResolver struct {
	cust *store.Customer
	err  error
}

func (f fakeResolver) Resolve(_ context.Context, _, _ string) (*store.Customer, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.cust, nil
}

// makeEnvelope builds a canonical UsageRecordedPayload with realistic
// values so the test does not have to repeat the fields per case.
func makeEnvelope(reqID string, microOMR int64) events.UsageRecordedPayload {
	return events.UsageRecordedPayload{
		CustomerID:     "user-uuid-1",
		AmountOMR:      float64(microOMR) / 1_000_000,
		AmountMicroOMR: microOMR,
		Reason:         "usage:newapi:qwen3-coder",
		Metadata: events.UsageRecordedMetadata{
			TokensUsed:  1500,
			Model:       "qwen3-coder",
			RequestID:   reqID,
			TenantID:    "tenant-acme",
			LatencyMS:   423,
			CompletedAt: "2026-05-04T17:00:00Z",
		},
	}
}

// expectInsertReturning expects the RecordUsage transaction (BEGIN +
// INSERT … RETURNING + SELECT balance + COMMIT) and returns the
// supplied ledger id, "existed" flag, and balance.
func expectInsertReturning(mock sqlmock.Sqlmock, customerID, ledgerID string, micro int64, requestID string, existed bool, balanceMicro int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO credit_ledger")).
		WithArgs(customerID, micro, "usage:newapi:qwen3-coder", requestID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "existed"}).AddRow(ledgerID, existed))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT) * 1000000")).
		WithArgs(customerID).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(balanceMicro))
	mock.ExpectCommit()
}

// TestMeteringConsumer_Handle_HappyPath: an envelope with a previously
// unseen request_id results in one ledger insert + balance read; the
// returned balanceAfterMicroOMR is logged but the function returns nil.
func TestMeteringConsumer_Handle_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	c := &MeteringConsumer{
		Store: store.New(db),
		CustomerResolver: fakeResolver{
			cust: &store.Customer{ID: "cust-uuid", UserID: "user-uuid-1", TenantID: "tenant-acme"},
		},
	}

	expectInsertReturning(mock, "cust-uuid", "ledger-1", -234, "req-1", false, -234)

	body, _ := json.Marshal(makeEnvelope("req-1", -234))
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestMeteringConsumer_Handle_DuplicateIsIdempotent: the same
// request_id arriving twice must collapse to a single insert. The
// second call still completes the transaction (INSERT … ON CONFLICT
// DO UPDATE returns the row) but reports duplicate=true via the log
// path; from the broker's perspective, it's an Ack.
func TestMeteringConsumer_Handle_DuplicateIsIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	c := &MeteringConsumer{
		Store: store.New(db),
		CustomerResolver: fakeResolver{
			cust: &store.Customer{ID: "cust-uuid", UserID: "user-uuid-1", TenantID: "tenant-acme"},
		},
	}

	// First delivery — INSERT happens, returns existed=false.
	expectInsertReturning(mock, "cust-uuid", "ledger-1", -234, "req-dup", false, -234)
	// Second delivery — ON CONFLICT path, returns existed=true, balance unchanged.
	expectInsertReturning(mock, "cust-uuid", "ledger-1", -234, "req-dup", true, -234)

	body, _ := json.Marshal(makeEnvelope("req-dup", -234))
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("second Handle (duplicate): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestMeteringConsumer_Handle_MalformedPayloadAcks: a body that is not
// valid JSON returns nil so the broker Acks. The consumer MUST NOT
// hot-loop on a poison pill.
func TestMeteringConsumer_Handle_MalformedPayloadAcks(t *testing.T) {
	c := &MeteringConsumer{}
	if err := c.Handle(context.Background(), []byte("{not valid json")); err != nil {
		t.Fatalf("malformed payload should ack with nil, got %v", err)
	}
}

// TestMeteringConsumer_Handle_MissingRequestIDAcks: an envelope with an
// empty metadata.request_id cannot be deduplicated and is therefore
// untrustworthy. We log + ack to skip rather than insert without the
// idempotency guard.
func TestMeteringConsumer_Handle_MissingRequestIDAcks(t *testing.T) {
	c := &MeteringConsumer{}
	env := makeEnvelope("", -234)
	body, _ := json.Marshal(env)
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("missing request_id should ack with nil, got %v", err)
	}
}

// TestMeteringConsumer_Handle_NonNegativeAmountAcks: a usage envelope
// with a positive (or zero) amount is suspicious — top-ups go through
// the checkout/redeem path, never through metering. We ack to skip.
func TestMeteringConsumer_Handle_NonNegativeAmountAcks(t *testing.T) {
	c := &MeteringConsumer{}
	env := makeEnvelope("req-bad", 100) // positive
	body, _ := json.Marshal(env)
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("positive amount should ack with nil, got %v", err)
	}

	env = makeEnvelope("req-zero", 0) // zero with zero AmountOMR
	env.AmountOMR = 0
	body, _ = json.Marshal(env)
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("zero amount should ack with nil, got %v", err)
	}
}

// TestMeteringConsumer_Handle_ResolverErrorNaks: when CustomerResolver
// errors (e.g., "user not found and no tenant_id"), the handler MUST
// return that error so JetStream redelivers after AckWait. The
// rbac-side user-create may land in the meantime.
func TestMeteringConsumer_Handle_ResolverErrorNaks(t *testing.T) {
	c := &MeteringConsumer{
		CustomerResolver: fakeResolver{err: errors.New("user not found")},
	}
	env := makeEnvelope("req-resolve-fail", -234)
	body, _ := json.Marshal(env)
	if err := c.Handle(context.Background(), body); err == nil {
		t.Fatal("expected resolver error to surface so the broker redelivers, got nil")
	}
}

// TestMeteringConsumer_Handle_DerivesMicroOMRFromOMR: a sidecar that
// publishes only AmountOMR (the human-readable field) must result in
// the same insert as one that publishes AmountMicroOMR. round-half-
// away-from-zero applies — -0.0000005 OMR rounds to -1 micro-OMR.
func TestMeteringConsumer_Handle_DerivesMicroOMRFromOMR(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	c := &MeteringConsumer{
		Store: store.New(db),
		CustomerResolver: fakeResolver{
			cust: &store.Customer{ID: "cust-uuid", UserID: "user-uuid-1", TenantID: "tenant-acme"},
		},
	}

	// Sidecar publishes only AmountOMR = -0.000234 → -234 micro-OMR.
	expectInsertReturning(mock, "cust-uuid", "ledger-derive", -234, "req-derive", false, -234)

	env := events.UsageRecordedPayload{
		CustomerID:     "user-uuid-1",
		AmountOMR:      -0.000234,
		AmountMicroOMR: 0, // not set
		Reason:         "usage:newapi:qwen3-coder",
		Metadata: events.UsageRecordedMetadata{
			TokensUsed: 1500, Model: "qwen3-coder",
			RequestID: "req-derive", TenantID: "tenant-acme",
		},
	}
	body, _ := json.Marshal(env)
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestMeteringConsumer_Handle_DBErrorNaks: a transient DB failure
// during INSERT must propagate so the broker redelivers.
func TestMeteringConsumer_Handle_DBErrorNaks(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	c := &MeteringConsumer{
		Store: store.New(db),
		CustomerResolver: fakeResolver{
			cust: &store.Customer{ID: "cust-uuid", UserID: "user-uuid-1", TenantID: "tenant-acme"},
		},
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO credit_ledger")).
		WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	body, _ := json.Marshal(makeEnvelope("req-dberr", -234))
	if err := c.Handle(context.Background(), body); err == nil {
		t.Fatal("expected DB error to propagate (Nak), got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestMeteringConsumer_HumanReason: the helper formats the reason
// string consistently with the issue spec.
func TestMeteringConsumer_HumanReason(t *testing.T) {
	cases := []struct {
		model, prefix, want string
	}{
		{"qwen3-coder", "", "usage:newapi:qwen3-coder"},
		{"  Qwen3-Coder  ", "", "usage:newapi:qwen3-coder"},
		{"", "", "usage:newapi:unknown"},
		{"claude-sonnet-4-6", "usage:openclaw", "usage:openclaw:claude-sonnet-4-6"},
	}
	for _, tc := range cases {
		got := HumanReason(tc.model, tc.prefix)
		if got != tc.want {
			t.Errorf("HumanReason(%q,%q) = %q, want %q", tc.model, tc.prefix, got, tc.want)
		}
	}
}

// TestDefaultCustomerResolver_LooksUpExisting: the production resolver
// returns the existing customer row when GetCustomerByUserID finds one.
// Auto-create is NOT exercised on the happy path.
func TestDefaultCustomerResolver_LooksUpExisting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("FROM customers WHERE user_id = $1")).
		WithArgs("user-uuid-9").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "tenant_id", "stripe_customer_id", "email", "created_at",
		}).AddRow("cust-uuid-9", "user-uuid-9", "tenant-existing", nil, "x@example.com", time.Now()))

	r := DefaultCustomerResolver{Store: store.New(db)}
	cust, err := r.Resolve(context.Background(), "user-uuid-9", "tenant-existing")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cust == nil || cust.ID != "cust-uuid-9" {
		t.Fatalf("expected cust-uuid-9, got %#v", cust)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestDefaultCustomerResolver_AutoCreatesOnMiss: when no customer
// matches the userID, the resolver auto-creates one. This is the
// cold-start path for SME-tier provisioning where the first metered
// LLM call may arrive before the rbac org.user.created envelope.
func TestDefaultCustomerResolver_AutoCreatesOnMiss(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	// Lookup misses.
	mock.ExpectQuery(regexp.QuoteMeta("FROM customers WHERE user_id = $1")).
		WithArgs("user-new").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "tenant_id", "stripe_customer_id", "email", "created_at",
		}))
	// Auto-create.
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO customers")).
		WithArgs("user-new", "tenant-new", nil, "").
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("cust-auto", time.Now()))

	r := DefaultCustomerResolver{Store: store.New(db)}
	cust, err := r.Resolve(context.Background(), "user-new", "tenant-new")
	if err != nil {
		t.Fatalf("Resolve auto-create: %v", err)
	}
	if cust.ID != "cust-auto" {
		t.Fatalf("expected cust-auto, got %s", cust.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestDefaultCustomerResolver_NoTenantNoAutoCreate: missing tenant_id
// makes auto-create impossible — we surface a transient error so the
// broker redelivers. The resolver MUST NOT silently insert with an
// empty tenant_id (would violate the customers.tenant_id NOT NULL).
func TestDefaultCustomerResolver_NoTenantNoAutoCreate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("FROM customers WHERE user_id = $1")).
		WithArgs("user-orphan").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "tenant_id", "stripe_customer_id", "email", "created_at",
		}))

	r := DefaultCustomerResolver{Store: store.New(db)}
	if _, err := r.Resolve(context.Background(), "user-orphan", ""); err == nil {
		t.Fatal("expected error when tenant_id missing, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
