package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"
)

// NewToken returns 32 random bytes as hex (sessions, invites).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashPIN is the stored form of a PIN code.
func HashPIN(email, code string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)) + ":" + strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

// PutPIN stores a fresh PIN (one per email, replaces any older one).
func (s *Store) PutPIN(ctx context.Context, email, code string, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pins (email, code_hash, expires_at, attempts) VALUES ($1, $2, $3, 0)
		ON CONFLICT (email) DO UPDATE SET code_hash = EXCLUDED.code_hash, expires_at = EXCLUDED.expires_at, attempts = 0`,
		strings.ToLower(strings.TrimSpace(email)), HashPIN(email, code), time.Now().Add(ttl))
	return mapErr(err)
}

// PINIssuedRecently reports whether a PIN for the email was issued within
// the window (used to throttle requests).
func (s *Store) PINIssuedRecently(ctx context.Context, email string, ttl, window time.Duration) (bool, error) {
	var exp sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM pins WHERE email = $1`, strings.ToLower(strings.TrimSpace(email))).Scan(&exp)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, mapErr(err)
	}
	issuedAt := exp.Time.Add(-ttl)
	return time.Since(issuedAt) < window, nil
}

// VerifyPIN checks a code, counting attempts; a correct code consumes the PIN.
// It returns false for wrong, expired, or over-tried codes.
func (s *Store) VerifyPIN(ctx context.Context, email, code string, maxAttempts int) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var hash string
	var expires time.Time
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT code_hash, expires_at, attempts FROM pins WHERE email = $1 FOR UPDATE`, email).Scan(&hash, &expires, &attempts)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, mapErr(err)
	}
	if time.Now().After(expires) || attempts >= maxAttempts {
		_, _ = tx.ExecContext(ctx, `DELETE FROM pins WHERE email = $1`, email)
		return false, tx.Commit()
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(HashPIN(email, code))) != 1 {
		_, _ = tx.ExecContext(ctx, `UPDATE pins SET attempts = attempts + 1 WHERE email = $1`, email)
		return false, tx.Commit()
	}
	// Consume the code but keep the row so the request throttle still sees
	// when the last code was issued.
	if _, err := tx.ExecContext(ctx, `UPDATE pins SET attempts = $2, code_hash = '' WHERE email = $1`, email, maxAttempts); err != nil {
		return false, mapErr(err)
	}
	return true, tx.Commit()
}

// CreateSession issues a session token.
func (s *Store) CreateSession(ctx context.Context, email, role string, customerID *string, ttl time.Duration) (Session, error) {
	tok, err := NewToken()
	if err != nil {
		return Session{}, err
	}
	sess := Session{Token: tok, Email: strings.ToLower(strings.TrimSpace(email)), Role: role, CustomerID: customerID, ExpiresAt: time.Now().Add(ttl).UTC()}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (token, email, role, customer_id, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		sess.Token, sess.Email, sess.Role, nullStr(customerID), sess.ExpiresAt)
	if err != nil {
		return Session{}, mapErr(err)
	}
	return sess, nil
}

// GetSession resolves a live session (expired ones read as ErrNotFound).
func (s *Store) GetSession(ctx context.Context, token string) (Session, error) {
	var sess Session
	var cust sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT token, email, role, customer_id, expires_at FROM sessions WHERE token = $1 AND expires_at > now()`, token).
		Scan(&sess.Token, &sess.Email, &sess.Role, &cust, &sess.ExpiresAt)
	if err != nil {
		return sess, mapErr(err)
	}
	sess.CustomerID = strPtr(cust)
	sess.ExpiresAt = sess.ExpiresAt.UTC()
	return sess, nil
}

// DeleteSession logs a session out.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return mapErr(err)
}

// Scope derives the query scope of a session.
func (sess Session) Scope() Scope {
	if sess.Role == RoleOperator {
		return OperatorScope
	}
	if sess.CustomerID != nil {
		return CustomerScope(*sess.CustomerID)
	}
	return Scope{}
}

// CreateInvite issues an activation link token for a customer.
func (s *Store) CreateInvite(ctx context.Context, customerID, email string, ttl time.Duration) (Invite, error) {
	tok, err := NewToken()
	if err != nil {
		return Invite{}, err
	}
	inv := Invite{Token: tok, CustomerID: customerID, Email: strings.ToLower(strings.TrimSpace(email)), ExpiresAt: time.Now().Add(ttl).UTC(), CreatedAt: time.Now().UTC()}
	_, err = s.db.ExecContext(ctx, `INSERT INTO invites (token, customer_id, email, expires_at) VALUES ($1, $2, $3, $4)`, inv.Token, inv.CustomerID, inv.Email, inv.ExpiresAt)
	if err != nil {
		return Invite{}, mapErr(err)
	}
	return inv, nil
}

// GetInvite returns an invite regardless of state (callers check expiry/use).
func (s *Store) GetInvite(ctx context.Context, token string) (Invite, error) {
	var inv Invite
	var used sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT token, customer_id, email, expires_at, used_at, created_at FROM invites WHERE token = $1`, token).
		Scan(&inv.Token, &inv.CustomerID, &inv.Email, &inv.ExpiresAt, &used, &inv.CreatedAt)
	if err != nil {
		return inv, mapErr(err)
	}
	inv.UsedAt = timePtr(used)
	inv.ExpiresAt = inv.ExpiresAt.UTC()
	return inv, nil
}

// Usable reports whether the invite can still activate.
func (inv Invite) Usable(now time.Time) bool {
	return inv.UsedAt == nil && now.Before(inv.ExpiresAt)
}

// MarkInviteUsed consumes an invite.
func (s *Store) MarkInviteUsed(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE invites SET used_at = now() WHERE token = $1 AND used_at IS NULL`, token)
	return mapErr(err)
}

// PurgeExpired removes stale sessions, pins and invites (housekeeping).
func (s *Store) PurgeExpired(ctx context.Context) error {
	for _, q := range []string{
		`DELETE FROM sessions WHERE expires_at < now()`,
		`DELETE FROM pins WHERE expires_at < now()`,
		`DELETE FROM invites WHERE expires_at < now() - interval '30 days'`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return mapErr(err)
		}
	}
	return nil
}
