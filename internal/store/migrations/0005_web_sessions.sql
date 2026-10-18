-- Browser sessions of the optional web sign-in. A row stores only the SHA-256
-- of the random session ID carried by the cookie, never the ID or an API key.
-- The key is resolved on every request, so revoking it ends its sessions
-- immediately; the janitor removes expired and orphaned rows. There is no
-- foreign key: API keys are never deleted, only revoked.
CREATE TABLE web_sessions (
    id_hash      BLOB    PRIMARY KEY,
    key_id       TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
) STRICT;
