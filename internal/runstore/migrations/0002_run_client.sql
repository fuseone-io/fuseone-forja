-- The client a run was recorded under, the one client forja.status answers
-- it to. A row whose client was never recorded is answered to no caller: rows
-- written before this column, by stdio or by an HTTP client alike, and
-- rows a binary from before it still writes while it drains. The default
-- stays for that binary, whose insert names no client; this binary always
-- names one.
ALTER TABLE forja_runs ADD COLUMN client text NOT NULL DEFAULT '(unattributed)';

CREATE INDEX forja_runs_client_started_at ON forja_runs (client, started_at DESC);
