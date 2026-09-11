package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// accessMigrationSQL is the role-binding model (DESIGN.md §10, EPIC #6867,
// founder requirement 2026-09-10: "how will customers log in and how is
// their role-based access defined — scopes and roles").
//
// role_bindings replaces customer_users as the source of truth: one row per
// (email, role, scope). The customer rows are backfilled from customer_users
// (admin → customer-owner, viewer → customer-viewer) and customer_users
// stays as a VIEW over the bindings so an older reader keeps its columns
// and its JSON keys. group_role_mappings binds a directory group the SSO
// gate forwards (X-Forwarded-Groups) to a role. The sessions.role CHECK is
// widened to the new names; the legacy three stay valid.
//
// The customers_owner_binding trigger keeps "the admin_email is a
// customer-owner" true for every writer of the customers table — this
// store, the Organization sync, a repair script — without the store having
// to know about role_bindings on the create/update path (which also keeps
// the older-shape migration tests, which create customers at a version
// before this one, honest). It only ever ADDS a binding.
//
// Two partial unique indexes stand in for UNIQUE(subject_email, role,
// scope_kind, customer_id): a NULL customer_id is never equal to another
// NULL under a plain UNIQUE constraint, so a sovereign binding could be
// granted twice.
//
// Appended at the END of migrations on purpose: they are positional.
const accessMigrationSQL = `
CREATE TABLE IF NOT EXISTS role_bindings (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	subject_email TEXT NOT NULL CHECK (subject_email = lower(subject_email) AND subject_email <> ''),
	role TEXT NOT NULL CHECK (role IN ('sovereign-admin','billing-operator','finance-viewer','customer-owner','customer-billing','customer-viewer')),
	scope_kind TEXT NOT NULL CHECK (scope_kind IN ('sovereign','customer')),
	customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
	granted_by TEXT NOT NULL DEFAULT '',
	granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CHECK ((scope_kind = 'sovereign' AND customer_id IS NULL) OR (scope_kind = 'customer' AND customer_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS role_bindings_customer_uniq ON role_bindings (subject_email, role, scope_kind, customer_id) WHERE customer_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS role_bindings_sovereign_uniq ON role_bindings (subject_email, role, scope_kind) WHERE customer_id IS NULL;
CREATE INDEX IF NOT EXISTS role_bindings_email_idx ON role_bindings (subject_email);
CREATE INDEX IF NOT EXISTS role_bindings_customer_idx ON role_bindings (customer_id);
INSERT INTO role_bindings (subject_email, role, scope_kind, customer_id, granted_by)
	SELECT lower(email), CASE role WHEN 'admin' THEN 'customer-owner' ELSE 'customer-viewer' END, 'customer', customer_id, 'migration'
	FROM customer_users
	ON CONFLICT DO NOTHING;
INSERT INTO role_bindings (subject_email, role, scope_kind, customer_id, granted_by)
	SELECT lower(admin_email), 'customer-owner', 'customer', id, 'admin_email'
	FROM customers WHERE admin_email <> ''
	ON CONFLICT DO NOTHING;
DROP TABLE customer_users;
CREATE VIEW customer_users AS
	SELECT customer_id, subject_email AS email, CASE WHEN role = 'customer-owner' THEN 'admin' ELSE 'viewer' END AS role
	FROM role_bindings WHERE scope_kind = 'customer';
CREATE OR REPLACE FUNCTION customers_owner_binding() RETURNS trigger AS $fn$
BEGIN
	IF NEW.admin_email IS NOT NULL AND NEW.admin_email <> '' THEN
		INSERT INTO role_bindings (subject_email, role, scope_kind, customer_id, granted_by)
		VALUES (lower(NEW.admin_email), 'customer-owner', 'customer', NEW.id, 'admin_email')
		ON CONFLICT DO NOTHING;
	END IF;
	RETURN NEW;
END
$fn$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS customers_owner_binding ON customers;
CREATE TRIGGER customers_owner_binding AFTER INSERT OR UPDATE OF admin_email ON customers
	FOR EACH ROW EXECUTE FUNCTION customers_owner_binding();
CREATE TABLE IF NOT EXISTS group_role_mappings (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	group_name TEXT NOT NULL CHECK (group_name <> ''),
	role TEXT NOT NULL CHECK (role IN ('sovereign-admin','billing-operator','finance-viewer','customer-owner','customer-billing','customer-viewer')),
	scope_kind TEXT NOT NULL CHECK (scope_kind IN ('sovereign','customer')),
	customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CHECK ((scope_kind = 'sovereign' AND customer_id IS NULL) OR (scope_kind = 'customer' AND customer_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS group_role_mappings_customer_uniq ON group_role_mappings (group_name, role, scope_kind, customer_id) WHERE customer_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS group_role_mappings_sovereign_uniq ON group_role_mappings (group_name, role, scope_kind) WHERE customer_id IS NULL;
CREATE INDEX IF NOT EXISTS group_role_mappings_group_idx ON group_role_mappings (group_name);
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS sessions_role_check;
ALTER TABLE sessions ADD CONSTRAINT sessions_role_check CHECK (role IN ('operator','customer-admin','customer-viewer','sovereign-admin','billing-operator','finance-viewer','customer-owner','customer-billing'));
`

// MigrationRoleBindings is the schema_migrations version of the role-binding
// migration, located by content like the others so a migration appended
// after it cannot move this version. Its test stands a database at the
// version before it, writes customer_users rows, and applies it.
var MigrationRoleBindings = func() int {
	for i, m := range migrations {
		if m == accessMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

const roleBindingColumns = `b.id, b.subject_email, b.role, b.scope_kind, b.customer_id, COALESCE(c.name, ''), b.granted_by, b.granted_at, b.partner_id, COALESCE(p.name, '')`

// roleBindingFrom joins the customer of a customer binding and the partner
// of a partner binding (DESIGN.md §Partners).
const roleBindingFrom = ` FROM role_bindings b LEFT JOIN customers c ON c.id = b.customer_id LEFT JOIN partners p ON p.id = b.partner_id`

func scanRoleBinding(row interface{ Scan(...any) error }) (RoleBinding, error) {
	var b RoleBinding
	var cust, partner sql.NullString
	var at time.Time
	if err := row.Scan(&b.ID, &b.SubjectEmail, &b.Role, &b.ScopeKind, &cust, &b.CustomerName, &b.GrantedBy, &at, &partner, &b.PartnerName); err != nil {
		return b, mapErr(err)
	}
	b.CustomerID = strPtr(cust)
	b.PartnerID = strPtr(partner)
	at = at.UTC()
	b.GrantedAt = &at
	b.Source = BindingSourceExplicit
	return b, nil
}

// RoleBindingFilter narrows ListRoleBindings; empty fields match everything.
type RoleBindingFilter struct {
	SubjectEmail string
	CustomerID   string
	PartnerID    string
}

// ListRoleBindings returns explicit bindings, operator roles first, then by
// email. The implicit OPERATOR_EMAILS bindings are not rows and are added by
// the API layer.
func (s *Store) ListRoleBindings(ctx context.Context, f RoleBindingFilter) ([]RoleBinding, error) {
	q := `SELECT ` + roleBindingColumns + roleBindingFrom
	var where []string
	var args []any
	if e := strings.ToLower(strings.TrimSpace(f.SubjectEmail)); e != "" {
		args = append(args, e)
		where = append(where, fmt.Sprintf("b.subject_email = $%d", len(args)))
	}
	if f.CustomerID != "" {
		args = append(args, f.CustomerID)
		where = append(where, fmt.Sprintf("b.customer_id = $%d", len(args)))
	}
	if f.PartnerID != "" {
		args = append(args, f.PartnerID)
		where = append(where, fmt.Sprintf("b.partner_id = $%d", len(args)))
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY (b.scope_kind = 'sovereign') DESC, (b.scope_kind = 'partner') DESC, b.subject_email, p.name NULLS FIRST, c.name NULLS FIRST, b.role`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []RoleBinding{}
	for rows.Next() {
		b, err := scanRoleBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BindingsForEmail is every explicit binding an email holds (the session's
// resolution path; one indexed query per request).
func (s *Store) BindingsForEmail(ctx context.Context, email string) ([]RoleBinding, error) {
	return s.ListRoleBindings(ctx, RoleBindingFilter{SubjectEmail: email})
}

// GetRoleBinding returns one binding by id.
func (s *Store) GetRoleBinding(ctx context.Context, id string) (RoleBinding, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+roleBindingColumns+roleBindingFrom+` WHERE b.id = $1`, id)
	return scanRoleBinding(row)
}

// scopeTarget checks that the ids a binding or mapping carries match the
// scope kind its role fixes: a customer role needs a customer_id and nothing
// else, a partner role a partner_id and nothing else, a Sovereign role
// neither. It clears the ids a Sovereign role does not take.
func scopeTarget(role string, scopeKind string, customerID, partnerID **string) (string, error) {
	kind := ScopeKindOfRole(role)
	if scopeKind != "" && scopeKind != kind {
		return "", fmt.Errorf("%w: role %s is bound at the %s scope, not %s", ErrInvalid, role, kind, scopeKind)
	}
	hasCustomer := *customerID != nil && **customerID != ""
	hasPartner := *partnerID != nil && **partnerID != ""
	switch kind {
	case ScopeKindCustomer:
		if !hasCustomer {
			return "", fmt.Errorf("%w: a %s binding needs a customer_id", ErrInvalid, role)
		}
		if hasPartner {
			return "", fmt.Errorf("%w: a %s binding is bound to one customer and takes no partner_id", ErrInvalid, role)
		}
		*partnerID = nil
	case ScopeKindPartner:
		if !hasPartner {
			return "", fmt.Errorf("%w: a %s binding needs a partner_id", ErrInvalid, role)
		}
		if hasCustomer {
			return "", fmt.Errorf("%w: a %s binding is bound to one partner and takes no customer_id", ErrInvalid, role)
		}
		*customerID = nil
	default:
		if hasCustomer || hasPartner {
			return "", fmt.Errorf("%w: a %s binding is Sovereign-wide and takes no customer_id or partner_id", ErrInvalid, role)
		}
		*customerID, *partnerID = nil, nil
	}
	return kind, nil
}

// UpsertRoleBinding grants a role at a scope, idempotently: an identical
// binding is returned unchanged (granted_by and granted_at are kept from the
// first grant). The role fixes the scope kind; a customer role without a
// customer, a partner role without a partner, or a sovereign role with
// either, is ErrInvalid.
func (s *Store) UpsertRoleBinding(ctx context.Context, b RoleBinding) (RoleBinding, error) {
	b.SubjectEmail = strings.ToLower(strings.TrimSpace(b.SubjectEmail))
	if b.SubjectEmail == "" || !strings.Contains(b.SubjectEmail, "@") {
		return RoleBinding{}, fmt.Errorf("%w: subject_email must be an email address", ErrInvalid)
	}
	if !ValidRole(b.Role) {
		return RoleBinding{}, fmt.Errorf("%w: role must be one of %s", ErrInvalid, strings.Join(Roles, ", "))
	}
	kind, err := scopeTarget(b.Role, b.ScopeKind, &b.CustomerID, &b.PartnerID)
	if err != nil {
		return RoleBinding{}, err
	}
	b.ScopeKind = kind
	var id string
	err = s.db.QueryRowContext(ctx, `
		WITH ins AS (
			INSERT INTO role_bindings (subject_email, role, scope_kind, customer_id, partner_id, granted_by)
			VALUES ($1, $2, $3, $4, $6, $5)
			ON CONFLICT DO NOTHING
			RETURNING id
		)
		SELECT id FROM ins
		UNION ALL
		SELECT id FROM role_bindings WHERE subject_email = $1 AND role = $2 AND scope_kind = $3 AND customer_id IS NOT DISTINCT FROM $4::uuid AND partner_id IS NOT DISTINCT FROM $6::uuid
		LIMIT 1`,
		b.SubjectEmail, b.Role, b.ScopeKind, nullStr(b.CustomerID), strings.TrimSpace(b.GrantedBy), nullStr(b.PartnerID)).Scan(&id)
	if err != nil {
		return RoleBinding{}, mapErr(err)
	}
	return s.GetRoleBinding(ctx, id)
}

// DeleteRoleBinding revokes one binding by id.
func (s *Store) DeleteRoleBinding(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM role_bindings WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountSovereignAdmins counts explicit sovereign-admin bindings; the API
// refuses to revoke the last one when no OPERATOR_EMAILS back it up.
func (s *Store) CountSovereignAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM role_bindings WHERE role = $1`, RoleSovereignAdmin).Scan(&n)
	return n, mapErr(err)
}

// ---------------------------------------------------------------------------
// directory group mappings
// ---------------------------------------------------------------------------

const groupMappingColumns = `m.id, m.group_name, m.role, m.scope_kind, m.customer_id, COALESCE(c.name, ''), m.created_at, m.partner_id, COALESCE(p.name, '')`

const groupMappingFrom = ` FROM group_role_mappings m LEFT JOIN customers c ON c.id = m.customer_id LEFT JOIN partners p ON p.id = m.partner_id`

func scanGroupMapping(row interface{ Scan(...any) error }) (GroupRoleMapping, error) {
	var m GroupRoleMapping
	var cust, partner sql.NullString
	if err := row.Scan(&m.ID, &m.GroupName, &m.Role, &m.ScopeKind, &cust, &m.CustomerName, &m.CreatedAt, &partner, &m.PartnerName); err != nil {
		return m, mapErr(err)
	}
	m.CustomerID = strPtr(cust)
	m.PartnerID = strPtr(partner)
	m.CreatedAt = m.CreatedAt.UTC()
	return m, nil
}

// ListGroupRoleMappings returns every mapping, by group then role.
func (s *Store) ListGroupRoleMappings(ctx context.Context) ([]GroupRoleMapping, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupMappingColumns+groupMappingFrom+` ORDER BY m.group_name, (m.scope_kind = 'sovereign') DESC, (m.scope_kind = 'partner') DESC, p.name NULLS FIRST, c.name NULLS FIRST, m.role`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []GroupRoleMapping{}
	for rows.Next() {
		m, err := scanGroupMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// validateGroupMapping normalises one mapping the way UpsertRoleBinding
// normalises a binding.
func validateGroupMapping(m GroupRoleMapping) (GroupRoleMapping, error) {
	m.GroupName = strings.TrimSpace(m.GroupName)
	if m.GroupName == "" {
		return m, fmt.Errorf("%w: group_name is required", ErrInvalid)
	}
	if !ValidRole(m.Role) {
		return m, fmt.Errorf("%w: role must be one of %s", ErrInvalid, strings.Join(Roles, ", "))
	}
	kind, err := scopeTarget(m.Role, m.ScopeKind, &m.CustomerID, &m.PartnerID)
	if err != nil {
		return m, err
	}
	m.ScopeKind = kind
	return m, nil
}

// ReplaceGroupRoleMappings makes the given set THE set (PUT semantics), in
// one transaction: nothing is applied when any entry is invalid.
func (s *Store) ReplaceGroupRoleMappings(ctx context.Context, in []GroupRoleMapping) ([]GroupRoleMapping, error) {
	clean := make([]GroupRoleMapping, 0, len(in))
	for _, m := range in {
		v, err := validateGroupMapping(m)
		if err != nil {
			return nil, err
		}
		clean = append(clean, v)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_role_mappings`); err != nil {
		return nil, mapErr(err)
	}
	for _, m := range clean {
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_role_mappings (group_name, role, scope_kind, customer_id, partner_id) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
			m.GroupName, m.Role, m.ScopeKind, nullStr(m.CustomerID), nullStr(m.PartnerID)); err != nil {
			return nil, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListGroupRoleMappings(ctx)
}

// BindingsForGroups resolves the directory groups the gate forwarded into
// bindings, each carrying "group:<name>" as its source. Group names are
// matched exactly after trimming; an empty list resolves to nothing.
func (s *Store) BindingsForGroups(ctx context.Context, groups []string) ([]RoleBinding, error) {
	var names []string
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" {
			names = append(names, g)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupMappingColumns+groupMappingFrom+` WHERE m.group_name = ANY($1) ORDER BY m.group_name, m.role`, pq.Array(names))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []RoleBinding
	for rows.Next() {
		m, err := scanGroupMapping(rows)
		if err != nil {
			return nil, err
		}
		at := m.CreatedAt
		out = append(out, RoleBinding{Role: m.Role, ScopeKind: m.ScopeKind, CustomerID: m.CustomerID, CustomerName: m.CustomerName, PartnerID: m.PartnerID, PartnerName: m.PartnerName, GrantedAt: &at, Source: BindingSourceGroup + m.GroupName})
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// the customer-scoped view an older reader knows as customer_users
// ---------------------------------------------------------------------------

// LegacyCustomerRole maps a customer role onto the admin | viewer vocabulary
// of customer_users; anything that is not an owner reads as viewer.
func LegacyCustomerRole(role string) string {
	if role == RoleCustomerOwner {
		return "admin"
	}
	return "viewer"
}

// CustomerRoleFromLegacy accepts either vocabulary for a customer user: the
// legacy admin | viewer (and the legacy session name customer-admin), or one
// of the three customer roles. ok=false for anything else.
func CustomerRoleFromLegacy(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", RoleCustomerAdmin, RoleCustomerOwner:
		return RoleCustomerOwner, true
	case RoleCustomerBilling:
		return RoleCustomerBilling, true
	case "viewer", RoleCustomerViewer:
		return RoleCustomerViewer, true
	}
	return "", false
}

// ListCustomerUsers returns the users of one customer — its customer-scoped
// bindings, one row per (email, role), owners first.
func (s *Store) ListCustomerUsers(ctx context.Context, customerID string) ([]CustomerUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT customer_id, subject_email, role FROM role_bindings WHERE customer_id = $1 AND scope_kind = 'customer'
		ORDER BY (role = 'customer-owner') DESC, (role = 'customer-billing') DESC, subject_email`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CustomerUser{}
	for rows.Next() {
		var u CustomerUser
		if err := rows.Scan(&u.CustomerID, &u.Email, &u.BindingRole); err != nil {
			return nil, err
		}
		u.Role = LegacyCustomerRole(u.BindingRole)
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpsertCustomerUser adds or re-roles a user on a customer: the email ends
// up with exactly ONE customer-scoped role on that customer. role accepts
// the legacy admin | viewer or a customer role name.
func (s *Store) UpsertCustomerUser(ctx context.Context, customerID, email, role string) error {
	r, ok := CustomerRoleFromLegacy(role)
	if !ok {
		return fmt.Errorf("%w: role must be customer-owner, customer-billing or customer-viewer (admin | viewer accepted)", ErrInvalid)
	}
	email = strings.ToLower(strings.TrimSpace(email))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_bindings WHERE customer_id = $1 AND subject_email = $2 AND scope_kind = 'customer' AND role <> $3`, customerID, email, r); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO role_bindings (subject_email, role, scope_kind, customer_id, granted_by) VALUES ($1, $2, 'customer', $3, '') ON CONFLICT DO NOTHING`, email, r, customerID); err != nil {
		return mapErr(err)
	}
	return tx.Commit()
}

// DeleteCustomerUser removes every customer-scoped binding an email holds on
// a customer. Sovereign bindings are never touched here.
func (s *Store) DeleteCustomerUser(ctx context.Context, customerID, email string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM role_bindings WHERE customer_id = $1 AND subject_email = $2 AND scope_kind = 'customer'`, customerID, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RoleForEmail resolves the LEGACY customer role of an email: the highest
// customer binding, owner winning, then by customer slug. ok=false when
// none. Kept for the PIN "known principal" check and older callers; the
// session itself carries every binding.
func (s *Store) RoleForEmail(ctx context.Context, email string) (customerID, role string, ok bool, err error) {
	var bound string
	err = s.db.QueryRowContext(ctx, `SELECT b.customer_id, b.role FROM role_bindings b JOIN customers c ON c.id = b.customer_id
		WHERE b.subject_email = $1 AND b.scope_kind = 'customer' ORDER BY (b.role = 'customer-owner') DESC, (b.role = 'customer-billing') DESC, c.slug LIMIT 1`,
		strings.ToLower(strings.TrimSpace(email))).Scan(&customerID, &bound)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, mapErr(err)
	}
	return customerID, LegacyCustomerRole(bound), true, nil
}
