-- Reverts migration 0001: drops the seven tables of schema ops and every row in them.
-- Schema ops and ops.schema_migrations stay; the migrator owns them.

DROP TABLE ops.outbox;
DROP TABLE ops.feedback;
DROP TABLE ops.judgements;
DROP TABLE ops.incident_events;
DROP TABLE ops.signals;
DROP TABLE ops.incidents;
DROP TABLE ops.problems;
