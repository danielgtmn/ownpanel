-- Runs of a site: one entry per push (later also "Build now") that should
-- build and publish it. A run is only recorded here; building it is #113,
-- which takes queued runs and reports their end (internal/panel/runs.go).
-- The panel keeps the last runs of each site and removes older ones.
--
-- status is queued, running, succeeded, failed, cancelled or skipped;
-- commit_sha is 40 hex digits; both are checked in Go (store/runs.go).
-- Everything about the commit comes from GitHub's signed delivery and is
-- text for display only: message (first line), author and pusher are
-- shortened and never interpreted.
CREATE TABLE site_runs (
    id            INTEGER PRIMARY KEY,
    site_name     TEXT    NOT NULL REFERENCES sites(name) ON DELETE CASCADE,
    trigger       TEXT    NOT NULL,
    status        TEXT    NOT NULL,
    -- The source as it was when the run was recorded.
    source_kind   TEXT    NOT NULL,
    repository_id INTEGER NOT NULL DEFAULT 0,
    repository    TEXT    NOT NULL,
    ref           TEXT    NOT NULL,
    commit_sha    TEXT    NOT NULL,
    message       TEXT    NOT NULL DEFAULT '',
    author_name   TEXT    NOT NULL DEFAULT '',
    author_login  TEXT    NOT NULL DEFAULT '',
    pusher        TEXT    NOT NULL DEFAULT '',
    -- Why a run was skipped, failed or cancelled.
    detail        TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    started_at    INTEGER NOT NULL DEFAULT 0,
    finished_at   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX site_runs_site ON site_runs (site_name, id);
CREATE INDEX site_runs_status ON site_runs (status, site_name);

-- GitHub deliveries already handled, by their X-GitHub-Delivery id, so a
-- delivery that arrives again is ignored. Kept for a bounded time; app_id
-- is the panel's id of the App that signed it.
CREATE TABLE github_deliveries (
    id          TEXT    PRIMARY KEY,
    app_id      INTEGER NOT NULL REFERENCES github_apps(id) ON DELETE CASCADE,
    received_at INTEGER NOT NULL
);

CREATE INDEX github_deliveries_received ON github_deliveries (received_at);

-- Repositories GitHub reported as no longer seen by an installation
-- (taken away from it, or deleted). id is "<installation>/<repository>".
-- Removed when GitHub shows the repository again or a site is linked to it
-- anew, and with the installation.
CREATE TABLE github_repositories_gone (
    id              TEXT    PRIMARY KEY,
    installation_id INTEGER NOT NULL,
    repository_id   INTEGER NOT NULL,
    since           INTEGER NOT NULL
);

CREATE INDEX github_repositories_gone_installation ON github_repositories_gone (installation_id);
