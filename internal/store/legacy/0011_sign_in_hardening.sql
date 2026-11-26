-- Failed sign-in attempts, so throttling survives a restart. key is the hex
-- SHA-256 of what is throttled (client address and name, or the address
-- alone); rows older than the throttling window are removed on the next
-- attempt.
CREATE TABLE login_attempts (
    id  INTEGER PRIMARY KEY,
    key TEXT    NOT NULL,
    at  INTEGER NOT NULL
);
CREATE INDEX login_attempts_key ON login_attempts(key, at);
CREATE INDEX login_attempts_at ON login_attempts(at);

-- When an access token stops working; NULL means never.
ALTER TABLE tokens ADD COLUMN expires_at INTEGER;
