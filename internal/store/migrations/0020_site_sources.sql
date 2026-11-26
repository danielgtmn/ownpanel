-- Where a site's code comes from: at most one source per site, removed
-- with the site.
--
-- kind 'github': a repository of a GitHub App installation. The repository
-- is identified by GitHub's numeric id, which survives renames;
-- repository holds owner/name for display and is refreshed whenever the
-- panel sees the repository. installation_id becomes NULL when the
-- installation or its whole connection is removed: the source stays, so
-- the site shows "connection missing" with the repository it was linked
-- to, until someone links it again or unlinks it.
--
-- kind 'git': any Git host, fetched with a deploy key (#111). repository
-- holds the clone address; deploy_key and notify_secret are sealed with
-- the panel's secret.key (purposes in internal/panel) and stay NULL until
-- #111 fills them in.
CREATE TABLE site_sources (
    site_name       TEXT    PRIMARY KEY REFERENCES sites(name) ON DELETE CASCADE,
    kind            TEXT    NOT NULL CHECK (kind IN ('github', 'git')),
    installation_id INTEGER REFERENCES github_installations(id) ON DELETE SET NULL,
    repository_id   INTEGER,
    repository      TEXT    NOT NULL,
    branch          TEXT    NOT NULL,
    publish_on_push INTEGER NOT NULL DEFAULT 1,
    deploy_key      BLOB,
    notify_secret   BLOB,
    created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    CHECK (kind <> 'github' OR (repository_id IS NOT NULL AND deploy_key IS NULL AND notify_secret IS NULL)),
    CHECK (kind <> 'git' OR (installation_id IS NULL AND repository_id IS NULL))
);

-- Finding the sites of a repository when GitHub reports a push (#109).
CREATE INDEX site_sources_github ON site_sources (repository_id) WHERE kind = 'github';
CREATE INDEX site_sources_installation ON site_sources (installation_id);
