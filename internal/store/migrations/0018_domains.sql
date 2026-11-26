-- Domains a person entered by hand, whose DNS is managed somewhere else (at
-- another provider or the registrar). They keep the domain in the person's
-- panel as a domain of its own, for example to prepare a site or e-mail for
-- it later. Nothing at any DNS provider belongs to a row here.
CREATE TABLE domains (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    domain     TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, domain)
);
