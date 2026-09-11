package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The two tables §21 adds, against Postgres: the four preference scopes and
// the uniqueness that keeps one row per scope, the delivery log with its
// filters and tallies, and the scope confinement that decides what a
// customer principal can read of either.

func notifyCustomer(t *testing.T, st *store.Store, slug string) store.Customer {
	t.Helper()
	c, err := st.CreateCustomer(context.Background(), store.CustomerInput{Slug: slug, Name: strings.ToUpper(slug), AdminEmail: "ap@" + slug + ".example"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIntegrationNotificationPreferenceScopesAreOneRowEach(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	a := notifyCustomer(t, st, "acme")
	b := notifyCustomer(t, st, "beta")

	rows := []store.NotificationPreference{
		{Event: "account.low_balance", Enabled: true, Channels: []string{"email"}},
		{Event: "account.low_balance", CustomerID: &a.ID, Enabled: false},
		{Event: "account.low_balance", Email: "fin@acme.example", Enabled: true},
		{Event: "account.low_balance", CustomerID: &a.ID, Email: "fin@acme.example", Enabled: false},
		{Event: "account.low_balance", CustomerID: &b.ID, Enabled: true},
	}
	for _, p := range rows {
		if _, err := st.PutNotificationPreference(ctx, p); err != nil {
			t.Fatalf("put %+v: %v", p, err)
		}
	}
	all, err := st.ListNotificationPreferences(ctx, store.OperatorScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("rows = %d, want 5: %+v", len(all), all)
	}

	// The same scope again REPLACES rather than adding: two Sovereign
	// defaults for one event is a contradiction the schema refuses.
	got, err := st.PutNotificationPreference(ctx, store.NotificationPreference{Event: "account.low_balance", Enabled: false, Channels: []string{"email", "sms"}, Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || len(got.Channels) != 2 || got.Locale != "en" {
		t.Fatalf("upsert = %+v", got)
	}
	if all, _ = st.ListNotificationPreferences(ctx, store.OperatorScope); len(all) != 5 {
		t.Fatalf("upsert added a row: %d", len(all))
	}

	// The resolver's read: only what could apply to (acme, fin@acme.example).
	applies, err := st.NotificationPreferencesFor(ctx, a.ID, "FIN@Acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(applies) != 4 {
		t.Fatalf("applicable rows = %d, want 4 (beta's is not one): %+v", len(applies), applies)
	}
	for _, p := range applies {
		if p.CustomerID != nil && *p.CustomerID == b.ID {
			t.Fatalf("another customer's row was returned: %+v", p)
		}
	}
	// Rank is what the resolver orders by, and it must come back on the row.
	ranks := map[int]bool{}
	for _, p := range applies {
		ranks[p.Rank()] = true
	}
	for want := 0; want <= 3; want++ {
		if !ranks[want] {
			t.Fatalf("rank %d missing from %+v", want, applies)
		}
	}

	// Deleting one scope's row leaves the others exactly as they were.
	if err := st.DeleteNotificationPreference(ctx, "account.low_balance", &a.ID, "fin@acme.example"); err != nil {
		t.Fatal(err)
	}
	if applies, _ = st.NotificationPreferencesFor(ctx, a.ID, "fin@acme.example"); len(applies) != 3 {
		t.Fatalf("after delete = %+v", applies)
	}
	// Deleting one that is not there is not an error: the caller asked for
	// "no row here", and there is none.
	if err := st.DeleteNotificationPreference(ctx, "account.low_balance", &a.ID, "nobody@acme.example"); err != nil {
		t.Fatalf("deleting an absent row must not fail: %v", err)
	}

	// A customer principal reads only its own customers' rows — never the
	// Sovereign defaults, which are the operator's policy.
	mine, err := st.ListNotificationPreferences(ctx, store.CustomerScope(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range mine {
		if p.CustomerID == nil || *p.CustomerID != a.ID {
			t.Fatalf("customer scope leaked %+v", p)
		}
	}

	// Deleting the customer takes its preference rows with it.
	if err := st.DeleteCustomer(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	all, _ = st.ListNotificationPreferences(ctx, store.OperatorScope)
	for _, p := range all {
		if p.CustomerID != nil && *p.CustomerID == a.ID {
			t.Fatalf("a deleted customer's preference survived: %+v", p)
		}
	}
}

func TestIntegrationNotificationPreferenceRefusesAMalformedRow(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	if _, err := st.PutNotificationPreference(ctx, store.NotificationPreference{Event: "  ", Enabled: true}); err == nil {
		t.Fatal("an empty event must be refused")
	}
	if _, err := st.PutNotificationPreference(ctx, store.NotificationPreference{Event: "auth.pin", Email: "not-an-address", Enabled: true}); err == nil {
		t.Fatal("a malformed email must be refused")
	}
	// The email is stored lower-cased, which is what makes the unique index
	// on (event, customer, email) mean one row per PERSON.
	got, err := st.PutNotificationPreference(ctx, store.NotificationPreference{Event: "auth.pin", Email: "  OPS@NC.Example ", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "ops@nc.example" {
		t.Fatalf("email = %q", got.Email)
	}
}

func TestIntegrationDeliveryLogRecordsEveryAttemptAndIsScoped(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	a := notifyCustomer(t, st, "acme")
	b := notifyCustomer(t, st, "beta")
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)

	write := func(event string, cust *string, to, status, reason string, attempt int, at time.Time) store.NotificationDelivery {
		t.Helper()
		d, err := st.RecordNotificationDelivery(ctx, store.NotificationDeliveryInput{
			Event: event, CustomerID: cust, Channel: "email", Recipient: to, Locale: "en",
			Subject: "S", Attempt: attempt, Status: status, Reason: reason, At: at,
		})
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		return d
	}
	write("statement.issued", &a.ID, "ap@acme.example", store.NotifyStatusRetrying, "smtp dial", 1, now.Add(-2*time.Minute))
	write("statement.issued", &a.ID, "ap@acme.example", store.NotifyStatusSent, "", 2, now.Add(-time.Minute))
	write("collections.reminder", &a.ID, "ap@acme.example", store.NotifyStatusFailed, "550 mailbox unavailable", 3, now)
	write("account.low_balance", &b.ID, "ap@beta.example", store.NotifyStatusSuppressed, "switched off by the customer:… preference", 1, now)
	// A delivery that belongs to NO customer: an operator's sign-in code.
	write("auth.pin", nil, "ops@nc.example", store.NotifyStatusSent, "", 1, now)
	// Old enough to be purged later.
	write("auth.pin", nil, "ops@nc.example", store.NotifyStatusSent, "", 1, now.AddDate(-1, 0, 0))

	all, err := st.ListNotificationDeliveries(ctx, store.OperatorScope, store.NotificationDeliveryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Fatalf("rows = %d, want 6", len(all))
	}
	// Newest first.
	if !all[0].At.After(all[len(all)-1].At) {
		t.Fatalf("order = %v … %v", all[0].At, all[len(all)-1].At)
	}
	// Both attempts on the statement are there, with their own status.
	statuses := map[string]int{}
	for _, d := range all {
		statuses[d.Status]++
	}
	if statuses[store.NotifyStatusRetrying] != 1 || statuses[store.NotifyStatusSent] != 3 || statuses[store.NotifyStatusFailed] != 1 || statuses[store.NotifyStatusSuppressed] != 1 {
		t.Fatalf("statuses = %v", statuses)
	}

	// The filters.
	byEvent, _ := st.ListNotificationDeliveries(ctx, store.OperatorScope, store.NotificationDeliveryFilter{Event: "statement.issued"})
	if len(byEvent) != 2 {
		t.Fatalf("by event = %d", len(byEvent))
	}
	// "problems" is the console's opening view: everything that did not get
	// through, whatever the reason.
	problems, _ := st.ListNotificationDeliveries(ctx, store.OperatorScope, store.NotificationDeliveryFilter{Status: store.StatusProblems})
	if len(problems) != 1 || problems[0].Status != store.NotifyStatusFailed {
		t.Fatalf("problems = %+v", problems)
	}
	byCustomer, _ := st.ListNotificationDeliveries(ctx, store.OperatorScope, store.NotificationDeliveryFilter{CustomerID: a.ID})
	if len(byCustomer) != 3 {
		t.Fatalf("by customer = %d", len(byCustomer))
	}
	byRecipient, _ := st.ListNotificationDeliveries(ctx, store.OperatorScope, store.NotificationDeliveryFilter{Recipient: "OPS@NC.example"})
	if len(byRecipient) != 2 {
		t.Fatalf("by recipient = %d", len(byRecipient))
	}
	limited, _ := st.ListNotificationDeliveries(ctx, store.OperatorScope, store.NotificationDeliveryFilter{Limit: 2})
	if len(limited) != 2 {
		t.Fatalf("limit = %d", len(limited))
	}

	// A CUSTOMER principal sees its own rows and nothing else — including
	// none of the rows that belong to no customer at all.
	mine, err := st.ListNotificationDeliveries(ctx, store.CustomerScope(a.ID), store.NotificationDeliveryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 3 {
		t.Fatalf("customer scope = %d rows, want 3: %+v", len(mine), mine)
	}
	for _, d := range mine {
		if d.CustomerID == nil || *d.CustomerID != a.ID {
			t.Fatalf("customer scope leaked %+v", d)
		}
	}

	// The tallies the console's tiles read.
	stats, err := st.NotificationDeliveryStats(ctx, store.OperatorScope, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range stats {
		total += s.Count
	}
	if total != 5 {
		t.Fatalf("stats over the window = %d, want 5 (the year-old row is outside it): %+v", total, stats)
	}

	// Retention: the year-old row goes, the rest stay.
	if err := st.PurgeNotificationDeliveries(ctx, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ListNotificationDeliveries(ctx, store.OperatorScope, store.NotificationDeliveryFilter{})
	if len(after) != 5 {
		t.Fatalf("after purge = %d, want 5", len(after))
	}

	// A DELETED customer must not take its delivery history with it: "was
	// the customer told" is a question asked after the account is closed.
	if err := st.DeleteCustomer(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	kept, _ := st.ListNotificationDeliveries(ctx, store.OperatorScope, store.NotificationDeliveryFilter{})
	if len(kept) != 5 {
		t.Fatalf("deleting a customer dropped its delivery rows: %d", len(kept))
	}
}

// The migration is APPENDED, and the locator finds it by content rather than
// by position — the invariant every migration in this package rests on.
func TestIntegrationNotificationMigrationIsAppendedAndLocatedByContent(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	if store.MigrationNotifications <= store.MigrationCostRollup {
		t.Fatalf("the notification migration (%d) must come after the rollup one (%d) — it was appended", store.MigrationNotifications, store.MigrationCostRollup)
	}
	var applied bool
	if err := st.DB().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, store.MigrationNotifications).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatalf("version %d (MigrationNotifications) is not recorded as applied", store.MigrationNotifications)
	}
}
