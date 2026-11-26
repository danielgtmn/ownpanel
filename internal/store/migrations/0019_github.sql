-- GitHub connections: GitHub Apps the panel created from a manifest, and
-- where they are installed. A private App can only be installed on the
-- account that owns it, so connecting several GitHub accounts or
-- organisations means one App each; nothing here limits their number.
--
-- private_key, webhook_secret and client_secret are sealed with the
-- panel's secret.key (internal/panel/github.go names the purposes); they
-- are never stored in the clear and never leave the panel.
CREATE TABLE github_apps (
    id              INTEGER PRIMARY KEY,
    app_id          INTEGER NOT NULL UNIQUE, -- GitHub's id of the App
    slug            TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    client_id       TEXT    NOT NULL,
    owner_login     TEXT    NOT NULL,
    owner_type      TEXT    NOT NULL CHECK (owner_type IN ('User', 'Organization')),
    html_url        TEXT    NOT NULL,
    private_key     BLOB    NOT NULL,
    webhook_secret  BLOB    NOT NULL,
    client_secret   BLOB    NOT NULL,
    created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at      INTEGER NOT NULL,
    last_checked_at INTEGER,
    last_error      TEXT    NOT NULL DEFAULT ''
);

-- Installations as GitHub last reported them (GitHub stays the source of
-- truth; a refresh replaces the rows of an App). id is GitHub's
-- installation id. repository_count is NULL until the repositories were
-- listed once.
CREATE TABLE github_installations (
    id                   INTEGER PRIMARY KEY,
    app_id               INTEGER NOT NULL REFERENCES github_apps(id) ON DELETE CASCADE,
    account_login        TEXT    NOT NULL,
    account_type         TEXT    NOT NULL CHECK (account_type IN ('User', 'Organization')),
    repository_selection TEXT    NOT NULL CHECK (repository_selection IN ('all', 'selected')),
    suspended            INTEGER NOT NULL DEFAULT 0,
    repository_count     INTEGER,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

CREATE INDEX github_installations_app ON github_installations (app_id);
