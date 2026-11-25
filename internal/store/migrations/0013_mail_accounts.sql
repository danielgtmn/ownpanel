-- Mailbox accounts (G1 in wiki/Groupware-Architecture.md).
--
-- An organisation groups mail domains: mailboxes may share (later) only
-- within one. id is "org-" and random hex; it is pushed to the mail
-- service as is and says nothing about the owner. A domain without an
-- organisation is its own. Deleting the owner deletes the organisation;
-- its domains then stand alone.
CREATE TABLE organisations (
    id         TEXT    PRIMARY KEY,
    owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX organisations_owner ON organisations(owner_id);

ALTER TABLE mail_domains ADD COLUMN organisation_id TEXT REFERENCES organisations(id) ON DELETE SET NULL;

-- display_name: shown as the sender's name and in the address book.
-- app_passwords_only: mail programs sign in only with app passwords.
-- revision: the highest revision of the mailbox's own changes in the mail
-- service that was recorded here (config.Mailbox.Revision).
-- admin_changed_at: when the panel last set the password, display name or
-- app_passwords_only itself; a change the mailbox made in the mail
-- service at or before that time does not override it.
ALTER TABLE mailboxes ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE mailboxes ADD COLUMN app_passwords_only INTEGER NOT NULL DEFAULT 0 CHECK (app_passwords_only IN (0, 1));
ALTER TABLE mailboxes ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mailboxes ADD COLUMN admin_changed_at INTEGER NOT NULL DEFAULT 0;

-- App passwords of a mailbox. id is the public part of the token
-- ("op_<id>_<secret>"); mac is HMAC-SHA-256 of the secret under the app
-- password key (config.AppPasswordMAC), never the token. last_used_net is
-- the /24 (IPv4) or /48 (IPv6) the last sign-in came from.
CREATE TABLE mailbox_app_passwords (
    id            TEXT    PRIMARY KEY,
    mailbox_id    INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    name          TEXT    NOT NULL,
    mac           TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER,
    last_used_net TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX mailbox_app_passwords_mailbox ON mailbox_app_passwords(mailbox_id);
