-- A redirect answers requests for source_domain (and, for a path redirect,
-- source_path) with a redirect to target.
--
-- Domain redirects have source_path '' and site_name NULL: the whole domain
-- redirects. Path redirects have source_path set ("/old" exactly, or a prefix
-- "/blog/*") and belong to the site that has source_domain; they are removed
-- with the site (and when the site gives up that domain).
--
-- owner_id is the site's owner for path redirects (NULL while the site has
-- none) and the creator for domain redirects. No ON DELETE action: a user who
-- still owns domain redirects cannot be deleted.
CREATE TABLE redirects (
    id            INTEGER PRIMARY KEY,
    owner_id      INTEGER REFERENCES users(id),
    site_name     TEXT    REFERENCES sites(name) ON DELETE CASCADE,
    source_domain TEXT    NOT NULL,
    source_path   TEXT    NOT NULL DEFAULT '',
    target        TEXT    NOT NULL,
    code          INTEGER NOT NULL CHECK (code IN (301, 302, 307, 308)),
    keep_path     INTEGER NOT NULL DEFAULT 0 CHECK (keep_path IN (0, 1)),
    keep_query    INTEGER NOT NULL DEFAULT 0 CHECK (keep_query IN (0, 1)),
    created_at    INTEGER NOT NULL,
    UNIQUE (source_domain, source_path),
    CHECK ((source_path = '') = (site_name IS NULL))
);
CREATE INDEX redirects_owner ON redirects(owner_id);
CREATE INDEX redirects_site ON redirects(site_name);
