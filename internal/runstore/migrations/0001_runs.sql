-- One row per run. The run itself is the json document; the columns beside
-- it are copies the filters of forja.status select on.
CREATE TABLE runs (
    id         text        PRIMARY KEY,
    repository text        NOT NULL,
    tool       text        NOT NULL,
    state      text        NOT NULL,
    started_at timestamptz NOT NULL,
    run        json        NOT NULL
);

CREATE INDEX runs_repository_started_at ON runs (repository, started_at DESC);
CREATE INDEX runs_state ON runs (state);
