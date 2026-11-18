-- Step 9: public share links for recipes. Only a hash of the token is kept; a
-- link always has an expiry (the app enforces at most 365 days) and can be revoked.
CREATE TABLE shares (
    token_hash TEXT PRIMARY KEY,
    recipe_id  INTEGER NOT NULL REFERENCES recipes (id) ON DELETE CASCADE,
    created_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);
CREATE INDEX shares_recipe ON shares (recipe_id);
