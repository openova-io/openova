package store

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const migration = `
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'member',
    password_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_providers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(provider, provider_id)
);

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT UNIQUE NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// User represents a platform user.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatar_url"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AuthProvider links a user to an external identity provider.
type AuthProvider struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Provider   string    `json:"provider"`
	ProviderID string    `json:"provider_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// Store provides PostgreSQL-backed persistence for auth data.
type Store struct {
	db *sql.DB
}

// New creates a Store and runs the schema migration.
func New(db *sql.DB) *Store {
	if _, err := db.Exec(migration); err != nil {
		panic("auth store migration: " + err.Error())
	}
	// Add password_hash column if missing (existing tables).
	db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT ''`)
	return &Store{db: db}
}

// SeedSuperadmin ensures a superadmin user exists with the given password.
// If the user exists but isn't superadmin, promotes them and resets the password.
// Returns true if a new user was created or an existing one was promoted.
func (s *Store) SeedSuperadmin(ctx context.Context, email, name, password string) bool {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("failed to hash admin password", "error", err)
		return false
	}

	existing, _ := s.GetUserByEmail(ctx, email)
	if existing != nil {
		if existing.Role == "superadmin" {
			return false // already superadmin
		}
		// Promote existing user to superadmin and reset password
		_, err = s.db.ExecContext(ctx,
			`UPDATE users SET role = 'superadmin', password_hash = $1 WHERE email = $2`,
			string(hash), email,
		)
		if err != nil {
			slog.Error("failed to promote user to superadmin", "error", err)
			return false
		}
		slog.Info("promoted existing user to superadmin", "email", email)
		return true
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (email, name, role, password_hash)
		 VALUES ($1, $2, 'superadmin', $3)
		 ON CONFLICT (email) DO NOTHING`,
		email, name, string(hash),
	)
	if err != nil {
		slog.Error("failed to seed superadmin", "error", err)
		return false
	}
	return true
}

// VerifyPassword checks the user's password hash.
func (s *Store) VerifyPassword(ctx context.Context, email, password string) (*User, error) {
	u := &User{}
	var passwordHash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, name, avatar_url, role, password_hash, created_at, updated_at
		 FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.Role, &passwordHash, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if passwordHash == "" {
		return nil, nil // no password set
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return nil, nil // wrong password
	}
	return u, nil
}

// CreateUser inserts a new user and returns it.
func (s *Store) CreateUser(ctx context.Context, email, name, avatarURL, role string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO users (email, name, avatar_url, role)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, email, name, avatar_url, role, created_at, updated_at`,
		email, name, avatarURL, role,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByEmail returns a user by email, or nil if not found.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, name, avatar_url, role, created_at, updated_at
		 FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByID returns a user by ID, or nil if not found.
func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, name, avatar_url, role, created_at, updated_at
		 FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UpsertAuthProvider creates or updates an auth provider link.
func (s *Store) UpsertAuthProvider(ctx context.Context, userID, provider, providerID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_providers (user_id, provider, provider_id)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (provider, provider_id) DO UPDATE SET user_id = $1`,
		userID, provider, providerID,
	)
	return err
}

// FindUserByProvider returns the user linked to the given provider identity.
func (s *Store) FindUserByProvider(ctx context.Context, provider, providerID string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.email, u.name, u.avatar_url, u.role, u.created_at, u.updated_at
		 FROM users u
		 JOIN auth_providers ap ON ap.user_id = u.id
		 WHERE ap.provider = $1 AND ap.provider_id = $2`,
		provider, providerID,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// StoreRefreshToken persists a hashed refresh token.
func (s *Store) StoreRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt,
	)
	return err
}

// ValidateRefreshToken checks a hashed token exists and is not expired, returning the user ID.
func (s *Store) ValidateRefreshToken(ctx context.Context, tokenHash string) (string, error) {
	var userID string
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM refresh_tokens WHERE token_hash = $1`,
		tokenHash,
	).Scan(&userID, &expiresAt)
	if err == sql.ErrNoRows {
		return "", sql.ErrNoRows
	}
	if err != nil {
		return "", err
	}
	if time.Now().After(expiresAt) {
		// Clean up expired token
		s.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1`, tokenHash)
		return "", sql.ErrNoRows
	}
	return userID, nil
}

// DeleteRefreshToken removes a specific refresh token.
func (s *Store) DeleteRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE token_hash = $1`, tokenHash,
	)
	return err
}

// DeleteUserRefreshTokens removes all refresh tokens for a user.
func (s *Store) DeleteUserRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE user_id = $1`, userID,
	)
	return err
}
