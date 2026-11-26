-- Rows in every table of the last schema from before ablage (legacy/*.sql),
-- with the edge cases the conversion has to keep: NULLs, 0 meaning never,
-- JSON, binary secrets, unicode, several rows in the same second, and ids
-- that sort differently as text ("9" < "10" as numbers only).

-- sesam's tables come from sesam_v1.sql; the conversion leaves them alone.

INSERT INTO users (id, name, password_hash, role, created_at, auth_user_id) VALUES
    (9,  'root', '', 'admin', 1700000000, 'v1-signin-root'),
    (10, 'bob', '', 'member', 1700000000, 'v1-signin-bob'),
    (11, 'jürgen', 'kept-hash-of-juergen', 'member', 1700000005, NULL);

INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES
    ('a1b2', 11, 1700000010, 4000000000);

INSERT INTO tokens (id, user_id, name, hash, access, sites, created_at, last_used_at, expires_at) VALUES
    (1, 9, 'ci', 'hash-1', 'write', NULL, 1700000100, NULL, NULL),
    (2, 10, 'deploy ✓', 'hash-2', 'read', '["bobsite","other"]', 1700000100, 1700000200, 1800000000);

INSERT INTO sites (name, owner_id, created_at) VALUES
    ('bobsite', 10, 1700000300),
    ('orphan', NULL, 1700000301);

INSERT INTO site_domains (domain, site_name) VALUES
    ('bob.example.com', 'bobsite'),
    ('www.bob.example.com', 'bobsite'),
    ('orphan.example.org', 'orphan');

INSERT INTO activity (id, at, actor, action, target, detail) VALUES
    (9, 1700000400, 'root', 'user.create', 'bob', ''),
    (10, 1700000400, 'root', 'site.create', 'bobsite', 'bob.example.com'),
    (11, 1700000399, 'bob', 'site.publish', 'bobsite', 'v1 — ünïcode');

INSERT INTO settings (key, value) VALUES
    ('panel_domain', 'panel.example.com'),
    ('mail_app_password_key', 'sealed-value-placeholder');

INSERT INTO dns_connections (id, user_id, provider, name, secret, created_at, last_checked_at, last_error, can_write, zone_count) VALUES
    (3, 10, 'hetzner', 'Bob''s project', X'00ff10a0', 1700000500, NULL, '', NULL, NULL),
    (4, 9, 'hetzner', 'main', X'deadbeef', 1700000501, 1700000600, 'rate limited', 0, 5),
    (5, 9, 'hetzner', 'second', X'', 1700000502, 1700000601, '', 1, 0);

INSERT INTO dns_zones (connection_id, zone, created_at) VALUES
    (3, 'bob.example.com', 1700000700),
    (4, 'example.net', 1700000701);

INSERT INTO domains (user_id, domain, created_at) VALUES
    (10, 'elsewhere.example', 1700000800);

INSERT INTO redirects (id, owner_id, site_name, source_domain, source_path, target, code, keep_path, keep_query, created_at) VALUES
    (7, 9, NULL, 'old.example.com', '', 'https://new.example.com', 301, 1, 0, 1700000900),
    (8, NULL, 'bobsite', 'bob.example.com', '/blog/*', '/news', 308, 0, 1, 1700000901);

INSERT INTO login_attempts (key, at) VALUES ('abc', 1700001000);

INSERT INTO organisations (id, owner_id, name, created_at) VALUES
    ('org-1a2b', 10, 'Bob & Co', 1700001100);

INSERT INTO mail_domains (domain, owner_id, active, activated_at, checked_at, check_error, created_at, organisation_id) VALUES
    ('bob.example.com', 10, 1, 1700001210, 1700001200, '', 1700001150, 'org-1a2b'),
    ('example.net', 9, 0, NULL, NULL, 'no MX record', 1700001151, NULL);

INSERT INTO mail_dkim_keys (id, domain, selector, private_key, public_key, state, created_at) VALUES
    (1, 'bob.example.com', 's1', X'0001020304', 'MIIBpub1', 'retired', 1700001300),
    (2, 'bob.example.com', 's2', X'ffeedd', 'MIIBpub2', 'active', 1700001300),
    (3, 'example.net', 's1', X'aa', 'MIIBpub3', 'pending', 1700001301);

INSERT INTO mailboxes (id, storage_id, domain, local_part, password_hash, quota_bytes, send_per_hour, send_per_day, created_at,
        display_name, app_passwords_only, revision, admin_password_at, admin_display_name_at, admin_app_passwords_only_at,
        kind, keep_sent_copy, admin_grants_at) VALUES
    (9, 'storage09', 'bob.example.com', 'info', '$argon2id$box', 0, 100, 1000, 1700001400,
        'Bob Ünïcode', 1, 42, 0, 1700001500, 0, 'personal', 0, 0),
    (10, 'storage10', 'bob.example.com', 'team', '', 5368709120, 0, 0, 1700001400,
        '', 0, 0, 1700001600, 0, 1700001601, 'shared', 1, 1700001602);

INSERT INTO mail_aliases (id, domain, local_part, targets, created_at, sender_policy, hide_from_directory) VALUES
    (5, 'bob.example.com', '*', '["info@bob.example.com"]', 1700001700, 'anyone', 0),
    (6, 'bob.example.com', 'sales', '["info@bob.example.com","extern@example.org"]', 1700001701, 'members', 1);

INSERT INTO mail_pending_deletes (storage_id, created_at) VALUES
    ('gone01', 1700001800);

INSERT INTO mailbox_app_passwords (id, mailbox_id, name, mac, created_at, last_used_at, last_used_net) VALUES
    ('ap1', 9, 'Thunderbird', 'mac1', 1700001900, NULL, ''),
    ('ap2', 9, 'Phone 📱', 'mac2', 1700001901, 1700002000, '203.0.113.0/24');

INSERT INTO mailbox_grants (mailbox_id, grantee_id, rights, created_at) VALUES
    (10, 9, 'read send_as', 1700002100);

INSERT INTO mailbox_no_forwarding (mailbox_id) VALUES (10);
