package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The role-binding migration (DESIGN.md §10). Stands a database at the shape
// BEFORE it — customer_users as a table — writes an admin and a viewer, applies
// the migration, and asserts: the rows became customer-owner / customer-viewer
// bindings, customer_users still answers as a view with its old columns, the
// sessions CHECK admits the new role names, and a binding cannot be granted
// twice.
func TestIntegrationRoleBindingsMigrationBackfillsCustomerUsers(t *testing.T) {
	dsn := os.Getenv(testdb.EnvVar)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", testdb.EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// A private schema: the pre-migration shape never touches the shared
	// public one, and one connection so the search_path holds.
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS access_migration CASCADE`,
		`CREATE SCHEMA access_migration`,
		`SET search_path = access_migration, public`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = db.ExecContext(c, `DROP SCHEMA IF EXISTS access_migration CASCADE`)
	})

	st := store.New(db)
	if err := st.MigrateUpTo(ctx, store.MigrationRoleBindings-1); err != nil {
		t.Fatalf("migrate to the version before role bindings: %v", err)
	}
	var cid string
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (slug, name, admin_email) VALUES ('acme', 'Acme', 'owner@acme.example') RETURNING id`).Scan(&cid); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	for _, row := range [][2]string{{"Owner@Acme.example", "admin"}, {"reader@acme.example", "viewer"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO customer_users (customer_id, email, role) VALUES ($1, $2, $3)`, cid, row[0], row[1]); err != nil {
			t.Fatalf("seed customer_users: %v", err)
		}
	}
	var relkind string
	if err := db.QueryRowContext(ctx, `SELECT c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'access_migration' AND c.relname = 'customer_users'`).Scan(&relkind); err != nil || relkind != "r" {
		t.Fatalf("before the migration customer_users must be a table: relkind=%q err=%v", relkind, err)
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The backfill.
	users, err := st.ListCustomerUsers(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("users after migration = %+v", users)
	}
	want := map[string][2]string{"owner@acme.example": {"admin", store.RoleCustomerOwner}, "reader@acme.example": {"viewer", store.RoleCustomerViewer}}
	for _, u := range users {
		w, ok := want[u.Email]
		if !ok || u.Role != w[0] || u.BindingRole != w[1] {
			t.Fatalf("user %+v, want role %s binding %s", u, w[0], w[1])
		}
	}
	var grantedBy string
	if err := db.QueryRowContext(ctx, `SELECT granted_by FROM role_bindings WHERE subject_email = 'owner@acme.example'`).Scan(&grantedBy); err != nil || grantedBy != "migration" {
		t.Fatalf("backfilled binding granted_by=%q err=%v", grantedBy, err)
	}

	// customer_users is still there for an older reader — as a view, with its
	// columns and its admin | viewer vocabulary.
	if err := db.QueryRowContext(ctx, `SELECT c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'access_migration' AND c.relname = 'customer_users'`).Scan(&relkind); err != nil || relkind != "v" {
		t.Fatalf("after the migration customer_users must be a view: relkind=%q err=%v", relkind, err)
	}
	var legacyRole string
	if err := db.QueryRowContext(ctx, `SELECT role FROM customer_users WHERE customer_id = $1 AND email = 'owner@acme.example'`, cid).Scan(&legacyRole); err != nil || legacyRole != "admin" {
		t.Fatalf("view role=%q err=%v", legacyRole, err)
	}

	// RoleForEmail still speaks the legacy vocabulary.
	gotCID, role, ok, err := st.RoleForEmail(ctx, "owner@acme.example")
	if err != nil || !ok || gotCID != cid || role != "admin" {
		t.Fatalf("RoleForEmail = (%s, %s, %v, %v)", gotCID, role, ok, err)
	}

	// The sessions CHECK admits every role name, old and new.
	for _, r := range []string{"operator", "customer-admin", "customer-viewer", "sovereign-admin", "billing-operator", "finance-viewer", "customer-owner", "customer-billing"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions (token, email, role, customer_id, expires_at) VALUES ($1, 'x@example.com', $2, NULL, now() + interval '1 hour')`, "tok-"+r, r); err != nil {
			t.Fatalf("sessions CHECK refused role %s: %v", r, err)
		}
	}

	// Idempotent grants: the same binding twice is one row, at either scope.
	b1, err := st.UpsertRoleBinding(ctx, store.RoleBinding{SubjectEmail: "Fin@NC.example", Role: store.RoleFinanceViewer, GrantedBy: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := st.UpsertRoleBinding(ctx, store.RoleBinding{SubjectEmail: "fin@nc.example", Role: store.RoleFinanceViewer, GrantedBy: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	if b1.ID != b2.ID || b2.GrantedBy != "ops" || b1.ScopeKind != store.ScopeKindSovereign || b1.CustomerID != nil {
		t.Fatalf("sovereign upsert not idempotent: %+v vs %+v", b1, b2)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO role_bindings (subject_email, role, scope_kind) VALUES ('fin@nc.example', 'finance-viewer', 'sovereign')`); err == nil {
		t.Fatal("a duplicate sovereign binding was accepted; the partial unique index is missing")
	}
	c1, err := st.UpsertRoleBinding(ctx, store.RoleBinding{SubjectEmail: "b@acme.example", Role: store.RoleCustomerBilling, CustomerID: &cid})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := st.UpsertRoleBinding(ctx, store.RoleBinding{SubjectEmail: "b@acme.example", Role: store.RoleCustomerBilling, CustomerID: &cid})
	if err != nil || c1.ID != c2.ID {
		t.Fatalf("customer upsert not idempotent: %+v vs %+v (%v)", c1, c2, err)
	}
	// The role fixes the scope: a customer role without a customer, a
	// Sovereign role with one, an unknown role — all ErrInvalid.
	for _, bad := range []store.RoleBinding{
		{SubjectEmail: "x@acme.example", Role: store.RoleCustomerOwner},
		{SubjectEmail: "x@acme.example", Role: store.RoleSovereignAdmin, CustomerID: &cid},
		{SubjectEmail: "x@acme.example", Role: "root"},
		{SubjectEmail: "not-an-email", Role: store.RoleFinanceViewer},
	} {
		if _, err := st.UpsertRoleBinding(ctx, bad); err == nil || !isInvalid(err) {
			t.Fatalf("binding %+v accepted (err=%v), want ErrInvalid", bad, err)
		}
	}

	// UpsertCustomerUser re-roles: one customer role per email per customer.
	if err := st.UpsertCustomerUser(ctx, cid, "b@acme.example", "admin"); err != nil {
		t.Fatal(err)
	}
	users, _ = st.ListCustomerUsers(ctx, cid)
	n := 0
	for _, u := range users {
		if u.Email == "b@acme.example" {
			n++
			if u.BindingRole != store.RoleCustomerOwner || u.Role != "admin" {
				t.Fatalf("re-roled user = %+v", u)
			}
		}
	}
	if n != 1 {
		t.Fatalf("b@acme.example holds %d customer roles, want exactly 1", n)
	}

	// Group mappings: replace is the whole set, and resolution carries the
	// group name as the source.
	ms, err := st.ReplaceGroupRoleMappings(ctx, []store.GroupRoleMapping{
		{GroupName: "finance", Role: store.RoleFinanceViewer},
		{GroupName: "acme-admins", Role: store.RoleCustomerOwner, CustomerID: &cid},
	})
	if err != nil || len(ms) != 2 {
		t.Fatalf("replace mappings: %v %+v", err, ms)
	}
	via, err := st.BindingsForGroups(ctx, []string{"finance", "nobody"})
	if err != nil || len(via) != 1 || via[0].Role != store.RoleFinanceViewer || via[0].Source != "group:finance" {
		t.Fatalf("bindings via groups = %+v (%v)", via, err)
	}
	if _, err := st.ReplaceGroupRoleMappings(ctx, []store.GroupRoleMapping{{GroupName: "x", Role: "root"}}); err == nil || !isInvalid(err) {
		t.Fatalf("invalid mapping accepted: %v", err)
	}
	if ms, _ = st.ListGroupRoleMappings(ctx); len(ms) != 2 {
		t.Fatalf("a refused replace changed the set: %+v", ms)
	}
	if ms, err = st.ReplaceGroupRoleMappings(ctx, nil); err != nil || len(ms) != 0 {
		t.Fatalf("empty replace: %v %+v", err, ms)
	}
}

func isInvalid(err error) bool { return errors.Is(err, store.ErrInvalid) }
