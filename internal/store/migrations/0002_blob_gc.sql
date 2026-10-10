-- Blobs waiting for physical deletion. A blob is queued in the same
-- transaction that drops its last reference and deleted only after a grace
-- period and a durability barrier (see internal/service/gc.go).
CREATE TABLE blob_gc (
    blob_id    TEXT    PRIMARY KEY,
    not_before INTEGER NOT NULL
) STRICT;

CREATE INDEX blob_gc_not_before ON blob_gc (not_before);
