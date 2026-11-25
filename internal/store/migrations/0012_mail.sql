-- E-mail (see wiki/E-Mail-Architecture.md). The panel keeps the desired
-- state here and pushes it to the mail service; stored mail lives only in
-- the mail service.
--
-- A mail domain belongs to one person (owner_id). No ON DELETE action: a
-- user who still owns mail domains cannot be deleted. A domain is active
-- (only then is it in the pushed configuration) once its MX record at the
-- authoritative nameservers pointed to the mail host; it stays active
-- afterwards. checked_at and check_error tell when and with what result
-- the MX record was last looked at while inactive.
CREATE TABLE mail_domains (
    domain       TEXT    PRIMARY KEY,
    owner_id     INTEGER NOT NULL REFERENCES users(id),
    active       INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
    activated_at INTEGER,
    checked_at   INTEGER,
    check_error  TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);
CREATE INDEX mail_domains_owner ON mail_domains(owner_id);

-- DKIM keys of a domain. private_key is a PKCS#8 PEM RSA key encrypted with
-- the key in <data>/secret.key (internal/secrets); public_key is the
-- base64 DER public key for the DNS record (not secret). At most one key of
-- a domain is active; a pending key (rotation, later) signs only once its
-- record is visible; retired keys are kept until their record is removed.
CREATE TABLE mail_dkim_keys (
    id          INTEGER PRIMARY KEY,
    domain      TEXT    NOT NULL REFERENCES mail_domains(domain) ON DELETE CASCADE,
    selector    TEXT    NOT NULL,
    private_key BLOB    NOT NULL,
    public_key  TEXT    NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN ('active', 'pending', 'retired')),
    created_at  INTEGER NOT NULL,
    UNIQUE (domain, selector)
);
CREATE UNIQUE INDEX mail_dkim_keys_one_active ON mail_dkim_keys(domain) WHERE state = 'active';

-- A mailbox people sign in to as local_part@domain. storage_id (32 hex
-- characters, random) keys its data in the mail service and is never
-- reused: data of a deleted mailbox can never show up in a new one.
-- password_hash is argon2id (internal/passwd). quota_bytes 0 means no limit.
CREATE TABLE mailboxes (
    id            INTEGER PRIMARY KEY,
    storage_id    TEXT    NOT NULL UNIQUE,
    domain        TEXT    NOT NULL REFERENCES mail_domains(domain) ON DELETE CASCADE,
    local_part    TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    quota_bytes   INTEGER NOT NULL CHECK (quota_bytes >= 0),
    send_per_hour INTEGER NOT NULL CHECK (send_per_hour >= 0),
    send_per_day  INTEGER NOT NULL CHECK (send_per_day >= 0),
    created_at    INTEGER NOT NULL,
    UNIQUE (domain, local_part)
);

-- An alias delivers mail for local_part@domain to targets (a JSON list of
-- addresses: mailboxes of the panel or external). local_part '*' is the
-- domain's catch-all. A local part is never both a mailbox and an alias of
-- the same domain (checked when either is saved).
CREATE TABLE mail_aliases (
    id         INTEGER PRIMARY KEY,
    domain     TEXT    NOT NULL REFERENCES mail_domains(domain) ON DELETE CASCADE,
    local_part TEXT    NOT NULL,
    targets    TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (domain, local_part)
);

-- Storage ids of deleted mailboxes whose data the mail service has not yet
-- confirmed deleting. Written in the same transaction that removes the
-- mailbox; the panel retries until the service answers.
CREATE TABLE mail_pending_deletes (
    storage_id TEXT    PRIMARY KEY,
    created_at INTEGER NOT NULL
);
