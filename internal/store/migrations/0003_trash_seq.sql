-- Trash entries get a monotonic sequence number in deletion order. Entry IDs
-- are ULIDs whose random part is not ordered within one millisecond, so they
-- cannot tell which of two deletions in the same millisecond came last.
CREATE TABLE counters (
    name  TEXT    PRIMARY KEY,
    value INTEGER NOT NULL
) STRICT;

ALTER TABLE trash ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;

-- Existing entries: number them by deletion time, then ID.
UPDATE trash SET seq = (
    SELECT COUNT(*) FROM trash AS t2
    WHERE t2.deleted_at < trash.deleted_at OR (t2.deleted_at = trash.deleted_at AND t2.id <= trash.id)
);

INSERT INTO counters (name, value) SELECT 'trash', COALESCE(MAX(seq), 0) FROM trash;

CREATE UNIQUE INDEX trash_seq ON trash (seq);
