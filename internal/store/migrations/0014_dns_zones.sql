-- The domains (zones) a person chose to manage in the panel, through one of
-- their DNS connections. Connecting an account adopts nothing; a zone that a
-- site, redirect, mail domain or the panel itself uses counts as managed
-- without a row here. Rows go away with their connection; nothing at the
-- provider changes when a row is added or removed.
CREATE TABLE dns_zones (
    connection_id INTEGER NOT NULL REFERENCES dns_connections(id) ON DELETE CASCADE,
    zone          TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    PRIMARY KEY (connection_id, zone)
);
