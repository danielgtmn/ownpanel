-- Sign-in moves to the auth_* tables (users, password hashes and sessions
-- there). auth_user_id links a panel user to its sign-in user; it is NULL
-- only until the user was moved over at the next start. password_hash and
-- the sessions table are no longer used once a user is linked.
ALTER TABLE users ADD COLUMN auth_user_id TEXT;
CREATE UNIQUE INDEX users_auth_user ON users(auth_user_id);
