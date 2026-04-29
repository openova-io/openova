// Package store — CloudNativePG / Postgres persistence for pool-domain-manager.
//
// The PDM owns a single table — pool_allocations — that holds the canonical
// allocation state for every (pool_domain, subdomain) pair the OpenOva fleet
// has ever reserved or activated. The table is intentionally simple: PDM is
// a small, stateless HTTP service backed by a single-writer Postgres database
// (CloudNativePG running in the openova-system namespace). Concurrency is
// resolved by Postgres row-level locks + UPSERT semantics rather than any
// application-level mutex.
//
// Schema (also encoded as a migration in migrations.sql):
//
//	CREATE TABLE pool_allocations (
//	    pool_domain       TEXT        NOT NULL,
//	    subdomain         TEXT        NOT NULL,
//	    state             TEXT        NOT NULL CHECK (state IN ('reserved','active')),
//	    reserved_at       TIMESTAMPTZ NOT NULL,
//	    expires_at        TIMESTAMPTZ,                       -- NULL when state='active'
//	    sovereign_fqdn    TEXT,                              -- set when state='active'
//	    load_balancer_ip  TEXT,                              -- set when state='active'
//	    reservation_token UUID,                              -- set when state='reserved'
//	    created_by        TEXT        NOT NULL,
//	    PRIMARY KEY (pool_domain, subdomain)
//	);
//	CREATE INDEX pool_allocations_expires_idx ON pool_allocations (expires_at)
//	    WHERE state = 'reserved';
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the connection string is read from the
// PDM_DATABASE_URL env var — never hardcoded. The K8s ExternalSecret pulls
// the credentials out of CNPG's auto-generated app secret and projects them
// here.
package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// State enumerates the three lifecycle states of a (pool, subdomain) row.
// NULL — implicit (no row) — is the fourth state, represented by absence.
type State string

const (
	// StateReserved — the name is held with a TTL. expires_at is non-NULL.
	// On TTL expiry the row is deleted by the sweeper goroutine.
	StateReserved State = "reserved"
	// StateActive — the name has been committed and Dynadot DNS records have
	// been written. expires_at is NULL; the row stays until Release.
	StateActive State = "active"
)

// ErrConflict — somebody else holds this (pool_domain, subdomain) — used by
// the allocator to map to HTTP 409 Conflict.
var ErrConflict = errors.New("pool allocation conflict — name already reserved or active")

// ErrNotFound — no row exists for the (pool_domain, subdomain) pair. The
// handlers map this to HTTP 404.
var ErrNotFound = errors.New("pool allocation not found")

// Allocation is the persistent shape of a row in pool_allocations.
type Allocation struct {
	PoolDomain       string     `json:"poolDomain"`
	Subdomain        string     `json:"subdomain"`
	State            State      `json:"state"`
	ReservedAt       time.Time  `json:"reservedAt"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	SovereignFQDN    string     `json:"sovereignFQDN,omitempty"`
	LoadBalancerIP   string     `json:"loadBalancerIP,omitempty"`
	ReservationToken string     `json:"reservationToken,omitempty"`
	CreatedBy        string     `json:"createdBy"`
}

// Store wraps the pgxpool.Pool with the SQL operations PDM needs.
type Store struct {
	pool *pgxpool.Pool
}

// New connects to Postgres using the DSN, applies migrations, and returns a
// ready-to-use Store. Caller is responsible for calling Close on shutdown.
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PDM_DATABASE_URL: %w", err)
	}
	// Modest pool — PDM handles low QPS (a wizard click rate of order 0.1/s
	// fleet-wide) and we'd rather queue than monopolise CNPG connections.
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return s, nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping verifies the database is reachable. Used by /healthz.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

//go:embed migrations.sql
var migrationsSQL string

// migrate applies the embedded migrations.sql idempotently. We deliberately
// avoid a full migration framework — the schema is tiny and idempotent SQL
// keeps PDM's startup path one TCP connect + one CREATE TABLE IF NOT EXISTS.
func (s *Store) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, migrationsSQL)
	return err
}

// Reserve atomically inserts a row in state='reserved' with the given TTL.
// Returns ErrConflict if a row already exists for the (pool, subdomain) pair
// in any state — including an expired reservation that the sweeper has not
// yet collected (we treat sweeper lag conservatively and rely on the caller
// running ExpireReservations periodically; the SQL also prunes expired rows
// for the specific key it touches as part of the same transaction).
func (s *Store) Reserve(ctx context.Context, poolDomain, subdomain string, ttl time.Duration, createdBy string) (*Allocation, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("Reserve: ttl must be positive, got %s", ttl)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Step 1: opportunistic prune — if a row exists for this exact key with
	// state='reserved' and expires_at in the past, delete it. This keeps the
	// reservation path responsive even if the background sweeper is slow.
	if _, err := tx.Exec(ctx, `
		DELETE FROM pool_allocations
		WHERE pool_domain = $1
		  AND subdomain   = $2
		  AND state       = 'reserved'
		  AND expires_at  < NOW()
	`, poolDomain, subdomain); err != nil {
		return nil, fmt.Errorf("prune expired: %w", err)
	}

	now := time.Now().UTC()
	expires := now.Add(ttl)
	token := uuid.New()

	// Step 2: insert. If the row already exists (active OR not-yet-expired
	// reservation) the unique constraint fires and we return ErrConflict.
	_, err = tx.Exec(ctx, `
		INSERT INTO pool_allocations
		    (pool_domain, subdomain, state, reserved_at, expires_at, reservation_token, created_by)
		VALUES
		    ($1, $2, 'reserved', $3, $4, $5, $6)
	`, poolDomain, subdomain, now, expires, token, createdBy)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" /* unique_violation */ {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("insert reservation: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reservation: %w", err)
	}

	exp := expires
	return &Allocation{
		PoolDomain:       poolDomain,
		Subdomain:        subdomain,
		State:            StateReserved,
		ReservedAt:       now,
		ExpiresAt:        &exp,
		ReservationToken: token.String(),
		CreatedBy:        createdBy,
	}, nil
}

// Get returns the current allocation for the (pool, subdomain) pair, or
// ErrNotFound. Get does NOT prune expired reservations on read — callers
// who want fresh results should run after ExpireReservations or accept that
// /check may briefly return state='reserved' for a row that has just expired.
func (s *Store) Get(ctx context.Context, poolDomain, subdomain string) (*Allocation, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT pool_domain, subdomain, state, reserved_at, expires_at,
		       sovereign_fqdn, load_balancer_ip, reservation_token, created_by
		FROM pool_allocations
		WHERE pool_domain = $1 AND subdomain = $2
	`, poolDomain, subdomain)

	var a Allocation
	var (
		expiresAt        *time.Time
		sovereignFQDN    *string
		loadBalancerIP   *string
		reservationToken *uuid.UUID
	)
	err := row.Scan(
		&a.PoolDomain,
		&a.Subdomain,
		&a.State,
		&a.ReservedAt,
		&expiresAt,
		&sovereignFQDN,
		&loadBalancerIP,
		&reservationToken,
		&a.CreatedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan allocation: %w", err)
	}
	a.ExpiresAt = expiresAt
	if sovereignFQDN != nil {
		a.SovereignFQDN = *sovereignFQDN
	}
	if loadBalancerIP != nil {
		a.LoadBalancerIP = *loadBalancerIP
	}
	if reservationToken != nil {
		a.ReservationToken = reservationToken.String()
	}
	return &a, nil
}

// IsAvailable reports whether the (pool, subdomain) pair is free for a fresh
// reservation. A row in state='reserved' that has expired is treated as free
// (we transparently prune it on next Reserve). All other rows = taken.
func (s *Store) IsAvailable(ctx context.Context, poolDomain, subdomain string) (bool, error) {
	a, err := s.Get(ctx, poolDomain, subdomain)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if a.State == StateReserved && a.ExpiresAt != nil && a.ExpiresAt.Before(time.Now().UTC()) {
		return true, nil
	}
	return false, nil
}

// CommitInput is what the /commit endpoint provides.
type CommitInput struct {
	ReservationToken string
	SovereignFQDN    string
	LoadBalancerIP   string
}

// Commit promotes an existing reservation to state='active'. Verifies the
// reservation_token matches (so a stale wizard tab can't commit somebody
// else's reservation) and that the reservation has not yet expired.
//
// Returns ErrNotFound if the row is gone, ErrConflict if it is already
// active, ErrTokenMismatch if the token doesn't match, and ErrExpired if the
// reservation TTL elapsed.
func (s *Store) Commit(ctx context.Context, poolDomain, subdomain string, in CommitInput) (*Allocation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the row for the duration of this transaction.
	row := tx.QueryRow(ctx, `
		SELECT state, expires_at, reservation_token
		FROM pool_allocations
		WHERE pool_domain = $1 AND subdomain = $2
		FOR UPDATE
	`, poolDomain, subdomain)
	var (
		state            State
		expiresAt        *time.Time
		reservationToken *uuid.UUID
	)
	if err := row.Scan(&state, &expiresAt, &reservationToken); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lock row: %w", err)
	}

	if state == StateActive {
		return nil, ErrConflict
	}
	if expiresAt != nil && expiresAt.Before(time.Now().UTC()) {
		return nil, ErrExpired
	}
	wantToken, err := uuid.Parse(in.ReservationToken)
	if err != nil {
		return nil, ErrTokenMismatch
	}
	if reservationToken == nil || *reservationToken != wantToken {
		return nil, ErrTokenMismatch
	}

	if _, err := tx.Exec(ctx, `
		UPDATE pool_allocations
		SET state             = 'active',
		    expires_at        = NULL,
		    reservation_token = NULL,
		    sovereign_fqdn    = $3,
		    load_balancer_ip  = $4
		WHERE pool_domain = $1 AND subdomain = $2
	`, poolDomain, subdomain, in.SovereignFQDN, in.LoadBalancerIP); err != nil {
		return nil, fmt.Errorf("commit update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return s.Get(ctx, poolDomain, subdomain)
}

// Release deletes the row for (pool, subdomain) regardless of state. Returns
// the released row's previous state so the handler can decide whether to
// fire Dynadot delete calls (only state='active' rows have DNS records).
func (s *Store) Release(ctx context.Context, poolDomain, subdomain string) (*Allocation, error) {
	row := s.pool.QueryRow(ctx, `
		DELETE FROM pool_allocations
		WHERE pool_domain = $1 AND subdomain = $2
		RETURNING pool_domain, subdomain, state, reserved_at, expires_at,
		          sovereign_fqdn, load_balancer_ip, reservation_token, created_by
	`, poolDomain, subdomain)

	var a Allocation
	var (
		expiresAt        *time.Time
		sovereignFQDN    *string
		loadBalancerIP   *string
		reservationToken *uuid.UUID
	)
	err := row.Scan(
		&a.PoolDomain,
		&a.Subdomain,
		&a.State,
		&a.ReservedAt,
		&expiresAt,
		&sovereignFQDN,
		&loadBalancerIP,
		&reservationToken,
		&a.CreatedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("delete allocation: %w", err)
	}
	a.ExpiresAt = expiresAt
	if sovereignFQDN != nil {
		a.SovereignFQDN = *sovereignFQDN
	}
	if loadBalancerIP != nil {
		a.LoadBalancerIP = *loadBalancerIP
	}
	if reservationToken != nil {
		a.ReservationToken = reservationToken.String()
	}
	return &a, nil
}

// List returns every allocation for the given pool domain. Used by the
// operator-facing /list endpoint.
func (s *Store) List(ctx context.Context, poolDomain string) ([]Allocation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pool_domain, subdomain, state, reserved_at, expires_at,
		       sovereign_fqdn, load_balancer_ip, reservation_token, created_by
		FROM pool_allocations
		WHERE pool_domain = $1
		ORDER BY subdomain
	`, poolDomain)
	if err != nil {
		return nil, fmt.Errorf("list allocations: %w", err)
	}
	defer rows.Close()

	var out []Allocation
	for rows.Next() {
		var a Allocation
		var (
			expiresAt        *time.Time
			sovereignFQDN    *string
			loadBalancerIP   *string
			reservationToken *uuid.UUID
		)
		if err := rows.Scan(
			&a.PoolDomain,
			&a.Subdomain,
			&a.State,
			&a.ReservedAt,
			&expiresAt,
			&sovereignFQDN,
			&loadBalancerIP,
			&reservationToken,
			&a.CreatedBy,
		); err != nil {
			return nil, fmt.Errorf("scan list row: %w", err)
		}
		a.ExpiresAt = expiresAt
		if sovereignFQDN != nil {
			a.SovereignFQDN = *sovereignFQDN
		}
		if loadBalancerIP != nil {
			a.LoadBalancerIP = *loadBalancerIP
		}
		if reservationToken != nil {
			a.ReservationToken = reservationToken.String()
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ExpireReservations deletes every state='reserved' row whose expires_at is
// in the past. Returns the count of rows deleted. Called periodically by the
// sweeper goroutine.
func (s *Store) ExpireReservations(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM pool_allocations
		WHERE state      = 'reserved'
		  AND expires_at < NOW()
	`)
	if err != nil {
		return 0, fmt.Errorf("expire reservations: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ErrTokenMismatch — the reservation_token in the request did not match
// the row's stored token. The caller likely is a stale tab.
var ErrTokenMismatch = errors.New("reservation token does not match")

// ErrExpired — the reservation TTL has elapsed; the caller must Reserve
// again before committing.
var ErrExpired = errors.New("reservation expired before commit")
