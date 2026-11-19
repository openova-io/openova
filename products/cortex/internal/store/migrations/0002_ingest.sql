-- Migration 0002 of cortex: what the ingestion of uploads needs.
--
-- source gains the connector type and the chunk settings of its definition; the definitions
-- loader writes them. A source that migration 0001 left has no connector until the loader
-- applies the definitions again, and takes no upload before.
--
-- job gains item, what the query process staged for the index process (an upload's title,
-- media type, content hash, object and readers). Every insert of a job and every change of its
-- state sends NOTIFY cortex_job with the job's id: the index process wakes on it to claim, and
-- the query process to stream the change to its callers. queued and failed jobs are claimed in
-- the order they were created.

ALTER TABLE source
  ADD COLUMN connector text CHECK (connector IN ('upload','git','web')),
  ADD COLUMN chunk_tokens int NOT NULL DEFAULT 512 CHECK (chunk_tokens BETWEEN 128 AND 2048),
  ADD COLUMN chunk_overlap int NOT NULL DEFAULT 64 CHECK (chunk_overlap BETWEEN 0 AND 256 AND chunk_overlap < chunk_tokens);

ALTER TABLE job ADD COLUMN item jsonb;
CREATE INDEX job_due ON job (created_at) WHERE state IN ('queued','failed','running');

CREATE FUNCTION job_notify() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('cortex_job', NEW.id::text);
  RETURN NULL;
END
$$;
CREATE TRIGGER job_notify AFTER INSERT OR UPDATE OF state ON job
  FOR EACH ROW EXECUTE FUNCTION job_notify();
