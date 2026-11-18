-- Step 8: invites, reset links, TOTP, passkeys, single sign-on.

-- TOTP: the secret is stored encrypted (internal/secrets). totp_last_step
-- stops a code being used twice.
ALTER TABLE users ADD COLUMN totp_secret    TEXT    NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_enabled   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;

-- One-time recovery codes for TOTP; only a hash is kept, a code is deleted when used.
CREATE TABLE recovery_codes (
    user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash TEXT    NOT NULL,
    PRIMARY KEY (user_id, code_hash)
);

-- WebAuthn credentials. data is the library's credential as JSON (public key, counter).
CREATE TABLE passkeys (
    id            INTEGER PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    credential_id BLOB    NOT NULL UNIQUE,
    data          TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX passkeys_user ON passkeys (user_id);

-- Single-use links: an invite (user_id NULL, carries the new account's email
-- and name) or a password reset (user_id set). Only a hash of the token is kept.
CREATE TABLE user_tokens (
    token_hash   TEXT PRIMARY KEY,
    kind         TEXT    NOT NULL CHECK (kind IN ('invite', 'reset')),
    user_id      INTEGER REFERENCES users (id) ON DELETE CASCADE,
    email        TEXT    NOT NULL DEFAULT '',
    display_name TEXT    NOT NULL DEFAULT '',
    is_admin     INTEGER NOT NULL DEFAULT 0,
    created_by   INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    used_at      INTEGER
);

-- Accounts known to the single sign-on provider, by its stable subject.
CREATE TABLE oidc_identities (
    issuer  TEXT    NOT NULL,
    subject TEXT    NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (issuer, subject)
);
