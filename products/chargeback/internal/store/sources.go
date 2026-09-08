package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const sourceColumns = `s.id, s.customer_id, COALESCE((SELECT c.name FROM customers c WHERE c.id = s.customer_id), ''), s.kind, s.layer, s.price_book_id,
	COALESCE((SELECT b.name FROM price_books b WHERE b.id = s.price_book_id), ''), s.internal,
	s.region, s.project_id, s.domain_id, s.credential_id, s.status, s.verified_at, s.last_collected_at, s.last_error, s.scope_token,
	COALESCE((SELECT c.access_key FROM credentials c WHERE c.id = s.credential_id), '')`

func scanSource(row interface{ Scan(...any) error }) (CostSource, error) {
	var src CostSource
	var customer, book, domain, cred, lastErr sql.NullString
	var verified, collected sql.NullTime
	err := row.Scan(&src.ID, &customer, &src.CustomerName, &src.Kind, &src.Layer, &book, &src.PriceBookName, &src.Internal,
		&src.Region, &src.ProjectID, &domain, &cred, &src.Status, &verified, &collected, &lastErr, &src.ScopeToken, &src.AccessKey)
	if err != nil {
		return src, mapErr(err)
	}
	src.CustomerID = customer.String
	src.PriceBookID = strPtr(book)
	src.DomainID = strPtr(domain)
	src.CredentialID = strPtr(cred)
	src.VerifiedAt = timePtr(verified)
	src.LastCollectedAt = timePtr(collected)
	src.LastError = strPtr(lastErr)
	return src, nil
}

// Label names a source for people: the project id (cloud) or the
// Organization slug (platform), falling back to the kind.
func (s CostSource) Label() string {
	if s.ProjectID != "" {
		return s.ProjectID
	}
	return s.Kind
}

func (s *Store) querySources(ctx context.Context, where string, args ...any) ([]CostSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceColumns+` FROM cost_sources s `+where, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CostSource{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListSources returns a customer's sources. The internal platform source has
// no customer and is never in this list.
func (s *Store) ListSources(ctx context.Context, scope Scope, customerID string) ([]CostSource, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	return s.querySources(ctx, `WHERE s.customer_id = $1 ORDER BY s.layer, s.region, s.project_id`, customerID)
}

// ListAllSources is the operator-wide listing: every source of every
// customer, and the internal platform source when includeInternal is set.
func (s *Store) ListAllSources(ctx context.Context, includeInternal bool) ([]CostSource, error) {
	where := `WHERE NOT s.internal`
	if includeInternal {
		where = `WHERE true`
	}
	return s.querySources(ctx, where+` ORDER BY (SELECT c.name FROM customers c WHERE c.id = s.customer_id) NULLS LAST, s.layer, s.region, s.project_id`)
}

// GetSource returns one source inside the scope. The internal source (no
// customer) is visible to the operator only.
func (s *Store) GetSource(ctx context.Context, scope Scope, id string) (CostSource, error) {
	src, err := scanSource(s.db.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM cost_sources s WHERE s.id = $1`, id))
	if err != nil {
		return src, err
	}
	if src.Internal {
		if !scope.Operator {
			return CostSource{}, ErrNotFound
		}
		return src, nil
	}
	if !scope.Allows(src.CustomerID) {
		return CostSource{}, ErrNotFound
	}
	return src, nil
}

// UpsertSource creates a source or returns the existing one for the same
// (customer, kind, region, project); created reports which happened.
func (s *Store) UpsertSource(ctx context.Context, customerID, kind, region, projectID string) (src CostSource, created bool, err error) {
	if kind == SourceKindPlatform {
		return CostSource{}, false, fmt.Errorf("%w: %s is the internal platform source and has no customer; use EnsureInternalSource", ErrInvalid, kind)
	}
	var id string
	err = s.db.QueryRowContext(ctx, `INSERT INTO cost_sources (customer_id, kind, region, project_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (customer_id, kind, region, project_id) DO UPDATE SET kind = EXCLUDED.kind RETURNING id, (xmax = 0)`,
		customerID, kind, strings.TrimSpace(region), strings.TrimSpace(projectID)).Scan(&id, &created)
	if err != nil {
		return CostSource{}, false, mapErr(err)
	}
	src, err = s.GetSource(ctx, OperatorScope, id)
	return src, created, err
}

// EnsureInternalSource returns the Sovereign's own platform source for the
// given Organization slug (projectID), creating it when absent: kind
// openova-platform, no customer, internal, verified (there is nothing
// external to check). created reports whether this call made it.
func (s *Store) EnsureInternalSource(ctx context.Context, projectID string) (src CostSource, created bool, err error) {
	var id string
	err = s.db.QueryRowContext(ctx, `INSERT INTO cost_sources (customer_id, kind, region, project_id, internal, status, verified_at)
		VALUES (NULL, $1, '', $2, true, 'verified', now())
		ON CONFLICT (kind, region, project_id) WHERE customer_id IS NULL DO UPDATE SET kind = EXCLUDED.kind RETURNING id, (xmax = 0)`,
		SourceKindPlatform, strings.TrimSpace(projectID)).Scan(&id, &created)
	if err != nil {
		return CostSource{}, false, mapErr(err)
	}
	src, err = s.GetSource(ctx, OperatorScope, id)
	return src, created, err
}

// GetInternalSource returns the internal platform source for an Organization
// slug, or ErrNotFound.
func (s *Store) GetInternalSource(ctx context.Context, projectID string) (CostSource, error) {
	return scanSource(s.db.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM cost_sources s WHERE s.internal AND s.kind = $1 AND s.project_id = $2`, SourceKindPlatform, strings.TrimSpace(projectID)))
}

// RetireOrganizationCustomer turns the customer row that the Sovereign's own
// Organization was once synced as into a plain external customer (its cloud
// sources stay) and moves its openova-org source — with every usage record on
// it — onto the internal platform source (DESIGN.md §4.1). The two-layer
// migration does the same for a database that already carried overhead
// usage; this is the runtime path for one that did not yet.
func (s *Store) RetireOrganizationCustomer(ctx context.Context, customerID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE usage_records u SET customer_id = NULL FROM cost_sources s
		WHERE s.id = u.source_id AND s.customer_id = $1 AND s.kind = $2`, customerID, SourceKindOrg); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cost_sources SET kind = $3, internal = true, customer_id = NULL, price_book_id = NULL, status = 'verified', verified_at = COALESCE(verified_at, now())
		WHERE customer_id = $1 AND kind = $2`, customerID, SourceKindOrg, SourceKindPlatform); err != nil {
		return mapErr(err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE customers SET kind = 'external', org_slug = NULL, plan_slug = '', updated_at = now() WHERE id = $1`, customerID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SetSourceScopeToken narrows a source to one deployment's resources (#6859).
//
// The scope column shipped in #6855 but nothing could write it, so the fix was
// inert: every source kept billing the whole project. That is the
// activation-left-to-a-second-file shape — the field existed, the filter
// existed, and no path connected them.
//
// An empty token clears the scope, which is a legitimate operation (bill the
// whole project again) and must stay possible.
func (s *Store) SetSourceScopeToken(ctx context.Context, sourceID, token string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE cost_sources SET scope_token = $2 WHERE id = $1`, sourceID, strings.TrimSpace(token))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSourcePriceBook assigns a price book to a source, or clears it with an
// empty id. The book's scope must equal the source's layer — a cloud book
// on a platform source (or the reverse) is refused with ErrInvalid, and so
// is any book on the internal source, which is never billed. A missing book
// is ErrNotFound.
func (s *Store) SetSourcePriceBook(ctx context.Context, sourceID, bookID string) error {
	src, err := s.GetSource(ctx, OperatorScope, sourceID)
	if err != nil {
		return err
	}
	if src.Internal {
		return fmt.Errorf("%w: the internal platform source is never billed and takes no price book", ErrInvalid)
	}
	bookID = strings.TrimSpace(bookID)
	if bookID == "" {
		_, err := s.db.ExecContext(ctx, `UPDATE cost_sources SET price_book_id = NULL WHERE id = $1`, sourceID)
		return mapErr(err)
	}
	var scope string
	if err := s.db.QueryRowContext(ctx, `SELECT scope FROM price_books WHERE id = $1`, bookID).Scan(&scope); err != nil {
		if errors.Is(mapErr(err), ErrNotFound) {
			return fmt.Errorf("%w: price book %s does not exist", ErrInvalid, bookID)
		}
		return mapErr(err)
	}
	if scope != src.Layer {
		return fmt.Errorf("%w: price book scope %s does not match source layer %s", ErrInvalid, scope, src.Layer)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE cost_sources SET price_book_id = $2 WHERE id = $1`, sourceID, bookID)
	return mapErr(err)
}

// StatusDisabled is a decommissioned source: the collector skips it and it
// counts as neither verified nor live, but its history is billing data — it
// still rates, still appears in the explorer, and still stands on every
// statement already issued from it.
const StatusDisabled = "disabled"

// SetSourceDisabled decommissions a source, or brings it back. Disabling
// never touches the ledger; enabling restores the source to `verified` when
// it had been verified before (its credential and location are unchanged)
// and to `pending` otherwise, so a source that never proved itself must
// still be verified before it collects.
//
// The internal platform source is maintained by the collector and cannot be
// disabled: there is no operator decision to record there.
func (s *Store) SetSourceDisabled(ctx context.Context, sourceID string, disabled bool) (CostSource, error) {
	src, err := s.GetSource(ctx, OperatorScope, sourceID)
	if err != nil {
		return CostSource{}, err
	}
	if src.Internal {
		return CostSource{}, fmt.Errorf("%w: the internal platform source is maintained by the platform collector and cannot be disabled", ErrInvalid)
	}
	if disabled {
		if _, err := s.db.ExecContext(ctx, `UPDATE cost_sources SET status = $2 WHERE id = $1`, sourceID, StatusDisabled); err != nil {
			return CostSource{}, mapErr(err)
		}
		return s.GetSource(ctx, OperatorScope, sourceID)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE cost_sources
		SET status = CASE WHEN verified_at IS NOT NULL THEN 'verified' ELSE 'pending' END
		WHERE id = $1 AND status = $2`, sourceID, StatusDisabled); err != nil {
		return CostSource{}, mapErr(err)
	}
	return s.GetSource(ctx, OperatorScope, sourceID)
}

// SetSourceCredential links a credential to a source and resets it to pending.
func (s *Store) SetSourceCredential(ctx context.Context, sourceID, credentialID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE cost_sources SET credential_id = $2, status = 'pending', last_error = NULL WHERE id = $1`, sourceID, credentialID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSourceVerified records a successful activation check.
func (s *Store) SetSourceVerified(ctx context.Context, sourceID string, domainID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cost_sources SET status = 'verified', verified_at = now(), last_error = NULL, domain_id = COALESCE(NULLIF($2, ''), domain_id) WHERE id = $1`, sourceID, domainID)
	return mapErr(err)
}

// SetSourceFailed records a failed activation check with the gateway code.
func (s *Store) SetSourceFailed(ctx context.Context, sourceID, lastError string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cost_sources SET status = 'failed', last_error = $2 WHERE id = $1`, sourceID, truncateErr(lastError))
	return mapErr(err)
}

// SetSourceCollected stamps a successful collection tick.
func (s *Store) SetSourceCollected(ctx context.Context, sourceID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cost_sources SET last_collected_at = $2, last_error = NULL WHERE id = $1`, sourceID, at)
	return mapErr(err)
}

// SetSourceError records a collection error without changing status.
func (s *Store) SetSourceError(ctx context.Context, sourceID, lastError string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cost_sources SET last_error = $2 WHERE id = $1`, sourceID, truncateErr(lastError))
	return mapErr(err)
}

// DeleteSource removes a source and its inventory/usage (cascade).
func (s *Store) DeleteSource(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cost_sources WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListVerifiedSources returns every collectable source with its customer's
// status, for the collector loop.
func (s *Store) ListVerifiedSources(ctx context.Context) ([]CostSource, error) {
	return s.querySources(ctx, `JOIN customers c ON c.id = s.customer_id
		JOIN credentials cr ON cr.id = s.credential_id AND cr.revoked_at IS NULL
		WHERE s.status = 'verified' AND s.kind = 'huawei-project' AND c.status = 'active' ORDER BY s.id`)
}

// SourceStatusCounts feeds the metrics gauge and the overview. The internal
// platform source is not a customer's source and is not counted.
func (s *Store) SourceStatusCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM cost_sources WHERE NOT internal GROUP BY status`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]int{"pending": 0, "verified": 0, "failed": 0, StatusDisabled: 0}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// credentials — the secret is stored envelope-encrypted and only ever
// returned by GetCredentialSecret, which the collector and verifier call.
// ---------------------------------------------------------------------------

// CreateCredential stores an encrypted secret and returns the public view.
func (s *Store) CreateCredential(ctx context.Context, customerID, accessKey string, secretEnc []byte) (Credential, error) {
	var c Credential
	var rotated, revoked sql.NullTime
	err := s.db.QueryRowContext(ctx, `INSERT INTO credentials (customer_id, kind, access_key, secret_key_enc) VALUES ($1, 'aksk', $2, $3)
		RETURNING id, customer_id, kind, access_key, created_at, rotated_at, revoked_at`, customerID, strings.TrimSpace(accessKey), secretEnc).
		Scan(&c.ID, &c.CustomerID, &c.Kind, &c.AccessKey, &c.CreatedAt, &rotated, &revoked)
	if err != nil {
		return c, mapErr(err)
	}
	c.RotatedAt = timePtr(rotated)
	c.RevokedAt = timePtr(revoked)
	return c, nil
}

// MarkCredentialRotated stamps the superseded credential.
func (s *Store) MarkCredentialRotated(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE credentials SET rotated_at = now() WHERE id = $1 AND rotated_at IS NULL`, id)
	return mapErr(err)
}

// RevokeCredential retires a credential that failed verification or was
// superseded; the collector never reads revoked rows.
func (s *Store) RevokeCredential(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE credentials SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return mapErr(err)
}

// GetCredentialSecret returns the access key and the encrypted secret blob.
func (s *Store) GetCredentialSecret(ctx context.Context, id string) (accessKey string, secretEnc []byte, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT access_key, secret_key_enc FROM credentials WHERE id = $1 AND revoked_at IS NULL`, id).Scan(&accessKey, &secretEnc)
	return accessKey, secretEnc, mapErr(err)
}

func truncateErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// SourcePatch carries optional source updates; nil means unchanged.
type SourcePatch struct {
	Region     *string
	ProjectID  *string
	ScopeToken *string
	DomainID   *string
	// PriceBookID assigns the book that rates this source ("" clears it).
	// Its scope must equal the source's layer (SetSourcePriceBook).
	PriceBookID *string
}

// Fields names the fields the patch changes, for the audit entry.
func (p SourcePatch) Fields() []string {
	var f []string
	if p.Region != nil {
		f = append(f, "region")
	}
	if p.ProjectID != nil {
		f = append(f, "project_id")
	}
	if p.ScopeToken != nil {
		f = append(f, "scope_token")
	}
	if p.DomainID != nil {
		f = append(f, "domain_id")
	}
	if p.PriceBookID != nil {
		f = append(f, "price_book_id")
	}
	return f
}

// UpdateSource applies a patch. Changing the region or the project id points
// the source at a different cloud location, so the stored verification no
// longer proves anything: status drops back to pending and last_error is
// cleared, and the operator re-verifies. A scope_token, domain_id or
// price_book_id change keeps the status — the credential's reach is
// unchanged. The book is validated first (scope vs layer), so a refused
// assignment changes nothing else either.
func (s *Store) UpdateSource(ctx context.Context, id string, p SourcePatch) (CostSource, error) {
	if p.PriceBookID != nil {
		if err := s.SetSourcePriceBook(ctx, id, *p.PriceBookID); err != nil {
			return CostSource{}, err
		}
	}
	sets := []string{}
	var args []any
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Region != nil {
		add("region", strings.TrimSpace(*p.Region))
	}
	if p.ProjectID != nil {
		add("project_id", strings.TrimSpace(*p.ProjectID))
	}
	if p.ScopeToken != nil {
		add("scope_token", strings.TrimSpace(*p.ScopeToken))
	}
	if p.DomainID != nil {
		add("domain_id", nullStr(p.DomainID))
	}
	if p.Region != nil || p.ProjectID != nil {
		sets = append(sets, "status = 'pending'", "last_error = NULL", "verified_at = NULL")
	}
	if len(sets) == 0 {
		return s.GetSource(ctx, OperatorScope, id)
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE cost_sources SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args)), args...)
	if err != nil {
		return CostSource{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return CostSource{}, ErrNotFound
	}
	return s.GetSource(ctx, OperatorScope, id)
}
