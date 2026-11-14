-- Migration 0001 of the open-banking product: the consents and their history.
--
-- consent holds one row per consent; consent_history one row per accepted transition, written
-- in the same transaction as the state. The column request is of type json, not jsonb: it keeps
-- the body byte for byte as it was received, which jsonb would rewrite. The other tables of the
-- product come with the migrations of the packages that read them.

CREATE TABLE consent (
    id          text PRIMARY KEY,
    family      text NOT NULL CHECK (family IN ('account-access', 'payment', 'funds-confirmation', 'vrp')),
    client_id   text NOT NULL,
    state       text NOT NULL CHECK (state IN ('awaiting', 'authorised', 'rejected', 'revoked', 'expired', 'consumed')),
    request     json,
    customer_id text,
    accounts    text[],
    expires_at  timestamptz,
    created_at  timestamptz NOT NULL,
    state_at    timestamptz NOT NULL
);

CREATE TABLE consent_history (
    consent_id text NOT NULL REFERENCES consent (id),
    seq        integer NOT NULL CHECK (seq >= 1),
    from_state text NOT NULL,
    to_state   text NOT NULL,
    actor      text NOT NULL CHECK (actor IN ('third-party', 'customer', 'bank', 'system')),
    reason     text NOT NULL,
    at         timestamptz NOT NULL,
    PRIMARY KEY (consent_id, seq)
);
