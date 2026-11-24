-- Panel settings (panel_domain, server_ips, acme_email, setup_complete) live
-- in the settings table from 0001. Installs that already have users were set
-- up before the first-run wizard existed: they count as complete, so they
-- never see the wizard.
INSERT OR IGNORE INTO settings (key, value)
SELECT 'setup_complete', 'true' WHERE EXISTS (SELECT 1 FROM users);
