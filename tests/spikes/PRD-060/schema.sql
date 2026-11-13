-- Lab test PRD-060: the store's tables, the two roles' grants and the
-- row-level security exactly as the design fixes them, with <D> = 1024.
-- The two indexes of the design (chunk_embedding, chunk_source) are created
-- by build.sql AFTER the load, only to keep the CI job short: a pgvector HNSW
-- build is the same incremental insertion as inserts into an existing index.
-- The roles cortex_query and cortex_index exist already (spike.py schema).

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE source (
  id text PRIMARY KEY, title text NOT NULL,
  definition_sha256 text NOT NULL,
  state text NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','syncing','error')),
  last_sync_at timestamptz, last_error text);

CREATE TABLE source_acl (
  source_id text NOT NULL REFERENCES source ON DELETE CASCADE,
  principal text NOT NULL, role text NOT NULL CHECK (role IN ('reader','manager')),
  PRIMARY KEY (source_id, principal, role));

CREATE TABLE document (
  id uuid PRIMARY KEY, source_id text NOT NULL REFERENCES source ON DELETE CASCADE,
  external_id text NOT NULL, title text NOT NULL, uri text NOT NULL,
  content_sha256 text NOT NULL, object_key text NOT NULL,
  state text NOT NULL CHECK (state IN ('fetched','indexed','failed')),
  acl_inherit boolean NOT NULL DEFAULT true, updated_at timestamptz NOT NULL,
  UNIQUE (source_id, external_id));

CREATE TABLE document_acl (
  document_id uuid NOT NULL REFERENCES document ON DELETE CASCADE,
  principal text NOT NULL, PRIMARY KEY (document_id, principal));

CREATE TABLE chunk (
  id bigserial PRIMARY KEY,
  document_id uuid NOT NULL REFERENCES document ON DELETE CASCADE,
  source_id text NOT NULL, ordinal int NOT NULL, page int, text text NOT NULL,
  embedding vector(1024) NOT NULL);

CREATE TABLE job (
  id uuid PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN ('sync','index','delete')),
  source_id text, document_id uuid,
  state text NOT NULL CHECK (state IN ('queued','running','done','failed','dead')),
  attempts int NOT NULL DEFAULT 0, not_before timestamptz NOT NULL DEFAULT now(),
  error text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());

-- Grants: cortex_query SELECT on everything and INSERT on job only;
-- cortex_index writes everything else.
GRANT USAGE ON SCHEMA public TO cortex_query, cortex_index;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO cortex_query;
GRANT INSERT ON job TO cortex_query;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO cortex_index;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO cortex_index;

ALTER TABLE chunk ENABLE ROW LEVEL SECURITY;
ALTER TABLE chunk FORCE ROW LEVEL SECURITY;
CREATE POLICY chunk_read ON chunk FOR SELECT TO cortex_query USING (
  EXISTS (SELECT 1 FROM document d WHERE d.id = chunk.document_id AND (
    EXISTS (SELECT 1 FROM source_acl s
        WHERE s.source_id = d.source_id
          AND (d.acl_inherit OR s.role = 'manager')
          AND s.principal = ANY (current_setting('cortex.principals')::text[]))
    OR EXISTS (SELECT 1 FROM document_acl a
        WHERE a.document_id = d.id
          AND a.principal = ANY (current_setting('cortex.principals')::text[])))));
CREATE POLICY chunk_write ON chunk FOR ALL TO cortex_index USING (true) WITH CHECK (true);

-- The pair on document. The read rule tests the document row's own columns.
-- (A policy on document that selects FROM document is the literal reading of
-- "with document.id in place of chunk.document_id"; spike.py schema shows
-- what PostgreSQL does with it, in a separate database.)
ALTER TABLE document ENABLE ROW LEVEL SECURITY;
ALTER TABLE document FORCE ROW LEVEL SECURITY;
CREATE POLICY document_read ON document FOR SELECT TO cortex_query USING (
  EXISTS (SELECT 1 FROM source_acl s
      WHERE s.source_id = document.source_id
        AND (document.acl_inherit OR s.role = 'manager')
        AND s.principal = ANY (current_setting('cortex.principals')::text[]))
  OR EXISTS (SELECT 1 FROM document_acl a
      WHERE a.document_id = document.id
        AND a.principal = ANY (current_setting('cortex.principals')::text[])));
CREATE POLICY document_write ON document FOR ALL TO cortex_index USING (true) WITH CHECK (true);
