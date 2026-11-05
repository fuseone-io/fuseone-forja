-- The client a run was recorded under, the one client forja.status answers
-- it to. A row written before runs carried a client was written over
-- stdio, the only transport that wrote to this store then, so it belongs
-- to the stdio client. The default serves only those rows: every later
-- write names its client.
ALTER TABLE forja_runs ADD COLUMN client text NOT NULL DEFAULT 'stdio';
ALTER TABLE forja_runs ALTER COLUMN client DROP DEFAULT;

CREATE INDEX forja_runs_client_started_at ON forja_runs (client, started_at DESC);
