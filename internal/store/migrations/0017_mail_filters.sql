-- Filters and out-of-office (G2 in wiki/Groupware-Architecture.md).
--
-- The filters themselves are user data of the mail service. The panel only
-- keeps whether a mailbox's filters may forward mail: a row here means
-- they may not (forwarding is allowed by default). Pushed as
-- config.Mailbox.NoForwarding.
CREATE TABLE mailbox_no_forwarding (
    mailbox_id INTEGER PRIMARY KEY REFERENCES mailboxes(id) ON DELETE CASCADE
);
