-- Shared mailboxes, delegation and distribution groups (G3 in
-- wiki/Groupware-Architecture.md).
--
-- kind: 'personal' or 'shared' ('resource' comes later, so there is no
-- CHECK here; the code validates it). A shared mailbox has no password
-- (password_hash ''), no app passwords and cannot sign in.
-- keep_sent_copy: mail sent as or on behalf of a shared mailbox is also
-- stored in its Sent folder.
ALTER TABLE mailboxes ADD COLUMN kind TEXT NOT NULL DEFAULT 'personal';
ALTER TABLE mailboxes ADD COLUMN keep_sent_copy INTEGER NOT NULL DEFAULT 0 CHECK (keep_sent_copy IN (0, 1));

-- Rights of a personal mailbox (grantee) on another mailbox: set by the
-- domain owner for a shared mailbox, by the person for their own
-- (self-delegation, recorded from the mail service's events). rights are
-- the names of config.Rights separated by spaces. Grants never cross an
-- organisation; the panel checks it when they are made and leaves out
-- those that no longer fit when it pushes.
CREATE TABLE mailbox_grants (
    mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    grantee_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    rights     TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (mailbox_id, grantee_id),
    CHECK (mailbox_id != grantee_id)
);
CREATE INDEX mailbox_grants_grantee ON mailbox_grants(grantee_id);

-- Distribution groups: who may write to an alias ('anyone',
-- 'organisation', 'members') and whether it is left out of the
-- organisation's address book.
ALTER TABLE mail_aliases ADD COLUMN sender_policy TEXT NOT NULL DEFAULT 'anyone'
    CHECK (sender_policy IN ('anyone', 'organisation', 'members'));
ALTER TABLE mail_aliases ADD COLUMN hide_from_directory INTEGER NOT NULL DEFAULT 0 CHECK (hide_from_directory IN (0, 1));
