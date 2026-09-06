package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// SavedView is one user's saved explorer (or other page) state (#6867). A
// view belongs to the email that saved it; nobody else can list or delete it.
type SavedView struct {
	ID         string          `json:"id"`
	OwnerEmail string          `json:"owner_email"`
	Name       string          `json:"name"`
	Page       string          `json:"page"`
	Params     json.RawMessage `json:"params"`
	CreatedAt  time.Time       `json:"created_at"`
}

const viewColumns = `id, owner_email, name, page, params, created_at`

func scanView(row interface{ Scan(...any) error }) (SavedView, error) {
	var v SavedView
	var params []byte
	if err := row.Scan(&v.ID, &v.OwnerEmail, &v.Name, &v.Page, &params, &v.CreatedAt); err != nil {
		return v, mapErr(err)
	}
	if len(params) == 0 {
		params = []byte("{}")
	}
	v.Params = params
	v.CreatedAt = v.CreatedAt.UTC()
	return v, nil
}

// ListViews returns the caller's views for one page, oldest first so the
// order in a menu is stable as views are added.
func (s *Store) ListViews(ctx context.Context, ownerEmail, page string) ([]SavedView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+viewColumns+` FROM saved_views WHERE owner_email = $1 AND page = $2 ORDER BY created_at, name`,
		strings.ToLower(strings.TrimSpace(ownerEmail)), page)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []SavedView{}
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateView saves a view. The (owner, page, name) unique key surfaces as
// ErrConflict so a duplicate name is refused rather than silently doubled.
func (s *Store) CreateView(ctx context.Context, ownerEmail, name, page string, params json.RawMessage) (SavedView, error) {
	if len(params) == 0 {
		params = []byte("{}")
	}
	return scanView(s.db.QueryRowContext(ctx, `INSERT INTO saved_views (owner_email, name, page, params) VALUES ($1, $2, $3, $4) RETURNING `+viewColumns,
		strings.ToLower(strings.TrimSpace(ownerEmail)), strings.TrimSpace(name), page, []byte(params)))
}

// DeleteView removes one of the caller's views. Another user's view reads
// as ErrNotFound: ids are not confirmed across owners.
func (s *Store) DeleteView(ctx context.Context, ownerEmail, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM saved_views WHERE id = $1 AND owner_email = $2`, id, strings.ToLower(strings.TrimSpace(ownerEmail)))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
