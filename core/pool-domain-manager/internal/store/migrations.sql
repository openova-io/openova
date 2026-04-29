-- pool-domain-manager schema, applied idempotently at process start.
-- Per docs/INVIOLABLE-PRINCIPLES.md #3 the database is CloudNativePG and
-- the schema lives in this single file (no Atlas / Goose / Flyway needed
-- for two tables); running the same SQL repeatedly is safe.
--
-- The CHECK constraint on state and the PRIMARY KEY on (pool_domain,
-- subdomain) together guarantee that no two callers can hold a name in
-- conflicting states. The expires_at index speeds up the sweeper.

CREATE TABLE IF NOT EXISTS pool_allocations (
    pool_domain       TEXT        NOT NULL,
    subdomain         TEXT        NOT NULL,
    state             TEXT        NOT NULL CHECK (state IN ('reserved', 'active')),
    reserved_at       TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ,
    sovereign_fqdn    TEXT,
    load_balancer_ip  TEXT,
    reservation_token UUID,
    created_by        TEXT        NOT NULL,
    PRIMARY KEY (pool_domain, subdomain)
);

-- Sweeper-friendly partial index — only the (small) set of reserved rows
-- need to be scanned for TTL expiry.
CREATE INDEX IF NOT EXISTS pool_allocations_expires_idx
    ON pool_allocations (expires_at)
    WHERE state = 'reserved';

-- Operator-facing index — list all active rows for a pool fast.
CREATE INDEX IF NOT EXISTS pool_allocations_state_idx
    ON pool_allocations (pool_domain, state);
