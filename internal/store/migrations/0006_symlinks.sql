-- Symlinks: an object of kind 'symlink' has no blob and refers to a file by
-- its ID. SQLite cannot drop NOT NULL from blob_id in place, so objects and
-- trash are rebuilt. A symlink row stores size 0, an empty sha256 and an
-- empty content type; readers take those from the target.

CREATE TABLE objects_new (
    id           TEXT    PRIMARY KEY,
    path         TEXT    NOT NULL UNIQUE,
    kind         TEXT    NOT NULL DEFAULT 'file' CHECK (kind IN ('file', 'symlink')),
    target_id    TEXT,
    blob_id      TEXT    UNIQUE,
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
    revision     INTEGER NOT NULL DEFAULT 1,
    CHECK ((kind = 'file' AND blob_id IS NOT NULL AND target_id IS NULL)
        OR (kind = 'symlink' AND blob_id IS NULL AND target_id IS NOT NULL))
) STRICT;

INSERT INTO objects_new (id, path, kind, target_id, blob_id, size, sha256, content_type, disposition,
    visibility, title, description, tags, created_at, updated_at, expires_at, revision)
SELECT id, path, 'file', NULL, blob_id, size, sha256, content_type, disposition,
    visibility, title, description, tags, created_at, updated_at, expires_at, revision
FROM objects;

DROP TABLE objects;
ALTER TABLE objects_new RENAME TO objects;

CREATE INDEX objects_expires_at ON objects (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX objects_target_id ON objects (target_id) WHERE target_id IS NOT NULL;

CREATE TABLE trash_new (
    id             TEXT    PRIMARY KEY,
    object_id      TEXT    NOT NULL UNIQUE,
    original_path  TEXT    NOT NULL,
    kind           TEXT    NOT NULL DEFAULT 'file' CHECK (kind IN ('file', 'symlink')),
    target_id      TEXT,
    blob_id        TEXT    UNIQUE,
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
    deleted_by_key TEXT,
    seq            INTEGER NOT NULL DEFAULT 0,
    CHECK ((kind = 'file' AND blob_id IS NOT NULL AND target_id IS NULL)
        OR (kind = 'symlink' AND blob_id IS NULL AND target_id IS NOT NULL))
) STRICT;

INSERT INTO trash_new (id, object_id, original_path, kind, target_id, blob_id, size, sha256, content_type,
    disposition, visibility, title, description, tags, created_at, updated_at, expires_at, revision,
    deleted_at, deleted_by_key, seq)
SELECT id, object_id, original_path, 'file', NULL, blob_id, size, sha256, content_type,
    disposition, visibility, title, description, tags, created_at, updated_at, expires_at, revision,
    deleted_at, deleted_by_key, seq
FROM trash;

DROP TABLE trash;
ALTER TABLE trash_new RENAME TO trash;

CREATE INDEX trash_original_path ON trash (original_path);
CREATE INDEX trash_deleted_at ON trash (deleted_at);
CREATE UNIQUE INDEX trash_seq ON trash (seq);
