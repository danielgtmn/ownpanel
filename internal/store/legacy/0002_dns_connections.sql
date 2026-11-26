-- A DNS connection is one person's access to one DNS provider account (for
-- Hetzner: one project). secret is the provider's API token, encrypted with
-- the key in <data>/secret.key (internal/secrets); the plain token is never
-- stored. can_write is NULL while unknown, 0 once a write was refused, 1 once
-- a write succeeded. zone_count is NULL until the zones were listed once.
CREATE TABLE dns_connections (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider        TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    secret          BLOB    NOT NULL,
    created_at      INTEGER NOT NULL,
    last_checked_at INTEGER,
    last_error      TEXT    NOT NULL DEFAULT '',
    can_write       INTEGER CHECK (can_write IN (0, 1)),
    zone_count      INTEGER,
    UNIQUE (user_id, name)
);
