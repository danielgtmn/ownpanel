-- sesam's tables (auth_*) with rows, as sesam v1.0.0 (sqladapter, SQLite)
-- created and wrote them with the panel's configuration of the password
-- plugin: `sqlite3 <db> .dump` of a database made with sesam v1's Go API.
-- Times are microseconds, flags 0/1, empty e-mail addresses NULL. The
-- random user ids sesam made are replaced by readable ones (v1-signin-…)
-- in every column that holds them, and the attempt counter keyed by bob's
-- id is re-keyed (SHA-256 of "password:sign-in:user:v1-signin-bob");
-- nothing else is changed.
--
--   root  v1-signin-root  password "root-password-1", signed in
--         (cookie token B7ljy4WKTe3_NpTkn7uvXCP0kYLeRoKptLY2irdt6-k) at
--         2026-11-20 10:00:00.123456 UTC, idle timeout 7 days
--   bob   v1-signin-bob  password "bob-password-1", e-mail
--         bob@example.org, 3 failed sign-ins from 192.0.2.9, a reset link
--         (token 0pGy5dJb-B1S1TDJQXUz8QD1NEeBGD49-_fknAJ3LR4, 1 hour)
--   nopw  v1-signin-nopw  no password
CREATE TABLE IF NOT EXISTS "auth_users" (
  "id" TEXT NOT NULL PRIMARY KEY,
  "email" TEXT,
  "email_verified" INTEGER NOT NULL,
  "username" TEXT,
  "name" TEXT NOT NULL,
  "created_at" INTEGER NOT NULL,
  "updated_at" INTEGER NOT NULL
);
INSERT INTO auth_users VALUES('v1-signin-root',NULL,0,'root','',1795168800123456,1795168800123456);
INSERT INTO auth_users VALUES('v1-signin-bob','bob@example.org',0,'bob','Bob ✓',1795168800123456,1795168800123456);
INSERT INTO auth_users VALUES('v1-signin-nopw',NULL,0,'nopw','',1795168800123456,1795168800123456);
CREATE TABLE IF NOT EXISTS "auth_sessions" (
  "id" TEXT NOT NULL PRIMARY KEY,
  "user_id" TEXT NOT NULL REFERENCES "auth_users"("id"),
  "created_at" INTEGER NOT NULL,
  "expires_at" INTEGER NOT NULL,
  "last_seen_at" INTEGER NOT NULL,
  "ip" TEXT,
  "user_agent" TEXT
);
INSERT INTO auth_sessions VALUES('dc50d44b47b3a41bbfda42c57b31f4492180a30bd66c6d6f5e1a0785277e2659','v1-signin-root',1795168800123456,1795773600123456,1795168800123456,'192.0.2.1','Firefox ü');
CREATE TABLE IF NOT EXISTS "auth_accounts" (
  "id" TEXT NOT NULL PRIMARY KEY,
  "user_id" TEXT NOT NULL REFERENCES "auth_users"("id"),
  "provider" TEXT NOT NULL,
  "provider_account_id" TEXT NOT NULL,
  "password_hash" TEXT,
  "created_at" INTEGER NOT NULL,
  "updated_at" INTEGER NOT NULL
);
INSERT INTO auth_accounts VALUES('GJ3LPCCEQTWXCTCPMPYGJXP7Z4','v1-signin-root','credential','v1-signin-root','$argon2id$v=19$m=65536,t=2,p=2$jPir+Shi21ASVAUN/xiCtw$8XIYbjQy+QdK3qT0dW9AnZJ185zqEclbZR1pUUD7cc8',1795168800123456,1795168800123456);
INSERT INTO auth_accounts VALUES('E2LBYJTTSJCVUYQU7MPX2UUNUI','v1-signin-bob','credential','v1-signin-bob','$argon2id$v=19$m=65536,t=2,p=2$jCJOdMltFVDrN2IvLnVKtQ$/vTx+oQyi5zx4UkZeCyheqMgQ6Hyc/MtPMdEB7R8rlY',1795168800123456,1795168800123456);
CREATE TABLE IF NOT EXISTS "auth_verifications" (
  "id" TEXT NOT NULL PRIMARY KEY,
  "purpose" TEXT NOT NULL,
  "identifier" TEXT NOT NULL,
  "expires_at" INTEGER NOT NULL,
  "created_at" INTEGER NOT NULL
);
INSERT INTO auth_verifications VALUES('b3b60fbeb5b7e1b0c4afe5c6441d5157762fdadf19ec0a195bba93247d2083dd','password-reset','v1-signin-bob',1795172400123456,1795168800123456);
CREATE TABLE IF NOT EXISTS "auth_rate_limits" (
  "id" TEXT NOT NULL PRIMARY KEY,
  "count" INTEGER NOT NULL,
  "window_start" INTEGER NOT NULL
);
INSERT INTO auth_rate_limits VALUES('519f0f19c7cd39ab8fea7b95085d2ab2af1fb9e981495b09a7baebc8699d5571',0,1795168800123456);
INSERT INTO auth_rate_limits VALUES('3dc42f512d622e9d8f24ea9346b9924435856810ce18cfefd77cc5ed0054e742',3,1795168800123456);
INSERT INTO auth_rate_limits VALUES('80bfbc4764c59ee89ada1c8115c21542f0af8c39fc49c2de4820d9307ffc1930',3,1795168800123456);
INSERT INTO auth_rate_limits VALUES('209d031fad203a05fbd0c2274e94ce91ccd7b20f7ebd521f2a442d5bc60fc8ce',1,1795168800123456);
INSERT INTO auth_rate_limits VALUES('47a1f82a73be0d94eb2316a059632f6b055940a56db54e55ad067c4dbacb0341',1,1795168800123456);
CREATE UNIQUE INDEX "auth_users_email_key" ON "auth_users" ("email");
CREATE UNIQUE INDEX "auth_users_username_key" ON "auth_users" ("username");
CREATE INDEX "auth_users_created_at_idx" ON "auth_users" ("created_at");
CREATE INDEX "auth_sessions_user_id_idx" ON "auth_sessions" ("user_id");
CREATE INDEX "auth_sessions_expires_at_idx" ON "auth_sessions" ("expires_at");
CREATE INDEX "auth_accounts_user_id_idx" ON "auth_accounts" ("user_id");
CREATE UNIQUE INDEX "auth_accounts_provider_provider_account_id_key" ON "auth_accounts" ("provider", "provider_account_id");
CREATE INDEX "auth_verifications_identifier_idx" ON "auth_verifications" ("identifier");
CREATE INDEX "auth_verifications_expires_at_idx" ON "auth_verifications" ("expires_at");
CREATE INDEX "auth_rate_limits_window_start_idx" ON "auth_rate_limits" ("window_start");
