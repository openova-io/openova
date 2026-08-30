package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

const sourceColumns = `s.id, s.customer_id, s.kind, s.region, s.project_id, s.domain_id, s.credential_id, s.status, s.verified_at, s.last_collected_at, s.last_error,
	COALESCE((SELECT c.access_key FROM credentials c WHERE c.id = s.credential_id), '')`

func scanSource(row interface{ Scan(...any) error }) (CostSource, error) {
	var src CostSource
	var domain, cred, lastErr sql.NullString
	var verified, collected sql.NullTime
	err := row.Scan(&src.ID, &src.CustomerID, &src.Kind, &src.Region, &src.ProjectID, &domain, &cred, &src.Status, &verified, &collected, &lastErr, &src.AccessKey)
	if err != nil {
		return src, mapErr(err)
	}
	src.DomainID = strPtr(domain)
	src.CredentialID = strPtr(cred)
	src.VerifiedAt = timePtr(verified)
	src.LastCollectedAt = timePtr(collected)
	src.LastError = strPtr(lastErr)
	return src, nil
}

// ListSources returns a customer's sources.
func (s *Store) ListSources(ctx context.Context, scope Scope, customerID string) ([]CostSource, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceColumns+` FROM cost_sources s WHERE s.customer_id = $1 ORDER BY s.region, s.project_id`, customerID)
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

// GetSource returns one source inside the scope.
func (s *Store) GetSource(ctx context.Context, scope Scope, id string) (CostSource, error) {
	src, err := scanSource(s.db.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM cost_sources s WHERE s.id = $1`, id))
	if err != nil {
		return src, err
	}
	if !scope.Allows(src.CustomerID) {
		return CostSource{}, ErrNotFound
	}
	return src, nil
}

// UpsertSource creates a source or returns the existing one for the same
// (customer, kind, region, project); created reports which happened.
func (s *Store) UpsertSource(ctx context.Context, customerID, kind, region, projectID string) (src CostSource, created bool, err error) {
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceColumns+` FROM cost_sources s
		JOIN customers c ON c.id = s.customer_id
		JOIN credentials cr ON cr.id = s.credential_id AND cr.revoked_at IS NULL
		WHERE s.status = 'verified' AND s.kind = 'huawei-project' AND c.status = 'active' ORDER BY s.id`)
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

// SourceStatusCounts feeds the metrics gauge.
func (s *Store) SourceStatusCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM cost_sources GROUP BY status`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]int{"pending": 0, "verified": 0, "failed": 0}
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
