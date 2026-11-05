-- One row per run. The run itself is the json document; the columns beside
-- it are copies the filters of forja.status select on.
CREATE TABLE forja_runs (
    id         text        PRIMARY KEY,
    repository text        NOT NULL,
    tool       text        NOT NULL,
    state      text        NOT NULL,
    started_at timestamptz NOT NULL,
    run        json        NOT NULL
);

CREATE INDEX forja_runs_repository_started_at ON forja_runs (repository, started_at DESC);
CREATE INDEX forja_runs_state ON forja_runs (state);
