-- Initial schema. All timestamps are Unix milliseconds in UTC.
-- Paths compare with SQLite's default BINARY collation, i.e. byte-wise, which
-- matches Go string ordering and makes prefix range scans correct.

CREATE TABLE objects (
    id           TEXT    PRIMARY KEY,
    path         TEXT    NOT NULL UNIQUE,
    blob_id      TEXT    NOT NULL UNIQUE,
    size         INTEGER NOT NULL CHECK (size >= 0),
    sha256       TEXT    NOT NULL,
    content_type TEXT    NOT NULL,
    disposition  TEXT    NOT NULL DEFAULT 'inline' CHECK (disposition IN ('inline', 'attachment')),
    visibility   TEXT    NOT NULL CHECK (visibility IN ('public', 'unlisted', 'private')),
    title        TEXT    NOT NULL DEFAULT '',
    description  TEXT    NOT NULL DEFAULT '',
    tags         TEXT    NOT NULL DEFAULT '[]', -- JSON array of strings
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    expires_at   INTEGER,
    revision     INTEGER NOT NULL DEFAULT 1
) STRICT;

CREATE INDEX objects_expires_at ON objects (expires_at) WHERE expires_at IS NOT NULL;

-- Trash keeps the full object row as columns (not a JSON snapshot) so that
-- blob references can be indexed for orphan detection and queried by path.
CREATE TABLE trash (
    id             TEXT    PRIMARY KEY,
    object_id      TEXT    NOT NULL UNIQUE,
    original_path  TEXT    NOT NULL,
    blob_id        TEXT    NOT NULL UNIQUE,
    size           INTEGER NOT NULL,
    sha256         TEXT    NOT NULL,
    content_type   TEXT    NOT NULL,
    disposition    TEXT    NOT NULL,
    visibility     TEXT    NOT NULL,
    title          TEXT    NOT NULL,
    description    TEXT    NOT NULL,
    tags           TEXT    NOT NULL,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    expires_at     INTEGER,
    revision       INTEGER NOT NULL,
    deleted_at     INTEGER NOT NULL,
    deleted_by_key TEXT
) STRICT;

CREATE INDEX trash_original_path ON trash (original_path);
CREATE INDEX trash_deleted_at ON trash (deleted_at);

CREATE TABLE signing_keys (
    id         TEXT    PRIMARY KEY,
    secret     BLOB    NOT NULL,
    created_at INTEGER NOT NULL,
    retired_at INTEGER
) STRICT;

-- No foreign keys: a link's object may sit in the trash, and a link may
-- outlive its signing key after an emergency rotation (it is then dead but
-- still listed until the janitor removes it).
CREATE TABLE links (
    id             TEXT    PRIMARY KEY,
    object_id      TEXT    NOT NULL,
    signing_key_id TEXT    NOT NULL,
    filename       TEXT    NOT NULL,
    note           TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,
    revoked_at     INTEGER,
    created_by_key TEXT
) STRICT;

CREATE INDEX links_object_id ON links (object_id);
CREATE INDEX links_expires_at ON links (expires_at);
CREATE INDEX links_signing_key_id ON links (signing_key_id);

CREATE TABLE api_keys (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL,
    secret_hash  BLOB    NOT NULL,
    scope        TEXT    NOT NULL CHECK (scope IN ('ro', 'rw', 'admin')),
    prefix       TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
) STRICT;

-- Names identify keys for humans; a revoked key frees its name.
CREATE UNIQUE INDEX api_keys_active_name ON api_keys (name) WHERE revoked_at IS NULL;
