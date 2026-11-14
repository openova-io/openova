-- Migration 0001 of the operations-records service: the seven tables of schema ops.
--
-- The migrator (internal/store/migrate.go) creates schema ops and ops.schema_migrations
-- before it runs this file, and runs it in one transaction. The rules of each column are
-- written in internal/store/store.go; the checks below repeat them, so that a row that
-- breaks them cannot be written by any program. Every table has the column organization,
-- and every reference names a row of the same Organization (the two-column foreign keys).
-- The table change_links is not part of this version.

CREATE TABLE ops.problems (
    id           text        PRIMARY KEY CHECK (id ~ '^PRB-[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    organization text        NOT NULL CHECK (organization <> ''),
    title        text        NOT NULL CHECK (title <> ''),
    statement    text        NOT NULL DEFAULT '',
    state        text        NOT NULL CHECK (state IN ('open', 'known', 'closed')),
    created_at   timestamptz NOT NULL,
    UNIQUE (organization, id)
);
CREATE INDEX problems_by_created ON ops.problems (organization, created_at DESC);

CREATE TABLE ops.incidents (
    id              text        PRIMARY KEY CHECK (id ~ '^INC-[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    organization    text        NOT NULL CHECK (organization <> ''),
    title           text        NOT NULL CHECK (title <> ''),
    severity        integer     NOT NULL CHECK (severity BETWEEN 1 AND 4),
    severity_set_by text        NOT NULL CHECK (severity_set_by IN ('rules', 'agent', 'person')),
    state           text        NOT NULL CHECK (state IN ('open', 'mitigated', 'resolved')),
    opened_by       text        NOT NULL CHECK (opened_by IN ('rules', 'agent')),
    opened_at       timestamptz NOT NULL,
    resolved_at     timestamptz NULL,
    parent_id       text        NULL,
    problem_id      text        NULL,
    UNIQUE (organization, id),
    CHECK (parent_id <> id),
    CHECK ((state = 'resolved') = (resolved_at IS NOT NULL)),
    CHECK (resolved_at IS NULL OR resolved_at >= opened_at),
    FOREIGN KEY (organization, parent_id) REFERENCES ops.incidents (organization, id),
    FOREIGN KEY (organization, problem_id) REFERENCES ops.problems (organization, id)
);
CREATE INDEX incidents_by_opened ON ops.incidents (organization, opened_at DESC);
CREATE INDEX incidents_by_parent ON ops.incidents (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX incidents_by_problem ON ops.incidents (problem_id) WHERE problem_id IS NOT NULL;

CREATE TABLE ops.signals (
    id             text        PRIMARY KEY CHECK (id ~ '^[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    organization   text        NOT NULL CHECK (organization <> ''),
    source         text        NOT NULL CHECK (source IN ('alert', 'security', 'access')),
    kind           text        NOT NULL CHECK (kind <> ''),
    entity_kind    text        NOT NULL CHECK (entity_kind <> ''),
    entity_ref     text        NOT NULL CHECK (entity_ref <> ''),
    cluster        text        NOT NULL DEFAULT '',
    severity_floor integer     NOT NULL CHECK (severity_floor BETWEEN 1 AND 4),
    title          text        NOT NULL CHECK (title <> ''),
    body           jsonb       NOT NULL DEFAULT '{}',
    fingerprint    text        NOT NULL CHECK (fingerprint <> ''),
    state          text        NOT NULL CHECK (state IN ('open', 'cleared')),
    first_seen     timestamptz NOT NULL,
    last_seen      timestamptz NOT NULL CHECK (last_seen >= first_seen),
    incident_id    text        NULL,
    FOREIGN KEY (organization, incident_id) REFERENCES ops.incidents (organization, id)
);
CREATE INDEX signals_by_first_seen ON ops.signals (organization, first_seen);
CREATE INDEX signals_by_fingerprint ON ops.signals (organization, fingerprint);
CREATE INDEX signals_by_incident ON ops.signals (incident_id) WHERE incident_id IS NOT NULL;

CREATE TABLE ops.incident_events (
    id           bigserial   PRIMARY KEY,
    organization text        NOT NULL CHECK (organization <> ''),
    incident_id  text        NOT NULL,
    at           timestamptz NOT NULL,
    actor_kind   text        NOT NULL CHECK (actor_kind IN ('rules', 'agent', 'person', 'system')),
    actor        text        NOT NULL CHECK (actor <> ''),
    kind         text        NOT NULL CHECK (kind <> ''),
    data         jsonb       NOT NULL DEFAULT '{}',
    FOREIGN KEY (organization, incident_id) REFERENCES ops.incidents (organization, id)
);
CREATE INDEX incident_events_by_incident ON ops.incident_events (incident_id, id);

CREATE TABLE ops.judgements (
    id           text        PRIMARY KEY CHECK (id ~ '^[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    organization text        NOT NULL CHECK (organization <> ''),
    judge        text        NOT NULL CHECK (judge <> ''),
    subject_kind text        NOT NULL CHECK (subject_kind <> ''),
    subject_id   text        NOT NULL CHECK (subject_id <> ''),
    offered      jsonb       NOT NULL DEFAULT '{}',
    rule_result  jsonb       NOT NULL DEFAULT '{}',
    answer       jsonb       NULL,
    outcome      text        NOT NULL DEFAULT '' CHECK (outcome IN ('', 'applied', 'rejected_invalid',
                                 'late_applied', 'late_rejected', 'timeout', 'unavailable')),
    asked_at     timestamptz NOT NULL,
    deadline_at  timestamptz NOT NULL CHECK (deadline_at >= asked_at),
    answered_at  timestamptz NULL CHECK (answered_at >= asked_at),
    CHECK ((answer IS NULL) = (answered_at IS NULL))
);
CREATE INDEX judgements_by_asked ON ops.judgements (organization, asked_at DESC);
CREATE INDEX judgements_undecided ON ops.judgements (deadline_at) WHERE outcome = '';

CREATE TABLE ops.feedback (
    id           bigserial   PRIMARY KEY,
    organization text        NOT NULL CHECK (organization <> ''),
    incident_id  text        NOT NULL,
    person       text        NOT NULL CHECK (person <> ''),
    kind         text        NOT NULL CHECK (kind IN ('merge', 'detach', 'severity', 'not_related')),
    data         jsonb       NOT NULL DEFAULT '{}',
    at           timestamptz NOT NULL,
    FOREIGN KEY (organization, incident_id) REFERENCES ops.incidents (organization, id)
);
CREATE INDEX feedback_by_incident ON ops.feedback (incident_id, id);

-- The outbox of the event contract: a row is written in the same transaction as the business
-- row it reports. seq is the order of enqueueing, which PendingOutbox follows.
CREATE TABLE ops.outbox (
    id           text        PRIMARY KEY CHECK (id ~ '^[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    seq          bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    organization text        NOT NULL CHECK (organization <> ''),
    subject      text        NOT NULL CHECK (subject ~ '^catalyst\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.v[0-9]+$'),
    payload      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz NULL
);
CREATE INDEX outbox_pending ON ops.outbox (seq) WHERE published_at IS NULL;
