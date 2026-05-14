-- openova-flow-server initial schema. CNPG-backed durable graph store.
-- Replaces the in-memory map+RingBuffer that lost all state on pod restart.

CREATE TABLE IF NOT EXISTS flow_instances (
    flow_id          TEXT PRIMARY KEY,
    definition_id    TEXT,
    parent_flow_id   TEXT,
    triggered_by     JSONB NOT NULL DEFAULT '[]'::jsonb,
    status           TEXT NOT NULL DEFAULT 'open',
    started_at       BIGINT,
    ended_at         BIGINT,
    meta             JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS flow_nodes (
    flow_id      TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    label        TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending',
    family       TEXT,
    region       TEXT,
    started_at   BIGINT,
    ended_at     BIGINT,
    meta         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (flow_id, node_id),
    FOREIGN KEY (flow_id) REFERENCES flow_instances(flow_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS flow_nodes_flow_status_idx ON flow_nodes (flow_id, status);
CREATE INDEX IF NOT EXISTS flow_nodes_flow_region_idx ON flow_nodes (flow_id, region);
CREATE INDEX IF NOT EXISTS flow_nodes_flow_family_idx ON flow_nodes (flow_id, family);

CREATE TABLE IF NOT EXISTS flow_relationships (
    flow_id        TEXT NOT NULL,
    from_id        TEXT NOT NULL,
    to_id          TEXT NOT NULL,
    rel_type       TEXT NOT NULL,
    from_flow_id   TEXT,
    to_flow_id     TEXT,
    condition      TEXT NOT NULL DEFAULT 'always',
    lag_seconds    BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (flow_id, from_id, to_id, rel_type),
    FOREIGN KEY (flow_id) REFERENCES flow_instances(flow_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS flow_relationships_to_idx ON flow_relationships (flow_id, to_id);
CREATE INDEX IF NOT EXISTS flow_relationships_from_idx ON flow_relationships (flow_id, from_id);

-- Append-only event log used for SSE replay. Each row's seq is the
-- monotonic identifier the SSE handler emits as `id: <seq>`.
-- Bounded ring per flow via the trigger below (keep last 4096 events
-- per flow_id — matches the previous in-memory RingBuffer capacity).
CREATE TABLE IF NOT EXISTS flow_events (
    seq          BIGSERIAL PRIMARY KEY,
    flow_id      TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (flow_id) REFERENCES flow_instances(flow_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS flow_events_flow_seq_idx ON flow_events (flow_id, seq DESC);

-- Bounded retention per flow — fires AFTER INSERT to keep the last
-- N events per flow_id. N=4096 matches the prior in-memory RingBuffer.
CREATE OR REPLACE FUNCTION flow_events_retention() RETURNS TRIGGER AS $$
BEGIN
    DELETE FROM flow_events
    WHERE flow_id = NEW.flow_id
      AND seq < (
          SELECT seq FROM flow_events
          WHERE flow_id = NEW.flow_id
          ORDER BY seq DESC
          OFFSET 4096 LIMIT 1
      );
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER flow_events_retention_trg
AFTER INSERT ON flow_events
FOR EACH ROW EXECUTE FUNCTION flow_events_retention();

-- Per-execution log lines. Replaces the legacy NDJSON files on the
-- catalyst-api PVC. node_id maps to FlowNode.id (the job within the
-- flow), exec_id stitches multiple retries of the same node together.
CREATE TABLE IF NOT EXISTS flow_log_lines (
    seq          BIGSERIAL PRIMARY KEY,
    flow_id      TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    exec_id      TEXT NOT NULL,
    level        TEXT NOT NULL DEFAULT 'info',
    message      TEXT NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (flow_id) REFERENCES flow_instances(flow_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS flow_log_lines_node_seq_idx ON flow_log_lines (flow_id, node_id, exec_id, seq);
CREATE INDEX IF NOT EXISTS flow_log_lines_exec_idx ON flow_log_lines (exec_id);

-- Executions: one per non-pending Job lifecycle. Carries Status +
-- timestamps + LatestExecutionID pointers consumed by the JobDetail UI.
CREATE TABLE IF NOT EXISTS flow_executions (
    exec_id       TEXT PRIMARY KEY,
    flow_id       TEXT NOT NULL,
    node_id       TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'running',
    started_at    BIGINT NOT NULL,
    finished_at   BIGINT,
    duration_ms   BIGINT NOT NULL DEFAULT 0,
    FOREIGN KEY (flow_id) REFERENCES flow_instances(flow_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS flow_executions_node_idx ON flow_executions (flow_id, node_id);

-- updated_at auto-bump triggers
CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER flow_instances_touch BEFORE UPDATE ON flow_instances
FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TRIGGER flow_nodes_touch BEFORE UPDATE ON flow_nodes
FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
