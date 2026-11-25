# Users and Access

## Roles

| Role | Can |
|---|---|
| **Admin** | see and manage all sites; manage users; read the activity log; plus everything a member can |
| **Member** | create sites and see and manage only the sites they created; change their own password; create access tokens; connect their own DNS accounts |

The first admin is created during setup. Admins add further people under **People** (`POST /api/users`) with a name, a password and a role. Admins can change a person's role or set a new password, and delete a person.

Refused by design:

- The last admin cannot be deleted or made a member.
- A person who still owns sites cannot be deleted.
- Deleting a person also deletes their sessions, access tokens and DNS connections.

### Names and passwords

- **User name:** 3 to 32 characters: lowercase letters, digits, dots, underscores and hyphens. Input is trimmed and lowercased.
- **Password:** 10 to 200 characters. No other rules. Stored as an argon2id hash.

## Setup code

While no user exists, ownpaneld writes a one-time code to `<data>/setup-code` (mode 0600) and logs the path. Creating the first admin needs this code (letters are case-insensitive). Wrong codes count as failed attempts for the throttle below. Once the first admin exists, the file is deleted and `POST /api/setup` always answers `409`.

## Sessions

- Signing in with name and password starts a session. It ends after **3 days without use**; every use pushes that back, but never past **30 days** after signing in. Then you sign in again.
- Over HTTPS the browser gets the cookie `__Host-ownpanel_session` (`Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`, no `Domain`). Over plain HTTP, as in local development, it is `ownpanel_session` without `Secure`. After an update from a version that used `ownpanel_session` over HTTPS, the old cookie is removed and you sign in once more.
- Only a hash of the session secret is stored.
- Changing your password ends all your other sessions; an admin setting another person's password ends all of that person's sessions.
- Signing out ends the session. **Sign out everywhere** in the account menu ends all of your sessions on every device, this one included; access tokens keep working. Expired sessions are cleaned up whenever someone signs in.

Requests made with the cookie that change something must send the header `X-Ownpanel-Request: 1`; see [[API|API]].

## Login throttling

After 5 failed attempts for the same name from the same address within 15 minutes, further attempts are refused (`429`, "try again in N minutes") until the oldest failure leaves the window. A successful sign-in clears the count. The same limit applies to the setup code and to the "current password" check when changing a password. In addition, one address may fail at most 20 sign-ins within 15 minutes across all names. Attempts are stored (hashed) in the database, so restarting ownpaneld does not reset them.

Checking a password takes a lot of memory and time on purpose. At most 16 sign-ins and password changes are checked at once; while all are busy, further ones get `429` ("try again in a minute") right away.

A name that does not exist takes as long as a wrong password, so the answer does not reveal which names exist.

The counter is kept in memory; see [[Security|Security]].

## Access tokens

Access tokens let scripts, CI and AI agents use the API and MCP without a password.

- Every user can create tokens. A token starts with `op_` and is **shown once**; only its SHA-256 hash is stored.
- A token has a name (1 to 64 characters), an access level and a scope:
  - **Access:** `read` (can only look) or `write`.
  - **Scope:** all the user's sites, or a chosen list (at least one site, each must be visible to you).
- A token can expire: when creating it you choose 30 days, 90 days (suggested), 1 year or never (the API accepts 1 to 3650 days). An expired token is refused like an unknown one (`401`), including its open upload links. It stays in the list, marked as no longer working, until you delete it.
- A token never has more rights than its user has right now: if the user is demoted or deleted, the token follows.
- If a site that a limited token names is deleted, it is removed from the token's list.
- Tokens can only be created, listed and revoked when **signed in with a password**, not with another token. The list shows name, access, sites, creation time, last use and expiry.
- Revoking a token also invalidates its open upload links.

### What each kind of credential may do

"Own sites" means: sites the person created; for an admin, all sites. A limited token is further restricted to the sites in its list.

| Operation | Password sign-in | Token `read` | Token `write`, all sites | Token `write`, limited |
|---|---|---|---|---|
| List / show sites, versions | yes | yes | yes | only its sites |
| Domain status of a site | yes | yes | yes | its sites |
| Create a site | yes | no | yes | no |
| Change a site's domains | yes | no | yes | its sites |
| Delete a site | yes | no | yes | no |
| Publish, make a version live, create an upload link | yes | no | yes | its sites |
| "Point to this server" | yes | no | yes | its sites |
| Show server addresses | yes | yes | yes | yes |
| List DNS zones and records | yes | yes | yes | no |
| Change or delete DNS records | yes | no | yes | no |
| Add, check, remove DNS connections | yes | no | yes | no |
| List DNS connections | yes | yes | yes | no |
| Manage access tokens | yes | no | no | no |
| Change own password | yes | no | no | no |
| List, create, change, delete users; read activity log | admins only | no | admins only | no |

Notes:

- Rows marked "admins only" need the admin role **and** a password sign-in or an unrestricted `write` token. A `read` or limited token of an admin cannot manage users or read the log.
- Sites a person may not see answer "not found", the same as missing sites.
- DNS connections belong to the person who added them. Admins see everyone's connections (name and owner, never the token) and can remove them, but can only read and change records through their own. See [[DNS|DNS]].
- MCP accepts only tokens, never the cookie, and applies the same rules; see [[MCP|MCP]].

## Activity log

Admins can read the newest entries (web interface: **Activity**; API: `GET /api/activity?limit=50`, 1 to 200, default 50). Each entry has a time, an actor, an action, a target and a detail. When a token was used, the actor reads `<user> via token <token name>`.

| Action | When |
|---|---|
| `setup` | first admin created |
| `account.login` | someone signed in |
| `account.password` | someone changed their own password |
| `account.logout_everywhere` | someone signed out everywhere |
| `user.create`, `user.update`, `user.delete` | admin changes to people |
| `token.create`, `token.delete` | token created or revoked |
| `site.create`, `site.domains`, `site.delete` | site changes |
| `site.publish`, `site.make-live` | new version, version switched |
| `site.upload-link` | upload link created |
| `dns.connect`, `dns.disconnect` | DNS connection added or removed |
| `dns.record.set`, `dns.record.delete`, `dns.point` | DNS changes |

Passwords, tokens and DNS API tokens are never logged; TXT values longer than 60 characters are replaced by their length. Failed sign-ins are not logged.
