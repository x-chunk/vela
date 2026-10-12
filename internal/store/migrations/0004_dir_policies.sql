CREATE TABLE dir_policies (
 prefix TEXT PRIMARY KEY,
 idx TEXT NOT NULL DEFAULT 'inherit' CHECK (idx IN ('on','off','inherit')),
 hidden INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0,1)),
 noindex TEXT NOT NULL DEFAULT 'inherit' CHECK (noindex IN ('on','off','inherit')),
 title TEXT NOT NULL DEFAULT '',
 description TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1,
 CHECK (prefix = '' OR substr(prefix, -1) = '/'),
 CHECK (NOT (prefix = '' AND hidden = 1))
) STRICT;

-- A persistent generation works across pooled connections and processes.
INSERT INTO counters (name, value) VALUES ('dir_policies', 0);
CREATE TRIGGER dir_policies_insert AFTER INSERT ON dir_policies BEGIN
 UPDATE counters SET value = value + 1 WHERE name = 'dir_policies';
END;
CREATE TRIGGER dir_policies_update AFTER UPDATE ON dir_policies BEGIN
 UPDATE counters SET value = value + 1 WHERE name = 'dir_policies';
END;
CREATE TRIGGER dir_policies_delete AFTER DELETE ON dir_policies BEGIN
 UPDATE counters SET value = value + 1 WHERE name = 'dir_policies';
END;
