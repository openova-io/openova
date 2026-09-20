package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/lib/pq"
)

// The message a constraint violation carries must say nothing about the
// schema. The live defect was a cost-centre code typed twice, which reached
// the console as
//
//	conflict: Key (customer_id, code)=(9692021e-…, ENG) already exists.
//
// naming two columns, the shape of the table's key and another row's UUID.
// These tests are written against the ABSENCE of that payload, not the
// presence of friendlier words: a message that leaks alongside a kind prefix
// would pass a "contains the nice sentence" assertion and fail these.

// leakTokens are the identifiers a driver error carries that must never
// appear in a message: the Detail as a whole, the table, the constraint, any
// snake_case identifier inside the Detail, any UUID, and the driver's "Key".
var (
	snakeIdent = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*_[A-Za-z0-9_]+`)
	uuidLike   = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
)

// assertNoLeak fails if msg carries anything the driver said.
func assertNoLeak(t *testing.T, msg string, pqe *pq.Error) {
	t.Helper()
	banned := []string{"Key"}
	for _, s := range []string{pqe.Detail, pqe.Table, pqe.Constraint} {
		if s != "" {
			banned = append(banned, s)
		}
	}
	banned = append(banned, snakeIdent.FindAllString(pqe.Detail, -1)...)
	banned = append(banned, uuidLike.FindAllString(pqe.Detail, -1)...)
	for _, b := range banned {
		if strings.Contains(msg, b) {
			t.Errorf("the message leaks %q: %s", b, msg)
		}
	}
}

// duplicateCostCentre is the live defect's own driver error, field for field.
func duplicateCostCentre() *pq.Error {
	return &pq.Error{
		Code:       "23505",
		Message:    `duplicate key value violates unique constraint "cost_centres_customer_id_code_key"`,
		Detail:     "Key (customer_id, code)=(9692021e-ce13-4bbd-9429-292e5f9218cd, ENG) already exists.",
		Table:      "cost_centres",
		Constraint: "cost_centres_customer_id_code_key",
	}
}

// TestMapErrTellsThePersonAndNotTheSchema is the decisive one: the exact
// error the console showed comes back classified as before and saying
// nothing a schema reader could use.
func TestMapErrTellsThePersonAndNotTheSchema(t *testing.T) {
	pqe := duplicateCostCentre()
	err := mapErr(pqe)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("classification changed: %v, want ErrConflict", err)
	}
	assertNoLeak(t, err.Error(), pqe)
	if want := "conflict: that cost-centre code is already used by this customer"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

func TestMapErrClassifiesEveryConstraintCode(t *testing.T) {
	cases := []struct {
		name     string
		pqe      *pq.Error
		sentinel error
		want     string
	}{
		{
			name:     "unique violation on a mapped constraint",
			pqe:      duplicateCostCentre(),
			sentinel: ErrConflict,
			want:     "conflict: that cost-centre code is already used by this customer",
		},
		{
			name: "unique violation on a constraint nothing names",
			pqe: &pq.Error{Code: "23505", Table: "widgets", Constraint: "widgets_gizmo_id_key",
				Detail: "Key (gizmo_id)=(9692021e-ce13-4bbd-9429-292e5f9218cd) already exists."},
			sentinel: ErrConflict,
			want:     "conflict: " + conflictFallback,
		},
		{
			name: "foreign key violation",
			pqe: &pq.Error{Code: "23503", Table: "cost_centres", Constraint: "cost_centres_customer_id_fkey",
				Detail: `Key (customer_id)=(9692021e-ce13-4bbd-9429-292e5f9218cd) is not present in table "customers".`},
			sentinel: ErrNotFound,
			want:     "not found: " + referenceFallback,
		},
		{
			name:     "check violation on a mapped constraint",
			pqe:      &pq.Error{Code: "23514", Table: "budgets", Constraint: "budgets_amount_check"},
			sentinel: ErrConflict,
			want:     "conflict: a budget cannot be negative",
		},
		{
			name:     "check violation on a constraint nothing names",
			pqe:      &pq.Error{Code: "23514", Table: "widgets", Constraint: "widgets_sprocket_check"},
			sentinel: ErrConflict,
			want:     "conflict: " + checkValueFallback,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := mapErr(c.pqe)
			if !errors.Is(err, c.sentinel) {
				t.Fatalf("err = %v, want %v", err, c.sentinel)
			}
			if err.Error() != c.want {
				t.Errorf("message = %q, want %q", err.Error(), c.want)
			}
			assertNoLeak(t, err.Error(), c.pqe)
		})
	}
}

// Everything mapErr did that was not the message still holds.
func TestMapErrKeepsItsOtherBehaviour(t *testing.T) {
	if mapErr(nil) != nil {
		t.Error("nil must stay nil")
	}
	if err := mapErr(sql.ErrNoRows); !errors.Is(err, ErrNotFound) {
		t.Errorf("no rows = %v, want ErrNotFound", err)
	}
	badUUID := &pq.Error{Code: "22P02", Message: `invalid input syntax for type uuid: "nope"`}
	if err := mapErr(badUUID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad uuid = %v, want ErrNotFound", err)
	}
	// A code mapErr does not classify is handed back untouched, so the API's
	// default branch still logs it in full and answers "internal error".
	notNull := &pq.Error{Code: "23502", Column: "name", Table: "customers"}
	if err := mapErr(notNull); err != error(notNull) {
		t.Errorf("an unclassified driver error was rewritten: %v", err)
	}
}

// A store method that refused the write in its own words has said something
// better than any constraint name can produce; mapErr must not overwrite it.
// SetPriceBookPublic is the live example (store/estimates.go), and
// TestPublicPriceBookConflictKeepsItsOwnSentence walks it against Postgres.
//
// The cases that make the precedence OBSERVABLE are the joined ones: an error
// that is both classified and carries a driver error underneath is the only
// shape where "classify from the constraint" and "keep the caller's sentence"
// disagree. Delete the sentinel check at the top of mapErr and those two fail.
func TestMapErrPrefersTheCallersOwnMessage(t *testing.T) {
	pqe := duplicateCostCentre()
	cases := []error{
		fmt.Errorf("%w: %q is already the public price book; withdraw it first", ErrConflict, "NC 2026"),
		fmt.Errorf("%w: code must match %s", ErrInvalid, CostCentreCodeRule),
		fmt.Errorf("%w: cost centre %s is gone", ErrNotFound, "ENG"),
		errors.Join(fmt.Errorf("%w: this customer already has a cost centre called Engineering", ErrConflict), pqe),
		errors.Join(fmt.Errorf("%w: that customer is gone", ErrNotFound), pqe),
	}
	for _, own := range cases {
		got := mapErr(own)
		if got.Error() != own.Error() {
			t.Errorf("mapErr rewrote the caller's message\n got: %q\nwant: %q", got, own)
		}
	}
}

// On a DELETE a foreign-key violation means "still referred to", and the
// answer is a conflict — never the "not found" mapErr gives the same code,
// which the API turns into a 404 for a row that is right there (#6936).
// Everything that is not a 23503 still goes through mapErr unchanged.
func TestMapDeleteErrCallsAStillReferencedRowAConflict(t *testing.T) {
	cases := []struct {
		name string
		pqe  *pq.Error
		want string
	}{
		{
			name: "a reference this package names",
			pqe: &pq.Error{Code: "23503", Table: "cost_sources", Constraint: "cost_sources_price_book_id_fkey",
				Detail: `Key (id)=(9692021e-ce13-4bbd-9429-292e5f9218cd) is still referenced from table "cost_sources".`},
			want: "conflict: a cost source is still assigned to that price book; assign it another book first",
		},
		{
			name: "a reference nothing names",
			pqe: &pq.Error{Code: "23503", Table: "gizmos", Constraint: "gizmos_widget_id_fkey",
				Detail: `Key (id)=(9692021e-ce13-4bbd-9429-292e5f9218cd) is still referenced from table "gizmos".`},
			want: "conflict: " + stillReferencedFallback,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := mapDeleteErr(c.pqe)
			if !errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrConflict and not ErrNotFound", err)
			}
			if err.Error() != c.want {
				t.Errorf("message = %q, want %q", err.Error(), c.want)
			}
			assertNoLeak(t, err.Error(), c.pqe)
		})
	}
	if mapDeleteErr(nil) != nil {
		t.Error("nil must stay nil")
	}
	if err := mapDeleteErr(sql.ErrNoRows); !errors.Is(err, ErrNotFound) {
		t.Errorf("no rows = %v, want ErrNotFound", err)
	}
	if err := mapDeleteErr(duplicateCostCentre()); err.Error() != mapErr(duplicateCostCentre()).Error() {
		t.Errorf("a unique violation was rewritten on the delete path: %v", err)
	}
}

// No sentence may itself contain what it was written to keep out.
func TestConstraintMessagesCarryNoIdentifiers(t *testing.T) {
	all := map[string]string{}
	for name, msg := range constraintMessages {
		all[name] = msg
	}
	for name, msg := range deleteConstraintMessages {
		all["delete:"+name] = msg
	}
	for name, msg := range all {
		name = strings.TrimPrefix(name, "delete:")
		if strings.Contains(msg, name) {
			t.Errorf("%s: the message repeats the constraint name: %s", name, msg)
		}
		if uuidLike.MatchString(msg) {
			t.Errorf("%s: the message contains a UUID: %s", name, msg)
		}
		if msg == "" || strings.HasSuffix(msg, ".") {
			t.Errorf("%s: message %q does not match the style of the store's other sentences", name, msg)
		}
	}
	for _, msg := range []string{conflictFallback, referenceFallback, checkValueFallback, stillReferencedFallback} {
		if snakeIdent.MatchString(msg) {
			t.Errorf("fallback %q reads like an identifier", msg)
		}
	}
}

// TestConstraintMessagesNameLiveConstraints holds every key of the map to a
// constraint or unique index the migrated schema really has. Without it a
// rename leaves a dead entry that silently falls back to the general
// sentence, and nothing fails.
//
// It reads the catalogs of the integration-test database directly rather than
// through internal/testdb, which imports this package.
func TestConstraintMessagesNameLiveConstraints(t *testing.T) {
	dsn := os.Getenv("CHARGEBACK_TEST_DATABASE_URL") // = testdb.EnvVar
	if dsn == "" {
		t.Skip("CHARGEBACK_TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := t.Context()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := New(db).Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT conname FROM pg_constraint WHERE connamespace = 'public'::regnamespace
		UNION
		SELECT c.relname FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid
		 WHERE c.relnamespace = 'public'::regnamespace AND i.indisunique`)
	if err != nil {
		t.Fatalf("read the catalogs: %v", err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		live[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(live) < 50 {
		t.Fatalf("only %d constraints found; the schema did not migrate, so this test proves nothing", len(live))
	}
	for name := range constraintMessages {
		if !live[name] {
			t.Errorf("constraintMessages names %q, which no constraint or unique index in the schema has", name)
		}
	}
	for name := range deleteConstraintMessages {
		if !live[name] {
			t.Errorf("deleteConstraintMessages names %q, which no constraint in the schema has", name)
		}
	}
}
