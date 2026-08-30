package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

func mkCustomer(t *testing.T, st *store.Store, slug string) store.Customer {
	t.Helper()
	c, err := st.CreateCustomer(context.Background(), store.CustomerInput{Slug: slug, Name: "Customer " + slug, AdminEmail: "admin@" + slug + ".example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatalf("create customer %s: %v", slug, err)
	}
	return c
}

func mkSource(t *testing.T, st *store.Store, customerID, project string) store.CostSource {
	t.Helper()
	src, created, err := st.UpsertSource(context.Background(), customerID, "huawei-project", "me-east-215", project)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	if !created {
		t.Fatalf("source %s reported as pre-existing", project)
	}
	if again, createdAgain, _ := st.UpsertSource(context.Background(), customerID, "huawei-project", "me-east-215", project); createdAgain || again.ID != src.ID {
		t.Fatalf("upsert not idempotent: %v %s", createdAgain, again.ID)
	}
	return src
}

func TestIntegrationMigrateIsIdempotent(t *testing.T) {
	st := testdb.Open(t)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestIntegrationScopeFiltersEveryCustomerQuery(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	a, b := mkCustomer(t, st, "alpha"), mkCustomer(t, st, "bravo")
	srcB := mkSource(t, st, b.ID, "pb")
	scopeA := store.CustomerScope(a.ID)

	list, err := st.ListCustomers(ctx, scopeA)
	if err != nil || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("scoped list = %+v err=%v", list, err)
	}
	all, _ := st.ListCustomers(ctx, store.OperatorScope)
	if len(all) != 2 {
		t.Fatalf("operator list = %d", len(all))
	}
	if _, err := st.GetCustomer(ctx, scopeA, b.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer get = %v", err)
	}
	if _, err := st.ListSources(ctx, scopeA, b.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer sources = %v", err)
	}
	if _, err := st.GetSource(ctx, scopeA, srcB.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer source by id = %v", err)
	}
	if _, err := st.QueryUsage(ctx, scopeA, b.ID, store.UsageQuery{From: time.Now().Add(-time.Hour), To: time.Now()}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer usage = %v", err)
	}
	if _, err := st.ListCustomerInventory(ctx, scopeA, b.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer inventory = %v", err)
	}
	if _, err := st.ListAudit(ctx, scopeA, b.ID, 10); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer audit = %v", err)
	}
	from, to, _ := store.PeriodBounds("2026-08")
	stB, err := st.WriteDraftStatement(ctx, store.StatementDraft{CustomerID: b.ID, PeriodStart: from, PeriodEnd: to.AddDate(0, 0, -1), Currency: "OMR", Subtotal: "1", TaxRate: "0.05", Tax: "0.05", Total: "1.05"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetStatement(ctx, scopeA, stB.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer statement = %v", err)
	}
	if _, err := st.ListStatements(ctx, scopeA, b.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer statements = %v", err)
	}
	if got, _ := st.GetStatement(ctx, store.CustomerScope(b.ID), stB.ID); got.ID != stB.ID {
		t.Fatal("own statement not readable")
	}
}

func TestIntegrationUsageUpsertIsIdempotent(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := mkCustomer(t, st, "usage")
	src := mkSource(t, st, c.ID, "p1")
	ws := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	rec := store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: "s1", ResourceKind: "ecs", SKU: "ecs.s6.large.2", Quantity: "0.250000", Unit: "instance-hour",
		WindowStart: ws, WindowEnd: ws.Add(15 * time.Minute), Region: "me-east-215", Labels: []byte(`{"status":"ACTIVE"}`)}
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{rec}); err != nil {
		t.Fatal(err)
	}
	rec.Quantity, rec.WindowEnd = "1.000000", ws.Add(time.Hour)
	stopped := rec
	stopped.WindowStart, stopped.WindowEnd, stopped.Quantity, stopped.Labels = ws.Add(time.Hour), ws.Add(2*time.Hour), "1.000000", []byte(`{"status":"SHUTOFF"}`)
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{rec, stopped, rec}); err != nil {
		t.Fatal(err)
	}
	n, _ := st.UsageCount(ctx, src.ID)
	if n != 2 {
		t.Fatalf("records = %d, want 2 (upsert on the window key)", n)
	}
	recs, _ := st.ListUsageRecords(ctx, src.ID, "s1")
	if string(recs[0].Quantity) != "1.000000" || !recs[0].WindowEnd.Equal(ws.Add(time.Hour)) {
		t.Fatalf("first record not updated: %+v", recs[0])
	}
	rows, err := st.QueryUsage(ctx, store.CustomerScope(c.ID), c.ID, store.UsageQuery{From: ws.Add(-time.Hour), To: ws.Add(3 * time.Hour), GroupBy: "sku"})
	if err != nil || len(rows) != 1 || string(rows[0].Quantity) != "2.000000" || rows[0].ResourceCount != 1 {
		t.Fatalf("sku rows = %+v err=%v", rows, err)
	}
	rows, _ = st.QueryUsage(ctx, store.OperatorScope, c.ID, store.UsageQuery{From: ws.Add(-time.Hour), To: ws.Add(3 * time.Hour), GroupBy: "day"})
	if len(rows) != 1 || rows[0].Key != "2026-08-10" {
		t.Fatalf("day rows = %+v", rows)
	}
	rows, _ = st.QueryUsage(ctx, store.OperatorScope, c.ID, store.UsageQuery{From: ws.Add(-time.Hour), To: ws.Add(3 * time.Hour), GroupBy: "resource"})
	if len(rows) != 1 || rows[0].Key != "s1" || rows[0].ResourceKind != "ecs" {
		t.Fatalf("resource rows = %+v", rows)
	}
	rat, err := st.UsageForRating(ctx, c.ID, ws.Add(-time.Hour), ws.Add(3*time.Hour))
	if err != nil || len(rat) != 1 || string(rat[0].Quantity) != "2.000000" || string(rat[0].StoppedQuantity) != "1.000000" {
		t.Fatalf("ratable = %+v err=%v", rat, err)
	}
	del, _ := st.DeleteUsageInRange(ctx, src.ID, "s1", ws.Add(time.Hour), ws.Add(2*time.Hour))
	if del != 1 {
		t.Fatalf("deleted = %d", del)
	}
}

func TestIntegrationStatementsReplaceDraftAndRefuseIssued(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := mkCustomer(t, st, "stmt")
	from, to, _ := store.PeriodBounds("2026-08")
	draft := store.StatementDraft{CustomerID: c.ID, PeriodStart: from, PeriodEnd: to.AddDate(0, 0, -1), Currency: "OMR", Subtotal: "10", TaxRate: "0.05", Tax: "0.5", Total: "10.5",
		Lines: []store.RatedLine{{SKU: "eip", Quantity: "10", Unit: "hour", UnitPrice: "1", Amount: "10", ResourceCount: 1}}}
	s1, err := st.WriteDraftStatement(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	draft.Lines = append(draft.Lines, store.RatedLine{SKU: "elb", Quantity: "1", Unit: "hour", UnitPrice: "2", Amount: "2", ResourceCount: 1})
	draft.Subtotal, draft.Total = "12", "12.6"
	s2, err := st.WriteDraftStatement(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if s2.ID != s1.ID || len(s2.Lines) != 2 || string(s2.Total) != "12.600000" || s2.PeriodStart != "2026-08-01" || s2.PeriodEnd != "2026-08-31" {
		t.Fatalf("re-run draft = %+v", s2)
	}
	if _, err := st.IssueStatement(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteDraftStatement(ctx, draft); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("issued statement overwritten: %v", err)
	}
	period, total, n, err := st.LastPeriodTotal(ctx)
	if err != nil || period != "2026-08" || string(total) != "12.600000" || n != 1 {
		t.Fatalf("last period = %s %s %d err=%v", period, total, n, err)
	}
	all, _ := st.ListAllStatements(ctx, "2026-08")
	if len(all) != 1 || all[0].Status != "issued" || all[0].CustomerName == "" {
		t.Fatalf("all statements = %+v", all)
	}
}

func TestIntegrationPINSessionsInvites(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := mkCustomer(t, st, "auth")
	email := "admin@auth.example"
	if err := st.PutPIN(ctx, email, "123456", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if recent, _ := st.PINIssuedRecently(ctx, email, 10*time.Minute, 30*time.Second); !recent {
		t.Fatal("fresh PIN not reported as recent")
	}
	if ok, _ := st.VerifyPIN(ctx, email, "000000", 3); ok {
		t.Fatal("wrong code accepted")
	}
	if ok, _ := st.VerifyPIN(ctx, "Admin@Auth.example", "123456", 3); !ok {
		t.Fatal("right code rejected (case-insensitive email)")
	}
	if ok, _ := st.VerifyPIN(ctx, email, "123456", 3); ok {
		t.Fatal("PIN reusable")
	}
	_ = st.PutPIN(ctx, email, "654321", 10*time.Minute)
	for i := 0; i < 3; i++ {
		_, _ = st.VerifyPIN(ctx, email, "111111", 3)
	}
	if ok, _ := st.VerifyPIN(ctx, email, "654321", 3); ok {
		t.Fatal("PIN accepted after max attempts")
	}
	cid, role, ok, err := st.RoleForEmail(ctx, email)
	if err != nil || !ok || cid != c.ID || role != "admin" {
		t.Fatalf("role = %s %s %v %v", cid, role, ok, err)
	}
	if _, _, ok, _ := st.RoleForEmail(ctx, "nobody@example.com"); ok {
		t.Fatal("unknown email has a role")
	}
	sess, err := st.CreateSession(ctx, email, store.RoleCustomerAdmin, &c.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(ctx, sess.Token)
	if err != nil || got.Email != email || *got.CustomerID != c.ID || !got.Scope().Allows(c.ID) {
		t.Fatalf("session = %+v err=%v", got, err)
	}
	expired, _ := st.CreateSession(ctx, email, store.RoleCustomerAdmin, &c.ID, -time.Minute)
	if _, err := st.GetSession(ctx, expired.Token); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired session readable")
	}
	_ = st.DeleteSession(ctx, sess.Token)
	if _, err := st.GetSession(ctx, sess.Token); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("deleted session readable")
	}
	inv, err := st.CreateInvite(ctx, c.ID, email, time.Hour)
	if err != nil || !inv.Usable(time.Now()) {
		t.Fatalf("invite = %+v err=%v", inv, err)
	}
	_ = st.MarkInviteUsed(ctx, inv.Token)
	got2, _ := st.GetInvite(ctx, inv.Token)
	if got2.Usable(time.Now()) {
		t.Fatal("used invite still usable")
	}
	if err := st.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationPriceBookDivisorRecompute(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	pb, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "NC 2026", AnnualDivisor: 8760})
	if err != nil {
		t.Fatal(err)
	}
	annual := store.Decimal("8760")
	if _, err := st.PutPriceItems(ctx, pb.ID, []store.PriceItem{{SKU: "eip", Unit: "hour", UnitPrice: "1.00000000", AnnualPrice: &annual}, {SKU: "elb", Unit: "hour", UnitPrice: "0.5"}}, true); err != nil {
		t.Fatal(err)
	}
	pb2, err := st.UpdatePriceBook(ctx, pb.ID, store.PriceBookInput{AnnualDivisor: 4380})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]store.PriceItem{}
	for _, it := range pb2.Items {
		by[it.SKU] = it
	}
	if string(by["eip"].UnitPrice) != "2.00000000" {
		t.Fatalf("annual-derived price not recomputed: %s", by["eip"].UnitPrice)
	}
	if string(by["elb"].UnitPrice) != "0.50000000" {
		t.Fatalf("direct price changed: %s", by["elb"].UnitPrice)
	}
	if _, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "NC 2026"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name = %v", err)
	}
	if got, err := st.GetPriceBookByName(ctx, "nc 2026"); err != nil || got.ID != pb.ID {
		t.Fatalf("by name = %+v %v", got, err)
	}
}

func TestIntegrationCredentialsNeverExposeSecretInViews(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := mkCustomer(t, st, "cred")
	src := mkSource(t, st, c.ID, "p1")
	cred, err := st.CreateCredential(ctx, c.ID, "AKTEST", []byte("ciphertext-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceCredential(ctx, src.ID, cred.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetSource(ctx, store.OperatorScope, src.ID)
	if got.AccessKey != "AKTEST" || got.CredentialID == nil || got.Status != "pending" {
		t.Fatalf("source view = %+v", got)
	}
	ak, enc, err := st.GetCredentialSecret(ctx, cred.ID)
	if err != nil || ak != "AKTEST" || string(enc) != "ciphertext-bytes" {
		t.Fatalf("secret fetch = %s %q %v", ak, enc, err)
	}
	_ = st.RevokeCredential(ctx, cred.ID)
	if _, _, err := st.GetCredentialSecret(ctx, cred.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("revoked credential still readable")
	}
	_ = st.SetSourceFailed(ctx, src.ID, "APIGW.0301: verify aksk signature fail")
	got, _ = st.GetSource(ctx, store.OperatorScope, src.ID)
	if got.Status != "failed" || got.LastError == nil || *got.LastError == "" {
		t.Fatalf("failed source = %+v", got)
	}
	_ = st.SetCustomerStatus(ctx, c.ID, "active")
	_ = st.SetSourceVerified(ctx, src.ID, "dom-1")
	live, _ := st.ListVerifiedSources(ctx)
	if len(live) != 0 {
		t.Fatalf("source with revoked credential listed as collectable: %+v", live)
	}
	cred2, _ := st.CreateCredential(ctx, c.ID, "AK2", []byte("x"))
	_ = st.SetSourceCredential(ctx, src.ID, cred2.ID)
	_ = st.SetSourceVerified(ctx, src.ID, "")
	live, _ = st.ListVerifiedSources(ctx)
	if len(live) != 1 || live[0].DomainID == nil || *live[0].DomainID != "dom-1" {
		t.Fatalf("collectable sources = %+v", live)
	}
}
