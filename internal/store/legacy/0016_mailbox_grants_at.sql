-- admin_grants_at: when the panel last changed who has access to a mailbox
-- (0: never). A self-delegation the mailbox made in the mail service at or
-- before that time does not override it (like admin_password_at).
ALTER TABLE mailboxes ADD COLUMN admin_grants_at INTEGER NOT NULL DEFAULT 0;
