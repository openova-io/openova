package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// InventoryUpsert is one observed resource for UpsertInventory.
type InventoryUpsert struct {
	ResourceID string
	Kind       string
	Name       string
	Attrs      any
	Created    time.Time // zero = unknown; sets first_seen on insert when set
	SeenAt     time.Time
}

// UpsertInventory records observed resources: new rows get first_seen =
// created (when known) or the observation time; existing rows refresh
// last_seen/name/attrs and clear deleted_at (a resource seen again is alive).
// It returns the previous attrs of rows that already existed, keyed by
// resource id, so the caller can detect status/flavor changes.
func (s *Store) UpsertInventory(ctx context.Context, sourceID string, items []InventoryUpsert) (map[string]json.RawMessage, error) {
	prev := map[string]json.RawMessage{}
	if len(items) == 0 {
		return prev, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, it := range items {
		var old sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT attrs::text FROM resource_inventory WHERE source_id = $1 AND resource_id = $2`, sourceID, it.ResourceID).Scan(&old); err != nil && err != sql.ErrNoRows {
			return nil, mapErr(err)
		}
		if old.Valid {
			prev[it.ResourceID] = json.RawMessage(old.String)
		}
		first := it.SeenAt
		if !it.Created.IsZero() {
			first = it.Created
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_inventory (source_id, resource_id, kind, name, attrs, first_seen, last_seen)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (source_id, resource_id) DO UPDATE SET name = EXCLUDED.name, attrs = EXCLUDED.attrs, last_seen = EXCLUDED.last_seen, deleted_at = NULL`,
			sourceID, it.ResourceID, it.Kind, it.Name, jsonOrEmpty(it.Attrs), first, it.SeenAt); err != nil {
			return nil, mapErr(err)
		}
	}
	return prev, tx.Commit()
}

// MarkInventoryDeleted flags live rows of the given kinds that were not seen
// at this tick as deleted at the observation time.
func (s *Store) MarkInventoryDeleted(ctx context.Context, sourceID string, kinds []string, seenIDs []string, at time.Time) (int64, error) {
	if len(kinds) == 0 {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE resource_inventory SET deleted_at = $4
		WHERE source_id = $1 AND kind = ANY($2) AND deleted_at IS NULL AND NOT (resource_id = ANY($3))`,
		sourceID, pqArray(kinds), pqArray(seenIDs), at)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// SetInventoryAttrs replaces a row's attrs (used to store transitions).
func (s *Store) SetInventoryAttrs(ctx context.Context, sourceID, resourceID string, attrs any) error {
	_, err := s.db.ExecContext(ctx, `UPDATE resource_inventory SET attrs = $3 WHERE source_id = $1 AND resource_id = $2`, sourceID, resourceID, jsonOrEmpty(attrs))
	return mapErr(err)
}

// SetInventoryBounds corrects first_seen and/or deleted_at from the audit trail.
func (s *Store) SetInventoryBounds(ctx context.Context, sourceID, resourceID string, firstSeen, deletedAt *time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE resource_inventory SET
		first_seen = COALESCE($3, first_seen),
		deleted_at = COALESCE($4, deleted_at)
		WHERE source_id = $1 AND resource_id = $2`, sourceID, resourceID, nullTime(firstSeen), nullTime(deletedAt))
	return mapErr(err)
}

// ListInventory returns a source's rows (live and deleted).
func (s *Store) ListInventory(ctx context.Context, sourceID string) ([]InventoryItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, resource_id, kind, name, attrs, first_seen, last_seen, deleted_at FROM resource_inventory WHERE source_id = $1 ORDER BY kind, name`, sourceID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanInventory(rows)
}

// ListCustomerInventory returns every source's rows for a customer.
func (s *Store) ListCustomerInventory(ctx context.Context, scope Scope, customerID string) ([]InventoryItem, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.source_id, i.resource_id, i.kind, i.name, i.attrs, i.first_seen, i.last_seen, i.deleted_at
		FROM resource_inventory i JOIN cost_sources s ON s.id = i.source_id WHERE s.customer_id = $1 ORDER BY i.kind, i.name`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return scanInventory(rows)
}

// GetInventoryItem returns one row.
func (s *Store) GetInventoryItem(ctx context.Context, sourceID, resourceID string) (InventoryItem, error) {
	var it InventoryItem
	var attrs []byte
	var deleted sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT source_id, resource_id, kind, name, attrs, first_seen, last_seen, deleted_at FROM resource_inventory WHERE source_id = $1 AND resource_id = $2`, sourceID, resourceID).
		Scan(&it.SourceID, &it.ResourceID, &it.Kind, &it.Name, &attrs, &it.FirstSeen, &it.LastSeen, &deleted)
	if err != nil {
		return it, mapErr(err)
	}
	it.Attrs = attrs
	it.DeletedAt = timePtr(deleted)
	return it, nil
}

func scanInventory(rows *sql.Rows) ([]InventoryItem, error) {
	out := []InventoryItem{}
	for rows.Next() {
		var it InventoryItem
		var attrs []byte
		var deleted sql.NullTime
		if err := rows.Scan(&it.SourceID, &it.ResourceID, &it.Kind, &it.Name, &attrs, &it.FirstSeen, &it.LastSeen, &deleted); err != nil {
			return nil, err
		}
		it.Attrs = attrs
		it.DeletedAt = timePtr(deleted)
		it.FirstSeen = it.FirstSeen.UTC()
		it.LastSeen = it.LastSeen.UTC()
		out = append(out, it)
	}
	return out, rows.Err()
}
