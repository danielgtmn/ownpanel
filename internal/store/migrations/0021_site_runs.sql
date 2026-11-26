-- Runs of a site: one entry per push (or, later, "Build now") that should
-- build and publish it. A run is only recorded here; building it is #113,
-- which takes queued runs (ClaimNextRun) and reports their end. The panel
-- keeps the last runs of each site and removes older ones.
--
-- Everything about the commit comes from GitHub's signed delivery and is
-- text for display only: message (first line), author and pusher are
-- shortened and never interpreted.
CREATE TABLE site_runs (
    id             INTEGER PRIMARY KEY,
    site_name      TEXT    NOT NULL REFERENCES sites(name) ON DELETE CASCADE,
    trigger        TEXT    NOT NULL,           -- 'push' (more with #115, #116)
    status         TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    -- The source as it was when the run was recorded.
    source_kind    TEXT    NOT NULL,
    repository_id  INTEGER,
    repository     TEXT    NOT NULL,
    ref            TEXT    NOT NULL,           -- e.g. refs/heads/main
    commit_sha     TEXT    NOT NULL CHECK (length(commit_sha) = 40),
    message        TEXT    NOT NULL DEFAULT '',
    author_name    TEXT    NOT NULL DEFAULT '',
    author_login   TEXT    NOT NULL DEFAULT '',
    pusher         TEXT    NOT NULL DEFAULT '',
    -- detail says why a run was skipped, failed or cancelled.
    detail         TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    started_at     INTEGER,
    finished_at    INTEGER
);

CREATE INDEX site_runs_site ON site_runs (site_name, id);
CREATE INDEX site_runs_queued ON site_runs (site_name, id) WHERE status = 'queued';

-- GitHub deliveries already handled, by X-GitHub-Delivery, so a delivery
-- that arrives again is ignored. Kept for a bounded time (panel decides);
-- app_id is the panel's id of the App that signed it.
CREATE TABLE github_deliveries (
    id          TEXT    PRIMARY KEY,
    app_id      INTEGER NOT NULL REFERENCES github_apps(id) ON DELETE CASCADE,
    received_at INTEGER NOT NULL
);

CREATE INDEX github_deliveries_received ON github_deliveries (received_at);

-- GitHub told the panel that the installation no longer sees the
-- repository (removed from the installation, or deleted). Cleared when
-- the site is linked again or GitHub shows the repository again.
ALTER TABLE site_sources ADD COLUMN repository_gone INTEGER NOT NULL DEFAULT 0;
