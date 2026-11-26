-- Times are Unix seconds (UTC).

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('admin', 'member')),
    created_at    INTEGER NOT NULL
);

-- id is the hex SHA-256 of the cookie value; the value itself is never stored.
CREATE TABLE sessions (
    id         TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE INDEX sessions_expires ON sessions(expires_at);

-- hash is the hex SHA-256 of the token. sites is a JSON array of site names,
-- NULL meaning every site the user may use.
CREATE TABLE tokens (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    hash         TEXT    NOT NULL UNIQUE,
    access       TEXT    NOT NULL CHECK (access IN ('read', 'write')),
    sites        TEXT,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);
CREATE INDEX tokens_user ON tokens(user_id);

-- owner_id is NULL only for sites imported before the first admin existed.
-- No ON DELETE action: a user who still owns sites cannot be deleted.
CREATE TABLE sites (
    name       TEXT    PRIMARY KEY,
    owner_id   INTEGER REFERENCES users(id),
    created_at INTEGER NOT NULL
);
CREATE INDEX sites_owner ON sites(owner_id);

CREATE TABLE site_domains (
    domain    TEXT PRIMARY KEY,
    site_name TEXT NOT NULL REFERENCES sites(name) ON DELETE CASCADE
);
CREATE INDEX site_domains_site ON site_domains(site_name);

CREATE TABLE activity (
    id     INTEGER PRIMARY KEY,
    at     INTEGER NOT NULL,
    actor  TEXT    NOT NULL,
    action TEXT    NOT NULL,
    target TEXT    NOT NULL DEFAULT '',
    detail TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
