-- Migration 0001 of cortex: the tables, the two database roles, their grants and the
-- row-level security of the store. {{dimensions}} is the vector length of the embedding
-- model (models.embed.dimensions); the migrator writes it before it runs this file.
--
-- A principal may read a document when it is in source_acl for the document's source and
-- either the document inherits or that row's role is manager; or when it is in
-- document_acl for the document. chunk.readers holds exactly the principals of that rule
-- for the chunk's document, so the chunk policy tests the chunk row's own column. The
-- store rewrites readers whenever a grant changes.

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
  readers text[] NOT NULL,
  embedding vector({{dimensions}}) NOT NULL);
CREATE INDEX chunk_embedding ON chunk USING hnsw (embedding vector_cosine_ops);
CREATE INDEX chunk_source ON chunk (source_id);
CREATE INDEX chunk_readers ON chunk USING gin (readers);
CREATE INDEX chunk_document ON chunk (document_id);

CREATE TABLE job (
  id uuid PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN ('sync','index','delete')),
  source_id text, document_id uuid,
  state text NOT NULL CHECK (state IN ('queued','running','done','failed','dead')),
  attempts int NOT NULL DEFAULT 0, not_before timestamptz NOT NULL DEFAULT now(),
  error text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());

-- The two roles. They cannot log in: the processes connect as the database owner and take
-- one of them with SET ROLE. The owner may take them but does not inherit their rights.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cortex_query') THEN
    CREATE ROLE cortex_query NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cortex_index') THEN
    CREATE ROLE cortex_index NOLOGIN;
  END IF;
END
$$;
GRANT cortex_query, cortex_index TO CURRENT_USER WITH INHERIT FALSE, SET TRUE;

-- cortex_query: the query process, the only one a route reaches. It reads and queues jobs.
-- cortex_index: the index process. It writes everything else.
GRANT USAGE ON SCHEMA public TO cortex_query, cortex_index;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO cortex_query;
GRANT INSERT ON job TO cortex_query;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO cortex_index;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO cortex_index;

ALTER TABLE chunk ENABLE ROW LEVEL SECURITY;
ALTER TABLE chunk FORCE ROW LEVEL SECURITY;
CREATE POLICY chunk_read ON chunk FOR SELECT TO cortex_query USING (
  readers && current_setting('cortex.principals')::text[]);
CREATE POLICY chunk_write ON chunk FOR ALL TO cortex_index USING (true) WITH CHECK (true);

-- The document policy tests the document row's own columns: a policy on document that
-- reads document recurses.
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
